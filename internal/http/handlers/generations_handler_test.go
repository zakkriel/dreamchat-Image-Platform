package handlers

import (
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/zakkriel/drchat-image-platform/internal/idempotency"
	"github.com/zakkriel/drchat-image-platform/internal/identities"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
)

const (
	testIdentityID      = "vi_aaaaaaaaaaaaaaaa"
	testIdentityDisplay = "Alice Wonderland"
)

// seededGenIDRepo returns a stubIdentitiesRepo with one identity seeded for
// GetByIDForTenant lookup (used by the generations handler).
func seededGenIDRepo() *stubIdentitiesRepo {
	repo := newStubIdentitiesRepo()
	repo.byOwner[identityKey{tenantA, "w1", "character", "char_alice"}] = identities.VisualIdentity{
		ID:          testIdentityID,
		TenantID:    tenantA,
		DisplayName: testIdentityDisplay,
	}
	return repo
}

// minimalGenBody returns a minimal valid GenerationRequest JSON body map.
func minimalGenBody(identityID, idempKey string) map[string]any {
	return map[string]any{
		"governance": map[string]any{
			"schema_version":    "1.0",
			"classification_id": "cls_test",
			"visibility":        "private",
			"content_class":     "safe",
			"authorized_by":     "auth_test",
			"issued_at":         "2026-06-18T00:00:00Z",
			"signature":         "sig_test",
		},
		"subject": map[string]any{
			"identity_id": identityID,
		},
		"render": map[string]any{
			"intent": "draft",
		},
		"idempotency_key": idempKey,
	}
}

func newGenerationsRouter(creator jobs.Creator, idRepo identities.Repository, resolver RouteResolver) chi.Router {
	h := NewGenerationsHandler(creator, resolver, idRepo)
	r := chi.NewRouter()
	r.Post("/v1/generations", h.Create)
	return r
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// Valid minimal request → 202.
func TestGenerationsValidMinimal202(t *testing.T) {
	creator := newStubCreator()
	idRepo := seededGenIDRepo()
	router := newGenerationsRouter(creator, idRepo, okResolver())

	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/generations", tenantA,
		[]string{"images:write"}, minimalGenBody(testIdentityID, "idem-key-001"), nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	resp := decode[map[string]any](t, rec)
	if !jobIDRe.MatchString(resp["job_id"].(string)) {
		t.Fatalf("expected a job_id, got %v", resp["job_id"])
	}
	if resp["status"] != "queued" {
		t.Fatalf("expected status=queued, got %v", resp["status"])
	}
	if len(creator.calls) != 1 {
		t.Fatalf("expected exactly 1 service call, got %d", len(creator.calls))
	}
}

// Unknown field in body → 422 (DisallowUnknownFields).
func TestGenerationsUnknownField422(t *testing.T) {
	creator := newStubCreator()
	idRepo := seededGenIDRepo()
	router := newGenerationsRouter(creator, idRepo, okResolver())

	body := minimalGenBody(testIdentityID, "idem-key-002")
	body["bogus_field"] = "should_be_rejected"

	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/generations", tenantA,
		[]string{"images:write"}, body, nil)
	assertError(t, rec, http.StatusUnprocessableEntity, "invalid_request")
	if len(creator.calls) != 0 {
		t.Fatalf("expected no service call on unknown field, got %d", len(creator.calls))
	}
}

// render.transform_only=true → 501 transform_only_not_supported.
func TestGenerationsTransformOnlyTrue501(t *testing.T) {
	creator := newStubCreator()
	idRepo := seededGenIDRepo()
	router := newGenerationsRouter(creator, idRepo, okResolver())

	body := minimalGenBody(testIdentityID, "idem-key-003")
	body["render"] = map[string]any{
		"intent":         "draft",
		"transform_only": true,
	}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/generations", tenantA,
		[]string{"images:write"}, body, nil)
	assertError(t, rec, http.StatusNotImplemented, "transform_only_not_supported")
	if len(creator.calls) != 0 {
		t.Fatalf("expected no service call on 501 transform_only, got %d", len(creator.calls))
	}
}

// grid.enabled=true → 501 grid_not_supported.
func TestGenerationsGridEnabled501(t *testing.T) {
	creator := newStubCreator()
	idRepo := seededGenIDRepo()
	router := newGenerationsRouter(creator, idRepo, okResolver())

	body := minimalGenBody(testIdentityID, "idem-key-004")
	body["grid"] = map[string]any{"enabled": true}

	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/generations", tenantA,
		[]string{"images:write"}, body, nil)
	assertError(t, rec, http.StatusNotImplemented, "grid_not_supported")
	if len(creator.calls) != 0 {
		t.Fatalf("expected no service call on 501 grid, got %d", len(creator.calls))
	}
}

