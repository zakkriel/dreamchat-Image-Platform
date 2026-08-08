//go:build integration

package jobs_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zakkriel/drchat-image-platform/internal/assets"
	"github.com/zakkriel/drchat-image-platform/internal/cost"
	"github.com/zakkriel/drchat-image-platform/internal/http/handlers"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
	"github.com/zakkriel/drchat-image-platform/internal/providers"
	"github.com/zakkriel/drchat-image-platform/internal/providers/bfl"
	"github.com/zakkriel/drchat-image-platform/internal/providers/mock"
	"github.com/zakkriel/drchat-image-platform/internal/providers/routing"
	"github.com/zakkriel/drchat-image-platform/internal/styles"
)

// readResolvedProvenance loads the (provider_id, model_id, provider_route_id)
// the worker stamped on the single visual asset for a job.
func readResolvedProvenance(t *testing.T, pool *pgxpool.Pool, jobID string) (provider, model, route string) {
	t.Helper()
	row := pool.QueryRow(context.Background(),
		`SELECT va.provider_id, va.model_id, va.provider_route_id
		   FROM visual_assets va
		  WHERE va.generation_job_id = $1`, jobID)
	var p, m, r *string
	if err := row.Scan(&p, &m, &r); err != nil {
		t.Fatalf("scan provenance: %v", err)
	}
	deref := func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	}
	return deref(p), deref(m), deref(r)
}

// TestEndToEndStampsResolvedMockProvenance: a mock-routed artifact resolves the
// seeded mock route, the worker stamps it onto the asset, and the cost
// reservation was priced from the resolved (mock) model.
func TestEndToEndStampsResolvedMockProvenance(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	store := openTestStorage(t)

	jobsRepo := jobs.NewRepository(pool)
	assetsRepo := assets.NewRepository(pool)
	stylesRepo := styles.NewRepository(pool)
	svc := jobs.NewService(pool, newRecordingEnqueuer(), cost.NewService(nil)).WithFinalizer(cost.NewLifecycle(pool, nil))
	r := mountTestRouter(pool, svc, stylesRepo, jobsRepo, assetsRepo)

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
	// The resolved model price (mock = 0.0100) must be echoed as the estimate.
	if resp["estimated_cost_usd"] != "0.0100" {
		t.Fatalf("expected estimate from resolved mock model, got %v", resp["estimated_cost_usd"])
	}

	worker := &jobs.Worker{Jobs: jobsRepo, Assets: assetsRepo, Storage: store, Providers: registryFor(mock.New())}
	if err := worker.Process(context.Background(), jobID, 0); err != nil {
		t.Fatalf("worker process: %v", err)
	}

	provider, model, route := readResolvedProvenance(t, pool, jobID)
	if provider != "mock" || model != "pm_mock_v1" || route != "route_mock_text_to_image_standard" {
		t.Fatalf("asset provenance mismatch: provider=%q model=%q route=%q", provider, model, route)
	}

	// The persisted job payload carries the same resolved route the worker used.
	job, err := jobsRepo.GetByID(context.Background(), jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.InputPayload["provider_id"] != "mock" || job.InputPayload["model_id"] != "pm_mock_v1" {
		t.Fatalf("job payload missing resolved route: %+v", job.InputPayload)
	}
}

// TestNoRouteCreatesNoJobOrReservation: when no provider is available the
// resolver fails before cost reservation, so no job, reservation, or enqueue
// happens.
func TestNoRouteCreatesNoJobOrReservation(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)

	assetsRepo := assets.NewRepository(pool)
	stylesRepo := styles.NewRepository(pool)
	enq := newRecordingEnqueuer()
	svc := jobs.NewService(pool, enq, cost.NewService(nil))

	// Resolver with NO available providers → provider_unavailable_for_route.
	emptyResolver := routing.NewResolver(routing.NewDBRouteSource(pool), map[string]bool{})
	h := handlers.NewArtifactsHandler(svc, stylesRepo, emptyResolver, "", assetsRepo)
	r := chi.NewRouter()
	r.Post("/v1/artifacts/{artifact_id}/generate", h.Generate)

	rec := sendArtifactRequest(t, r, map[string]any{
		"world_id":         "w1",
		"style_profile_id": itStyleID,
		"description":      "no route please",
	}, "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d body=%s", rec.Code, rec.Body.String())
	}

	var jobCount, resvCount int
	_ = pool.QueryRow(context.Background(),
		`SELECT count(*) FROM generation_jobs WHERE tenant_id=$1`, itTenant).Scan(&jobCount)
	_ = pool.QueryRow(context.Background(),
		`SELECT count(*) FROM cost_reservations WHERE tenant_id=$1`, itTenant).Scan(&resvCount)
	if jobCount != 0 || resvCount != 0 {
		t.Fatalf("expected no job/reservation, got jobs=%d reservations=%d", jobCount, resvCount)
	}
	if len(enq.snapshot()) != 0 {
		t.Fatalf("expected nothing enqueued, got %v", enq.snapshot())
	}
}

