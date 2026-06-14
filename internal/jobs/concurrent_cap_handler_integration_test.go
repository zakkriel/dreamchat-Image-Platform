//go:build integration

package jobs_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"

	"github.com/zakkriel/drchat-image-platform/internal/assets"
	"github.com/zakkriel/drchat-image-platform/internal/auth"
	"github.com/zakkriel/drchat-image-platform/internal/cost"
	"github.com/zakkriel/drchat-image-platform/internal/http/handlers"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
	"github.com/zakkriel/drchat-image-platform/internal/ratelimit"
	"github.com/zakkriel/drchat-image-platform/internal/styles"
	"github.com/zakkriel/drchat-image-platform/internal/telemetry"
)

func redisAddrForTest(t *testing.T) string {
	t.Helper()
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		t.Skip("REDIS_ADDR not set; skipping Redis-backed rate-limit test")
	}
	return addr
}

// flushTokenKeys clears the test token's fixed-window counters so repeated runs
// start from a clean window.
func flushTokenKeys(t *testing.T, client *redis.Client) {
	t.Helper()
	ctx := context.Background()
	iter := client.Scan(ctx, 0, "rate_limit:token:"+itTokenID+":*", 100).Iterator()
	for iter.Next(ctx) {
		client.Del(ctx, iter.Val())
	}
	if err := iter.Err(); err != nil {
		t.Fatalf("flush token keys: %v", err)
	}
}

// sendRateLimited sends an artifact request whose principal carries an explicit
// per-minute request-rate cap so the rate-limit middleware enforces it.
func sendRateLimited(t *testing.T, r http.Handler, path string, body map[string]any, rpm int) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	ctx := auth.ContextWithPrincipal(req.Context(), &auth.Principal{
		TokenID:  itTokenID,
		TenantID: itTenant,
		Scopes:   []string{"images:write"},
		Limits:   auth.Limits{RequestsPerMinute: rpm, RequestsPerHour: 1000, MaxConcurrentJobs: 50},
	})
	ctx = telemetry.ContextWithRequestLog(telemetry.ContextWithRequestID(ctx, "req_test"), &telemetry.RequestLog{})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// sendCapped sends an artifact request whose principal carries an explicit
