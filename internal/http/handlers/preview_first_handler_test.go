package handlers

import (
	"net/http"
	"testing"

	"github.com/zakkriel/drchat-image-platform/internal/idempotency"
	"github.com/zakkriel/drchat-image-platform/internal/providers/routing"
)

// Phase 7B handler tests: the delivery_mode=preview_first opt-in must impose a
// hard true_preview routing requirement, persist the intent on the job payload,
// and leave final_only/omitted requests on the unchanged Phase 7A path. Packs
// must never two-phase.

// 1. artifact delivery_mode=preview_first sets RequiredPreviewCapability=true_preview.
func TestArtifactPreviewFirstSetsTruePreviewCapability(t *testing.T) {
	creator := newStubCreator()
	resolver := okResolver()
	router := newArtifactsRouterWithResolver(creator, seededStyles(), resolver, "mock", nil)
	body := map[string]any{
		"world_id":         "w1",
		"style_profile_id": "sty_ok",
		"description":      "A bronze key",
		"delivery_mode":    "preview_first",
	}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_pf/generate",
		tenantA, []string{"images:write"}, body, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	if resolver.lastReq.RequiredPreviewCapability != "true_preview" {
		t.Fatalf("preview_first must request true_preview, got %q", resolver.lastReq.RequiredPreviewCapability)
	}
	if resolver.lastReq.RequiredCapability != "scene_capable" {
		t.Fatalf("preview_first artifact must keep scene_capable, got %q", resolver.lastReq.RequiredCapability)
	}
}

// 4. preview-first intent is persisted in the job payload.
func TestArtifactPreviewFirstPersistsDeliveryModeInPayload(t *testing.T) {
	creator := newStubCreator()
	router := newArtifactsRouterWithResolver(creator, seededStyles(), okResolver(), "mock", nil)
	body := map[string]any{
		"world_id":         "w1",
		"style_profile_id": "sty_ok",
		"description":      "A bronze key",
		"delivery_mode":    "preview_first",
	}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_pf2/generate",
		tenantA, []string{"images:write"}, body, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(creator.calls) != 1 {
		t.Fatalf("expected one create call, got %d", len(creator.calls))
	}
	if creator.calls[0].InputPayload["delivery_mode"] != "preview_first" {
		t.Fatalf("payload must carry delivery_mode=preview_first, got %v", creator.calls[0].InputPayload["delivery_mode"])
	}
	// The resolved route's preview capability is persisted so the worker can
	// confirm true_preview without re-resolving.
	if creator.calls[0].InputPayload["preview_capability"] != "true_preview" {
		t.Fatalf("payload must carry preview_capability=true_preview, got %v", creator.calls[0].InputPayload["preview_capability"])
	}
}

// 3. omitted/final_only does not set the preview capability requirement, and
// does not persist a delivery_mode flag (the unchanged Phase 7A shape).
func TestArtifactFinalOnlyDoesNotSetPreviewCapability(t *testing.T) {
	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{"omitted", map[string]any{"world_id": "w1", "style_profile_id": "sty_ok", "description": "x"}},
		{"final_only", map[string]any{"world_id": "w1", "style_profile_id": "sty_ok", "description": "x", "delivery_mode": "final_only"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			creator := newStubCreator()
			resolver := okResolver()
			router := newArtifactsRouterWithResolver(creator, seededStyles(), resolver, "mock", nil)
			rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_fo/generate",
				tenantA, []string{"images:write"}, tc.body, nil)
			if rec.Code != http.StatusAccepted {
				t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
			}
			if resolver.lastReq.RequiredPreviewCapability != "" {
				t.Fatalf("final_only must not set a preview requirement, got %q", resolver.lastReq.RequiredPreviewCapability)
			}
			if _, ok := creator.calls[0].InputPayload["delivery_mode"]; ok {
				t.Fatalf("final_only/omitted must not persist a delivery_mode flag, got %v", creator.calls[0].InputPayload["delivery_mode"])
			}
		})
	}
}

// 6. preview-first unsupported capability returns 422 before cost/job/enqueue.
func TestArtifactPreviewFirstUnsupportedReturns422BeforeWrites(t *testing.T) {
	creator := newStubCreator()
	resolver := &fakeResolver{err: routing.ErrUnsupportedCapability}
	router := newArtifactsRouterWithResolver(creator, seededStyles(), resolver, "mock", nil)
	body := map[string]any{
		"world_id":         "w1",
		"style_profile_id": "sty_ok",
		"description":      "x",
		"delivery_mode":    "preview_first",
	}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_pf_bfl/generate",
		tenantA, []string{"images:write"}, body, nil)
	assertError(t, rec, http.StatusUnprocessableEntity, "unsupported_capability")
	if len(creator.calls) != 0 {
		t.Fatalf("preview_first unsupported must not reserve/create/enqueue, got %d calls", len(creator.calls))
	}
}

