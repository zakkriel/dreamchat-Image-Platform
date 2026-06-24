package handlers

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/zakkriel/drchat-image-platform/internal/audit"
	"github.com/zakkriel/drchat-image-platform/internal/governance"
	"github.com/zakkriel/drchat-image-platform/internal/idempotency"
	"github.com/zakkriel/drchat-image-platform/internal/identities"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
)

// ---------------------------------------------------------------------------
// Governance gate fakes
// ---------------------------------------------------------------------------

// fakeVerifier is a test-double governance.Verifier that returns a preset
// result and records the (Envelope, SubjectMeta) it was called with so tests
// can assert the handler never leaks prompt/description text into the gate.
type fakeVerifier struct {
	mu     sync.Mutex
	result governance.Result
	calls  []fakeVerifyCall
}

type fakeVerifyCall struct {
	env  governance.Envelope
	subj governance.SubjectMeta
}

func (f *fakeVerifier) Verify(_ context.Context, env governance.Envelope, subj governance.SubjectMeta) governance.Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeVerifyCall{env: env, subj: subj})
	return f.result
}

// fakeAuditSink records Emit calls for assertion in unit tests.
type fakeAuditSink struct {
	mu     sync.Mutex
	events []audit.Event
}

func (f *fakeAuditSink) Emit(_ context.Context, _ string, ev audit.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, ev)
	return nil
}

func (f *fakeAuditSink) lastEvent() (audit.Event, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.events) == 0 {
		return audit.Event{}, false
	}
	return f.events[len(f.events)-1], true
}

// newGovernedGenerationsRouter wires a GenerationsHandler with the supplied
// governance verifier, mode, and audit sink — used by governance gate unit tests.
func newGovernedGenerationsRouter(creator jobs.Creator, idRepo identities.Repository, resolver RouteResolver, verifier governance.Verifier, mode governance.Mode, sink AuditSink) chi.Router {
	h := NewGenerationsHandler(creator, resolver, idRepo)
	h.Verifier = verifier
	h.Mode = mode
	h.Audit = sink
	r := chi.NewRouter()
	r.Post("/v1/generations", h.Create)
	return r
}

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

// alwaysOKVerifier is a governance.Verifier that always approves — used by
// pre-gate tests so they don't need to supply governance credentials.
type alwaysOKVerifier struct{}

func (alwaysOKVerifier) Verify(_ context.Context, _ governance.Envelope, _ governance.SubjectMeta) governance.Result {
	return governance.Result{OK: true}
}

// noopAuditSink discards every Emit call.
type noopAuditSink struct{}

func (noopAuditSink) Emit(_ context.Context, _ string, _ audit.Event) error { return nil }

func newGenerationsRouter(creator jobs.Creator, idRepo identities.Repository, resolver RouteResolver) chi.Router {
	h := NewGenerationsHandler(creator, resolver, idRepo)
	h.Verifier = alwaysOKVerifier{}
	h.Mode = governance.ModeEnforce
	h.Audit = noopAuditSink{}
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

// render.transform present with non-string or empty schema_version → 422 (D-4).
func TestGenerationsTransformNonStringSchemaVersion422(t *testing.T) {
	creator := newStubCreator()
	idRepo := seededGenIDRepo()
	router := newGenerationsRouter(creator, idRepo, okResolver())

	// Numeric schema_version (e.g. JSON integer 1) must be rejected.
	body := minimalGenBody(testIdentityID, "idem-key-transform-sv-001")
	body["render"] = map[string]any{
		"intent": "draft",
		"transform": map[string]any{
			"schema_version": 1, // numeric, not a string — must fail
			"ops":            []any{},
		},
	}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/generations", tenantA,
		[]string{"images:write"}, body, nil)
	assertError(t, rec, http.StatusUnprocessableEntity, "invalid_request")
	if len(creator.calls) != 0 {
		t.Fatalf("expected no service call on invalid transform schema_version, got %d", len(creator.calls))
	}

	// Missing schema_version inside transform must also be rejected.
	creator2 := newStubCreator()
	body2 := minimalGenBody(testIdentityID, "idem-key-transform-sv-002")
	body2["render"] = map[string]any{
		"intent": "draft",
		"transform": map[string]any{
			"ops": []any{},
		},
	}
	rec2 := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/generations", tenantA,
		[]string{"images:write"}, body2, nil)
	_ = creator2
	assertError(t, rec2, http.StatusUnprocessableEntity, "invalid_request")
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
		"subject":         map[string]any{}, // no identity_id
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

// ---------------------------------------------------------------------------
// Governance gate unit tests (Task 8)
// ---------------------------------------------------------------------------

// log_only + governance block → 202 (proceeds) AND audit EventBlocked recorded.
func TestGenerationsGovernanceLogOnlyBlockProceeds(t *testing.T) {
	creator := newStubCreator()
	idRepo := seededGenIDRepo()
	verifier := &fakeVerifier{result: governance.Result{OK: false, Reason: "bad_sig"}}
	sink := &fakeAuditSink{}

	router := newGovernedGenerationsRouter(creator, idRepo, okResolver(), verifier, governance.ModeLogOnly, sink)

	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/generations", tenantA,
		[]string{"images:write"}, minimalGenBody(testIdentityID, "gov-log-001"), nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("log_only block: expected 202 (proceed), got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(creator.calls) != 1 {
		t.Fatalf("log_only block: expected resolver+creator called (1 CreateAndEnqueue), got %d", len(creator.calls))
	}
	ev, ok := sink.lastEvent()
	if !ok {
		t.Fatal("log_only block: expected at least one audit event")
	}
	if ev.EventType != governance.EventBlocked {
		t.Fatalf("log_only block: expected audit EventType=%q, got %q", governance.EventBlocked, ev.EventType)
	}
}

