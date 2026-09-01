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
		TenantID:       tenantA,
		IdentityID:     testIdentityID,
		DisplayName:    testIdentityDisplay,
		StyleProfileID: "sty_ok",
		Intent:         "draft",
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
	h := NewGenerationsHandler(creator, resolver, seededGenIDRepo(), seededStyles())
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

// genHashForIntent is the render hash for the minimal test request at a given
// intent — the same key the handler computes.
func genHashForIntent(intent string) string {
	return assets.GenerationRenderHash(assets.GenerationHashInput{
		TenantID:       tenantA,
		IdentityID:     testIdentityID,
		DisplayName:    testIdentityDisplay,
		StyleProfileID: "sty_ok",
		Intent:         intent,
	})
}

// genBodyWithIntent is minimalGenBody at an explicit intent.
func genBodyWithIntent(intent, idempKey string) map[string]any {
	body := minimalGenBody(testIdentityID, idempKey)
	body["render"] = map[string]any{"intent": intent}
	return body
}

// A DRAFT request may be served an existing COMMIT-keyed render: it asked for
// the cheaper tier and gets equal-or-better quality for free instead of paying
// for a second full-price render of the identical subject. intent is part of the
// reuse key (draft and commit rank routes differently), so without the second
// lookup this request generates.
func TestGenerationsDraftReusesCommitKeyedAsset(t *testing.T) {
	creator := newStubCreator()
	resolver := genTierResolver("standard")
	reuse := newStubAssetsRepo()
	commitHash := genHashForIntent("commit")
	reuse.seed(assets.VisualAsset{
		ID:         "va_commit_ready",
		TenantID:   tenantA,
		AssetType:  "artifact",
		VariantKey: "default",
		Status:     "ready",
		PromptHash: &commitHash,
	})
	router := newReuseGenerationsRouter(creator, resolver, reuse)

	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/generations", tenantA,
		[]string{"images:write"}, genBodyWithIntent("draft", "draft-reuses-commit"), nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(creator.cacheHitCalls) != 1 {
		t.Fatalf("a draft must reuse the ready commit render, got %d cache-hit jobs and %d generate calls",
			len(creator.cacheHitCalls), len(creator.calls))
	}
	hit := creator.cacheHitCalls[0]
	if hit.FinalAssetID != "va_commit_ready" {
		t.Fatalf("expected the commit-keyed asset served, got %q", hit.FinalAssetID)
	}
	// The reused job records the REQUEST's own key and intent; the asset carries
	// the key that produced it.
	if got := hit.InputPayload["prompt_hash"]; got != genHashForIntent("draft") {
		t.Fatalf("expected the draft request's own prompt_hash recorded, got %v", got)
	}
	if got := hit.InputPayload["intent"]; got != "draft" {
		t.Fatalf("expected intent draft recorded on the reused job, got %v", got)
	}
	if resolver.calls != 0 || len(creator.calls) != 0 {
		t.Fatalf("a hit must not resolve or reserve (resolver=%d, creates=%d)", resolver.calls, len(creator.calls))
	}
	body := decode[map[string]any](t, rec)
	if body["estimated_cost_usd"] != "0.0000" {
		t.Fatalf("expected free reuse estimate 0.0000, got %v", body["estimated_cost_usd"])
	}
}

// The banned direction: a COMMIT request must never be served a draft-keyed
// asset. That is the silent quality downgrade docs/architecture/cost-control.md
// §7 rejects — the caller asked for the committed tier and would get whatever a
// draft route produced, with nothing in the response saying so. It must MISS and
// generate.
func TestGenerationsCommitNeverReusesDraftKeyedAsset(t *testing.T) {
	creator := newStubCreator()
	resolver := genTierResolver("standard")
	reuse := newStubAssetsRepo()
	draftHash := genHashForIntent("draft")
	reuse.seed(assets.VisualAsset{
		ID:         "va_draft_ready",
		TenantID:   tenantA,
		AssetType:  "artifact",
		VariantKey: "default",
		Status:     "ready",
		PromptHash: &draftHash,
	})
	router := newReuseGenerationsRouter(creator, resolver, reuse)

	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/generations", tenantA,
		[]string{"images:write"}, genBodyWithIntent("commit", "commit-never-reuses-draft"), nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(creator.cacheHitCalls) != 0 {
		t.Fatalf("a commit request must never be served a draft render, got %+v", creator.cacheHitCalls)
	}
	if len(creator.calls) != 1 || resolver.calls != 1 {
		t.Fatalf("a commit request must resolve + generate (resolver=%d, creates=%d)", resolver.calls, len(creator.calls))
	}
}