// render.intent missing → 422.
func TestGenerationsIntentMissing422(t *testing.T) {
	creator := newStubCreator()
	idRepo := seededGenIDRepo()
	router := newGenerationsRouter(creator, idRepo, okResolver())

	body := map[string]any{
		"governance": map[string]any{
			"schema_version":    "1.0",
			"classification_id": "cls_test",
			"visibility":        "private",
			"content_class":     "safe",
			"authorized_by":     "auth_test",
			"issued_at":         "2026-06-18T00:00:00Z",
			"signature":         "sig_test",
		},
		"subject":         map[string]any{"identity_id": testIdentityID},
		"render":          map[string]any{}, // no intent
		"idempotency_key": "idem-key-005",
	}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/generations", tenantA,
		[]string{"images:write"}, body, nil)
	assertError(t, rec, http.StatusUnprocessableEntity, "invalid_request")
}

// render.intent invalid → 422.
func TestGenerationsIntentInvalid422(t *testing.T) {
	creator := newStubCreator()
	idRepo := seededGenIDRepo()
	router := newGenerationsRouter(creator, idRepo, okResolver())

	body := minimalGenBody(testIdentityID, "idem-key-006")
	body["render"] = map[string]any{"intent": "bogus_intent"}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/generations", tenantA,
		[]string{"images:write"}, body, nil)
	assertError(t, rec, http.StatusUnprocessableEntity, "invalid_request")
}

// subject.identity_id missing → 422.
func TestGenerationsIdentityIDMissing422(t *testing.T) {
	creator := newStubCreator()
	idRepo := seededGenIDRepo()
	router := newGenerationsRouter(creator, idRepo, okResolver())

	body := map[string]any{
		"governance": map[string]any{
			"schema_version":    "1.0",
			"classification_id": "cls_test",
			"visibility":        "private",
			"content_class":     "safe",
			"authorized_by":     "auth_test",
			"issued_at":         "2026-06-18T00:00:00Z",
			"signature":         "sig_test",
		},
		"subject":         map[string]any{},                   // no identity_id
		"render":          map[string]any{"intent": "draft"},
		"idempotency_key": "idem-key-007",
	}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/generations", tenantA,
		[]string{"images:write"}, body, nil)
	assertError(t, rec, http.StatusUnprocessableEntity, "invalid_request")
}

// subject.identity_id not found → 422.
func TestGenerationsIdentityNotFound422(t *testing.T) {
	creator := newStubCreator()
	// Empty identity repo — lookup returns ErrNotFound.
	idRepo := newStubIdentitiesRepo()
	router := newGenerationsRouter(creator, idRepo, okResolver())

	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/generations", tenantA,
		[]string{"images:write"}, minimalGenBody("vi_does_not_exist", "idem-key-008"), nil)
	assertError(t, rec, http.StatusUnprocessableEntity, "invalid_request")
	if len(creator.calls) != 0 {
		t.Fatalf("expected no service call on identity not found, got %d", len(creator.calls))
	}
}

// payload["description"] == identity.DisplayName (identity-derived prompt).
func TestGenerationsPayloadDescriptionEqualsIdentityDisplayName(t *testing.T) {
	creator := newStubCreator()
	idRepo := seededGenIDRepo()
	router := newGenerationsRouter(creator, idRepo, okResolver())

	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/generations", tenantA,
		[]string{"images:write"}, minimalGenBody(testIdentityID, "idem-key-009"), nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(creator.calls) != 1 {
		t.Fatalf("expected exactly 1 service call, got %d", len(creator.calls))
	}
	desc := creator.calls[0].InputPayload["description"]
	if desc != testIdentityDisplay {
		t.Fatalf("expected description=%q (identity.DisplayName), got %v", testIdentityDisplay, desc)
	}
}

// header Idempotency-Key present and != body idempotency_key → 422.
func TestGenerationsHeaderBodyIdempotencyKeyMismatch422(t *testing.T) {
	creator := newStubCreator()
	idRepo := seededGenIDRepo()
	router := newGenerationsRouter(creator, idRepo, okResolver())

	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/generations", tenantA,
		[]string{"images:write"}, minimalGenBody(testIdentityID, "body-key-010"),
		map[string]string{idempotency.HeaderKey: "different-header-key"})
	assertError(t, rec, http.StatusUnprocessableEntity, "invalid_request")
}

// header-only (no body idempotency_key) → 422.
func TestGenerationsHeaderOnlyIdempotencyKey422(t *testing.T) {
	creator := newStubCreator()
	idRepo := seededGenIDRepo()
	router := newGenerationsRouter(creator, idRepo, okResolver())

	// Body has no idempotency_key.
	body := map[string]any{
		"governance": map[string]any{
			"schema_version":    "1.0",
			"classification_id": "cls_test",
			"visibility":        "private",
			"content_class":     "safe",
			"authorized_by":     "auth_test",
			"issued_at":         "2026-06-18T00:00:00Z",
			"signature":         "sig_test",
		},
		"subject": map[string]any{"identity_id": testIdentityID},
		"render":  map[string]any{"intent": "draft"},
		// no idempotency_key field
	}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/generations", tenantA,
		[]string{"images:write"}, body, map[string]string{idempotency.HeaderKey: "header-only-011"})
	assertError(t, rec, http.StatusUnprocessableEntity, "invalid_request")
}

