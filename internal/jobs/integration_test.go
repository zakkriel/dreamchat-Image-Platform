//go:build integration

package jobs_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zakkriel/drchat-image-platform/internal/assets"
	"github.com/zakkriel/drchat-image-platform/internal/auth"
	"github.com/zakkriel/drchat-image-platform/internal/config"
	"github.com/zakkriel/drchat-image-platform/internal/cost"
	"github.com/zakkriel/drchat-image-platform/internal/http/handlers"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
	"github.com/zakkriel/drchat-image-platform/internal/providers/mock"
	"github.com/zakkriel/drchat-image-platform/internal/storage"
	"github.com/zakkriel/drchat-image-platform/internal/styles"
	"github.com/zakkriel/drchat-image-platform/internal/telemetry"
)

// To run end-to-end (requires Postgres + MinIO running):
//   POSTGRES_DSN=postgres://image_platform:image_platform@localhost:5432/image_platform?sslmode=disable \
//   S3_BUCKET=image-platform S3_REGION=us-east-1 \
//   S3_ENDPOINT=http://localhost:9000 \
//   S3_ACCESS_KEY_ID=minioadmin S3_SECRET_ACCESS_KEY=minioadmin \
//   S3_USE_PATH_STYLE=true \
//   go test -tags=integration ./internal/jobs/...

const (
	itTenant  = "tenant_it_jobs"
	itStyleID = "sty_it_jobs"
	itTokenID = "tok_it_jobs"
)

func openTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_DSN not set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("pgxpool.ParseConfig: %v", err)
	}
	// The concurrent idempotency test fans out N requests; each holds a
	// connection across its transaction so we need >= N to avoid starvation.
	cfg.MaxConns = 16
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("pgxpool.NewWithConfig: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("pool.Ping: %v", err)
	}
	return pool
}

func cleanup(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Logf("cleanup %q: %v", sql, err)
		}
	}
	exec(`DELETE FROM generation_cost_events WHERE tenant_id = $1`, itTenant)
	exec(`DELETE FROM provider_attempts WHERE generation_job_id IN (SELECT id FROM generation_jobs WHERE tenant_id = $1)`, itTenant)
	exec(`DELETE FROM idempotency_keys WHERE token_id = $1`, itTokenID)
	// asset_pack_items FKs both visual_assets and asset_packs; clear it first.
	exec(`DELETE FROM asset_pack_items WHERE asset_pack_id IN (SELECT id FROM asset_packs WHERE tenant_id = $1)`, itTenant)
	exec(`DELETE FROM visual_assets WHERE tenant_id = $1`, itTenant)
	// asset_packs FKs visual_identities + style_profiles + api_tokens; clear
	// before all three.
	exec(`DELETE FROM asset_packs WHERE tenant_id = $1`, itTenant)
	exec(`DELETE FROM visual_identity_versions WHERE visual_identity_id IN (SELECT id FROM visual_identities WHERE tenant_id = $1)`, itTenant)
	exec(`DELETE FROM visual_identities WHERE tenant_id = $1`, itTenant)
	// cost_reservation_budget_holds references both cost_reservations and
	// cost_budgets; clear it before either.
	exec(`DELETE FROM cost_reservation_budget_holds WHERE cost_reservation_id IN (SELECT id FROM cost_reservations WHERE tenant_id = $1)`, itTenant)
	// generation_jobs <-> cost_reservations is a circular FK: break the
	// job->reservation link before deleting the reservations.
	exec(`UPDATE generation_jobs SET cost_reservation_id = NULL WHERE tenant_id = $1`, itTenant)
	exec(`DELETE FROM cost_reservations WHERE tenant_id = $1`, itTenant)
	exec(`DELETE FROM generation_jobs WHERE tenant_id = $1`, itTenant)
	exec(`DELETE FROM cost_budgets WHERE tenant_id = $1`, itTenant)
	exec(`DELETE FROM api_tokens WHERE id = $1`, itTokenID)
	exec(`DELETE FROM style_profiles WHERE tenant_id = $1`, itTenant)
}

