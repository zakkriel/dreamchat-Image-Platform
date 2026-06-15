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

	// allowSyntheticIdentity=true so the mock pack route is judged on pure
	// capability (this test is about detecting an overstated route, not the
	// synthetic policy).
	report := Reconcile(routes, index, true)

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
	report := Reconcile(nil, index, true)
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
	r2 := Reconcile(nil, onlyBFL, true)
	if r2.Readiness.RealIdentityCapable || r2.Readiness.SyntheticIdentityCapable {
		t.Fatal("scene-only provider alone must report no identity-capable provider configured")
	}
}

// TestReconcileSyntheticPolicyMarksMockPackInvalidWhenDisabled proves the boot
// reconciliation agrees with the resolver: with synthetic identity disabled, a
// mock (synthetic) pack route is flagged invalid by POLICY (not a capability
// gap), while a scene route on the same provider stays valid.
func TestReconcileSyntheticPolicyMarksMockPackInvalidWhenDisabled(t *testing.T) {
	routes := []Route{
		{RouteID: "r_mock_scene", ProviderID: "mock", ModelID: "m_mock", OperationType: "text_to_image", RequiredCapability: "scene_capable", Enabled: true, ModelActive: true},
		{RouteID: "r_mock_pack", ProviderID: "mock", ModelID: "m_mock", OperationType: "text_to_image", RequiredCapability: "pack_capable", Enabled: true, ModelActive: true},
	}
	index := map[string]providers.ProviderCapabilities{
		// Mirrors the real mock adapter: scene + the full identity axis, synthetic.
		"mock": {ProviderID: "mock", Capabilities: []providers.Capability{
			providers.CapabilitySceneCapable, providers.CapabilityProductionCapable,
		}, Synthetic: true},
	}

	disabled := Reconcile(routes, index, false)
	byID := map[string]RouteDecision{}
	for _, d := range disabled.Decisions {
		byID[d.RouteID] = d
	}
	if !byID["r_mock_scene"].Valid {
		t.Errorf("synthetic provider must still back scene routes: %+v", byID["r_mock_scene"])
	}
	if byID["r_mock_pack"].Valid {
		t.Errorf("synthetic pack route must be invalid when synthetic identity disabled: %+v", byID["r_mock_pack"])
	}
	if byID["r_mock_pack"].Reason != reconcileReasonSyntheticDisabled {
		t.Errorf("expected reason %q, got %q", reconcileReasonSyntheticDisabled, byID["r_mock_pack"].Reason)
	}

	// Enabling synthetic identity (dev/test) makes the same pack route valid.
	enabled := Reconcile(routes, index, true)
	for _, d := range enabled.Decisions {
		if d.RouteID == "r_mock_pack" && !d.Valid {
			t.Errorf("synthetic pack route should be valid when synthetic identity enabled: %+v", d)
		}
	}
}

// TestReconcileAcceptsRealFalPackRoute proves boot reconciliation accepts the new
// REAL reference-conditioned provider's pack route under the production synthetic
// policy (ALLOW_SYNTHETIC_PROVIDERS=false): the fal pack route is valid and flips
// real identity readiness on, while the co-located synthetic mock pack route stays
// invalid by policy. This is the slice's core routing claim — a real
// identity/pack provider can serve recurring-character work in production.
func TestReconcileAcceptsRealFalPackRoute(t *testing.T) {
	routes := []Route{
		{RouteID: "route_fal_text_to_image_pack", ProviderID: "fal", ModelID: "pm_fal_flux_kontext_multi", OperationType: "text_to_image", RequiredCapability: "pack_capable", Enabled: true, ModelActive: true},
		{RouteID: "route_mock_text_to_image_pack", ProviderID: "mock", ModelID: "pm_mock_v1", OperationType: "text_to_image", RequiredCapability: "pack_capable", Enabled: true, ModelActive: true},
	}
	index := map[string]providers.ProviderCapabilities{
		// Mirrors the real fal adapter: reference-conditioned identity/pack, real.
		"fal": {ProviderID: "fal", Capabilities: []providers.Capability{
			providers.CapabilitySceneCapable, providers.CapabilityIdentityCapable, providers.CapabilityPackCapable,
		}, RequiresReferenceImage: true},
		"mock": {ProviderID: "mock", Capabilities: []providers.Capability{
			providers.CapabilitySceneCapable, providers.CapabilityProductionCapable,
		}, Synthetic: true},
	}

	// Production policy: synthetic identity disabled.
	report := Reconcile(routes, index, false)
	byID := map[string]RouteDecision{}
	for _, d := range report.Decisions {
		byID[d.RouteID] = d
	}
	if !byID["route_fal_text_to_image_pack"].Valid {
		t.Errorf("real fal pack route must be valid under production policy: %+v", byID["route_fal_text_to_image_pack"])
	}
	if byID["route_mock_text_to_image_pack"].Valid {
		t.Errorf("synthetic mock pack route must stay invalid under production policy: %+v", byID["route_mock_text_to_image_pack"])
	}
	if !report.Readiness.RealIdentityCapable {
		t.Error("fal must flip real identity readiness on")
	}
}
