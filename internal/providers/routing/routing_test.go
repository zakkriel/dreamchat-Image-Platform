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

func TestGeneralCapabilitySceneResolves(t *testing.T) {
	// mockRoute() has required_capability=scene_capable.
	got, err := resolve(t, []Route{mockRoute()}, map[string]bool{"mock": true},
		ResolveRequest{OperationType: "text_to_image", QualityTier: "standard", RequiredCapability: "scene_capable"})
	if err != nil {
		t.Fatalf("resolve scene_capable: %v", err)
	}
	if got.ProviderRouteID != "route_mock" {
		t.Fatalf("expected scene_capable route, got %+v", got)
	}
}

func TestGeneralCapabilityPackDoesNotResolveSceneRoute(t *testing.T) {
	// Only a scene_capable route exists; a pack_capable request must NOT collapse
	// to it — it must report unsupported_capability.
	if _, err := resolve(t, []Route{mockRoute()}, map[string]bool{"mock": true},
		ResolveRequest{OperationType: "text_to_image", QualityTier: "standard", RequiredCapability: "pack_capable"}); !errors.Is(err, ErrUnsupportedCapability) {
		t.Fatalf("expected ErrUnsupportedCapability for pack_capable vs scene route, got %v", err)
	}
}

func TestGeneralCapabilityResolvesPackRouteWhenPresent(t *testing.T) {
	scene := mockRoute()
	pack := mockRoute()
	pack.RouteID, pack.RequiredCapability = "route_pack", "pack_capable"
	got, err := resolve(t, []Route{scene, pack}, map[string]bool{"mock": true},
		ResolveRequest{OperationType: "text_to_image", QualityTier: "standard", RequiredCapability: "pack_capable"})
	if err != nil {
		t.Fatalf("resolve pack_capable: %v", err)
	}
	if got.ProviderRouteID != "route_pack" {
		t.Fatalf("expected pack_capable route, got %+v", got)
	}
}

func TestGeneralCapabilityUnsupportedNotCollapsedToNoRoute(t *testing.T) {
	// Routes exist for operation + quality, just none with the requested
	// capability → unsupported_capability, NOT no_route.
	err := func() error {
		_, e := resolve(t, []Route{mockRoute()}, map[string]bool{"mock": true},
			ResolveRequest{OperationType: "text_to_image", QualityTier: "standard", RequiredCapability: "production_capable"})
		return e
	}()
	if !errors.Is(err, ErrUnsupportedCapability) {
		t.Fatalf("expected ErrUnsupportedCapability, got %v", err)
	}
	if errors.Is(err, ErrNoRoute) {
		t.Fatalf("must not collapse to ErrNoRoute")
	}
}

func TestGeneralAndPreviewCapabilityIndependent(t *testing.T) {
	// General capability and preview capability filter independently. A route
	// matching general capability but not the preview requirement → unsupported.
	r := mockRoute() // scene_capable, true_preview
	r.PreviewCapability = "no_preview"
	if _, err := resolve(t, []Route{r}, map[string]bool{"mock": true},
		ResolveRequest{OperationType: "text_to_image", QualityTier: "standard", RequiredCapability: "scene_capable", RequiredPreviewCapability: "true_preview"}); !errors.Is(err, ErrUnsupportedCapability) {
		t.Fatalf("expected unsupported when preview misses despite capability match, got %v", err)
	}
}

// --- Phase 7B preview-first routing -----------------------------------------

// TestPreviewFirstResolvesMockTruePreview: a preview_first request
// (RequiredPreviewCapability=true_preview) resolves the mock true_preview route.
func TestPreviewFirstResolvesMockTruePreview(t *testing.T) {
	got, err := resolve(t, []Route{mockRoute()}, map[string]bool{"mock": true},
		ResolveRequest{
			OperationType:             "text_to_image",
			QualityTier:               "standard",
			RequiredCapability:        "scene_capable",
			RequiredPreviewCapability: "true_preview",
		})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.ProviderID != "mock" || got.ProviderRouteID != "route_mock" {
		t.Fatalf("expected mock true_preview route, got %+v", got)
	}
	if got.PreviewCapability != "true_preview" {
		t.Fatalf("expected resolved PreviewCapability=true_preview, got %q", got.PreviewCapability)
	}
}

