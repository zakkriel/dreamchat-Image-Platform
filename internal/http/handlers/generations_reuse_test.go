package handlers

import (
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/zakkriel/drchat-image-platform/internal/assets"
	"github.com/zakkriel/drchat-image-platform/internal/governance"
	"github.com/zakkriel/drchat-image-platform/internal/providers/routing"
)

// genTierResolver resolves the seeded mock route carrying an explicit quality
// tier, mirroring what the real resolver returns for an intent-driven request
// (no hard quality filter → the route's own tier is the effective tier).
func genTierResolver(tier string) *fakeResolver {
	return &fakeResolver{route: routing.ResolvedRoute{
		ProviderID:        "mock",
		ProviderRouteID:   "route_mock_text_to_image_standard",
		ProviderModelID:   "pm_mock_v1",
		OperationType:     "text_to_image",
		QualityTier:       tier,
		PreviewCapability: "true_preview",
	}}
}

// expectedGenHash is the render hash the handler must stamp for the minimal
// test request (draft intent, no anchors, no transform).
func expectedGenHash() string {
	return assets.GenerationRenderHash(assets.GenerationHashInput{
		TenantID:    tenantA,
		IdentityID:  testIdentityID,
		DisplayName: testIdentityDisplay,
		Intent:      "draft",
	})
}

// The generate path stamps the deterministic render hash and the resolved
// route's quality tier onto the job payload, so the worker persists them onto
// the produced asset and every /v1/generations output joins the reuse cache.
func TestGenerationsStampsRenderHashAndQualityTier(t *testing.T) {
	creator := newStubCreator()
	idRepo := seededGenIDRepo()
	resolver := genTierResolver("standard")
	router := newGenerationsRouter(creator, idRepo, resolver)

	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/generations", tenantA,
		[]string{"images:write"}, minimalGenBody(testIdentityID, "stamp-key-1"), nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(creator.calls) != 1 {
		t.Fatalf("expected 1 create call, got %d", len(creator.calls))
	}
	payload := creator.calls[0].InputPayload
	if got := payload["prompt_hash"]; got != expectedGenHash() {
		t.Fatalf("expected stamped prompt_hash %q, got %v", expectedGenHash(), got)
	}
	if got := payload["quality_tier"]; got != "standard" {
		t.Fatalf("expected stamped quality_tier standard, got %v", got)
	}
}

// newReuseGenerationsRouter wires a generations handler with the exact-reuse
// lookup, mirroring the production mount (router.go mountGenerations).
func newReuseGenerationsRouter(creator *stubCreator, resolver *fakeResolver, reuse GenerationReuseLookup) chi.Router {
	h := NewGenerationsHandler(creator, resolver, seededGenIDRepo())
	h.Verifier = alwaysOKVerifier{}
	h.Mode = governance.ModeEnforce
	h.Audit = noopAuditSink{}
	h.Reuse = reuse
	r := chi.NewRouter()
	r.Post("/v1/generations", h.Create)
	return r
}

// An existing ready asset with the exact render hash completes the request
// synchronously: no route resolution, no CreateAndEnqueue (and therefore no
// cost reservation), a completed cache-hit job, and a free (0.0000) estimate.
func TestGenerationsExactReuseHitSkipsResolveAndReserve(t *testing.T) {
	creator := newStubCreator()
	resolver := genTierResolver("standard")
	reuse := newStubAssetsRepo()
	hash := expectedGenHash()
	reuse.seed(assets.VisualAsset{
		ID:         "va_gen_hit",
		TenantID:   tenantA,
		AssetType:  "artifact",
		VariantKey: "default",
		Status:     "ready",
		PromptHash: &hash,
	})
	router := newReuseGenerationsRouter(creator, resolver, reuse)

	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/generations", tenantA,
		[]string{"images:write"}, minimalGenBody(testIdentityID, "reuse-key-1"), nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	if resolver.calls != 0 {
		t.Fatalf("cache hit must not resolve a route, got %d resolver calls", resolver.calls)
	}
	if len(creator.calls) != 0 {
		t.Fatalf("cache hit must not CreateAndEnqueue (no reservation), got %d calls", len(creator.calls))
	}
	if len(creator.cacheHitCalls) != 1 {
		t.Fatalf("expected exactly one cache-hit job creation, got %d", len(creator.cacheHitCalls))
	}
	hit := creator.cacheHitCalls[0]
	if hit.JobType != "generation" {
		t.Fatalf("expected job_type generation, got %q", hit.JobType)
	}
	if hit.FinalAssetID != "va_gen_hit" {
		t.Fatalf("expected reused asset va_gen_hit, got %q", hit.FinalAssetID)
	}
	body := decode[map[string]any](t, rec)
	if body["estimated_cost_usd"] != "0.0000" {
		t.Fatalf("expected free reuse estimate 0.0000, got %v", body["estimated_cost_usd"])
	}
}

// A reuse miss falls through to the normal resolve/reserve/enqueue path.
func TestGenerationsReuseMissGenerates(t *testing.T) {
	creator := newStubCreator()
	resolver := genTierResolver("standard")
	router := newReuseGenerationsRouter(creator, resolver, newStubAssetsRepo())

	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/generations", tenantA,
		[]string{"images:write"}, minimalGenBody(testIdentityID, "reuse-key-2"), nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(creator.cacheHitCalls) != 0 {
		t.Fatalf("miss must not create a cache-hit job, got %d", len(creator.cacheHitCalls))
	}
	if len(creator.calls) != 1 || resolver.calls != 1 {
		t.Fatalf("miss must resolve + create (resolver=%d, creates=%d)", resolver.calls, len(creator.calls))
	}
}