// Invalid delivery_mode is a 400 invalid_request.
func TestArtifactInvalidDeliveryModeReturns400(t *testing.T) {
	creator := newStubCreator()
	router := newArtifactsRouterWithResolver(creator, seededStyles(), okResolver(), "mock", nil)
	body := map[string]any{
		"world_id":         "w1",
		"style_profile_id": "sty_ok",
		"description":      "x",
		"delivery_mode":    "sometimes",
	}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_bad/generate",
		tenantA, []string{"images:write"}, body, nil)
	assertError(t, rec, http.StatusBadRequest, "invalid_request")
	if len(creator.calls) != 0 {
		t.Fatalf("invalid delivery_mode must not create a job, got %d calls", len(creator.calls))
	}
}

// 5. idempotency replay does not re-resolve (route resolution stays after the
// replay short-circuit, exactly as Phase 7A).
func TestArtifactPreviewFirstReplayDoesNotReResolve(t *testing.T) {
	creator := newStubCreator()
	resolver := okResolver()
	router := newArtifactsRouterWithResolver(creator, seededStyles(), resolver, "mock", nil)
	body := map[string]any{
		"world_id":         "w1",
		"style_profile_id": "sty_ok",
		"description":      "A bronze key",
		"delivery_mode":    "preview_first",
	}
	headers := map[string]string{idempotency.HeaderKey: "pf-replay-1"}
	// First call creates the job and resolves once.
	rec1 := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_pf_replay/generate",
		tenantA, []string{"images:write"}, body, headers)
	if rec1.Code != http.StatusAccepted {
		t.Fatalf("first call expected 202, got %d body=%s", rec1.Code, rec1.Body.String())
	}
	callsAfterFirst := resolver.calls
	// Replay (same token, key, endpoint, body) must short-circuit before resolution.
	rec2 := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_pf_replay/generate",
		tenantA, []string{"images:write"}, body, headers)
	if rec2.Code != http.StatusAccepted {
		t.Fatalf("replay expected 202, got %d body=%s", rec2.Code, rec2.Body.String())
	}
	if resolver.calls != callsAfterFirst {
		t.Fatalf("replay must not re-resolve: resolver called %d times before, %d after", callsAfterFirst, resolver.calls)
	}
	if len(creator.calls) != 1 {
		t.Fatalf("replay must not create a second job, got %d create calls", len(creator.calls))
	}
}

// 2. style preview delivery_mode=preview_first sets RequiredPreviewCapability=true_preview.
func TestStylePreviewPreviewFirstSetsTruePreviewCapability(t *testing.T) {
	creator := newStubCreator()
	resolver := okResolver()
	router := newStylePreviewRouterWithResolver(creator, seededStyles(), resolver)
	body := map[string]any{"world_id": "w1", "delivery_mode": "preview_first"}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/styles/sty_ok/preview",
		tenantA, []string{"images:write"}, body, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	if resolver.lastReq.RequiredPreviewCapability != "true_preview" {
		t.Fatalf("style preview_first must request true_preview, got %q", resolver.lastReq.RequiredPreviewCapability)
	}
	if creator.calls[0].InputPayload["delivery_mode"] != "preview_first" {
		t.Fatalf("style preview payload must carry delivery_mode=preview_first, got %v", creator.calls[0].InputPayload["delivery_mode"])
	}
}

func TestStylePreviewFinalOnlyDoesNotSetPreviewCapability(t *testing.T) {
	creator := newStubCreator()
	resolver := okResolver()
	router := newStylePreviewRouterWithResolver(creator, seededStyles(), resolver)
	body := map[string]any{"world_id": "w1"}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/styles/sty_ok/preview",
		tenantA, []string{"images:write"}, body, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	if resolver.lastReq.RequiredPreviewCapability != "" {
		t.Fatalf("final_only style preview must not set preview requirement, got %q", resolver.lastReq.RequiredPreviewCapability)
	}
}

// 7. pack endpoints do not support preview-first: a delivery_mode field in the
// body is ignored (the pack schema does not expose it), so the resolver is asked
// for pack_capable with NO preview requirement and the pack proceeds normally.
func TestPackEndpointsIgnoreDeliveryMode(t *testing.T) {
	creator := &estimatingPackCreator{}
	resolver := okResolver()
	router := newPacksRouterWithResolver(creator, seededPackIdentities(), resolver)
	body := map[string]any{
		"world_id":         packWorldID,
		"style_profile_id": "sty_ok",
		"delivery_mode":    "preview_first",
	}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/characters/char_hero/generate-pack",
		tenantA, []string{"images:write"}, body, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	if resolver.lastReq.RequiredPreviewCapability != "" {
		t.Fatalf("packs must never impose a preview requirement, got %q", resolver.lastReq.RequiredPreviewCapability)
	}
	if resolver.lastReq.RequiredCapability != "pack_capable" {
		t.Fatalf("packs must resolve pack_capable, got %q", resolver.lastReq.RequiredCapability)
	}
}
