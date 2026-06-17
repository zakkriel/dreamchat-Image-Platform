package routing

import (
	"context"
	"errors"
	"testing"
)

// falPackRoute mirrors migrations/0011_fal_provider_seed: fal's pack_capable
// text_to_image route at the standard tier (priority 200).
func falPackRoute() Route {
	return Route{
		RouteID: "route_fal_text_to_image_pack", ProviderID: "fal", ModelID: "pm_fal_flux_kontext_multi",
		OperationType: "text_to_image", RequiredCapability: "pack_capable",
		PreviewCapability: "no_preview", QualityTier: "standard", LatencyTier: "balanced",
		Priority: 200, Enabled: true, ModelActive: true,
	}
}

// mockPackRoute mirrors migrations/0006_bfl_provider_seed: the pack_capable mock
// route (priority 100). Synthetic; only selectable when synthetic identity is
// allowed, but here the capability index is unwired so the synthetic floor is
// not enforced (these tests target the provider-pin filter, not the §8 floor).
func mockPackRoute() Route {
	return Route{
		RouteID: "route_mock_text_to_image_pack", ProviderID: "mock", ModelID: "pm_mock_v1",
		OperationType: "text_to_image", RequiredCapability: "pack_capable",
		PreviewCapability: "true_preview", QualityTier: "standard", LatencyTier: "balanced",
		Priority: 100, Enabled: true, ModelActive: true,
	}
}

// The full text_to_image route set when bfl + fal + mock are all wired, matching
// the seeded production data (scene: mock/bfl; pack: mock/fal).
func allTextToImageRoutes() []Route {
	return []Route{mockRoute(), bflRoute(), mockPackRoute(), falPackRoute()}
}

func allAvailable() map[string]bool {
	return map[string]bool{"mock": true, "bfl": true, "fal": true}
}

