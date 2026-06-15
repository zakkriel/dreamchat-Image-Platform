package providers

import "testing"

// TestCapabilitySatisfiesHierarchy pins the §8.3 capability hierarchy used for
// provider-satisfies-route validation: production ⊇ pack ⊇ identity, with
// scene_capable and draft_only as parallel axes that only satisfy themselves.
func TestCapabilitySatisfiesHierarchy(t *testing.T) {
	cases := []struct {
		have Capability
		need Capability
		want bool
	}{
		// Exact match always satisfies.
		{CapabilityDraftOnly, CapabilityDraftOnly, true},
		{CapabilitySceneCapable, CapabilitySceneCapable, true},
		{CapabilityIdentityCapable, CapabilityIdentityCapable, true},
		{CapabilityPackCapable, CapabilityPackCapable, true},
		{CapabilityProductionCapable, CapabilityProductionCapable, true},

		// production_capable satisfies pack_capable and identity_capable.
		{CapabilityProductionCapable, CapabilityPackCapable, true},
		{CapabilityProductionCapable, CapabilityIdentityCapable, true},

		// pack_capable satisfies identity_capable.
		{CapabilityPackCapable, CapabilityIdentityCapable, true},

		// identity_capable does NOT climb to pack/production.
		{CapabilityIdentityCapable, CapabilityPackCapable, false},
		{CapabilityIdentityCapable, CapabilityProductionCapable, false},

		// scene_capable / draft_only never satisfy identity or pack (the bug this
		// whole change exists to prevent — routing cheap scene work would never be
		// allowed to claim identity/pack).
		{CapabilitySceneCapable, CapabilityIdentityCapable, false},
		{CapabilitySceneCapable, CapabilityPackCapable, false},
		{CapabilitySceneCapable, CapabilityProductionCapable, false},
		{CapabilityDraftOnly, CapabilityIdentityCapable, false},
		{CapabilityDraftOnly, CapabilityPackCapable, false},
		{CapabilityDraftOnly, CapabilitySceneCapable, false},

		// The identity axis does NOT imply the scene axis (they are parallel).
		{CapabilityProductionCapable, CapabilitySceneCapable, false},
		{CapabilityPackCapable, CapabilitySceneCapable, false},

		// Unknown / empty capabilities fail closed (only exact match).
		{Capability("future_tier"), CapabilityIdentityCapable, false},
		{Capability(""), CapabilityIdentityCapable, false},
	}
	for _, c := range cases {
		if got := CapabilitySatisfies(c.have, c.need); got != c.want {
			t.Errorf("CapabilitySatisfies(%q, %q) = %v, want %v", c.have, c.need, got, c.want)
		}
	}
}

func TestCapabilitiesSatisfyAnyAdvertised(t *testing.T) {
	// A provider advertising the explicit list still satisfies via any one entry.
	scene := []Capability{CapabilityDraftOnly, CapabilitySceneCapable}
	if CapabilitiesSatisfy(scene, CapabilityIdentityCapable) {
		t.Fatal("scene/draft provider must not satisfy identity_capable")
	}
	if !CapabilitiesSatisfy(scene, CapabilitySceneCapable) {
		t.Fatal("scene provider must satisfy scene_capable")
	}

	// A single production_capable entry satisfies the whole identity axis.
	prod := []Capability{CapabilityProductionCapable}
	for _, need := range []Capability{CapabilityIdentityCapable, CapabilityPackCapable, CapabilityProductionCapable} {
		if !CapabilitiesSatisfy(prod, need) {
			t.Fatalf("production_capable must satisfy %q", need)
		}
	}

	// Empty advertisement satisfies nothing.
	if CapabilitiesSatisfy(nil, CapabilitySceneCapable) {
		t.Fatal("no advertised capability must satisfy nothing")
	}
}

// TestAssessIdentityReadinessRealVsSynthetic proves a synthetic/test provider
// (mock) does not make production readiness report an identity-capable provider,
// while a real provider that satisfies identity does.
func TestAssessIdentityReadinessRealVsSynthetic(t *testing.T) {
	// Only a synthetic provider advertises identity → real readiness is false.
	synthOnly := map[string]ProviderCapabilities{
		"mock": {ProviderID: "mock", Capabilities: []Capability{CapabilityProductionCapable}, Synthetic: true},
		"bfl":  {ProviderID: "bfl", Capabilities: []Capability{CapabilityDraftOnly, CapabilitySceneCapable}},
	}
	r := AssessIdentityReadiness(synthOnly)
	if r.RealIdentityCapable {
		t.Fatal("synthetic-only identity must not count as real identity readiness")
	}
	if !r.SyntheticIdentityCapable {
		t.Fatal("synthetic identity capability should still be reported")
	}

	// A real provider advertising identity flips real readiness on.
	withReal := map[string]ProviderCapabilities{
		"mock":      {ProviderID: "mock", Capabilities: []Capability{CapabilityProductionCapable}, Synthetic: true},
		"realident": {ProviderID: "realident", Capabilities: []Capability{CapabilityIdentityCapable}},
	}
	r2 := AssessIdentityReadiness(withReal)
	if !r2.RealIdentityCapable {
		t.Fatal("a real identity_capable provider must report real readiness")
	}
}