func seedFixtures(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`INSERT INTO api_tokens (id, tenant_id, token_prefix, token_hash, name, owner_type, scopes, environment, status)
		 VALUES ($1, $2, $3, 'h', 't', 'tenant', ARRAY['images:write','jobs:read','images:read'], 'dev', 'active')`,
		itTokenID, itTenant, "dci_it_jobs",
	); err != nil {
		t.Fatalf("seed token: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO style_profiles (id, tenant_id, name, style_mode, positive_prompt, default_quality_tier, status)
		 VALUES ($1, $2, 'it', 'open_prompt', 'p', 'standard', 'active')`,
		itStyleID, itTenant,
	); err != nil {
		t.Fatalf("seed style: %v", err)
	}
}

func openTestStorage(t *testing.T) storage.Storage {
	t.Helper()
	bucket := os.Getenv("S3_BUCKET")
	if bucket == "" {
		t.Skip("S3_BUCKET not set; skipping S3-backed test")
	}
	store, err := storage.NewS3Storage(context.Background(), storage.S3Config{
		Bucket:          bucket,
		Region:          os.Getenv("S3_REGION"),
		Endpoint:        os.Getenv("S3_ENDPOINT"),
		AccessKeyID:     os.Getenv("S3_ACCESS_KEY_ID"),
		SecretAccessKey: os.Getenv("S3_SECRET_ACCESS_KEY"),
		UsePathStyle:    true,
	})
	if err != nil {
		t.Fatalf("NewS3Storage: %v", err)
	}
	return store
}

// recordingEnqueuer satisfies jobs.Enqueuer and lets tests assert exactly
// how many tasks were placed on the queue. The handler exercises the
// jobs.Service flow against the real database; the queue is in-process.
type recordingEnqueuer struct {
	mu         sync.Mutex
	jobIDs     []string
	packJobIDs []string
	failOn     map[string]bool
}

func newRecordingEnqueuer() *recordingEnqueuer {
	return &recordingEnqueuer{failOn: map[string]bool{}}
}

func (e *recordingEnqueuer) EnqueueGenerateArtifact(_ context.Context, jobID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.failOn[jobID] {
		return errors.New("forced enqueue failure")
	}
	if e.failOn["*"] {
		return errors.New("forced enqueue failure (all)")
	}
	e.jobIDs = append(e.jobIDs, jobID)
	return nil
}

func (e *recordingEnqueuer) EnqueueGeneratePack(_ context.Context, jobID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.failOn[jobID] || e.failOn["*"] {
		return errors.New("forced enqueue failure")
	}
	e.packJobIDs = append(e.packJobIDs, jobID)
	return nil
}

func (e *recordingEnqueuer) packSnapshot() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.packJobIDs))
	copy(out, e.packJobIDs)
	return out
}

func (e *recordingEnqueuer) Close() error { return nil }

func (e *recordingEnqueuer) snapshot() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.jobIDs))
	copy(out, e.jobIDs)
	return out
}

func mountTestRouter(svc jobs.Creator, stylesRepo styles.Repository, jobsRepo jobs.Repository) *chi.Mux {
	h := handlers.NewArtifactsHandler(svc, stylesRepo, config.ProviderMock)
	jobsH := handlers.NewJobsHandler(jobsRepo)
	r := chi.NewRouter()
	r.Post("/v1/artifacts/{artifact_id}/generate", h.Generate)
	r.Get("/v1/jobs/{job_id}", jobsH.Get)
	return r
}

func sendArtifactRequest(t *testing.T, r http.Handler, body map[string]any, idemKey string) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/artifacts/art_int/generate", strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	ctx := auth.ContextWithPrincipal(req.Context(), &auth.Principal{
		TokenID:  itTokenID,
		TenantID: itTenant,
		Scopes:   []string{"images:write"},
	})
	ctx = telemetry.ContextWithRequestID(ctx, "req_test")
	ctx = telemetry.ContextWithRequestLog(ctx, &telemetry.RequestLog{})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestEndToEndArtifactGeneration(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	store := openTestStorage(t)

	jobsRepo := jobs.NewRepository(pool)
	assetsRepo := assets.NewRepository(pool)
	stylesRepo := styles.NewRepository(pool)
	enq := newRecordingEnqueuer()
	svc := jobs.NewService(pool, enq, cost.NewService(nil))
	r := mountTestRouter(svc, stylesRepo, jobsRepo)

	rec := sendArtifactRequest(t, r, map[string]any{
		"world_id":         "w1",
		"style_profile_id": itStyleID,
		"description":      "A bronze key",
	}, "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	jobID, _ := resp["job_id"].(string)
	if jobID == "" {
		t.Fatalf("expected job_id in response: %v", resp)
	}

	// Drive the worker synchronously against the same DB + storage.
	worker := &jobs.Worker{
		Jobs:     jobsRepo,
		Assets:   assetsRepo,
		Storage:  store,
		Provider: mock.New(),
	}
	if err := worker.Process(context.Background(), jobID, 0); err != nil {
		t.Fatalf("worker process: %v", err)
	}

	// Poll the job.
	getReq := httptest.NewRequest(http.MethodGet, "/v1/jobs/"+jobID, nil)
	getReq = getReq.WithContext(auth.ContextWithPrincipal(
		telemetry.ContextWithRequestLog(telemetry.ContextWithRequestID(getReq.Context(), "req_test"), &telemetry.RequestLog{}),
		&auth.Principal{TokenID: itTokenID, TenantID: itTenant, Scopes: []string{"jobs:read"}},
	))
	getRec := httptest.NewRecorder()
	r.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET job expected 200, got %d body=%s", getRec.Code, getRec.Body.String())
	}
	var jobBody map[string]any
	_ = json.Unmarshal(getRec.Body.Bytes(), &jobBody)
	if jobBody["status"] != "completed" {
		t.Fatalf("expected job status=completed, got %v", jobBody["status"])
	}
	finalIDs, _ := jobBody["final_asset_ids"].([]any)
	if len(finalIDs) != 1 {
		t.Fatalf("expected 1 final_asset_id, got %v", finalIDs)
	}

	// Verify visual_assets row has three URLs and carries the mock provider
	// provenance (provider_id=mock, model_id=pm_mock_v1 — the seeded model the
	// pricing path resolves against).
	var lowURL, highURL, thumbURL *string
	var providerID, modelID *string
	if err := pool.QueryRow(context.Background(),
		`SELECT low_res_url, high_res_url, thumbnail_url, provider_id, model_id FROM visual_assets WHERE id = $1`,
		finalIDs[0],
	).Scan(&lowURL, &highURL, &thumbURL, &providerID, &modelID); err != nil {
		t.Fatalf("read asset row: %v", err)
	}
	if lowURL == nil || highURL == nil || thumbURL == nil {
		t.Fatalf("expected three URLs populated, got low=%v high=%v thumb=%v", lowURL, highURL, thumbURL)
	}
	if providerID == nil || *providerID != "mock" {
		t.Fatalf("expected provider_id=mock, got %v", providerID)
	}
	if modelID == nil || *modelID != "pm_mock_v1" {
		t.Fatalf("expected model_id=pm_mock_v1, got %v", modelID)
	}
}

func TestIdempotencyConcurrentRequestsCreateExactlyOneJob(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)

	jobsRepo := jobs.NewRepository(pool)
	stylesRepo := styles.NewRepository(pool)
	enq := newRecordingEnqueuer()
	svc := jobs.NewService(pool, enq, cost.NewService(nil))
	r := mountTestRouter(svc, stylesRepo, jobsRepo)

	body := map[string]any{
		"world_id":         "w1",
		"style_profile_id": itStyleID,
		"description":      "Concurrent test",
	}
	const idemKey = "phase3-concurrent-1"
	const N = 8

	results := make([]*httptest.ResponseRecorder, N)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i] = sendArtifactRequest(t, r, body, idemKey)
		}(i)
	}
	close(start)
	wg.Wait()

	jobIDs := map[string]struct{}{}
	for i, rec := range results {
		if rec.Code != http.StatusAccepted {
			t.Fatalf("worker %d: expected 202, got %d body=%s", i, rec.Code, rec.Body.String())
		}
		var resp map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		if id, ok := resp["job_id"].(string); ok {
			jobIDs[id] = struct{}{}
		}
	}
	if len(jobIDs) != 1 {
		t.Fatalf("expected all concurrent requests to converge on one job_id, got %d distinct ids: %v", len(jobIDs), jobIDs)
	}

	// generation_jobs must have exactly one row for this tenant.
	var jobCount int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM generation_jobs WHERE tenant_id = $1`, itTenant).Scan(&jobCount); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if jobCount != 1 {
		t.Fatalf("expected exactly one generation_jobs row, got %d", jobCount)
	}

	// idempotency_keys must also have exactly one row.
	var idemCount int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM idempotency_keys WHERE token_id = $1`, itTokenID).Scan(&idemCount); err != nil {
		t.Fatalf("count idem: %v", err)
	}
	if idemCount != 1 {
		t.Fatalf("expected exactly one idempotency_keys row, got %d", idemCount)
	}

	// Exactly one enqueue.
	if got := enq.snapshot(); len(got) != 1 {
		t.Fatalf("expected exactly one enqueue across concurrent requests, got %d: %v", len(got), got)
	}
}

func TestIdempotencyDifferentBodyReturns409(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)

	jobsRepo := jobs.NewRepository(pool)
	stylesRepo := styles.NewRepository(pool)
	enq := newRecordingEnqueuer()
	svc := jobs.NewService(pool, enq, cost.NewService(nil))
	r := mountTestRouter(svc, stylesRepo, jobsRepo)

	first := sendArtifactRequest(t, r, map[string]any{
		"world_id": "w1", "style_profile_id": itStyleID, "description": "A bronze key",
	}, "phase3-409-body")
	if first.Code != http.StatusAccepted {
		t.Fatalf("first: expected 202, got %d", first.Code)
	}

	second := sendArtifactRequest(t, r, map[string]any{
		"world_id": "w1", "style_profile_id": itStyleID, "description": "A silver key",
	}, "phase3-409-body")
	if second.Code != http.StatusConflict {
		t.Fatalf("second: expected 409, got %d body=%s", second.Code, second.Body.String())
	}
}

func TestEnqueueFailureMarksJobFailedAndReturns500(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)

	jobsRepo := jobs.NewRepository(pool)
	stylesRepo := styles.NewRepository(pool)
	enq := newRecordingEnqueuer()
	enq.failOn["*"] = true
	svc := jobs.NewService(pool, enq, cost.NewService(nil))
	r := mountTestRouter(svc, stylesRepo, jobsRepo)

	rec := sendArtifactRequest(t, r, map[string]any{
		"world_id": "w1", "style_profile_id": itStyleID, "description": "Will fail to enqueue",
	}, "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 on enqueue failure, got %d body=%s", rec.Code, rec.Body.String())
	}

	// One job row was created but it should be in status=failed, not queued.
	var status string
	var errorCode *string
	if err := pool.QueryRow(context.Background(),
		`SELECT status, error_code FROM generation_jobs WHERE tenant_id = $1 ORDER BY created_at DESC LIMIT 1`,
		itTenant,
	).Scan(&status, &errorCode); err != nil {
		t.Fatalf("read job: %v", err)
	}
	if status != "failed" {
		t.Fatalf("expected status=failed after enqueue failure, got %q", status)
	}
	if errorCode == nil || *errorCode != "enqueue_failed" {
		t.Fatalf("expected error_code=enqueue_failed, got %v", errorCode)
	}
}

// TestIdempotencyReplayEchoesCurrentJobStatus pins the end-to-end behavior:
// after an enqueue failure, the original 500 leaves the job at status=failed.
// A replay of the same Idempotency-Key must not lie about the job's status —
// it must return 202 with status=failed and the same job_id, never create a
// new job, and never enqueue.
func TestIdempotencyReplayEchoesCurrentJobStatus(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)

	jobsRepo := jobs.NewRepository(pool)
	stylesRepo := styles.NewRepository(pool)
	enq := newRecordingEnqueuer()
	enq.failOn["*"] = true
	svc := jobs.NewService(pool, enq, cost.NewService(nil))
	r := mountTestRouter(svc, stylesRepo, jobsRepo)

	body := map[string]any{
		"world_id": "w1", "style_profile_id": itStyleID, "description": "A bronze key",
	}
	const key = "phase3-replay-failed-integration"

	first := sendArtifactRequest(t, r, body, key)
	if first.Code != http.StatusInternalServerError {
		t.Fatalf("first: expected 500 on forced enqueue failure, got %d body=%s", first.Code, first.Body.String())
	}

	// Inspect the failed job to capture its id for cross-checking.
	var jobID, jobStatus string
	if err := pool.QueryRow(context.Background(),
		`SELECT id, status FROM generation_jobs WHERE tenant_id = $1 ORDER BY created_at DESC LIMIT 1`,
		itTenant,
	).Scan(&jobID, &jobStatus); err != nil {
		t.Fatalf("read job: %v", err)
	}
	if jobStatus != "failed" {
		t.Fatalf("expected status=failed after enqueue failure, got %q", jobStatus)
	}

	// Replay with the same key + body + endpoint.
	second := sendArtifactRequest(t, r, body, key)
	if second.Code != http.StatusAccepted {
		t.Fatalf("replay: expected 202, got %d body=%s", second.Code, second.Body.String())
	}
	var replayBody map[string]any
	_ = json.Unmarshal(second.Body.Bytes(), &replayBody)
	if replayBody["job_id"] != jobID {
		t.Fatalf("replay: expected same job_id=%s, got %v", jobID, replayBody["job_id"])
	}
	if replayBody["status"] != "failed" {
		t.Fatalf("replay: expected status=failed (the job's live status), got %v", replayBody["status"])
	}

	// No new job row, no second enqueue attempt (the first call exhausted
	// the failOn["*"] path; the recorder still has zero successful
	// enqueues recorded).
	var jobCount int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM generation_jobs WHERE tenant_id = $1`, itTenant).Scan(&jobCount); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if jobCount != 1 {
		t.Fatalf("expected exactly one job row across original + replay, got %d", jobCount)
	}
	if got := enq.snapshot(); len(got) != 0 {
		t.Fatalf("expected zero successful enqueues, got %d: %v", len(got), got)
	}
}
