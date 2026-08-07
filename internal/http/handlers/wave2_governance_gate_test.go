package handlers

import (
	"net/http"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/zakkriel/drchat-image-platform/internal/governance"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
)

// realGate builds a GovernanceGate over the REAL verifier (stub signature
// seam, "auth_test" issuer allowlist), mirroring production wiring.
func realGate(mode governance.Mode, sink AuditSink) GovernanceGate {
	return GovernanceGate{
		Verifier: governance.NewVerifier(governance.StubSignatureVerifier{}, 24*time.Hour, []string{"auth_test"}),
		Mode:     mode,
		Audit:    sink,
	}
}

// govEnvelopeBody is a currently-valid envelope for the "auth_test" issuer.
func govEnvelopeBody() map[string]any {
	return map[string]any{
		"schema_version":    "1.0",
		"classification_id": "cls_test",
		"visibility":        "private",
		"content_class":     "safe",
		"authorized_by":     "auth_test",
		"issued_at":         time.Now().UTC().Format(time.RFC3339),
		"signature":         "sig_test",
	}
}

func gatedArtifactsRouter(creator jobs.Creator, gate GovernanceGate) chi.Router {
	h := NewArtifactsHandler(creator, seededStyles(), okResolver(), "mock", nil)
	h.Gate = gate
	r := chi.NewRouter()
	r.Post("/v1/artifacts/{artifact_id}/generate", h.Generate)
	return r
}

func artifactBody() map[string]any {
	return map[string]any{
		"world_id":         "w1",
		"style_profile_id": "sty_ok",
		"description":      "A bronze key",
	}
}

// Enforce + no envelope on the legacy artifact endpoint → 403
// governance_blocked before any reservation (no service call), with the
// blocked verdict audited. This closes ADR-P002 Follow-up 1's governance
// bypass under enforcement.
func TestArtifactGovernanceEnforceMissingEnvelope403(t *testing.T) {
	creator := newStubCreator()
	sink := &fakeAuditSink{}
	router := gatedArtifactsRouter(creator, realGate(governance.ModeEnforce, sink))

	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_1/generate",
		tenantA, []string{"images:write"}, artifactBody(), nil)
	assertError(t, rec, http.StatusForbidden, "governance_blocked")
	if len(creator.calls) != 0 {
		t.Fatalf("blocked request must not reach the service, got %d calls", len(creator.calls))
	}
	ev, ok := sink.lastEvent()
	if !ok || ev.EventType != governance.EventBlocked {
		t.Fatalf("expected %s audit event, got %+v", governance.EventBlocked, ev)
	}
}

// log_only + no envelope proceeds (existing callers unaffected) while the
// would-block verdict is audited; nothing is stamped on the job.
func TestArtifactGovernanceLogOnlyMissingEnvelopeProceeds(t *testing.T) {
	creator := newStubCreator()
	sink := &fakeAuditSink{}
	router := gatedArtifactsRouter(creator, realGate(governance.ModeLogOnly, sink))

	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_1/generate",
		tenantA, []string{"images:write"}, artifactBody(), nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(creator.calls) != 1 {
		t.Fatalf("expected 1 service call, got %d", len(creator.calls))
	}
	params := creator.calls[0]
	if params.GovernanceVerifiedAt != nil || params.GovernanceEnvelope != nil {
		t.Fatalf("nothing to stamp without an envelope, got verified_at=%v envelope=%s", params.GovernanceVerifiedAt, params.GovernanceEnvelope)
	}
	ev, ok := sink.lastEvent()
	if !ok || ev.EventType != governance.EventBlocked {
		t.Fatalf("expected audited %s verdict in log_only, got %+v", governance.EventBlocked, ev)
	}
}

