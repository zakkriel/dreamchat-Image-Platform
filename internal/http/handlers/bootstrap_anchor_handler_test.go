package handlers

import (
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/zakkriel/drchat-image-platform/internal/identities"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
)

func newBootstrapAnchorRouter(creator jobs.Creator, idents identities.Repository, resolver RouteResolver, reuse ArtifactReuseLookup) chi.Router {
	h := NewCharacterBootstrapAnchorHandler(creator, idents, resolver, "mock", reuse)
	r := chi.NewRouter()
	r.Post("/v1/characters/{character_id}/visual-identity/bootstrap-anchor", h.BootstrapCharacterAnchor)
	return r
}

func TestBootstrapCharacterAnchorAlreadyAnchoredReturns200AndNoJob(t *testing.T) {
	creator := newStubCreator()
	idents := newStubIdentitiesRepo()
	idents.byOwner[identityKey{tenantA, "w1", "character", "char_alice"}] = identities.VisualIdentity{
		ID:             "vi_alice",
		TenantID:       tenantA,
		WorldID:        "w1",
		OwnerType:      "character",
		OwnerID:        "char_alice",
		DisplayName:    "Alice",
		StyleProfileID: "sty_ok",
		AnchorAssetIds: []string{"va_anchor_existing"},
	}

	r := newBootstrapAnchorRouter(creator, idents, okResolver(), nil)
	rec := sendJSONWithHeaders(t, r, http.MethodPost, "/v1/characters/char_alice/visual-identity/bootstrap-anchor", tenantA, []string{"images:write"}, map[string]any{
		"world_id":    "w1",
		"description": "a gaunt man in a salt-stained coat",
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := decode[map[string]any](t, rec)
	if body["status"] != "already_anchored" {
		t.Fatalf("status=%v, want already_anchored", body["status"])
	}
	if got := len(creator.calls); got != 0 {
		t.Fatalf("expected no job create calls, got %d", got)
	}
}

func TestBootstrapCharacterAnchorFreshIdentityEnqueuesWithAnchorPayload(t *testing.T) {
	creator := newStubCreator()
	idents := newStubIdentitiesRepo()
	idents.byOwner[identityKey{tenantA, "w1", "character", "char_alice"}] = identities.VisualIdentity{
		ID:             "vi_alice",
		TenantID:       tenantA,
		WorldID:        "w1",
		OwnerType:      "character",
		OwnerID:        "char_alice",
		DisplayName:    "Alice",
		StyleProfileID: "sty_ok",
	}

	r := newBootstrapAnchorRouter(creator, idents, okResolver(), nil)
	rec := sendJSONWithHeaders(t, r, http.MethodPost, "/v1/characters/char_alice/visual-identity/bootstrap-anchor", tenantA, []string{"images:write"}, map[string]any{
		"world_id":    "w1",
		"description": "a gaunt man in a salt-stained coat",
	}, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := len(creator.calls); got != 1 {
		t.Fatalf("expected one create call, got %d", got)
	}
	payload := creator.calls[0].InputPayload
	if payload["anchor_for_identity_id"] != "vi_alice" {
		t.Fatalf("anchor_for_identity_id=%v, want vi_alice", payload["anchor_for_identity_id"])
	}
}

// The anchor produced here conditions every later portrait of this character, so the prompt the
// provider renders must be the appearance the caller sent — not the identity's display_name.
//
// This exists because that is exactly what shipped: the handler prompted with DisplayName, bfl was
// handed the bare identifier "emery_voss", and the anchor came back as a furry creature on a cliff
// which then conditioned all three of that world's portraits. Every test passed, because none of
// them looked at what was being drawn.
func TestBootstrapCharacterAnchorPromptsWithTheAppearanceNotTheName(t *testing.T) {
	creator := newStubCreator()
	idents := newStubIdentitiesRepo()
	idents.byOwner[identityKey{tenantA, "w1", "character", "char_alice"}] = identities.VisualIdentity{
		ID:             "vi_alice",
		TenantID:       tenantA,
		WorldID:        "w1",
		OwnerType:      "character",
		OwnerID:        "char_alice",
		DisplayName:    "emery_voss",
		StyleProfileID: "sty_ok",
	}

	const appearance = "a gaunt man in a salt-stained coat, not looking at the door"

	r := newBootstrapAnchorRouter(creator, idents, okResolver(), nil)
	rec := sendJSONWithHeaders(t, r, http.MethodPost, "/v1/characters/char_alice/visual-identity/bootstrap-anchor", tenantA, []string{"images:write"}, map[string]any{
		"world_id":    "w1",
		"description": appearance,
	}, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	payload := creator.calls[0].InputPayload
	if got := payload["description"]; got != appearance {
		t.Fatalf("description=%v, want the caller's appearance prose %q", got, appearance)
	}
	if payload["description"] == "emery_voss" {
		t.Fatal("the provider was handed the display name; a name is not an appearance")
	}
}

// A request with no description and an identity carrying no canonical appearance has nothing to
// draw. Refusing beats rendering an identifier and binding the result as the character's anchor.
func TestBootstrapCharacterAnchorRefusesWhenThereIsNothingToDraw(t *testing.T) {
	creator := newStubCreator()
	idents := newStubIdentitiesRepo()
	idents.byOwner[identityKey{tenantA, "w1", "character", "char_alice"}] = identities.VisualIdentity{
		ID:             "vi_alice",
		TenantID:       tenantA,
		WorldID:        "w1",
		OwnerType:      "character",
		OwnerID:        "char_alice",
		DisplayName:    "emery_voss",
		StyleProfileID: "sty_ok",
	}

	r := newBootstrapAnchorRouter(creator, idents, okResolver(), nil)
	rec := sendJSONWithHeaders(t, r, http.MethodPost, "/v1/characters/char_alice/visual-identity/bootstrap-anchor", tenantA, []string{"images:write"}, map[string]any{
		"world_id": "w1",
	}, nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d, want 422 — there is no appearance to render", rec.Code)
	}
	if len(creator.calls) != 0 {
		t.Fatalf("nothing should be enqueued or billed, got %d create calls", len(creator.calls))
	}
}

func TestBootstrapCharacterAnchorUnknownCharacterReturns404(t *testing.T) {
	r := newBootstrapAnchorRouter(newStubCreator(), newStubIdentitiesRepo(), okResolver(), nil)
	rec := sendJSONWithHeaders(t, r, http.MethodPost, "/v1/characters/char_missing/visual-identity/bootstrap-anchor", tenantA, []string{"images:write"}, map[string]any{
		"world_id":    "w1",
		"description": "a gaunt man in a salt-stained coat",
	}, nil)
	assertError(t, rec, http.StatusNotFound, "not_found")
}

func TestBootstrapCharacterAnchorResolvesSceneCapable(t *testing.T) {
	creator := newStubCreator()
	idents := newStubIdentitiesRepo()
	idents.byOwner[identityKey{tenantA, "w1", "character", "char_alice"}] = identities.VisualIdentity{
		ID:             "vi_alice",
		TenantID:       tenantA,
		WorldID:        "w1",
		OwnerType:      "character",
		OwnerID:        "char_alice",
		DisplayName:    "Alice",
		StyleProfileID: "sty_ok",
	}
	resolver := okResolver()

	r := newBootstrapAnchorRouter(creator, idents, resolver, nil)
	rec := sendJSONWithHeaders(t, r, http.MethodPost, "/v1/characters/char_alice/visual-identity/bootstrap-anchor", tenantA, []string{"images:write"}, map[string]any{
		"world_id":    "w1",
		"description": "a gaunt man in a salt-stained coat",
	}, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if resolver.lastReq.RequiredCapability != "scene_capable" {
		t.Fatalf("required capability=%q, want scene_capable", resolver.lastReq.RequiredCapability)
	}
	if resolver.lastReq.RequiredCapability == "identity_capable" {
		t.Fatalf("required capability should not request identity_capable")
	}
}