// TestPreviewFirstWithOnlyBFLUnsupported: with only BFL (no_preview) available,
// a preview_first request returns ErrUnsupportedCapability (a hard true_preview
// requirement — no downgrade, no derived_preview fallback). It must NOT collapse
// to ErrNoRoute.
func TestPreviewFirstWithOnlyBFLUnsupported(t *testing.T) {
	_, err := resolve(t, []Route{bflRoute()}, map[string]bool{"bfl": true},
		ResolveRequest{
			OperationType:             "text_to_image",
			QualityTier:               "standard",
			RequiredCapability:        "scene_capable",
			RequiredPreviewCapability: "true_preview",
		})
	if !errors.Is(err, ErrUnsupportedCapability) {
		t.Fatalf("expected ErrUnsupportedCapability for BFL-only preview_first, got %v", err)
	}
	if errors.Is(err, ErrNoRoute) {
		t.Fatalf("must not collapse to ErrNoRoute")
	}
}

// TestPreviewFirstPrefersTruePreviewOverBFL: when both mock (true_preview) and
// BFL (no_preview) are available, preview_first selects mock and excludes BFL.
func TestPreviewFirstPrefersTruePreviewOverBFL(t *testing.T) {
	got, err := resolve(t, []Route{mockRoute(), bflRoute()}, map[string]bool{"mock": true, "bfl": true},
		ResolveRequest{
			OperationType:             "text_to_image",
			QualityTier:               "standard",
			RequiredCapability:        "scene_capable",
			RequiredPreviewCapability: "true_preview",
		})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.ProviderID != "mock" {
		t.Fatalf("preview_first must exclude BFL (no_preview) and pick mock, got %+v", got)
	}
}

// TestFinalOnlyDoesNotConstrainPreviewAndAllowsBFL: a final_only request
// (RequiredPreviewCapability empty) imposes no preview requirement, so BFL
// remains selectable (here via preference) even though it is no_preview.
func TestFinalOnlyDoesNotConstrainPreviewAndAllowsBFL(t *testing.T) {
	got, err := resolve(t, []Route{mockRoute(), bflRoute()}, map[string]bool{"mock": true, "bfl": true},
		ResolveRequest{
			OperationType:      "text_to_image",
			QualityTier:        "standard",
			RequiredCapability: "scene_capable",
			ProviderPreference: "bfl",
			// RequiredPreviewCapability intentionally empty (final_only).
		})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.ProviderID != "bfl" {
		t.Fatalf("final_only must leave BFL selectable, got %+v", got)
	}
}

