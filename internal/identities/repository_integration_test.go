//go:build integration

package identities_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zakkriel/drchat-image-platform/internal/identities"
)

// To run:
//   POSTGRES_DSN=postgres://image_platform:image_platform@localhost:5432/image_platform?sslmode=disable \
//   go test -tags=integration ./internal/identities/...
//
// The test inserts a tenant-scoped style profile and exercises the full
// upsert/versioning transaction path. It cleans up the rows it touches via
// DELETE before each run; it does not drop or recreate tables.

func openTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_DSN not set; skipping integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("pool.Ping: %v", err)
	}
	return pool
}

const (
	itTenantID   = "tenant_it_identities"
	itWorldID    = "world_it"
	itOwnerID    = "char_it_alice"
	itStyleID    = "sty_it_identities_test"
	itIdentityID = "vi_it_identities_test"
)

func cleanup(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `DELETE FROM visual_identity_versions WHERE visual_identity_id IN (SELECT id FROM visual_identities WHERE tenant_id = $1)`, itTenantID); err != nil {
		t.Fatalf("cleanup versions: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM visual_identities WHERE tenant_id = $1`, itTenantID); err != nil {
		t.Fatalf("cleanup identities: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM style_profiles WHERE tenant_id = $1`, itTenantID); err != nil {
		t.Fatalf("cleanup styles: %v", err)
	}
}

func seedStyle(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO style_profiles (id, tenant_id, name, style_mode, positive_prompt, default_quality_tier, status)
		 VALUES ($1, $2, 'integration style', 'open_prompt', 'watercolor', 'standard', 'active')`,
		itStyleID, itTenantID,
	)
	if err != nil {
		t.Fatalf("seed style: %v", err)
	}
}

func TestUpsertInsertAndVersionBump(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)

	seedStyle(t, pool)

	repo := identities.NewRepository(pool)
	ctx := context.Background()

	first, err := repo.Upsert(ctx, identities.UpsertParams{
		NewID:                 itIdentityID,
		TenantID:              itTenantID,
		WorldID:               itWorldID,
		OwnerType:             "character",
		OwnerID:               itOwnerID,
		DisplayName:           "Alice",
		CanonicalVisualTraits: map[string]any{},
		StyleProfileID:        itStyleID,
	})
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if first.CurrentVersion != 1 {
		t.Fatalf("expected version=1, got %d", first.CurrentVersion)
	}

	var versionCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM visual_identity_versions WHERE visual_identity_id = $1`, first.ID).Scan(&versionCount); err != nil {
		t.Fatalf("count versions after insert: %v", err)
	}
	if versionCount != 1 {
		t.Fatalf("expected 1 version row, got %d", versionCount)
	}

	second, err := repo.Upsert(ctx, identities.UpsertParams{
		NewID:                 "vi_should_not_be_used",
		TenantID:              itTenantID,
		WorldID:               itWorldID,
		OwnerType:             "character",
		OwnerID:               itOwnerID,
		DisplayName:           "Alice",
		CanonicalVisualTraits: map[string]any{},
		StyleProfileID:        itStyleID,
	})
	if err != nil {
		t.Fatalf("second upsert (unchanged): %v", err)
	}
	if second.CurrentVersion != 1 {
		t.Fatalf("expected version still 1, got %d", second.CurrentVersion)
	}
	if second.ID != first.ID {
		t.Fatalf("expected same identity id, got %s != %s", second.ID, first.ID)
	}

	if err := pool.QueryRow(ctx, `SELECT count(*) FROM visual_identity_versions WHERE visual_identity_id = $1`, first.ID).Scan(&versionCount); err != nil {
		t.Fatalf("count versions after unchanged upsert: %v", err)
	}
	if versionCount != 1 {
		t.Fatalf("expected still 1 version row, got %d", versionCount)
	}

	third, err := repo.Upsert(ctx, identities.UpsertParams{
		NewID:                 "vi_should_not_be_used_either",
		TenantID:              itTenantID,
		WorldID:               itWorldID,
		OwnerType:             "character",
		OwnerID:               itOwnerID,
		DisplayName:           "Alice",
		CanonicalVisualTraits: map[string]any{"hair": "black"},
		StyleProfileID:        itStyleID,
	})
	if err != nil {
		t.Fatalf("third upsert (changed): %v", err)
	}
	if third.CurrentVersion != 2 {
		t.Fatalf("expected version=2, got %d", third.CurrentVersion)
	}
	if third.ID != first.ID {
		t.Fatalf("expected same identity id, got %s != %s", third.ID, first.ID)
	}

	if err := pool.QueryRow(ctx, `SELECT count(*) FROM visual_identity_versions WHERE visual_identity_id = $1`, first.ID).Scan(&versionCount); err != nil {
		t.Fatalf("count versions after changed upsert: %v", err)
	}
	if versionCount != 2 {
		t.Fatalf("expected 2 version rows, got %d", versionCount)
	}

	var reason string
	if err := pool.QueryRow(ctx, `SELECT reason FROM visual_identity_versions WHERE visual_identity_id = $1 AND version = 2`, first.ID).Scan(&reason); err != nil {
		t.Fatalf("read version 2 reason: %v", err)
	}
	if reason != "canonical_change" {
		t.Fatalf("expected reason=canonical_change, got %q", reason)
	}
}

func TestUpsertInvalidStyleProfile(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)

	repo := identities.NewRepository(pool)
	_, err := repo.Upsert(context.Background(), identities.UpsertParams{
		NewID:                 itIdentityID,
		TenantID:              itTenantID,
		WorldID:               itWorldID,
		OwnerType:             "character",
		OwnerID:               itOwnerID,
		DisplayName:           "Alice",
		CanonicalVisualTraits: map[string]any{},
		StyleProfileID:        "sty_does_not_exist",
	})
	if err != identities.ErrInvalidStyle {
		t.Fatalf("expected ErrInvalidStyle, got %v", err)
	}
}
