package ratelimit

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zakkriel/drchat-image-platform/internal/auth"
	"github.com/zakkriel/drchat-image-platform/internal/telemetry"
)

func init() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(io.Discard, nil)))
}

// reqWithPrincipal builds a request carrying an authenticated principal with the
// given limits, plus the telemetry context httperr.Write needs.
func reqWithPrincipal(limits auth.Limits) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/v1/styles", nil)
	ctx := auth.ContextWithPrincipal(req.Context(), &auth.Principal{
		TokenID:  "tok_mw",
		TenantID: "tenant_mw",
		Scopes:   []string{"styles:read"},
		Limits:   limits,
	})
	ctx = telemetry.ContextWithRequestID(ctx, "req_test")
	return req.WithContext(ctx)
}

func okHandler(called *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*called = true
		w.WriteHeader(http.StatusOK)
	})
}

func TestMiddlewareDisabledPassesThrough(t *testing.T) {
	called := false
	h := Middleware(nil)(okHandler(&called))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, reqWithPrincipal(auth.DefaultLimits()))
	if !called || rec.Code != http.StatusOK {
		t.Fatalf("disabled limiter must pass through, got code=%d called=%v", rec.Code, called)
	}
}

func TestMiddlewareNoPrincipalPassesThrough(t *testing.T) {
	// Auth is responsible for rejecting unauthenticated requests; the limiter
	// must not 500 or 429 a request that lacks a principal — it passes through.
	called := false
	l := limiterAt(newFakeStore(), time.Now())
	h := Middleware(l)(okHandler(&called))
	req := httptest.NewRequest(http.MethodGet, "/v1/styles", nil)
	req = req.WithContext(telemetry.ContextWithRequestID(context.Background(), "req_test"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !called || rec.Code != http.StatusOK {
		t.Fatalf("missing principal must pass through, got code=%d called=%v", rec.Code, called)
	}
}

func TestMiddlewareUnderLimitReachesHandlerWithHeaders(t *testing.T) {
	called := false
	l := limiterAt(newFakeStore(), time.Date(2026, 6, 14, 12, 0, 30, 0, time.UTC))
	h := Middleware(l)(okHandler(&called))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, reqWithPrincipal(auth.Limits{RequestsPerMinute: 60, RequestsPerHour: 1000}))

	if !called || rec.Code != http.StatusOK {
		t.Fatalf("under-limit request must reach handler, got code=%d called=%v", rec.Code, called)
	}
	if got := rec.Header().Get(HeaderRPM); got != "60" {
		t.Fatalf("expected %s=60 on allow, got %q", HeaderRPM, got)
	}
	if got := rec.Header().Get(HeaderRPMRemaining); got != "59" {
		t.Fatalf("expected %s=59 on allow, got %q", HeaderRPMRemaining, got)
	}
	if rec.Header().Get(HeaderRPHReset) == "" {
		t.Fatalf("expected %s set on allow", HeaderRPHReset)
	}
}

func TestMiddlewareOverLimitReturns429(t *testing.T) {
	l := limiterAt(newFakeStore(), time.Date(2026, 6, 14, 12, 0, 30, 0, time.UTC))
	limits := auth.Limits{RequestsPerMinute: 1, RequestsPerHour: 1000}

	called := false
	h := Middleware(l)(okHandler(&called))

	// First request consumes the single slot.
	h.ServeHTTP(httptest.NewRecorder(), reqWithPrincipal(limits))

	// Second is denied.
	rec := httptest.NewRecorder()
	called = false
	h.ServeHTTP(rec, reqWithPrincipal(limits))

	if called {
		t.Fatal("over-limit request must not reach the handler")
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Fatalf("expected application/problem+json, got %q", ct)
	}
	if !strings.Contains(rec.Body.String(), `"code":"rate_limit_exceeded"`) {
		t.Fatalf("expected rate_limit_exceeded body, got %s", rec.Body.String())
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("expected Retry-After on a request-rate denial")
	}
	if rec.Header().Get(HeaderRPM) == "" {
		t.Fatal("expected X-RateLimit headers on a denial too")
	}
}

func TestMiddlewareRedisFailureFailsOpen(t *testing.T) {
	store := newFakeStore()
	store.err = errors.New("redis unreachable")
	l := limiterAt(store, time.Now())

	called := false
	h := Middleware(l)(okHandler(&called))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, reqWithPrincipal(auth.DefaultLimits()))

	if !called || rec.Code != http.StatusOK {
		t.Fatalf("Redis failure must fail open (allow), got code=%d called=%v", rec.Code, called)
	}
	// Headers are omitted when we fail open.
	if rec.Header().Get(HeaderRPM) != "" {
		t.Fatal("rate-limit headers must be omitted on fail-open")
	}
}

// TestMiddlewareAdminThrottledUnlessOverride proves admin endpoints are
// throttled by default but a higher per-token override lifts the cap. The
// middleware is endpoint-agnostic; "admin" here is simply a token whose
// override raises the limit.
func TestMiddlewareAdminThrottledUnlessOverride(t *testing.T) {
	at := time.Date(2026, 6, 14, 12, 0, 30, 0, time.UTC)

	// Default-limited admin token: second request over a tight cap is denied.
	tight := limiterAt(newFakeStore(), at)
	tightLimits := auth.Limits{RequestsPerMinute: 1, RequestsPerHour: 1000}
	hTight := Middleware(tight)(okHandler(new(bool)))
	hTight.ServeHTTP(httptest.NewRecorder(), reqWithPrincipal(tightLimits))
	recDenied := httptest.NewRecorder()
	hTight.ServeHTTP(recDenied, reqWithPrincipal(tightLimits))
	if recDenied.Code != http.StatusTooManyRequests {
		t.Fatalf("admin without override must be throttled, got %d", recDenied.Code)
	}

	// Admin token pinned higher: the same second request is allowed.
	loose := limiterAt(newFakeStore(), at)
	looseLimits := auth.Limits{RequestsPerMinute: 100, RequestsPerHour: 10000}
	called := false
	hLoose := Middleware(loose)(okHandler(&called))
	hLoose.ServeHTTP(httptest.NewRecorder(), reqWithPrincipal(looseLimits))
	recOK := httptest.NewRecorder()
	hLoose.ServeHTTP(recOK, reqWithPrincipal(looseLimits))
	if recOK.Code != http.StatusOK {
		t.Fatalf("admin with higher override must not be throttled, got %d", recOK.Code)
	}
}
