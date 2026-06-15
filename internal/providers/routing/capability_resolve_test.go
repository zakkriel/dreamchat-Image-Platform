package routing

import (
	"context"
	"errors"
	"testing"

	"github.com/zakkriel/drchat-image-platform/internal/providers"
)

// capIndex builds a provider capability index for the resolver's
// provider-satisfies-route filter (PRD 03 §8).
func capIndex(entries map[string][]providers.Capability) map[string]providers.ProviderCapabilities {
	out := make(map[string]providers.ProviderCapabilities, len(entries))
	for id, caps := range entries {
		out[id] = providers.ProviderCapabilities{ProviderID: id, Capabilities: caps}
	}
	return out
}

func resolveWithCaps(t *testing.T, routes []Route, available map[string]bool, caps map[string]providers.ProviderCapabilities, req ResolveRequest) (ResolvedRoute, error) {
	t.Helper()
	return NewResolver(fakeSource{routes: routes}, available).
		WithProviderCapabilities(caps).
		Resolve(context.Background(), req)
}

// TestDBRoutePackRejectedWhenProviderOnlyScene is the core config-trust test: a
// DB route CLAIMS pack_capable but its provider's adapter only advertises
// scene_capable/draft_only. The resolver must drop the route (fail closed)
// instead of trusting config and routing identity work to a scene-only provider.
func TestDBRoutePackRejectedWhenProviderOnlyScene(t *testing.T) {
	// A pack_capable route wired to "bfl", whose adapter is scene/draft only.
	bogus := bflRoute()
	bogus.RouteID, bogus.RequiredCapability = "route_bfl_pack_bogus", "pack_capable"

	caps := capIndex(map[string][]providers.Capability{
		"bfl": {providers.CapabilityDraftOnly, providers.CapabilitySceneCapable},
	})
	_, err := resolveWithCaps(t, []Route{bogus}, map[string]bool{"bfl": true}, caps,
		ResolveRequest{OperationType: "text_to_image", QualityTier: "standard", RequiredCapability: "pack_capable"})
	if !errors.Is(err, ErrRouteProviderCapabilityMismatch) {
		t.Fatalf("expected ErrRouteProviderCapabilityMismatch, got %v", err)
	}
}

// TestProductionProviderSatisfiesPackAndIdentityRoutes proves the §8.3 hierarchy
// is applied to provider-satisfies-route: a production_capable provider satisfies
// a pack_capable route AND an identity_capable route.
func TestProductionProviderSatisfiesPackAndIdentityRoutes(t *testing.T) {
	caps := capIndex(map[string][]providers.Capability{
		"mock": {providers.CapabilityProductionCapable},
	})

	pack := mockRoute()
	pack.RouteID, pack.RequiredCapability = "route_pack", "pack_capable"
	got, err := resolveWithCaps(t, []Route{pack}, map[string]bool{"mock": true}, caps,
		ResolveRequest{OperationType: "text_to_image", QualityTier: "standard", RequiredCapability: "pack_capable"})
	if err != nil {
		t.Fatalf("production provider should satisfy pack route: %v", err)
	}
	if got.ProviderRouteID != "route_pack" {
		t.Fatalf("unexpected route: %+v", got)
	}

	identity := mockRoute()
	identity.RouteID, identity.RequiredCapability = "route_identity", "identity_capable"
	got, err = resolveWithCaps(t, []Route{identity}, map[string]bool{"mock": true}, caps,
		ResolveRequest{OperationType: "text_to_image", QualityTier: "standard", RequiredCapability: "identity_capable"})
	if err != nil {
		t.Fatalf("production provider should satisfy identity route: %v", err)
	}
	if got.ProviderRouteID != "route_identity" {
		t.Fatalf("unexpected route: %+v", got)
	}
}

// TestSceneRoutingRemainsValidForSceneProvider proves the new check does not
// break the existing scene/place/artifact path: a scene_capable route wired to a
// scene_capable provider still resolves.
func TestSceneRoutingRemainsValidForSceneProvider(t *testing.T) {
	caps := capIndex(map[string][]providers.Capability{
		"bfl": {providers.CapabilityDraftOnly, providers.CapabilitySceneCapable},
	})
	got, err := resolveWithCaps(t, []Route{bflRoute()}, map[string]bool{"bfl": true}, caps,
		ResolveRequest{OperationType: "text_to_image", QualityTier: "standard", RequiredCapability: "scene_capable"})
	if err != nil {
		t.Fatalf("scene route to scene provider must resolve: %v", err)
	}
	if got.ProviderID != "bfl" {
		t.Fatalf("expected bfl scene route, got %+v", got)
	}
}

// TestProviderSatisfiesDropsMisconfiguredButKeepsValid proves a misconfigured
// route is dropped while a valid sibling route survives in the same request.
func TestProviderSatisfiesDropsMisconfiguredButKeepsValid(t *testing.T) {
	good := mockRoute() // mock is production_capable below
	good.RouteID, good.RequiredCapability = "route_mock_pack", "pack_capable"
	bogus := bflRoute()
	bogus.RouteID, bogus.RequiredCapability = "route_bfl_pack_bogus", "pack_capable"

	caps := capIndex(map[string][]providers.Capability{
		"mock": {providers.CapabilityProductionCapable},
		"bfl":  {providers.CapabilityDraftOnly, providers.CapabilitySceneCapable},
	})
	got, err := resolveWithCaps(t, []Route{bogus, good}, map[string]bool{"mock": true, "bfl": true}, caps,
		ResolveRequest{OperationType: "text_to_image", QualityTier: "standard", RequiredCapability: "pack_capable"})
	if err != nil {
		t.Fatalf("valid sibling should resolve: %v", err)
	}
	if got.ProviderID != "mock" {
		t.Fatalf("expected the valid mock pack route, got %+v", got)
	}
}

// TestRequestToRouteMatchingRemainsExactWithCaps proves the new hierarchy applies
// ONLY to provider-satisfies-route — NOT to request-to-route matching. A
// scene_capable request must NOT collapse onto a pack_capable route even though
// the provider could satisfy pack (and therefore identity). This stops cheap
// scene work being routed to an expensive identity/pack route.
func TestRequestToRouteMatchingRemainsExactWithCaps(t *testing.T) {
	pack := mockRoute()
	pack.RouteID, pack.RequiredCapability = "route_pack", "pack_capable"
	caps := capIndex(map[string][]providers.Capability{
		"mock": {providers.CapabilityProductionCapable},
	})
	// Only a pack_capable route exists; a scene_capable request must report
	// unsupported_capability, never silently take the pack route.
	if _, err := resolveWithCaps(t, []Route{pack}, map[string]bool{"mock": true}, caps,
		ResolveRequest{OperationType: "text_to_image", QualityTier: "standard", RequiredCapability: "scene_capable"}); !errors.Is(err, ErrUnsupportedCapability) {
		t.Fatalf("scene request must not collapse to pack route, got %v", err)
	}
}