// A valid envelope on the legacy artifact endpoint verifies under enforce and
// is persisted onto the job (envelope JSON + scalars + verified_at) exactly
// like the combined endpoint.
func TestArtifactGovernanceEnvelopeVerifiedAndPersisted(t *testing.T) {
	creator := newStubCreator()
	sink := &fakeAuditSink{}
	router := gatedArtifactsRouter(creator, realGate(governance.ModeEnforce, sink))

	body := artifactBody()
	body["governance"] = govEnvelopeBody()
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_1/generate",
		tenantA, []string{"images:write"}, body, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(creator.calls) != 1 {
		t.Fatalf("expected 1 service call, got %d", len(creator.calls))
	}
	params := creator.calls[0]
	if params.GovernanceVerifiedAt == nil {
		t.Fatal("expected governance_verified_at stamped after a verified envelope")
	}
	if params.GovernanceEnvelope == nil {
		t.Fatal("expected the envelope JSON persisted")
	}
	if params.ClassificationID == nil || *params.ClassificationID != "cls_test" {
		t.Fatalf("expected classification_id cls_test, got %v", params.ClassificationID)
	}
	if params.ContentClass == nil || *params.ContentClass != "safe" {
		t.Fatalf("expected content_class persisted opaquely, got %v", params.ContentClass)
	}
	ev, ok := sink.lastEvent()
	if !ok || ev.EventType != governance.EventVerified {
		t.Fatalf("expected %s audit event, got %+v", governance.EventVerified, ev)
	}
}

// Enforce + no envelope blocks the pack endpoints identically.
func TestPackGovernanceEnforceMissingEnvelope403(t *testing.T) {
	creator := newStubCreator()
	h := NewPacksHandler(creator, seededStyles(), seededPackIdentities(), okResolver(), "mock")
	h.Gate = realGate(governance.ModeEnforce, &fakeAuditSink{})
	r := chi.NewRouter()
	r.Post("/v1/characters/{character_id}/generate-pack", h.GenerateCharacterPack)

	body := map[string]any{"world_id": packWorldID, "style_profile_id": "sty_ok"}
	rec := sendJSONWithHeaders(t, r, http.MethodPost, "/v1/characters/char_hero/generate-pack",
		tenantA, []string{"images:write"}, body, nil)
	assertError(t, rec, http.StatusForbidden, "governance_blocked")
	if len(creator.calls) != 0 {
		t.Fatalf("blocked pack must not reach the service, got %d calls", len(creator.calls))
	}
}

// Enforce + no envelope blocks the style preview endpoint identically.
func TestStylePreviewGovernanceEnforceMissingEnvelope403(t *testing.T) {
	creator := newStubCreator()
	h := NewStylePreviewHandler(creator, seededPreviewStyles(), okResolver(), "mock")
	h.Gate = realGate(governance.ModeEnforce, &fakeAuditSink{})
	r := chi.NewRouter()
	r.Post("/v1/styles/{style_id}/preview", h.GeneratePreview)

	rec := sendJSONWithHeaders(t, r, http.MethodPost, "/v1/styles/sty_ok/preview",
		tenantA, []string{"images:write"}, map[string]any{"world_id": "w1"}, nil)
	assertError(t, rec, http.StatusForbidden, "governance_blocked")
	if len(creator.calls) != 0 {
		t.Fatalf("blocked preview must not reach the service, got %d calls", len(creator.calls))
	}
}

// fallback_policy=any_existing is a debug facility: it requires admin:read on
// generation and search, and passes with the scope present.
func TestAnyExistingRequiresAdminScope(t *testing.T) {
	// Artifact generate: images:write alone → 403.
	creator := newStubCreator()
	router := newArtifactsRouter(creator, seededStyles(), "mock")
	body := artifactBody()
	body["fallback_policy"] = "any_existing"
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_1/generate",
		tenantA, []string{"images:write"}, body, nil)
	assertError(t, rec, http.StatusForbidden, "forbidden")
	if len(creator.calls) != 0 {
		t.Fatalf("expected no service call without admin:read, got %d", len(creator.calls))
	}

	// With admin:read → proceeds.
	rec = sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_1/generate",
		tenantA, []string{"images:write", "admin:read"}, body, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202 with admin:read, got %d body=%s", rec.Code, rec.Body.String())
	}

	// Asset search: images:read alone → 403; with admin:read → 200.
	repo := newStubAssetsRepo()
	search := newAssetsRouter(repo)
	sBody := searchBody("character", "neutral_front_portrait", "any_existing")
	rec = sendJSONWithHeaders(t, search, http.MethodPost, "/v1/assets/search",
		tenantA, []string{"images:read"}, sBody, nil)
	assertError(t, rec, http.StatusForbidden, "forbidden")
	rec = sendJSONWithHeaders(t, search, http.MethodPost, "/v1/assets/search",
		tenantA, []string{"images:read", "admin:read"}, sBody, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with admin:read, got %d body=%s", rec.Code, rec.Body.String())
	}
}
