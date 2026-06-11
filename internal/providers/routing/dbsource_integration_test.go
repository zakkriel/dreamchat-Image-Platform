//go:build integration

package routing_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zakkriel/drchat-image-platform/internal/providers/routing"
)

// These tests exercise the DB-backed route source + resolver against the seeded
// mock route (migration 0002) and BFL route (migration 0006). CI applies both
// before running integration tests.
//
//	POSTGRES_DSN=postgres://image_platform:image_platform@localhost:5432/image_platform?sslmode=disable \
//	go test -tags=integration ./internal/providers/routing/...

func openPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_DSN not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
	return pool
}

func TestResolverResolvesSeededMockRoute(t *testing.T) {
	pool := openPool(t)
	defer pool.Close()

	r := routing.NewResolver(routing.NewDBRouteSource(pool), map[string]bool{"mock": true})
	got, err := r.Resolve(context.Background(), routing.ResolveRequest{
		OperationType: "text_to_image",
		QualityTier:   "standard",
	})
	if err != nil {
		t.Fatalf("resolve mock: %v", err)
	}
	if got.ProviderID != "mock" || got.ProviderModelID != "pm_mock_v1" {
		t.Fatalf("expected seeded mock route, got %+v", got)
	}
}

func TestResolverSelectsMockByDefaultWhenBothAvailable(t *testing.T) {
	pool := openPool(t)
	defer pool.Close()

	// Both providers available, no preference: mock (priority 100) beats bfl
	// (priority 200).
	r := routing.NewResolver(routing.NewDBRouteSource(pool), map[string]bool{"mock": true, "bfl": true})
	got, err := r.Resolve(context.Background(), routing.ResolveRequest{
		OperationType: "text_to_image",
		QualityTier:   "standard",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.ProviderID != "mock" {
		t.Fatalf("expected mock by default, got %+v", got)
	}
}

func TestResolverSelectsBFLWithPreferenceAndAvailability(t *testing.T) {
	pool := openPool(t)
	defer pool.Close()

	r := routing.NewResolver(routing.NewDBRouteSource(pool), map[string]bool{"mock": true, "bfl": true})
	got, err := r.Resolve(context.Background(), routing.ResolveRequest{
		OperationType:      "text_to_image",
		QualityTier:        "standard",
		ProviderPreference: "bfl",
	})
	if err != nil {
		t.Fatalf("resolve bfl: %v", err)
	}
	if got.ProviderID != "bfl" || got.ProviderModelID != "pm_bfl_flux_pro_11" {
		t.Fatalf("expected seeded bfl route, got %+v", got)
	}
}

func TestResolverResolvesPackCapableMockRoute(t *testing.T) {
	pool := openPool(t)
	defer pool.Close()

	r := routing.NewResolver(routing.NewDBRouteSource(pool), map[string]bool{"mock": true, "bfl": true})
	got, err := r.Resolve(context.Background(), routing.ResolveRequest{
		OperationType:      "text_to_image",
		QualityTier:        "standard",
		RequiredCapability: "pack_capable",
	})
	if err != nil {
		t.Fatalf("resolve pack_capable: %v", err)
	}
	// Only the mock model is pack_capable (seed 0006 route_mock_text_to_image_pack);
	// BFL's floor is scene_capable, so packs never resolve to BFL.
	if got.ProviderID != "mock" || got.ProviderRouteID != "route_mock_text_to_image_pack" {
		t.Fatalf("expected pack_capable mock route, got %+v", got)
	}
}

func TestResolverPackCapableUnsupportedWhenOnlyBFL(t *testing.T) {
	pool := openPool(t)
	defer pool.Close()

	// Only BFL available; BFL has no pack_capable route → unsupported_capability
	// (NOT no_route — a route exists for the operation/quality, just not the
	// capability).
	r := routing.NewResolver(routing.NewDBRouteSource(pool), map[string]bool{"bfl": true})
	_, err := r.Resolve(context.Background(), routing.ResolveRequest{
		OperationType:      "text_to_image",
		QualityTier:        "standard",
		RequiredCapability: "pack_capable",
	})
	if !errors.Is(err, routing.ErrUnsupportedCapability) {
		t.Fatalf("expected ErrUnsupportedCapability, got %v", err)
	}
}

func TestResolverResolvesAllSceneQualityTiers(t *testing.T) {
	pool := openPool(t)
	defer pool.Close()

	r := routing.NewResolver(routing.NewDBRouteSource(pool), map[string]bool{"mock": true})
	for _, tc := range []struct{ tier, route string }{
		{"draft", "route_mock_text_to_image_draft"},
		{"standard", "route_mock_text_to_image_standard"},
		{"high", "route_mock_text_to_image_high"},
	} {
		got, err := r.Resolve(context.Background(), routing.ResolveRequest{
			OperationType:      "text_to_image",
			QualityTier:        tc.tier,
			RequiredCapability: "scene_capable",
		})
		if err != nil {
			t.Fatalf("resolve %s: %v", tc.tier, err)
		}
		if got.ProviderRouteID != tc.route {
			t.Fatalf("quality %s: expected %s, got %s", tc.tier, tc.route, got.ProviderRouteID)
		}
	}
}

func TestResolverIgnoresBFLWhenUnavailable(t *testing.T) {
	pool := openPool(t)
	defer pool.Close()

	// bfl preferred but not available → mock is chosen, never bfl.
	r := routing.NewResolver(routing.NewDBRouteSource(pool), map[string]bool{"mock": true})
	got, err := r.Resolve(context.Background(), routing.ResolveRequest{
		OperationType:      "text_to_image",
		QualityTier:        "standard",
		ProviderPreference: "bfl",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.ProviderID != "mock" {
		t.Fatalf("expected mock when bfl unavailable, got %+v", got)
	}
}