// TestBFLRouteEndToEnd: with BFL available + preferred, the request resolves the
// seeded BFL route, prices the BFL model, and the worker drives a stubbed BFL
// adapter, stamping bfl/pm_bfl_flux_pro_11 onto the asset.
func TestBFLRouteEndToEnd(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	store := openTestStorage(t)

	jobsRepo := jobs.NewRepository(pool)
	assetsRepo := assets.NewRepository(pool)
	stylesRepo := styles.NewRepository(pool)
	svc := jobs.NewService(pool, newRecordingEnqueuer(), cost.NewService(nil)).WithFinalizer(cost.NewLifecycle(pool, nil))

	// Resolver + handler with BFL available and preferred.
	resolver := routing.NewResolver(routing.NewDBRouteSource(pool), map[string]bool{"mock": true, "bfl": true})
	h := handlers.NewArtifactsHandler(svc, stylesRepo, resolver, "bfl", assetsRepo)
	r := chi.NewRouter()
	r.Post("/v1/artifacts/{artifact_id}/generate", h.Generate)

	rec := sendArtifactRequest(t, r, map[string]any{
		"world_id":         "w1",
		"style_profile_id": itStyleID,
		"description":      "a misty harbor",
	}, "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	jobID, _ := resp["job_id"].(string)
	if resp["estimated_cost_usd"] != "0.0400" {
		t.Fatalf("expected estimate from resolved BFL model (0.0400), got %v", resp["estimated_cost_usd"])
	}

	// Worker with a BFL adapter backed by a stub HTTP client (no real network).
	bflAdapter := bfl.New("test-key",
		bfl.WithHTTPClient(newBFLStub(t)),
		bfl.WithBaseURL("https://bfl.test"),
		bfl.WithPollInterval(time.Millisecond),
		bfl.WithTimeout(5*time.Second),
	)
	reg := providers.NewRegistry()
	reg.Register("bfl", bflAdapter)
	worker := &jobs.Worker{Jobs: jobsRepo, Assets: assetsRepo, Storage: store, Providers: reg}
	if err := worker.Process(context.Background(), jobID, 0); err != nil {
		t.Fatalf("worker process: %v", err)
	}

	provider, model, route := readResolvedProvenance(t, pool, jobID)
	if provider != "bfl" || model != "pm_bfl_flux_pro_11" || route != "route_bfl_text_to_image_standard" {
		t.Fatalf("asset provenance mismatch: provider=%q model=%q route=%q", provider, model, route)
	}
}

// bflStub models the BFL submit → poll(ready) → download(PNG) flow.
type bflStub struct {
	png []byte
}

func newBFLStub(t *testing.T) *bflStub {
	t.Helper()
	return &bflStub{png: tinyITPNG(t)}
}

func (s *bflStub) Do(req *http.Request) (*http.Response, error) {
	url := req.URL.String()
	switch {
	case strings.Contains(url, "/v1/flux-pro-1.1"):
		return jsonResponse(`{"id":"req-it","polling_url":"https://bfl.test/poll?id=req-it"}`), nil
	case strings.Contains(url, "/poll"):
		return jsonResponse(`{"id":"req-it","status":"Ready","result":{"sample":"https://cdn.bfl.test/it.png"}}`), nil
	default: // download
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(bytes.NewReader(s.png)),
			Header:     http.Header{"Content-Type": []string{"image/png"}},
		}, nil
	}
}

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}

func tinyITPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 10, G: 20, B: 30, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}
