package handlers

import (
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/zakkriel/drchat-image-platform/internal/governance"
	"github.com/zakkriel/drchat-image-platform/internal/identities"
)

// genIDRepoWithWorld seeds the generations identity with a NON-EMPTY world_id.
// The shared seededGenIDRepo leaves WorldID empty, which makes any world-lineage
// assertion vacuous — visual_identities.world_id is NOT NULL in the schema, so
// an empty world is not a realistic fixture for this test.
func genIDRepoWithWorld(worldID string) *stubIdentitiesRepo {
	repo := newStubIdentitiesRepo()
	repo.byOwner[identityKey{tenantA, worldID, "character", "char_alice"}] = identities.VisualIdentity{
		ID:          testIdentityID,
		TenantID:    tenantA,
		WorldID:     worldID,
		DisplayName: testIdentityDisplay,
	}
	return repo
}

func newGenerationsHandlerWithReuse(creator *stubCreator, idRepo *stubIdentitiesRepo, reuse GenerationReuseLookup) chi.Router {
	h := NewGenerationsHandler(creator, okResolver(), idRepo, seededStyles())
	h.Verifier = alwaysOKVerifier{}
	h.Mode = governance.ModeEnforce
	h.Audit = noopAuditSink{}
	h.Reuse = reuse
	r := chi.NewRouter()
	r.Post("/v1/generations", h.Create)
	return r
}

// TestGenerationsCacheHitMatchesMissPathLineage pins the invariant that a reused
// generation job is the SAME job as a generated one with the provider work
// skipped: its policy and lineage columns must match what the miss path records.
//
// This is a regression test. The cache-hit path omitted FallbackPolicy entirely,
// so the column defaulted to "" and the INSERT violated
// generation_jobs_fallback_policy_check — every exact reuse on
// POST /v1/generations returned 500. The pre-existing cache-hit test passed
// because a stub creator accepts any params, so nothing compared the two paths.
func TestGenerationsCacheHitMatchesMissPathLineage(t *testing.T) {
	const worldID = "world_lineage"

	// Miss path: no reuse lookup wired, so the request generates.
	missCreator := newStubCreator()
	missRouter := newGenerationsRouter(missCreator, genIDRepoWithWorld(worldID), okResolver())
	rec := sendJSONWithHeaders(t, missRouter, http.MethodPost, "/v1/generations", tenantA,
		[]string{"images:write"}, minimalGenBody(testIdentityID, "idem-lineage-miss"), nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("miss: expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(missCreator.calls) != 1 {
		t.Fatalf("miss: expected 1 create call, got %d", len(missCreator.calls))
	}
	miss := missCreator.calls[0]

	// Hit path: the same request against a ready asset.
	hitCreator := newStubCreator()
	hitRouter := newGenerationsHandlerWithReuse(hitCreator, genIDRepoWithWorld(worldID), generationReuseHit{})
	rec = sendJSONWithHeaders(t, hitRouter, http.MethodPost, "/v1/generations", tenantA,
		[]string{"images:write"}, minimalGenBody(testIdentityID, "idem-lineage-hit"), nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("hit: expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(hitCreator.cacheHitCalls) != 1 {
		t.Fatalf("hit: expected 1 cache-hit call, got %d", len(hitCreator.cacheHitCalls))
	}
	hit := hitCreator.cacheHitCalls[0]

	// fallback_policy is NOT NULLable and CHECK-constrained; an empty value is a
	// guaranteed insert failure, not a benign default.
	if hit.FallbackPolicy == "" {
		t.Fatal("cache-hit FallbackPolicy is empty: generation_jobs.fallback_policy is CHECK-constrained, so this insert can only fail")
	}
	if hit.FallbackPolicy != miss.FallbackPolicy {
		t.Fatalf("FallbackPolicy diverges: miss=%q hit=%q", miss.FallbackPolicy, hit.FallbackPolicy)
	}
	if hit.WorldID != miss.WorldID {
		t.Fatalf("WorldID diverges: miss=%q hit=%q", miss.WorldID, hit.WorldID)
	}
	if hit.WorldID != worldID {
		t.Fatalf("cache-hit WorldID should come from the identity: want %q, got %q", worldID, hit.WorldID)
	}
	if hit.JobType != miss.JobType {
		t.Fatalf("JobType diverges: miss=%q hit=%q", miss.JobType, hit.JobType)
	}
	if hit.TenantID != tenantA {
		t.Fatalf("cache-hit TenantID: want %q, got %q", tenantA, hit.TenantID)
	}
}

// TestGenerationsCacheHitFallbackPolicyIsSchemaValid asserts the emitted value is
// one the DB actually accepts, independent of the miss path — so changing both
// paths to the same invalid value still fails.
func TestGenerationsCacheHitFallbackPolicyIsSchemaValid(t *testing.T) {
	creator := newStubCreator()
	router := newGenerationsHandlerWithReuse(creator, genIDRepoWithWorld("world_valid"), generationReuseHit{})

	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/generations", tenantA,
		[]string{"images:write"}, minimalGenBody(testIdentityID, "idem-policy-valid"), nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(creator.cacheHitCalls) != 1 {
		t.Fatalf("expected 1 cache-hit call, got %d", len(creator.cacheHitCalls))
	}

	// Mirrors generation_jobs_fallback_policy_check in migrations/0001_initial.sql.
	allowed := map[string]bool{
		"none":            true,
		"compatible_only": true,
		"preview_allowed": true,
		"any_existing":    true,
	}
	if got := creator.cacheHitCalls[0].FallbackPolicy; !allowed[got] {
		t.Fatalf("cache-hit FallbackPolicy %q is not accepted by generation_jobs_fallback_policy_check", got)
	}
}