// body idempotency_key present (no header) → 202.
func TestGenerationsBodyOnlyIdempotencyKey202(t *testing.T) {
	creator := newStubCreator()
	idRepo := seededGenIDRepo()
	router := newGenerationsRouter(creator, idRepo, okResolver())

	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/generations", tenantA,
		[]string{"images:write"}, minimalGenBody(testIdentityID, "body-only-012"), nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for body-only idempotency_key, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// Resolver received Intent + identity_capable floor + EMPTY QualityTier.
func TestGenerationsResolverReceivesIntentAndCapabilityAndEmptyQualityTier(t *testing.T) {
	creator := newStubCreator()
	idRepo := seededGenIDRepo()
	resolver := okResolver()
	router := newGenerationsRouter(creator, idRepo, resolver)

	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/generations", tenantA,
		[]string{"images:write"}, minimalGenBody(testIdentityID, "idem-key-013"), nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}

	// Intent must be threaded.
	if resolver.lastReq.Intent != "draft" {
		t.Fatalf("expected Intent=draft, got %q", resolver.lastReq.Intent)
	}
	// Floor = identity_capable when identity_id is present.
	if resolver.lastReq.RequiredCapability != "identity_capable" {
		t.Fatalf("expected RequiredCapability=identity_capable, got %q", resolver.lastReq.RequiredCapability)
	}
	// CRITICAL: QualityTier MUST be empty — setting it hard-filters before intent ranking.
	if resolver.lastReq.QualityTier != "" {
		t.Fatalf("QualityTier must be empty in ResolveRequest (task 7 critical), got %q", resolver.lastReq.QualityTier)
	}
}

// tenant_id in body → 400 (rejectBodyTenantID via readRawJSONBody shared helper).
func TestGenerationsTenantIDInBody400(t *testing.T) {
	creator := newStubCreator()
	idRepo := seededGenIDRepo()
	router := newGenerationsRouter(creator, idRepo, okResolver())

	body := minimalGenBody(testIdentityID, "idem-key-015")
	body["tenant_id"] = "tenant_should_be_rejected"

	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/generations", tenantA,
		[]string{"images:write"}, body, nil)
	assertError(t, rec, http.StatusBadRequest, "invalid_request")
	if len(creator.calls) != 0 {
		t.Fatalf("expected no service call when tenant_id in body, got %d", len(creator.calls))
	}
}

// transform_only=true → 501, no identity DB fetch (501 checked before identity fetch).
func TestGenerationsTransformOnly501NoIdentityFetch(t *testing.T) {
	creator := newStubCreator()
	idRepo := seededGenIDRepo()
	router := newGenerationsRouter(creator, idRepo, okResolver())

	body := minimalGenBody(testIdentityID, "idem-key-016")
	body["render"] = map[string]any{
		"intent":         "draft",
		"transform_only": true,
	}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/generations", tenantA,
		[]string{"images:write"}, body, nil)
	assertError(t, rec, http.StatusNotImplemented, "transform_only_not_supported")
	if idRepo.getByIDCallCount != 0 {
		t.Fatalf("expected 0 identity DB fetches on 501 path, got %d", idRepo.getByIDCallCount)
	}
}

// grid.enabled=true → 501, no identity DB fetch.
func TestGenerationsGrid501NoIdentityFetch(t *testing.T) {
	creator := newStubCreator()
	idRepo := seededGenIDRepo()
	router := newGenerationsRouter(creator, idRepo, okResolver())

	body := minimalGenBody(testIdentityID, "idem-key-017")
	body["grid"] = map[string]any{"enabled": true}

	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/generations", tenantA,
		[]string{"images:write"}, body, nil)
	assertError(t, rec, http.StatusNotImplemented, "grid_not_supported")
	if idRepo.getByIDCallCount != 0 {
		t.Fatalf("expected 0 identity DB fetches on 501 grid path, got %d", idRepo.getByIDCallCount)
	}
}

// Governance missing required fields → 422.
func TestGenerationsGovernanceMissingFields422(t *testing.T) {
	creator := newStubCreator()
	idRepo := seededGenIDRepo()
	router := newGenerationsRouter(creator, idRepo, okResolver())

	body := map[string]any{
		// governance without classification_id, visibility, etc.
		"governance":      map[string]any{"schema_version": "1.0"},
		"subject":         map[string]any{"identity_id": testIdentityID},
		"render":          map[string]any{"intent": "draft"},
		"idempotency_key": "idem-key-014",
	}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/generations", tenantA,
		[]string{"images:write"}, body, nil)
	assertError(t, rec, http.StatusUnprocessableEntity, "invalid_request")
}
