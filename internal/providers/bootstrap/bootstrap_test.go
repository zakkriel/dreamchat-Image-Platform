package bootstrap

import (
	"testing"

	"github.com/zakkriel/drchat-image-platform/internal/config"
	"github.com/zakkriel/drchat-image-platform/internal/providers"
)

// TestMockSatisfiesIdentityInDevButNotRealReadiness proves the §8 readiness
// distinction end-to-end at the wiring layer: with only the default (mock)
// provider configured, mock CAN satisfy identity/pack capability tests (dev/test
// can run), but because mock is synthetic it does NOT make production readiness
// report a real identity-capable provider.
func TestMockSatisfiesIdentityInDevButNotRealReadiness(t *testing.T) {
	cfg := &config.Config{} // no BFL key → mock only
	index := CapabilityIndex(cfg)

	mockCaps, ok := index["mock"]
	if !ok {
		t.Fatal("mock must always be configured")
	}
	if !mockCaps.Synthetic {
		t.Fatal("mock must be marked synthetic")
	}
	// Mock still satisfies pack/identity for dev/test routing.
	if !providers.CapabilitiesSatisfy(mockCaps.Capabilities, providers.CapabilityPackCapable) {
		t.Fatal("mock should satisfy pack_capable in dev/test")
	}

	readiness := providers.AssessIdentityReadiness(index)
	if readiness.RealIdentityCapable {
		t.Fatal("mock-only config must NOT report a real identity-capable provider")
	}
	if !readiness.SyntheticIdentityCapable {
		t.Fatal("mock-only config should report synthetic identity capability")
	}
}

// TestBFLGatedByKeyAndSceneOnly proves bfl is only configured when a key is set
// and, when present, is a real (non-synthetic) provider that is scene-only — so
// it never flips real identity readiness on.
func TestBFLGatedByKeyAndSceneOnly(t *testing.T) {
	if _, ok := CapabilityIndex(&config.Config{})["bfl"]; ok {
		t.Fatal("bfl must not be configured without a key")
	}

	cfg := &config.Config{BFLAPIKey: "test-key"}
	index := CapabilityIndex(cfg)
	bflCaps, ok := index["bfl"]
	if !ok {
		t.Fatal("bfl must be configured when a key is set")
	}
	if bflCaps.Synthetic {
		t.Fatal("bfl is a real provider, not synthetic")
	}
	if providers.CapabilitiesSatisfy(bflCaps.Capabilities, providers.CapabilityIdentityCapable) {
		t.Fatal("bfl flux-pro-1.1 is scene-only and must not satisfy identity_capable")
	}

	// Even with bfl configured, there is still no REAL identity-capable provider.
	if providers.AssessIdentityReadiness(index).RealIdentityCapable {
		t.Fatal("bfl (scene-only) + mock (synthetic) must report no real identity-capable provider")
	}
}

// TestFalGatedByKeyAndRealIdentityCapable proves fal is only configured when
// FAL_KEY is set and, when present, is a real (non-synthetic) reference-
// conditioned provider that flips real identity/pack readiness on — the first
// real provider that lets recurring-character work resolve in production.
func TestFalGatedByKeyAndRealIdentityCapable(t *testing.T) {
	if _, ok := CapabilityIndex(&config.Config{})["fal"]; ok {
		t.Fatal("fal must not be configured without FAL_KEY")
	}

	cfg := &config.Config{FalKey: "test-key"}
	index := CapabilityIndex(cfg)
	falCaps, ok := index["fal"]
	if !ok {
		t.Fatal("fal must be configured when FAL_KEY is set")
	}
	if falCaps.Synthetic {
		t.Fatal("fal is a real provider, not synthetic")
	}
	if !falCaps.RequiresReferenceImage {
		t.Fatal("fal must require reference images")
	}
	if !providers.CapabilitiesSatisfy(falCaps.Capabilities, providers.CapabilityPackCapable) {
		t.Fatal("fal must satisfy pack_capable")
	}
	if providers.CapabilitiesSatisfy(falCaps.Capabilities, providers.CapabilityProductionCapable) {
		t.Fatal("fal must NOT claim production_capable without benchmark evidence")
	}

	// fal flips REAL identity readiness on even with ALLOW_SYNTHETIC_PROVIDERS=false:
	// it is a real provider, so character/pack requests resolve in production.
	readiness := providers.AssessIdentityReadiness(index)
	if !readiness.RealIdentityCapable {
		t.Fatal("fal must report a real identity-capable provider")
	}
}