// TestProviderPinArtifactBFLResolvesBFLRoute: an artifact (scene_capable) request
// pinned to bfl resolves the BFL scene route even though mock (priority 100) is
// the default — the hard pin overrides the default tie-break.
func TestProviderPinArtifactBFLResolvesBFLRoute(t *testing.T) {
	got, err := resolve(t, allTextToImageRoutes(), allAvailable(), ResolveRequest{
		OperationType:      "text_to_image",
		QualityTier:        "standard",
		RequiredCapability: "scene_capable",
		ProviderID:         "bfl",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.ProviderID != "bfl" || got.ProviderRouteID != "route_bfl" {
		t.Fatalf("expected pinned bfl scene route, got %+v", got)
	}
}

// TestProviderPinPackFalResolvesFalPackRoute: a pack (pack_capable) request
// pinned to fal resolves the fal pack route even though the mock pack route
// (priority 100) would otherwise win the tie-break.
func TestProviderPinPackFalResolvesFalPackRoute(t *testing.T) {
	got, err := resolve(t, allTextToImageRoutes(), allAvailable(), ResolveRequest{
		OperationType:      "text_to_image",
		QualityTier:        "standard",
		RequiredCapability: "pack_capable",
		ProviderID:         "fal",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.ProviderID != "fal" || got.ProviderRouteID != "route_fal_text_to_image_pack" {
		t.Fatalf("expected pinned fal pack route, got %+v", got)
	}
}

// TestProviderPinPackBFLFailsClosed: a pack request pinned to bfl fails closed.
// bfl has only a scene_capable route, so after pinning to bfl no route satisfies
// pack_capable → ErrUnsupportedCapability (422), NOT a silent fallback to fal/mock.
func TestProviderPinPackBFLFailsClosed(t *testing.T) {
	_, err := resolve(t, allTextToImageRoutes(), allAvailable(), ResolveRequest{
		OperationType:      "text_to_image",
		QualityTier:        "standard",
		RequiredCapability: "pack_capable",
		ProviderID:         "bfl",
	})
	if !errors.Is(err, ErrUnsupportedCapability) {
		t.Fatalf("expected ErrUnsupportedCapability (fail closed), got %v", err)
	}
}

// TestProviderPinArtifactFalDoesNotResolvePackRoute: an artifact (scene_capable)
// request pinned to fal must NOT resolve fal's pack_capable route. fal has no
// scene_capable route, so capability matching stays exact and the request fails
// closed rather than serving a pack route to a scene request.
func TestProviderPinArtifactFalDoesNotResolvePackRoute(t *testing.T) {
	got, err := resolve(t, allTextToImageRoutes(), allAvailable(), ResolveRequest{
		OperationType:      "text_to_image",
		QualityTier:        "standard",
		RequiredCapability: "scene_capable",
		ProviderID:         "fal",
	})
	if err == nil {
		t.Fatalf("expected fail closed, got resolved route %+v", got)
	}
	if !errors.Is(err, ErrUnsupportedCapability) {
		t.Fatalf("expected ErrUnsupportedCapability, got %v", err)
	}
}

// TestProviderPinUnavailableFailsClosed: pinning a provider not configured in
// this process returns the dedicated ErrRequestedProviderUnavailable sentinel,
// not a silent fallback to an available provider.
func TestProviderPinUnavailableFailsClosed(t *testing.T) {
	// fal pinned but only mock + bfl are available.
	_, err := resolve(t, allTextToImageRoutes(), map[string]bool{"mock": true, "bfl": true}, ResolveRequest{
		OperationType:      "text_to_image",
		QualityTier:        "standard",
		RequiredCapability: "pack_capable",
		ProviderID:         "fal",
	})
	if !errors.Is(err, ErrRequestedProviderUnavailable) {
		t.Fatalf("expected ErrRequestedProviderUnavailable, got %v", err)
	}
}

// TestNoProviderPinPreservesDefault: with no pin, resolution is unchanged — the
// default tie-break picks mock (priority 100) over bfl (200) for a scene request.
func TestNoProviderPinPreservesDefault(t *testing.T) {
	got, err := resolve(t, allTextToImageRoutes(), allAvailable(), ResolveRequest{
		OperationType:      "text_to_image",
		QualityTier:        "standard",
		RequiredCapability: "scene_capable",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.ProviderID != "mock" {
		t.Fatalf("expected default mock route with no pin, got %+v", got)
	}
}

// TestProviderPinOverridesSoftPreference: a hard ProviderID pin takes precedence
// over the soft ProviderPreference (IMAGE_PROVIDER) tie-break — the pinned
// provider is resolved even when the soft preference names another.
func TestProviderPinOverridesSoftPreference(t *testing.T) {
	got, err := resolve(t, allTextToImageRoutes(), allAvailable(), ResolveRequest{
		OperationType:      "text_to_image",
		QualityTier:        "standard",
		RequiredCapability: "scene_capable",
		ProviderPreference: "mock", // deployment default leans mock
		ProviderID:         "bfl",  // per-request pin wins
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.ProviderID != "bfl" {
		t.Fatalf("expected hard pin bfl to beat soft preference mock, got %+v", got)
	}
}

// TestProviderPinChainSharesFilters: ResolveChain applies the same pin filter as
// Resolve, so a pinned fal pack request yields a chain whose only entries are fal
// routes (here, the single fal pack route).
func TestProviderPinChainSharesFilters(t *testing.T) {
	chain, err := NewResolver(fakeSource{routes: allTextToImageRoutes()}, allAvailable()).
		ResolveChain(context.Background(), ResolveRequest{
			OperationType:      "text_to_image",
			QualityTier:        "standard",
			RequiredCapability: "pack_capable",
			ProviderID:         "fal",
		})
	if err != nil {
		t.Fatalf("ResolveChain: %v", err)
	}
	for _, rt := range chain {
		if rt.ProviderID != "fal" {
			t.Fatalf("expected only fal routes in pinned chain, got %+v", chain)
		}
	}
	if len(chain) != 1 {
		t.Fatalf("expected single fal pack route, got %+v", chain)
	}
}
