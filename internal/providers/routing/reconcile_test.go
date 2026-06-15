package routing

import (
	"testing"

	"github.com/zakkriel/drchat-image-platform/internal/providers"
)

// TestReconcileFlagsOverstatedRoute proves boot-time reconciliation marks a route
// invalid when its provider cannot back the capability it claims, and valid when
// the provider satisfies it (PRD 03 §8).
func TestReconcileFlagsOverstatedRoute(t *testing.T) {
	routes := []Route{
		// Valid: bfl scene route, bfl is scene_capable.
		{RouteID: "r_scene", ProviderID: "bfl", ModelID: "m_bfl", OperationType: "text_to_image", RequiredCapability: "scene_capable", Enabled: true, ModelActive: true},
		// Invalid: pack route claiming pack_capable wired to scene-only bfl.
		{RouteID: "r_pack_bogus", ProviderID: "bfl", ModelID: "m_bfl", OperationType: "text_to_image", RequiredCapability: "pack_capable", Enabled: true, ModelActive: true},
		// Valid: mock pack route, mock is production_capable.
		{RouteID: "r_pack_ok", ProviderID: "mock", ModelID: "m_mock", OperationType: "text_to_image", RequiredCapability: "pack_capable", Enabled: true, ModelActive: true},
	}
	index := map[string]providers.ProviderCapabilities{
		"bfl":  {ProviderID: "bfl", Capabilities: []providers.Capability{providers.CapabilityDraftOnly, providers.CapabilitySceneCapable}},
		"mock": {ProviderID: "mock", Capabilities: []providers.Capability{providers.CapabilityProductionCapable}, Synthetic: true},
	}

	report := Reconcile(routes, index)

	byID := map[string]RouteDecision{}
	for _, d := range report.Decisions {
		byID[d.RouteID] = d
	}
	if !byID["r_scene"].Valid {
		t.Errorf("scene route to scene provider should be valid: %+v", byID["r_scene"])
	}
	if byID["r_pack_bogus"].Valid {
		t.Errorf("overstated pack route should be invalid: %+v", byID["r_pack_bogus"])
	}
	if !byID["r_pack_ok"].Valid {
		t.Errorf("mock pack route should be valid: %+v", byID["r_pack_ok"])
	}
	if report.InvalidCount() != 1 {
		t.Errorf("expected exactly 1 invalid route, got %d", report.InvalidCount())
	}
}

// TestReconcileReadinessNoRealIdentityProvider proves the readiness signal:
// with only a scene-only real provider (bfl) and a synthetic mock, there is no
// REAL identity-capable provider configured — the case PRD 03 §8 demands be
// visible rather than silently producing placeholders.
func TestReconcileReadinessNoRealIdentityProvider(t *testing.T) {
	index := map[string]providers.ProviderCapabilities{
		"bfl":  {ProviderID: "bfl", Capabilities: []providers.Capability{providers.CapabilityDraftOnly, providers.CapabilitySceneCapable}},
		"mock": {ProviderID: "mock", Capabilities: []providers.Capability{providers.CapabilityProductionCapable}, Synthetic: true},
	}
	report := Reconcile(nil, index)
	if report.Readiness.RealIdentityCapable {
		t.Fatal("scene-only real provider + synthetic mock must NOT report real identity readiness")
	}
	if !report.Readiness.SyntheticIdentityCapable {
		t.Fatal("synthetic identity capability should be reported separately")
	}

	// Disabling the mock (synthetic) entirely leaves only the scene-only real
	// provider: no identity capability at all.
	onlyBFL := map[string]providers.ProviderCapabilities{
		"bfl": index["bfl"],
	}
	r2 := Reconcile(nil, onlyBFL)
	if r2.Readiness.RealIdentityCapable || r2.Readiness.SyntheticIdentityCapable {
		t.Fatal("scene-only provider alone must report no identity-capable provider configured")
	}
}
