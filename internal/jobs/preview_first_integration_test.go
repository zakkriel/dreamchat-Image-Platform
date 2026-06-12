//go:build integration

package jobs_test

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zakkriel/drchat-image-platform/internal/assets"
	"github.com/zakkriel/drchat-image-platform/internal/cost"
	"github.com/zakkriel/drchat-image-platform/internal/http/handlers"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
	"github.com/zakkriel/drchat-image-platform/internal/providers"
	"github.com/zakkriel/drchat-image-platform/internal/providers/mock"
	"github.com/zakkriel/drchat-image-platform/internal/providers/routing"
	"github.com/zakkriel/drchat-image-platform/internal/styles"
)

// mountPreviewFirstRouter wires the Phase 7B read+write surface: artifact +
// style-preview generate (both honor delivery_mode), the job read, and the
// job-assets read, against the real DB + storage. resolverAvail selects which
// providers the route resolver treats as available.
func mountPreviewFirstRouter(pool *pgxpool.Pool, svc jobs.Creator, stylesRepo styles.Repository, jobsRepo jobs.Repository, assetsRepo assets.Repository, store handlers.AssetURLSigner, resolverAvail map[string]bool) *chi.Mux {
	resolver := routing.NewResolver(routing.NewDBRouteSource(pool), resolverAvail)
	artifacts := handlers.NewArtifactsHandler(svc, stylesRepo, resolver, "mock", assetsRepo)
	preview := handlers.NewStylePreviewHandler(svc, stylesRepo, resolver, "mock")
	jobsH := handlers.NewJobsHandler(jobsRepo)
	assetsH := handlers.NewAssetsHandler(assetsRepo, assets.NewRetriever(assetsRepo)).
		WithDelivery(store, 15*time.Minute).
		WithJobs(jobsRepo)

	r := chi.NewRouter()
	r.Post("/v1/artifacts/{artifact_id}/generate", artifacts.Generate)
	r.Post("/v1/styles/{style_id}/preview", preview.GeneratePreview)
	r.Get("/v1/jobs/{job_id}", jobsH.Get)
	r.Get("/v1/jobs/{job_id}/assets", assetsH.JobAssets)
	return r
}

// blockingFinalProvider succeeds on the preview (1st) Generate call, then blocks
// on the final (2nd) call until the test releases it — so the test can observe
// the externally-visible preview_ready state (job read + job-assets read) BEFORE
// final completion. It delegates real image bytes to the mock provider.
type blockingFinalProvider struct {
	inner        *mock.Provider
	mu           sync.Mutex
	calls        int
	reachedFinal chan struct{}
	release      chan struct{}
}

func newBlockingFinalProvider() *blockingFinalProvider {
	return &blockingFinalProvider{
		inner:        mock.New(),
		reachedFinal: make(chan struct{}, 1),
		release:      make(chan struct{}),
	}
}

func (p *blockingFinalProvider) Generate(ctx context.Context, req providers.ProviderGenerateRequest) (providers.ProviderGenerateResult, error) {
	p.mu.Lock()
	p.calls++
	n := p.calls
	p.mu.Unlock()
	if n == 2 {
		p.reachedFinal <- struct{}{}
		<-p.release
	}
	return p.inner.Generate(ctx, req)
}

func (p *blockingFinalProvider) PollStatus(ctx context.Context, id string) (providers.ProviderJobStatus, error) {
	return p.inner.PollStatus(ctx, id)
}
func (p *blockingFinalProvider) Upscale(ctx context.Context, req providers.ProviderUpscaleRequest) (providers.ProviderGenerateResult, error) {
	return p.inner.Upscale(ctx, req)
}
func (p *blockingFinalProvider) Capabilities() providers.ProviderCapabilities {
	return p.inner.Capabilities()
}