// per-token concurrent cap, so the handler threads MaxConcurrentJobs into the
// create params exactly as the production auth path would.
func sendCapped(t *testing.T, r http.Handler, method, path string, body map[string]any, maxConcurrent int) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	ctx := auth.ContextWithPrincipal(req.Context(), &auth.Principal{
		TokenID:  itTokenID,
		TenantID: itTenant,
		Scopes:   []string{"images:write"},
		Limits:   auth.Limits{RequestsPerMinute: 60, RequestsPerHour: 1000, MaxConcurrentJobs: maxConcurrent},
	})
	ctx = telemetry.ContextWithRequestLog(telemetry.ContextWithRequestID(ctx, "req_test"), &telemetry.RequestLog{})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func assertConcurrentDenied(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"code":"concurrent_jobs_exceeded"`) {
		t.Fatalf("expected concurrent_jobs_exceeded body, got %s", rec.Body.String())
	}
	if rec.Header().Get("X-RateLimit-Concurrent-Jobs") == "" {
		t.Fatalf("expected X-RateLimit-Concurrent-Jobs header on denial")
	}
	if rec.Header().Get("Retry-After") != "" {
		t.Fatalf("Retry-After must NOT be set on a concurrent-job denial")
	}
}

func TestArtifactGenerationAtCapReturns429(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)

	for i := 0; i < 3; i++ {
		seedJobStatus(t, pool, "queued")
	}
	jobsBefore := countScalar(t, pool, `SELECT count(*) FROM generation_jobs WHERE tenant_id = $1`, itTenant)

	enq := newRecordingEnqueuer()
	svc := jobs.NewService(pool, enq, cost.NewService(nil))
	h := handlers.NewArtifactsHandler(svc, styles.NewRepository(pool), itResolver(pool), "mock", assets.NewRepository(pool))
	r := chi.NewRouter()
	r.Post("/v1/artifacts/{artifact_id}/generate", h.Generate)

	rec := sendCapped(t, r, http.MethodPost, "/v1/artifacts/art_cap/generate",
		map[string]any{"world_id": "w1", "style_profile_id": itStyleID, "description": "capped"}, 3)
	assertConcurrentDenied(t, rec)

	if got := countScalar(t, pool, `SELECT count(*) FROM generation_jobs WHERE tenant_id = $1`, itTenant); got != jobsBefore {
		t.Fatalf("handler denial must create no job: had %d, now %d", jobsBefore, got)
	}
	if got := len(enq.snapshot()); got != 0 {
		t.Fatalf("handler denial must not enqueue, got %d", got)
	}
}

func TestArtifactUnderCapReservesAndEnqueues(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)

	enq := newRecordingEnqueuer()
	svc := jobs.NewService(pool, enq, cost.NewService(nil))
	h := handlers.NewArtifactsHandler(svc, styles.NewRepository(pool), itResolver(pool), "mock", assets.NewRepository(pool))
	r := chi.NewRouter()
	r.Post("/v1/artifacts/{artifact_id}/generate", h.Generate)

	rec := sendCapped(t, r, http.MethodPost, "/v1/artifacts/art_ok/generate",
		map[string]any{"world_id": "w1", "style_profile_id": itStyleID, "description": "under cap"}, 5)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("under cap: expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-RateLimit-Concurrent-Jobs"); got != "5" {
		t.Fatalf("expected X-RateLimit-Concurrent-Jobs=5 on accept, got %q", got)
	}
	if got := rec.Header().Get("X-RateLimit-Concurrent-Jobs-Remaining"); got != "4" {
		t.Fatalf("expected remaining=4 after one job, got %q", got)
	}
	if got := len(enq.snapshot()); got != 1 {
		t.Fatalf("expected one enqueue, got %d", got)
	}
}

func TestStylePreviewAtCapReturns429(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)

	for i := 0; i < 2; i++ {
		seedJobStatus(t, pool, "running")
	}

	enq := newRecordingEnqueuer()
	svc := jobs.NewService(pool, enq, cost.NewService(nil))
	h := handlers.NewStylePreviewHandler(svc, styles.NewRepository(pool), itResolver(pool), "mock")
	r := chi.NewRouter()
	r.Post("/v1/styles/{style_id}/preview", h.GeneratePreview)

	rec := sendCapped(t, r, http.MethodPost, "/v1/styles/"+itStyleID+"/preview",
		map[string]any{"world_id": "w1"}, 2)
	assertConcurrentDenied(t, rec)
	if got := len(enq.snapshot()); got != 0 {
		t.Fatalf("style preview denial must not enqueue, got %d", got)
	}
}

func TestPackGenerationAtCapReturns429(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	seedPackIdentities(t, pool)

	for i := 0; i < 2; i++ {
		seedJobStatus(t, pool, "preview_ready")
	}

	enq := newRecordingEnqueuer()
	svc := jobs.NewService(pool, enq, cost.NewService(nil))
	r := mountPackTestRouter(svc, pool, jobs.NewRepository(pool))

	rec := sendCapped(t, r, http.MethodPost, "/v1/characters/"+itCharacterID+"/generate-pack",
		map[string]any{"world_id": "w1", "style_profile_id": itStyleID, "pack_type": "character_starter"}, 2)
	assertConcurrentDenied(t, rec)
	if got := len(enq.packSnapshot()); got != 0 {
		t.Fatalf("pack denial must not enqueue, got %d", got)
	}
}

// TestRequestRateBlocksBeforeHandler proves the request-rate middleware (with a
// real Redis store) denies an over-limit request before the artifact handler
// runs, so no job is created. Uses Redis (REDIS_ADDR).
func TestRequestRateBlocksBeforeHandler(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)

	redisAddr := redisAddrForTest(t)
	client := ratelimit.NewRedisClient(redisAddr, "")
	defer func() { _ = client.Close() }()
	flushTokenKeys(t, client)
	limiter := ratelimit.New(ratelimit.NewRedisStore(client), nil)

	enq := newRecordingEnqueuer()
	svc := jobs.NewService(pool, enq, cost.NewService(nil))
	h := handlers.NewArtifactsHandler(svc, styles.NewRepository(pool), itResolver(pool), "mock", assets.NewRepository(pool))

	// Mount auth-injected principal with RPM=1, then the rate-limit middleware,
	// then the handler — the production order (auth → ratelimit → handler).
	r := chi.NewRouter()
	r.With(ratelimit.Middleware(limiter)).Post("/v1/artifacts/{artifact_id}/generate", h.Generate)

	body := map[string]any{"world_id": "w1", "style_profile_id": itStyleID, "description": "rate"}

	// First request passes the limiter and reaches the handler.
	first := sendRateLimited(t, r, "/v1/artifacts/art_rate/generate", body, 1)
	if first.Code != http.StatusAccepted {
		t.Fatalf("first request expected 202, got %d body=%s", first.Code, first.Body.String())
	}
	jobsAfterFirst := countScalar(t, pool, `SELECT count(*) FROM generation_jobs WHERE tenant_id = $1`, itTenant)

	// Second request is over the per-minute cap → 429 before the handler runs.
	second := sendRateLimited(t, r, "/v1/artifacts/art_rate/generate", body, 1)
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second request expected 429, got %d body=%s", second.Code, second.Body.String())
	}
	if !strings.Contains(second.Body.String(), `"code":"rate_limit_exceeded"`) {
		t.Fatalf("expected rate_limit_exceeded body, got %s", second.Body.String())
	}
	// The denied request created no job (denial happened before handler logic).
	if got := countScalar(t, pool, `SELECT count(*) FROM generation_jobs WHERE tenant_id = $1`, itTenant); got != jobsAfterFirst {
		t.Fatalf("rate-limit denial must create no job: had %d, now %d", jobsAfterFirst, got)
	}
}
