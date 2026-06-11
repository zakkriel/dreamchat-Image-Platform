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

	"github.com/zakkriel/drchat-image-platform/internal/assets"
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
	mu             sync.Mutex
	calls          []jobs.CreateAndEnqueueParams
	cacheHitCalls  []jobs.CreateCacheHitParams
	packReuseCalls []jobs.CreatePackReuseParams
	byKey          map[string]storedKey
	statusByJobID  map[string]string
	failErr        error
	// cacheHitErr, when set, is returned by CreateCompletedCacheHitJob.
	cacheHitErr error
}

type storedKey struct {
	jobID       string
	packID      string
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
	packID := ""
	if params.AssetPack != nil {
		packID = ids.NewAssetPackID()
	}
	if params.IdempotencyKey == "" {
		return jobs.CreateResult{JobID: ids.NewGenerationJobID(), Status: "queued", AssetPackID: packID}, nil
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
		return jobs.CreateResult{JobID: existing.jobID, Status: status, Replayed: true, AssetPackID: existing.packID}, nil
	}
	jobID := ids.NewGenerationJobID()
	s.byKey[k] = storedKey{jobID: jobID, packID: packID, endpoint: params.Endpoint, requestHash: params.RequestHash}
	return jobs.CreateResult{JobID: jobID, Status: "queued", AssetPackID: packID}, nil
}

