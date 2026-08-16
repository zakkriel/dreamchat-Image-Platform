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
		"world_id": "w1",
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
		"world_id": "w1",
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

func TestBootstrapCharacterAnchorUnknownCharacterReturns404(t *testing.T) {
	r := newBootstrapAnchorRouter(newStubCreator(), newStubIdentitiesRepo(), okResolver(), nil)
	rec := sendJSONWithHeaders(t, r, http.MethodPost, "/v1/characters/char_missing/visual-identity/bootstrap-anchor", tenantA, []string{"images:write"}, map[string]any{
		"world_id": "w1",
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
		"world_id": "w1",
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
