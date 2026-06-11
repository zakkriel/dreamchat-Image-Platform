package routing

import (
	"context"
	"errors"
	"testing"
)

// fakeSource is an in-memory RouteSource so the resolver is exercised without a
// database. It returns every route whose OperationType matches.
type fakeSource struct {
	routes []Route
	err    error
}

func (f fakeSource) ListRoutes(_ context.Context, op string) ([]Route, error) {
	if f.err != nil {
		return nil, f.err
	}
	var out []Route
	for _, r := range f.routes {
		if r.OperationType == op {
			out = append(out, r)
		}
	}
	return out, nil
}

func mockRoute() Route {
	return Route{
		RouteID: "route_mock", ProviderID: "mock", ModelID: "pm_mock_v1",
		OperationType: "text_to_image", RequiredCapability: "scene_capable",
		PreviewCapability: "true_preview", QualityTier: "standard", LatencyTier: "balanced",
		Priority: 100, Enabled: true, ModelActive: true,
	}
}

func bflRoute() Route {
	return Route{
		RouteID: "route_bfl", ProviderID: "bfl", ModelID: "pm_bfl",
		OperationType: "text_to_image", RequiredCapability: "scene_capable",
		PreviewCapability: "no_preview", QualityTier: "standard", LatencyTier: "balanced",
		Priority: 200, Enabled: true, ModelActive: true,
	}
}

func resolve(t *testing.T, routes []Route, available map[string]bool, req ResolveRequest) (ResolvedRoute, error) {
	t.Helper()
	return NewResolver(fakeSource{routes: routes}, available).Resolve(context.Background(), req)
}