// TestEndToEndPreviewFirstArtifact is the Phase 7B acceptance test: a
// preview_first artifact request reaches preview_ready (observable through both
// the job read and the job-assets read) BEFORE final completion, then completes
// the same job, with distinct preview/final rows, both carrying resolved
// provenance, and the cost reserved once / committed once.
func TestEndToEndPreviewFirstArtifact(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	store := openTestStorage(t)
	seedBudget(t, pool, "bud_pf_art", "tenant", itTenant, "active", "1.0000")

	jobsRepo := jobs.NewRepository(pool)
	assetsRepo := assets.NewRepository(pool)
	stylesRepo := styles.NewRepository(pool)
	svc := jobs.NewService(pool, newRecordingEnqueuer(), cost.NewService(nil)).WithFinalizer(cost.NewLifecycle(pool, nil))
	r := mountPreviewFirstRouter(pool, svc, stylesRepo, jobsRepo, assetsRepo, store, map[string]bool{"mock": true})

	rec := sendArtifactRequest(t, r, map[string]any{
		"world_id":         "w1",
		"style_profile_id": itStyleID,
		"description":      "A bronze key",
		"delivery_mode":    "preview_first",
	}, "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST generate expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	var acc map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &acc)
	jobID := acc["job_id"].(string)

	provider := newBlockingFinalProvider()
	worker := &jobs.Worker{
		Jobs:      jobsRepo,
		Assets:    assetsRepo,
		Storage:   store,
		Providers: registryFor(provider),
		Finalizer: cost.NewLifecycle(pool, nil),
	}
	done := make(chan error, 1)
	go func() { done <- worker.Process(context.Background(), jobID, 0) }()

	// Wait until the worker has committed the preview and is about to run final.
	select {
	case <-provider.reachedFinal:
	case <-time.After(30 * time.Second):
		t.Fatal("worker never reached the final generation phase")
	}

	// The preview state is externally observable through the job read…
	jrec := deliveryReq(t, r, http.MethodGet, "/v1/jobs/"+jobID, []string{"jobs:read"}, nil)
	if jrec.Code != http.StatusOK {
		t.Fatalf("GET job expected 200, got %d body=%s", jrec.Code, jrec.Body.String())
	}
	var jobView struct {
		Status          string   `json:"status"`
		PreviewAssetIds []string `json:"preview_asset_ids"`
		FinalAssetIds   []string `json:"final_asset_ids"`
	}
	_ = json.Unmarshal(jrec.Body.Bytes(), &jobView)
	if jobView.Status != "preview_ready" {
		t.Fatalf("expected status=preview_ready before final, got %q", jobView.Status)
	}
	if len(jobView.PreviewAssetIds) != 1 {
		t.Fatalf("expected one preview_asset_id before final, got %v", jobView.PreviewAssetIds)
	}
	if len(jobView.FinalAssetIds) != 0 {
		t.Fatalf("final_asset_ids must be empty before final, got %v", jobView.FinalAssetIds)
	}
	previewAssetID := jobView.PreviewAssetIds[0]

	// …and through the job-assets read, which returns the preview asset.
	pa := deliveryReq(t, r, http.MethodGet, "/v1/jobs/"+jobID+"/assets", []string{"images:read"}, nil)
	if pa.Code != http.StatusOK {
		t.Fatalf("GET job-assets (preview) expected 200, got %d", pa.Code)
	}
	var paResp struct {
		Assets []map[string]any `json:"assets"`
	}
	_ = json.Unmarshal(pa.Body.Bytes(), &paResp)
	if len(paResp.Assets) != 1 || paResp.Assets[0]["id"] != previewAssetID {
		t.Fatalf("job-assets must return the preview asset before final, got %+v", paResp.Assets)
	}
	if paResp.Assets[0]["status"] != "preview_ready" {
		t.Fatalf("delivered preview asset must be status=preview_ready, got %v", paResp.Assets[0]["status"])
	}

	// Let final generation proceed and wait for completion.
	close(provider.release)
	if err := <-done; err != nil {
		t.Fatalf("worker process: %v", err)
	}

	// The same job is now completed with final_asset_ids; preview survives.
	job, err := jobsRepo.GetByID(context.Background(), jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.Status != "completed" {
		t.Fatalf("expected completed, got %q", job.Status)
	}
	if len(job.FinalAssetIds) != 1 {
		t.Fatalf("expected one final_asset_id, got %v", job.FinalAssetIds)
	}
	finalAssetID := job.FinalAssetIds[0]
	if finalAssetID == previewAssetID {
		t.Fatalf("preview and final must be distinct rows, both %q", finalAssetID)
	}
	if len(job.PreviewAssetIds) != 1 || job.PreviewAssetIds[0] != previewAssetID {
		t.Fatalf("preview_asset_ids must survive to completion, got %v", job.PreviewAssetIds)
	}

	// Asset rows: preview_ready vs ready, both with resolved provenance.
	assertAssetRow(t, pool, previewAssetID, "preview_ready")
	assertAssetRow(t, pool, finalAssetID, "ready")

	// preview tier carries the preview_safe compatibility tag.
	var tags []string
	if err := pool.QueryRow(context.Background(),
		`SELECT compatibility_tags FROM visual_assets WHERE id=$1`, previewAssetID).Scan(&tags); err != nil {
		t.Fatalf("read preview tags: %v", err)
	}
	if !containsTag(tags, "preview_safe") {
		t.Fatalf("preview asset must carry preview_safe tag, got %v", tags)
	}

	// After completion the job-assets read returns the FINAL asset.
	fa := deliveryReq(t, r, http.MethodGet, "/v1/jobs/"+jobID+"/assets", []string{"images:read"}, nil)
	var faResp struct {
		Assets []map[string]any `json:"assets"`
	}
	_ = json.Unmarshal(fa.Body.Bytes(), &faResp)
	if len(faResp.Assets) != 1 || faResp.Assets[0]["id"] != finalAssetID {
		t.Fatalf("after completion job-assets must return the final asset, got %+v", faResp.Assets)
	}

	// Cost reserved once, committed once: actual cost stamped, budget moved.
	if got := scalar(t, pool, `SELECT count(*)::text FROM cost_reservations WHERE generation_job_id=$1`, jobID); got != "1" {
		t.Fatalf("expected exactly one reservation, got %s", got)
	}
	if got := scalar(t, pool, `SELECT status FROM cost_reservations WHERE generation_job_id=$1`, jobID); got != "committed" {
		t.Fatalf("expected committed reservation, got %s", got)
	}
	if got := scalar(t, pool, `SELECT actual_cost_usd::text FROM generation_jobs WHERE id=$1`, jobID); got != "0.0100" {
		t.Fatalf("expected actual_cost 0.0100 (single charge), got %s", got)
	}
}

// TestEndToEndPreviewFirstStylePreview: a preview_first style-preview sample runs
// the two-phase lifecycle and lands a distinct preview + final.
func TestEndToEndPreviewFirstStylePreview(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	store := openTestStorage(t)
	seedBudget(t, pool, "bud_pf_style", "tenant", itTenant, "active", "1.0000")

	jobsRepo := jobs.NewRepository(pool)
	assetsRepo := assets.NewRepository(pool)
	stylesRepo := styles.NewRepository(pool)
	svc := jobs.NewService(pool, newRecordingEnqueuer(), cost.NewService(nil)).WithFinalizer(cost.NewLifecycle(pool, nil))
	r := mountPreviewFirstRouter(pool, svc, stylesRepo, jobsRepo, assetsRepo, store, map[string]bool{"mock": true})

	rec := deliveryReq(t, r, http.MethodPost, "/v1/styles/"+itStyleID+"/preview", []string{"images:write"},
		map[string]any{"world_id": "w1", "delivery_mode": "preview_first"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("style preview expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	var acc map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &acc)
	jobID := acc["job_id"].(string)

	worker := &jobs.Worker{
		Jobs: jobsRepo, Assets: assetsRepo, Storage: store,
		Providers: registryFor(mock.New()), Finalizer: cost.NewLifecycle(pool, nil),
	}
	if err := worker.Process(context.Background(), jobID, 0); err != nil {
		t.Fatalf("worker process preview: %v", err)
	}

	job, err := jobsRepo.GetByID(context.Background(), jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.Status != "completed" {
		t.Fatalf("expected completed, got %q", job.Status)
	}
	if len(job.PreviewAssetIds) != 1 || len(job.FinalAssetIds) != 1 {
		t.Fatalf("expected one preview + one final id, got preview=%v final=%v", job.PreviewAssetIds, job.FinalAssetIds)
	}
	if job.PreviewAssetIds[0] == job.FinalAssetIds[0] {
		t.Fatalf("preview and final must be distinct rows")
	}
	assertAssetRow(t, pool, job.PreviewAssetIds[0], "preview_ready")
	assertAssetRow(t, pool, job.FinalAssetIds[0], "ready")
}

// TestEndToEndPreviewFirstFinalFailureKeepsPreview: when final generation fails
// after the preview was delivered, the job is failed, the cost reservation is
// released, final_asset_ids stays empty, and the preview asset remains readable
// through the job-assets read.
func TestEndToEndPreviewFirstFinalFailureKeepsPreview(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	store := openTestStorage(t)
	seedBudget(t, pool, "bud_pf_fail", "tenant", itTenant, "active", "1.0000")

	jobsRepo := jobs.NewRepository(pool)
	assetsRepo := assets.NewRepository(pool)
	stylesRepo := styles.NewRepository(pool)
	svc := jobs.NewService(pool, newRecordingEnqueuer(), cost.NewService(nil)).WithFinalizer(cost.NewLifecycle(pool, nil))
	r := mountPreviewFirstRouter(pool, svc, stylesRepo, jobsRepo, assetsRepo, store, map[string]bool{"mock": true})

	rec := sendArtifactRequest(t, r, map[string]any{
		"world_id":         "w1",
		"style_profile_id": itStyleID,
		"description":      "A bronze key fail",
		"delivery_mode":    "preview_first",
	}, "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	var acc map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &acc)
	jobID := acc["job_id"].(string)

	worker := &jobs.Worker{
		Jobs: jobsRepo, Assets: assetsRepo, Storage: store,
		Providers: registryFor(&previewThenFailITProvider{inner: mock.New()}),
		Finalizer: cost.NewLifecycle(pool, nil),
	}
	// Drive the terminal attempt so the failure releases the reservation.
	if err := worker.Process(context.Background(), jobID, int32(jobs.MaxAttempts-1)); err == nil {
		t.Fatal("expected final-phase failure error")
	}

	job, err := jobsRepo.GetByID(context.Background(), jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.Status != "failed" {
		t.Fatalf("expected failed, got %q", job.Status)
	}
	if len(job.PreviewAssetIds) != 1 {
		t.Fatalf("preview must remain after final failure, got %v", job.PreviewAssetIds)
	}
	if len(job.FinalAssetIds) != 0 {
		t.Fatalf("final_asset_ids must stay empty after final failure, got %v", job.FinalAssetIds)
	}
	// Preview asset still readable and still preview_ready (not archived).
	assertAssetRow(t, pool, job.PreviewAssetIds[0], "preview_ready")

	// Reservation released (returned to budget), not committed.
	if got := scalar(t, pool, `SELECT status FROM cost_reservations WHERE generation_job_id=$1`, jobID); got != "released" {
		t.Fatalf("expected released reservation after final failure, got %s", got)
	}

	// GET /v1/jobs/{id}/assets returns the preview asset (final empty).
	ja := deliveryReq(t, r, http.MethodGet, "/v1/jobs/"+jobID+"/assets", []string{"images:read"}, nil)
	if ja.Code != http.StatusOK {
		t.Fatalf("GET job-assets expected 200, got %d", ja.Code)
	}
	var jaResp struct {
		Assets []map[string]any `json:"assets"`
	}
	_ = json.Unmarshal(ja.Body.Bytes(), &jaResp)
	if len(jaResp.Assets) != 1 || jaResp.Assets[0]["id"] != job.PreviewAssetIds[0] {
		t.Fatalf("failed two-phase job must still deliver the preview asset, got %+v", jaResp.Assets)
	}
}

// TestEndToEndPreviewFirstBFLOnlyReturns422: with only BFL (no_preview) available,
// a preview_first request returns 422 unsupported_capability before any cost
// reservation, job creation, or enqueue.
func TestEndToEndPreviewFirstBFLOnlyReturns422(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)

	jobsRepo := jobs.NewRepository(pool)
	assetsRepo := assets.NewRepository(pool)
	stylesRepo := styles.NewRepository(pool)
	enq := newRecordingEnqueuer()
	svc := jobs.NewService(pool, enq, cost.NewService(nil))
	// Only BFL available — BFL advertises no_preview, so preview_first cannot resolve.
	r := mountPreviewFirstRouter(pool, svc, stylesRepo, jobsRepo, assetsRepo, nil, map[string]bool{"bfl": true})

	rec := sendArtifactRequest(t, r, map[string]any{
		"world_id":         "w1",
		"style_profile_id": itStyleID,
		"description":      "no preview here",
		"delivery_mode":    "preview_first",
	}, "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["code"] != "unsupported_capability" {
		t.Fatalf("expected unsupported_capability, got %v", body["code"])
	}

	// No writes happened before the routing failure.
	if got := scalar(t, pool, `SELECT count(*)::text FROM generation_jobs WHERE tenant_id=$1`, itTenant); got != "0" {
		t.Fatalf("expected no job created, got %s", got)
	}
	if got := scalar(t, pool, `SELECT count(*)::text FROM cost_reservations WHERE tenant_id=$1`, itTenant); got != "0" {
		t.Fatalf("expected no reservation, got %s", got)
	}
	if len(enq.snapshot()) != 0 {
		t.Fatalf("expected nothing enqueued, got %v", enq.snapshot())
	}

	// Control: the same request as final_only resolves BFL and is accepted.
	ok := sendArtifactRequest(t, r, map[string]any{
		"world_id":         "w1",
		"style_profile_id": itStyleID,
		"description":      "final only is fine",
	}, "")
	if ok.Code != http.StatusAccepted {
		t.Fatalf("final_only with BFL available expected 202, got %d body=%s", ok.Code, ok.Body.String())
	}
}

// previewThenFailITProvider succeeds on the preview (1st) call and fails on the
// final (2nd) call, to drive the failure-after-preview path end to end.
type previewThenFailITProvider struct {
	inner *mock.Provider
	mu    sync.Mutex
	calls int
}

func (p *previewThenFailITProvider) Generate(ctx context.Context, req providers.ProviderGenerateRequest) (providers.ProviderGenerateResult, error) {
	p.mu.Lock()
	p.calls++
	n := p.calls
	p.mu.Unlock()
	if n >= 2 {
		return providers.ProviderGenerateResult{}, errPreviewThenFail
	}
	return p.inner.Generate(ctx, req)
}
func (p *previewThenFailITProvider) PollStatus(ctx context.Context, id string) (providers.ProviderJobStatus, error) {
	return p.inner.PollStatus(ctx, id)
}
func (p *previewThenFailITProvider) Upscale(ctx context.Context, req providers.ProviderUpscaleRequest) (providers.ProviderGenerateResult, error) {
	return p.inner.Upscale(ctx, req)
}
func (p *previewThenFailITProvider) Capabilities() providers.ProviderCapabilities {
	return p.inner.Capabilities()
}

var errPreviewThenFail = errProviderFinalFailure{}

type errProviderFinalFailure struct{}

func (errProviderFinalFailure) Error() string { return "final provider failure" }

func assertAssetRow(t *testing.T, pool *pgxpool.Pool, assetID, wantStatus string) {
	t.Helper()
	var status string
	var provider, model, route *string
	if err := pool.QueryRow(context.Background(),
		`SELECT status, provider_id, model_id, provider_route_id FROM visual_assets WHERE id=$1`, assetID,
	).Scan(&status, &provider, &model, &route); err != nil {
		t.Fatalf("read asset %s: %v", assetID, err)
	}
	if status != wantStatus {
		t.Fatalf("asset %s: expected status=%s, got %s", assetID, wantStatus, status)
	}
	if provider == nil || *provider != "mock" || model == nil || *model != "pm_mock_v1" ||
		route == nil || *route != "route_mock_text_to_image_standard" {
		t.Fatalf("asset %s missing resolved provenance: provider=%v model=%v route=%v", assetID, provider, model, route)
	}
}

func containsTag(tags []string, want string) bool {
	for _, tag := range tags {
		if tag == want {
			return true
		}
	}
	return false
}