func TestTieBreakDeterministicAfterCapabilityFilter(t *testing.T) {
	// Two pack_capable routes differing only in model/route id; capability filter
	// keeps both, tie-break must pick the same one regardless of order.
	a := mockRoute()
	a.RouteID, a.ModelID, a.RequiredCapability = "route_a", "pm_a", "pack_capable"
	b := mockRoute()
	b.RouteID, b.ModelID, b.RequiredCapability = "route_b", "pm_b", "pack_capable"
	req := ResolveRequest{OperationType: "text_to_image", QualityTier: "standard", RequiredCapability: "pack_capable"}
	g1, err := resolve(t, []Route{a, b}, map[string]bool{"mock": true}, req)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	g2, err := resolve(t, []Route{b, a}, map[string]bool{"mock": true}, req)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if g1.ProviderModelID != "pm_a" || g2.ProviderModelID != "pm_a" {
		t.Fatalf("tie-break not deterministic after capability filter: %q vs %q", g1.ProviderModelID, g2.ProviderModelID)
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

// --- Phase 7C-4 fallback chain ----------------------------------------------

func resolveChain(t *testing.T, routes []Route, available map[string]bool, req ResolveRequest) ([]ResolvedRoute, error) {
	t.Helper()
	return NewResolver(fakeSource{routes: routes}, available).ResolveChain(context.Background(), req)
}

// TestResolveChainReturnsCandidatesInTieBreakOrder: with three same-tier routes
// at distinct priorities, ResolveChain returns all three in the tie-break order
// (priority ASC), independent of the source row order, and chain[0] equals the
// Resolve result.
func TestResolveChainReturnsCandidatesInTieBreakOrder(t *testing.T) {
	low := mockRoute()
	low.RouteID, low.ModelID, low.Priority = "route_low", "pm_low", 50
	mid := mockRoute()
	mid.RouteID, mid.ModelID, mid.Priority = "route_mid", "pm_mid", 100
	high := mockRoute()
	high.RouteID, high.ModelID, high.Priority = "route_high", "pm_high", 150

	req := ResolveRequest{OperationType: "text_to_image", QualityTier: "standard"}
	// Source order deliberately scrambled to prove ordering comes from ranksBefore.
	chain, err := resolveChain(t, []Route{high, low, mid}, map[string]bool{"mock": true}, req)
	if err != nil {
		t.Fatalf("ResolveChain: %v", err)
	}
	if len(chain) != 3 {
		t.Fatalf("expected 3 candidates, got %d: %+v", len(chain), chain)
	}
	wantOrder := []string{"route_low", "route_mid", "route_high"}
	for i, want := range wantOrder {
		if chain[i].ProviderRouteID != want {
			t.Fatalf("chain[%d] = %q, want %q (full chain %+v)", i, chain[i].ProviderRouteID, want, chain)
		}
	}

	// chain[0] must equal the single Resolve pick for the same request.
	got, err := resolve(t, []Route{high, low, mid}, map[string]bool{"mock": true}, req)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != chain[0] {
		t.Fatalf("Resolve result %+v must equal ResolveChain[0] %+v", got, chain[0])
	}
}

// TestResolveChainMultiCandidateAcrossProviders: a multi-candidate chain spanning
// two providers is ordered by priority then provider_id; chain[0] equals Resolve,
// and unavailable providers are filtered out of the chain entirely.
func TestResolveChainMultiCandidateAcrossProviders(t *testing.T) {
	m := mockRoute() // mock, priority 100
	b := bflRoute()  // bfl, priority 200
	req := ResolveRequest{OperationType: "text_to_image", QualityTier: "standard"}

	chain, err := resolveChain(t, []Route{b, m}, map[string]bool{"mock": true, "bfl": true}, req)
	if err != nil {
		t.Fatalf("ResolveChain: %v", err)
	}
	if len(chain) != 2 {
		t.Fatalf("expected 2 candidates, got %d: %+v", len(chain), chain)
	}
	if chain[0].ProviderID != "mock" || chain[1].ProviderID != "bfl" {
		t.Fatalf("expected [mock, bfl] by priority, got %+v", chain)
	}
	got, err := resolve(t, []Route{b, m}, map[string]bool{"mock": true, "bfl": true}, req)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != chain[0] {
		t.Fatalf("Resolve result %+v must equal ResolveChain[0] %+v", got, chain[0])
	}

	// bfl unavailable → chain has only the mock route.
	chain2, err := resolveChain(t, []Route{b, m}, map[string]bool{"mock": true}, req)
	if err != nil {
		t.Fatalf("ResolveChain (bfl unavailable): %v", err)
	}
	if len(chain2) != 1 || chain2[0].ProviderID != "mock" {
		t.Fatalf("expected single mock candidate when bfl unavailable, got %+v", chain2)
	}
}

// TestResolveChainPropagatesSentinels: ResolveChain returns the SAME sentinel
// errors as Resolve for the no-candidate cases.
func TestResolveChainPropagatesSentinels(t *testing.T) {
	if _, err := resolveChain(t, []Route{mockRoute()}, map[string]bool{"mock": true},
		ResolveRequest{OperationType: "upscale"}); !errors.Is(err, ErrNoRoute) {
		t.Fatalf("expected ErrNoRoute, got %v", err)
	}
	if _, err := resolveChain(t, []Route{bflRoute()}, map[string]bool{"mock": true},
		ResolveRequest{OperationType: "text_to_image"}); !errors.Is(err, ErrProviderUnavailableForRoute) {
		t.Fatalf("expected ErrProviderUnavailableForRoute, got %v", err)
	}
	if _, err := resolveChain(t, []Route{mockRoute()}, map[string]bool{"mock": true},
		ResolveRequest{OperationType: "text_to_image", QualityTier: "standard", RequiredCapability: "pack_capable"}); !errors.Is(err, ErrUnsupportedCapability) {
		t.Fatalf("expected ErrUnsupportedCapability, got %v", err)
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