func TestMockRouteResolvesForTextToImage(t *testing.T) {
	got, err := resolve(t, []Route{mockRoute()}, map[string]bool{"mock": true},
		ResolveRequest{OperationType: "text_to_image", QualityTier: "standard"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.ProviderID != "mock" || got.ProviderModelID != "pm_mock_v1" || got.ProviderRouteID != "route_mock" {
		t.Fatalf("unexpected route: %+v", got)
	}
}

func TestBFLResolvesWhenConfiguredViaPreference(t *testing.T) {
	got, err := resolve(t, []Route{mockRoute(), bflRoute()}, map[string]bool{"mock": true, "bfl": true},
		ResolveRequest{OperationType: "text_to_image", QualityTier: "standard", ProviderPreference: "bfl"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.ProviderID != "bfl" {
		t.Fatalf("expected bfl via preference, got %+v", got)
	}
}

func TestBFLIgnoredWhenKeyMissing(t *testing.T) {
	// bfl not available; even with a bfl preference, mock is chosen.
	got, err := resolve(t, []Route{mockRoute(), bflRoute()}, map[string]bool{"mock": true},
		ResolveRequest{OperationType: "text_to_image", QualityTier: "standard", ProviderPreference: "bfl"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.ProviderID != "mock" {
		t.Fatalf("expected mock when bfl unavailable, got %+v", got)
	}
}

func TestInactiveRouteIgnored(t *testing.T) {
	r := mockRoute()
	r.Enabled = false
	if _, err := resolve(t, []Route{r}, map[string]bool{"mock": true},
		ResolveRequest{OperationType: "text_to_image"}); !errors.Is(err, ErrNoRoute) {
		t.Fatalf("expected ErrNoRoute for disabled route, got %v", err)
	}
}

func TestInactiveModelIgnored(t *testing.T) {
	r := mockRoute()
	r.ModelActive = false
	if _, err := resolve(t, []Route{r}, map[string]bool{"mock": true},
		ResolveRequest{OperationType: "text_to_image"}); !errors.Is(err, ErrNoRoute) {
		t.Fatalf("expected ErrNoRoute for inactive model, got %v", err)
	}
}

// provider_routes has no effective_from/to columns, so calendar effective-dating
// applies only to provider_model_prices and is enforced + tested at the cost
// layer. For the resolver, the "active" dimension the schema supports is route
// is_enabled + model status; a disabled route OR an inactive model is ignored,
// which is what an expired/not-yet-effective route would reduce to here.
func TestActiveDimensionFromSchema(t *testing.T) {
	disabled := mockRoute()
	disabled.Enabled = false
	inactiveModel := bflRoute()
	inactiveModel.ModelActive = false
	if _, err := resolve(t, []Route{disabled, inactiveModel}, map[string]bool{"mock": true, "bfl": true},
		ResolveRequest{OperationType: "text_to_image"}); !errors.Is(err, ErrNoRoute) {
		t.Fatalf("expected ErrNoRoute when only disabled/inactive routes exist, got %v", err)
	}
}

func TestQualityTierParticipates(t *testing.T) {
	std := mockRoute() // standard
	high := mockRoute()
	high.RouteID, high.ModelID, high.QualityTier = "route_high", "pm_high", "high"
	got, err := resolve(t, []Route{std, high}, map[string]bool{"mock": true},
		ResolveRequest{OperationType: "text_to_image", QualityTier: "high"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.ProviderRouteID != "route_high" {
		t.Fatalf("expected high-quality route, got %+v", got)
	}
	// No route at the requested quality → no_route.
	if _, err := resolve(t, []Route{std}, map[string]bool{"mock": true},
		ResolveRequest{OperationType: "text_to_image", QualityTier: "draft"}); !errors.Is(err, ErrNoRoute) {
		t.Fatalf("expected ErrNoRoute for missing quality, got %v", err)
	}
}

func TestLatencyTierParticipates(t *testing.T) {
	fast := mockRoute()
	fast.RouteID, fast.ModelID, fast.LatencyTier, fast.Priority = "route_fast", "pm_fast", "fast", 100
	balanced := mockRoute()
	balanced.RouteID, balanced.ModelID, balanced.LatencyTier, balanced.Priority = "route_bal", "pm_bal", "balanced", 100
	// Same priority; latency match decides.
	got, err := resolve(t, []Route{balanced, fast}, map[string]bool{"mock": true},
		ResolveRequest{OperationType: "text_to_image", QualityTier: "standard", LatencyTier: "fast"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.ProviderRouteID != "route_fast" {
		t.Fatalf("expected latency-matched route, got %+v", got)
	}
}

func TestCapabilityMissReturnsUnsupported(t *testing.T) {
	// mock route advertises true_preview; require a capability nothing satisfies.
	r := mockRoute()
	r.PreviewCapability = "no_preview"
	if _, err := resolve(t, []Route{r}, map[string]bool{"mock": true},
		ResolveRequest{OperationType: "text_to_image", QualityTier: "standard", RequiredPreviewCapability: "true_preview"}); !errors.Is(err, ErrUnsupportedCapability) {
		t.Fatalf("expected ErrUnsupportedCapability, got %v", err)
	}
}

func TestProviderUnavailableForRoute(t *testing.T) {
	// Only a bfl route exists, bfl unavailable.
	if _, err := resolve(t, []Route{bflRoute()}, map[string]bool{"mock": true},
		ResolveRequest{OperationType: "text_to_image"}); !errors.Is(err, ErrProviderUnavailableForRoute) {
		t.Fatalf("expected ErrProviderUnavailableForRoute, got %v", err)
	}
}

func TestNoRouteForUnknownOperation(t *testing.T) {
	if _, err := resolve(t, []Route{mockRoute()}, map[string]bool{"mock": true},
		ResolveRequest{OperationType: "upscale"}); !errors.Is(err, ErrNoRoute) {
		t.Fatalf("expected ErrNoRoute, got %v", err)
	}
}

func TestTieBreakDeterministic(t *testing.T) {
	// Two equivalent routes (same provider, priority, tiers) differing only in
	// model_id/route_id. provider_id then model_id then route_id ASC must pick a
	// stable winner regardless of input order.
	a := mockRoute()
	a.RouteID, a.ModelID = "route_a", "pm_a"
	b := mockRoute()
	b.RouteID, b.ModelID = "route_b", "pm_b"

	req := ResolveRequest{OperationType: "text_to_image", QualityTier: "standard"}
	got1, err := resolve(t, []Route{a, b}, map[string]bool{"mock": true}, req)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	got2, err := resolve(t, []Route{b, a}, map[string]bool{"mock": true}, req)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got1.ProviderModelID != "pm_a" || got2.ProviderModelID != "pm_a" {
		t.Fatalf("tie-break not deterministic: %q vs %q", got1.ProviderModelID, got2.ProviderModelID)
	}
}

func TestPriorityBeatsModelOrder(t *testing.T) {
	// Lower priority number wins even if its model_id sorts later.
	low := mockRoute()
	low.RouteID, low.ModelID, low.Priority = "route_low", "pm_zzz", 50
	high := mockRoute()
	high.RouteID, high.ModelID, high.Priority = "route_high", "pm_aaa", 100
	got, err := resolve(t, []Route{high, low}, map[string]bool{"mock": true},
		ResolveRequest{OperationType: "text_to_image", QualityTier: "standard"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.ProviderModelID != "pm_zzz" {
		t.Fatalf("expected lower-priority route to win, got %+v", got)
	}
}