// enforce + governance block → 403 governance_blocked; audit EventBlocked; resolver/creator NOT called.
func TestGenerationsGovernanceEnforceBlockReturns403(t *testing.T) {
	creator := newStubCreator()
	idRepo := seededGenIDRepo()
	verifier := &fakeVerifier{result: governance.Result{OK: false, Reason: "bad_sig"}}
	sink := &fakeAuditSink{}

	router := newGovernedGenerationsRouter(creator, idRepo, okResolver(), verifier, governance.ModeEnforce, sink)

	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/generations", tenantA,
		[]string{"images:write"}, minimalGenBody(testIdentityID, "gov-enforce-001"), nil)
	assertError(t, rec, http.StatusForbidden, "governance_blocked")
	if len(creator.calls) != 0 {
		t.Fatalf("enforce block: expected NO CreateAndEnqueue call, got %d", len(creator.calls))
	}
	ev, ok := sink.lastEvent()
	if !ok {
		t.Fatal("enforce block: expected at least one audit event")
	}
	if ev.EventType != governance.EventBlocked {
		t.Fatalf("enforce block: expected audit EventType=%q, got %q", governance.EventBlocked, ev.EventType)
	}
}

// governance verified → 202, audit EventVerified, params.GovernanceVerifiedAt set.
func TestGenerationsGovernanceVerifiedSetsGovernanceVerifiedAt(t *testing.T) {
	creator := newStubCreator()
	idRepo := seededGenIDRepo()
	verifier := &fakeVerifier{result: governance.Result{OK: true}}
	sink := &fakeAuditSink{}

	router := newGovernedGenerationsRouter(creator, idRepo, okResolver(), verifier, governance.ModeEnforce, sink)

	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/generations", tenantA,
		[]string{"images:write"}, minimalGenBody(testIdentityID, "gov-verified-001"), nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("verified: expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(creator.calls) != 1 {
		t.Fatalf("verified: expected 1 CreateAndEnqueue call, got %d", len(creator.calls))
	}
	if creator.calls[0].GovernanceVerifiedAt == nil {
		t.Fatal("verified: expected GovernanceVerifiedAt to be set on CreateAndEnqueueParams")
	}
	ev, ok := sink.lastEvent()
	if !ok {
		t.Fatal("verified: expected at least one audit event")
	}
	if ev.EventType != governance.EventVerified {
		t.Fatalf("verified: expected audit EventType=%q, got %q", governance.EventVerified, ev.EventType)
	}
}

// prompt-never-read spy: the gate receives Envelope + SubjectMeta with ONLY
// the identity IDs — no prompt/description/display-name text.
func TestGenerationsGovernanceGateNeverSeesPromptText(t *testing.T) {
	creator := newStubCreator()
	idRepo := seededGenIDRepo() // identity has DisplayName = testIdentityDisplay
	verifier := &fakeVerifier{result: governance.Result{OK: true}}
	sink := noopAuditSink{}

	router := newGovernedGenerationsRouter(creator, idRepo, okResolver(), verifier, governance.ModeEnforce, sink)

	body := minimalGenBody(testIdentityID, "gov-spy-001")
	// Add optional subject fields to check they pass through only as IDs.
	body["subject"] = map[string]any{
		"identity_id":     testIdentityID,
		"anchor_asset_id": "anchor_abc",
		"derive_from":     "derive_xyz",
	}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/generations", tenantA,
		[]string{"images:write"}, body, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("spy: expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}

	verifier.mu.Lock()
	calls := verifier.calls
	verifier.mu.Unlock()

	if len(calls) == 0 {
		t.Fatal("spy: verifier was never called")
	}
	call := calls[0]

	// SubjectMeta must carry only ID fields.
	if call.subj.IdentityID != testIdentityID {
		t.Fatalf("spy: IdentityID=%q, want %q", call.subj.IdentityID, testIdentityID)
	}
	if call.subj.AnchorAssetID != "anchor_abc" {
		t.Fatalf("spy: AnchorAssetID=%q, want %q", call.subj.AnchorAssetID, "anchor_abc")
	}
	if call.subj.DeriveFrom != "derive_xyz" {
		t.Fatalf("spy: DeriveFrom=%q, want %q", call.subj.DeriveFrom, "derive_xyz")
	}

	// The identity DisplayName must NOT appear in the Envelope fields.
	envFields := []string{
		call.env.SchemaVersion, call.env.ClassificationID, call.env.Visibility,
		call.env.ContentClass, call.env.AuthorizedBy, call.env.Signature,
	}
	for _, f := range envFields {
		if f == testIdentityDisplay {
			t.Fatalf("spy: identity DisplayName %q leaked into Envelope field", testIdentityDisplay)
		}
	}
	// SubjectMeta fields must be IDs, not display names.
	subjFields := []string{call.subj.IdentityID, call.subj.AnchorAssetID, call.subj.DeriveFrom}
	for _, f := range subjFields {
		if f == testIdentityDisplay {
			t.Fatalf("spy: identity DisplayName %q leaked into SubjectMeta field", testIdentityDisplay)
		}
	}
}
