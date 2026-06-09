package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/zakkriel/drchat-image-platform/internal/config"
	"github.com/zakkriel/drchat-image-platform/internal/idempotency"
	"github.com/zakkriel/drchat-image-platform/internal/ids"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
	"github.com/zakkriel/drchat-image-platform/internal/styles"
)

var jobIDRe = regexp.MustCompile(`^job_[0-9a-f]{16}$`)

// stubCreator simulates the jobs.Service contract in-process. It supports
// the idempotency flow the handler depends on: same (token, key, endpoint,
// body) returns the same job_id with Replayed=true; same (token, key) +
// different endpoint or body returns ErrIdempotencyConflict. statusByJobID
// lets tests force a particular live status on replay so they can assert
// the handler reports it instead of hard-coding "queued".
type stubCreator struct {
	mu            sync.Mutex
	calls         []jobs.CreateAndEnqueueParams
	byKey         map[string]storedKey
	statusByJobID map[string]string
	failErr       error
}

type storedKey struct {
	jobID       string
	endpoint    string
	requestHash string
}

func newStubCreator() *stubCreator {
	return &stubCreator{
		byKey:         map[string]storedKey{},
		statusByJobID: map[string]string{},
	}
}

// setReplayStatus forces a particular live status on the next replay of an
// existing (token, key). The handler should echo it instead of "queued".
func (s *stubCreator) setReplayStatus(tokenID, key, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.byKey[tokenID+"|"+key]; ok {
		s.statusByJobID[existing.jobID] = status
	}
}

func (s *stubCreator) CreateAndEnqueue(_ context.Context, params jobs.CreateAndEnqueueParams) (jobs.CreateResult, error) {
	if s.failErr != nil {
		return jobs.CreateResult{}, s.failErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, params)
	if params.IdempotencyKey == "" {
		return jobs.CreateResult{JobID: ids.NewGenerationJobID(), Status: "queued"}, nil
	}
	k := params.RequestedByTokenID + "|" + params.IdempotencyKey
	if existing, ok := s.byKey[k]; ok {
		if existing.endpoint != params.Endpoint || existing.requestHash != params.RequestHash {
			return jobs.CreateResult{}, jobs.ErrIdempotencyConflict
		}
		status := s.statusByJobID[existing.jobID]
		if status == "" {
			status = "queued"
		}
		return jobs.CreateResult{JobID: existing.jobID, Status: status, Replayed: true}, nil
	}
	jobID := ids.NewGenerationJobID()
	s.byKey[k] = storedKey{jobID: jobID, endpoint: params.Endpoint, requestHash: params.RequestHash}
	return jobs.CreateResult{JobID: jobID, Status: "queued"}, nil
}

func newArtifactsRouter(creator jobs.Creator, stylesRepo styles.Repository, provider config.Provider) chi.Router {
	h := NewArtifactsHandler(creator, stylesRepo, provider)
	r := chi.NewRouter()
	r.Post("/v1/artifacts/{artifact_id}/generate", h.Generate)
	return r
}