// CreateCompletedCacheHitJob records the cache-hit call and mirrors the
// idempotency contract: same (token, key, endpoint, body) returns the same
// job_id, a different endpoint/body returns ErrIdempotencyConflict.
func (s *stubCreator) CreateCompletedCacheHitJob(_ context.Context, params jobs.CreateCacheHitParams) (jobs.CreateResult, error) {
	if s.cacheHitErr != nil {
		return jobs.CreateResult{}, s.cacheHitErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cacheHitCalls = append(s.cacheHitCalls, params)
	result := func(jobID string) jobs.CreateResult {
		return jobs.CreateResult{
			JobID:         jobID,
			Status:        "completed",
			CacheResult:   "exact_match",
			FinalAssetIDs: []string{params.FinalAssetID},
		}
	}
	if params.IdempotencyKey == "" {
		return result(ids.NewGenerationJobID()), nil
	}
	k := params.RequestedByTokenID + "|" + params.IdempotencyKey
	if existing, ok := s.byKey[k]; ok {
		if existing.endpoint != params.Endpoint || existing.requestHash != params.RequestHash {
			return jobs.CreateResult{}, jobs.ErrIdempotencyConflict
		}
		return result(existing.jobID), nil
	}
	jobID := ids.NewGenerationJobID()
	s.byKey[k] = storedKey{jobID: jobID, endpoint: params.Endpoint, requestHash: params.RequestHash}
	return result(jobID), nil
}

// CreateCompletedPackReuseJob mirrors the all-hits pack reuse idempotency
// contract: same (token, key, endpoint, body) returns the same job_id, a
// different endpoint/body returns ErrIdempotencyConflict. Pack reuse is exercised
// by the pack handler tests; stubCreator implements it to satisfy jobs.Creator.
func (s *stubCreator) CreateCompletedPackReuseJob(_ context.Context, params jobs.CreatePackReuseParams) (jobs.CreateResult, error) {
	if s.failErr != nil {
		return jobs.CreateResult{}, s.failErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.packReuseCalls = append(s.packReuseCalls, params)
	final := make([]string, 0, len(params.ReusedItems))
	for _, item := range params.ReusedItems {
		final = append(final, item.AssetID)
	}
	result := func(jobID, packID string) jobs.CreateResult {
		return jobs.CreateResult{
			JobID:         jobID,
			Status:        "completed",
			CacheResult:   params.CacheResult,
			FinalAssetIDs: final,
			AssetPackID:   packID,
		}
	}
	if params.IdempotencyKey == "" {
		return result(ids.NewGenerationJobID(), ids.NewAssetPackID()), nil
	}
	k := params.RequestedByTokenID + "|" + params.IdempotencyKey
	if existing, ok := s.byKey[k]; ok {
		if existing.endpoint != params.Endpoint || existing.requestHash != params.RequestHash {
			return jobs.CreateResult{}, jobs.ErrIdempotencyConflict
		}
		return result(existing.jobID, existing.packID), nil
	}
	jobID := ids.NewGenerationJobID()
	packID := ids.NewAssetPackID()
	s.byKey[k] = storedKey{jobID: jobID, packID: packID, endpoint: params.Endpoint, requestHash: params.RequestHash}
	return result(jobID, packID), nil
}

func newArtifactsRouter(creator jobs.Creator, stylesRepo styles.Repository, provider config.Provider) chi.Router {
	return newArtifactsRouterWithReuse(creator, stylesRepo, provider, nil)
}

func newArtifactsRouterWithReuse(creator jobs.Creator, stylesRepo styles.Repository, provider config.Provider, reuse ArtifactReuseLookup) chi.Router {
	h := NewArtifactsHandler(creator, stylesRepo, provider, reuse)
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

func (c *estimatingCreator) CreateCompletedPackReuseJob(_ context.Context, _ jobs.CreatePackReuseParams) (jobs.CreateResult, error) {
	return jobs.CreateResult{}, nil
}

func (c *estimatingCreator) CreateCompletedCacheHitJob(_ context.Context, params jobs.CreateCacheHitParams) (jobs.CreateResult, error) {
	return jobs.CreateResult{
		JobID:         ids.NewGenerationJobID(),
		Status:        "completed",
		CacheResult:   "exact_match",
		FinalAssetIDs: []string{params.FinalAssetID},
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

func artifactStrPtr(s string) *string { return &s }

// seedReadyArtifact registers a ready artifact whose prompt_hash is the render
// hash for the given request fields, so the handler's exact-reuse lookup finds
// it. Returns the asset id.
func seedReadyArtifact(repo *stubAssetsRepo, assetID, worldID, artifactID, description, styleProfileID, qualityTier string) string {
	hash := assets.ArtifactRenderHash(assets.ArtifactHashInput{
		TenantID:       tenantA,
		WorldID:        worldID,
		ArtifactID:     artifactID,
		Description:    description,
		StyleProfileID: styleProfileID,
		QualityTier:    qualityTier,
	})
	repo.seed(assets.VisualAsset{
		ID:             assetID,
		TenantID:       tenantA,
		WorldID:        worldID,
		AssetType:      "artifact",
		VariantKey:     "default",
		StyleProfileID: artifactStrPtr(styleProfileID),
		QualityTier:    qualityTier,
		PromptHash:     artifactStrPtr(hash),
		Status:         "ready",
	})
	return assetID
}

func TestArtifactGenerateExactHitCreatesCompletedJobAndDoesNotEnqueue(t *testing.T) {
	creator := newStubCreator()
	assetsRepo := newStubAssetsRepo()
	existingID := seedReadyArtifact(assetsRepo, "asset_existing1", "w1", "art_bronze_key", "A bronze key", "sty_ok", "standard")

	router := newArtifactsRouterWithReuse(creator, seededStyles(), config.ProviderMock, assetsRepo)
	body := map[string]any{
		"world_id":         "w1",
		"style_profile_id": "sty_ok",
		"description":      "A bronze key",
	}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_bronze_key/generate",
		tenantA, []string{"images:write"}, body, nil)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 on cache hit, got %d body=%s", rec.Code, rec.Body.String())
	}
	// No normal create/reserve/enqueue happened — that path is what reserves
	// cost and enqueues provider work.
	if len(creator.calls) != 0 {
		t.Fatalf("cache hit must not call CreateAndEnqueue (no reservation/enqueue), got %d calls", len(creator.calls))
	}
	if len(creator.cacheHitCalls) != 1 {
		t.Fatalf("expected exactly one cache-hit job creation, got %d", len(creator.cacheHitCalls))
	}
	if got := creator.cacheHitCalls[0].FinalAssetID; got != existingID {
		t.Fatalf("cache-hit job must reuse the existing asset id %q, got %q", existingID, got)
	}
	// The 202 is an acceptance envelope; estimated cost signals the reuse is free.
	resp := decode[map[string]any](t, rec)
	if resp["estimated_cost_usd"] != "0.0000" {
		t.Fatalf("expected estimated_cost_usd=0.0000 on cache hit, got %v", resp["estimated_cost_usd"])
	}
	if !jobIDRe.MatchString(resp["job_id"].(string)) {
		t.Fatalf("expected a job_id in the cache-hit response, got %v", resp["job_id"])
	}
}

func TestArtifactGenerateExactHitRecordsExactMatchResult(t *testing.T) {
	creator := newStubCreator()
	assetsRepo := newStubAssetsRepo()
	existingID := seedReadyArtifact(assetsRepo, "asset_existing2", "w1", "art_1", "A bronze key", "sty_ok", "standard")

	router := newArtifactsRouterWithReuse(creator, seededStyles(), config.ProviderMock, assetsRepo)
	body := map[string]any{"world_id": "w1", "style_profile_id": "sty_ok", "description": "A bronze key"}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_1/generate",
		tenantA, []string{"images:write"}, body, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}
	call := creator.cacheHitCalls[0]
	if call.FallbackPolicy != "compatible_only" {
		t.Fatalf("expected default fallback_policy compatible_only carried into cache-hit job, got %q", call.FallbackPolicy)
	}
	if call.InputPayload["prompt_hash"] == "" || call.InputPayload["prompt_hash"] == nil {
		t.Fatalf("cache-hit job payload must carry the render hash, got %v", call.InputPayload["prompt_hash"])
	}
	if call.FinalAssetID != existingID {
		t.Fatalf("expected reused asset id %q, got %q", existingID, call.FinalAssetID)
	}
}

func TestArtifactGenerateMissGoesThroughNormalGeneratePath(t *testing.T) {
	creator := newStubCreator()
	assetsRepo := newStubAssetsRepo() // empty: nothing to reuse

	router := newArtifactsRouterWithReuse(creator, seededStyles(), config.ProviderMock, assetsRepo)
	body := map[string]any{"world_id": "w1", "style_profile_id": "sty_ok", "description": "A novel artifact"}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_novel/generate",
		tenantA, []string{"images:write"}, body, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(creator.cacheHitCalls) != 0 {
		t.Fatalf("a miss must not create a cache-hit job, got %d", len(creator.cacheHitCalls))
	}
	if len(creator.calls) != 1 {
		t.Fatalf("a miss must go through the normal create/reserve/enqueue path, got %d calls", len(creator.calls))
	}
	if creator.calls[0].CacheResult != "generated_required" {
		t.Fatalf("expected cache_result=generated_required on a miss, got %q", creator.calls[0].CacheResult)
	}
	// The render hash must be carried so the worker persists it on the asset.
	if creator.calls[0].InputPayload["prompt_hash"] == nil || creator.calls[0].InputPayload["prompt_hash"] == "" {
		t.Fatalf("miss path must carry prompt_hash in the payload, got %v", creator.calls[0].InputPayload["prompt_hash"])
	}
}

func TestArtifactGenerateFallbackNoneStillReusesExactHit(t *testing.T) {
	creator := newStubCreator()
	assetsRepo := newStubAssetsRepo()
	existingID := seedReadyArtifact(assetsRepo, "asset_existing3", "w1", "art_none", "A bronze key", "sty_ok", "standard")

	router := newArtifactsRouterWithReuse(creator, seededStyles(), config.ProviderMock, assetsRepo)
	body := map[string]any{
		"world_id":         "w1",
		"style_profile_id": "sty_ok",
		"description":      "A bronze key",
		"fallback_policy":  "none",
	}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_none/generate",
		tenantA, []string{"images:write"}, body, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	// fallback_policy=none gates compatible/preview fallback, not exact reuse:
	// an exact hash hit must still be reused (no generation).
	if len(creator.calls) != 0 {
		t.Fatalf("fallback_policy=none must still reuse an exact hit (no generate), got %d generate calls", len(creator.calls))
	}
	if len(creator.cacheHitCalls) != 1 || creator.cacheHitCalls[0].FinalAssetID != existingID {
		t.Fatalf("expected exact reuse of %q under fallback_policy=none, got %+v", existingID, creator.cacheHitCalls)
	}
	if creator.cacheHitCalls[0].FallbackPolicy != "none" {
		t.Fatalf("expected the request fallback_policy 'none' carried onto the cache-hit job, got %q", creator.cacheHitCalls[0].FallbackPolicy)
	}
}

func TestArtifactGenerateDifferentArtifactIDMissesEvenWithSameDescription(t *testing.T) {
	creator := newStubCreator()
	assetsRepo := newStubAssetsRepo()
	// Seed an asset for art_one; request art_two with the same description.
	seedReadyArtifact(assetsRepo, "asset_one", "w1", "art_one", "A bronze key", "sty_ok", "standard")

	router := newArtifactsRouterWithReuse(creator, seededStyles(), config.ProviderMock, assetsRepo)
	body := map[string]any{"world_id": "w1", "style_profile_id": "sty_ok", "description": "A bronze key"}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_two/generate",
		tenantA, []string{"images:write"}, body, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}
	if len(creator.cacheHitCalls) != 0 {
		t.Fatalf("a different artifact_id must not reuse another artifact's asset, got %d cache hits", len(creator.cacheHitCalls))
	}
	if len(creator.calls) != 1 {
		t.Fatalf("expected the normal generate path for a different artifact_id, got %d", len(creator.calls))
	}
}
