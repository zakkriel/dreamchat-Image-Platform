//go:build integration

package routing_test

import (
	"context"
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