func sendJSONWithHeaders(t *testing.T, h http.Handler, method, path, tenant string, scopes []string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var buf []byte
	if body != nil {
		if raw, ok := body.(json.RawMessage); ok {
			buf = raw
		} else {
			var err error
			buf, err = json.Marshal(body)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(buf)).WithContext(authedContext(tenant, scopes...))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestArtifactGenerateHappyPath(t *testing.T) {
	creator := newStubCreator()
	stylesRepo := seededStyles()

	router := newArtifactsRouter(creator, stylesRepo, config.ProviderMock)
	body := map[string]any{
		"world_id":         "w1",
		"style_profile_id": "sty_ok",
		"description":      "A bronze key",
	}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_bronze_key/generate",
		tenantA, []string{"images:write"}, body, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	resp := decode[map[string]any](t, rec)
	jobID, _ := resp["job_id"].(string)
	if !jobIDRe.MatchString(jobID) {
		t.Fatalf("expected job_<16 hex>, got %q", jobID)
	}
	if resp["status"] != "queued" {
		t.Fatalf("expected status=queued, got %v", resp["status"])
	}
	if len(creator.calls) != 1 {
		t.Fatalf("expected exactly one service call, got %d", len(creator.calls))
	}
	if creator.calls[0].TenantID != tenantA {
		t.Fatalf("expected tenant_a, got %s", creator.calls[0].TenantID)
	}
	if creator.calls[0].FallbackPolicy != "compatible_only" {
		t.Fatalf("expected fallback_policy=compatible_only, got %q", creator.calls[0].FallbackPolicy)
	}
	if creator.calls[0].CacheResult != "generated_required" {
		t.Fatalf("expected cache_result=generated_required, got %q", creator.calls[0].CacheResult)
	}
	if creator.calls[0].IdempotencyKey != "" {
		t.Fatalf("expected no idempotency key for no-header request, got %q", creator.calls[0].IdempotencyKey)
	}
}

func TestArtifactGenerateNoPriceEntryReturns422(t *testing.T) {
	creator := newStubCreator()
	creator.failErr = jobs.ErrNoPriceEntry
	router := newArtifactsRouter(creator, seededStyles(), config.ProviderMock)
	body := map[string]any{"world_id": "w1", "style_profile_id": "sty_ok", "description": "x"}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_1/generate", tenantA, []string{"images:write"}, body, nil)
	assertError(t, rec, http.StatusUnprocessableEntity, "no_price_entry")
}

func TestArtifactGenerateBudgetExceededReturns422(t *testing.T) {
	creator := newStubCreator()
	creator.failErr = jobs.ErrBudgetExceeded
	router := newArtifactsRouter(creator, seededStyles(), config.ProviderMock)
	body := map[string]any{"world_id": "w1", "style_profile_id": "sty_ok", "description": "x"}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_1/generate", tenantA, []string{"images:write"}, body, nil)
	assertError(t, rec, http.StatusUnprocessableEntity, "budget_exceeded")
}

func TestArtifactGeneratePassesPricingContextAndEchoesEstimate(t *testing.T) {
	creator := &estimatingCreator{}
	router := newArtifactsRouter(creator, seededStyles(), config.ProviderMock)
	body := map[string]any{"world_id": "w1", "style_profile_id": "sty_ok", "description": "x"}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_1/generate", tenantA, []string{"images:write"}, body, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	resp := decode[map[string]any](t, rec)
	if resp["estimated_cost_usd"] != "0.0100" {
		t.Fatalf("expected estimated_cost_usd=0.0100, got %v", resp["estimated_cost_usd"])
	}
	if resp["currency"] != "USD" {
		t.Fatalf("expected currency=USD, got %v", resp["currency"])
	}
	if resp["cost_reservation_id"] != "resv_test" {
		t.Fatalf("expected cost_reservation_id=resv_test, got %v", resp["cost_reservation_id"])
	}
	// The handler must hand the pricing context to the service.
	if creator.got.ProviderID != "mock" || creator.got.ModelID != "pm_mock_v1" ||
		creator.got.OperationType != "text_to_image" || creator.got.Units != 1 {
		t.Fatalf("pricing context not forwarded: %+v", creator.got)
	}
}

// estimatingCreator captures the params and returns a populated cost result.
type estimatingCreator struct {
	got jobs.CreateAndEnqueueParams
}

func (c *estimatingCreator) CreateAndEnqueue(_ context.Context, params jobs.CreateAndEnqueueParams) (jobs.CreateResult, error) {
	c.got = params
	return jobs.CreateResult{
		JobID:             ids.NewGenerationJobID(),
		Status:            "queued",
		EstimatedCostUSD:  "0.0100",
		Currency:          "USD",
		CostReservationID: "resv_test",
	}, nil
}

func TestArtifactGenerateMissingWorldIDReturns400(t *testing.T) {
	router := newArtifactsRouter(newStubCreator(), seededStyles(), config.ProviderMock)
	body := map[string]any{"style_profile_id": "sty_ok", "description": "x"}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_1/generate", tenantA, []string{"images:write"}, body, nil)
	assertError(t, rec, http.StatusBadRequest, "invalid_request")
}

func TestArtifactGenerateMissingStyleReturns400(t *testing.T) {
	router := newArtifactsRouter(newStubCreator(), seededStyles(), config.ProviderMock)
	body := map[string]any{"world_id": "w1", "description": "x"}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_1/generate", tenantA, []string{"images:write"}, body, nil)
	assertError(t, rec, http.StatusBadRequest, "invalid_request")
}

func TestArtifactGenerateMissingDescriptionReturns400(t *testing.T) {
	router := newArtifactsRouter(newStubCreator(), seededStyles(), config.ProviderMock)
	body := map[string]any{"world_id": "w1", "style_profile_id": "sty_ok"}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_1/generate", tenantA, []string{"images:write"}, body, nil)
	assertError(t, rec, http.StatusBadRequest, "invalid_request")
}

func TestArtifactGenerateBodyTenantIDReturns400(t *testing.T) {
	router := newArtifactsRouter(newStubCreator(), seededStyles(), config.ProviderMock)
	body := map[string]any{"tenant_id": "tenant_other", "world_id": "w1", "style_profile_id": "sty_ok", "description": "x"}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_1/generate", tenantA, []string{"images:write"}, body, nil)
	assertError(t, rec, http.StatusBadRequest, "invalid_request")
}

func TestArtifactGenerateUnknownStyleReturns422(t *testing.T) {
	creator := newStubCreator()
	stylesRepo := newStubStylesRepo() // empty
	router := newArtifactsRouter(creator, stylesRepo, config.ProviderMock)
	body := map[string]any{"world_id": "w1", "style_profile_id": "sty_ghost", "description": "x"}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_1/generate", tenantA, []string{"images:write"}, body, nil)
	assertError(t, rec, http.StatusUnprocessableEntity, "invalid_style_profile")
	if len(creator.calls) != 0 {
		t.Fatalf("expected zero service calls on unknown style, got %d", len(creator.calls))
	}
}

func TestArtifactGenerateBFLProviderReturns503BeforeAnyWrites(t *testing.T) {
	creator := newStubCreator()
	router := newArtifactsRouter(creator, seededStyles(), config.ProviderBFL)
	body := map[string]any{"world_id": "w1", "style_profile_id": "sty_ok", "description": "x"}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_1/generate", tenantA, []string{"images:write"},
		body, map[string]string{idempotency.HeaderKey: "phase3-bfl-1"})
	assertError(t, rec, http.StatusServiceUnavailable, "provider_unavailable")
	if len(creator.calls) != 0 {
		t.Fatalf("expected zero service calls when provider unavailable, got %d", len(creator.calls))
	}
}

func TestArtifactGenerateEnqueueFailureReturns500(t *testing.T) {
	creator := newStubCreator()
	creator.failErr = errors.New("wraps: " + jobs.ErrEnqueueFailed.Error())
	// Use the real wrapping pattern so errors.Is works.
	creator.failErr = wrapEnqueueErr()
	router := newArtifactsRouter(creator, seededStyles(), config.ProviderMock)
	body := map[string]any{"world_id": "w1", "style_profile_id": "sty_ok", "description": "x"}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_1/generate", tenantA, []string{"images:write"}, body, nil)
	assertError(t, rec, http.StatusInternalServerError, "internal_error")
}

func wrapEnqueueErr() error {
	return wrap(jobs.ErrEnqueueFailed)
}

func wrap(err error) error {
	return &wrappedErr{err: err}
}

type wrappedErr struct{ err error }

func (w *wrappedErr) Error() string { return "wrapped: " + w.err.Error() }
func (w *wrappedErr) Unwrap() error { return w.err }

func seededStyles() *stubStylesRepo {
	repo := newStubStylesRepo()
	repo.seed(styles.StyleProfile{ID: "sty_ok", TenantID: tenantA, Status: "active"})
	return repo
}
