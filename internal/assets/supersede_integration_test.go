//go:build integration

package assets_test

import (
	"context"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zakkriel/drchat-image-platform/internal/assets"
)

// Phase 6A4 artifact supersede SQL against Postgres: exercises the
// repository's SupersedeAndInsertArtifact (advisory lock + max version + insert
// ready + archive prior ready, all in one transaction) without the worker or
// S3, so the artifact-specific supersede queries get real-DB coverage here too
// (the worker+S3 end-to-end variant lives in internal/jobs and needs MinIO).

const supersedeHash = "render_hash_supersede"

func artifactInsertParams(id string) assets.InsertParams {
	style := styleID(itTenant)
	hash := supersedeHash
	return assets.InsertParams{
		ID:             id,
		TenantID:       itTenant,
		WorldID:        itWorld,
		AssetType:      "artifact",
		VariantKey:     "default",
		StyleProfileID: &style,
		QualityTier:    "standard",
		PromptHash:     &hash,
	}
}

func artifactSupersedeSlot() assets.ArtifactSlot {
	return assets.ArtifactSlot{
		TenantID:       itTenant,
		WorldID:        itWorld,
		StyleProfileID: styleID(itTenant),
		QualityTier:    "standard",
		PromptHash:     supersedeHash,
	}
}

func readyArtifactCount(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM visual_assets
		 WHERE tenant_id = $1 AND asset_type = 'artifact' AND prompt_hash = $2 AND status = 'ready'`,
		itTenant, supersedeHash).Scan(&n); err != nil {
		t.Fatalf("count ready: %v", err)
	}
	return n
}

// TestIntegrationArtifactSupersede: a forced regeneration archives the prior
// ready artifact of the exact slot, links it forward, and inserts the new asset
// ready at version = prior_max + 1 — leaving exactly one ready row.
func TestIntegrationArtifactSupersede(t *testing.T) {
	pool, repo, _ := withFixtures(t)
	ctx := context.Background()

	// Prior ready artifact (version 1).
	prior, err := repo.Insert(ctx, artifactInsertParams("art_prior"))
	if err != nil {
		t.Fatalf("seed prior artifact: %v", err)
	}
	if prior.Version != 1 {
		t.Fatalf("prior version: want 1, got %d", prior.Version)
	}

	// Forced regeneration supersedes the slot.
	fresh, err := repo.SupersedeAndInsertArtifact(ctx, artifactInsertParams("art_fresh"), artifactSupersedeSlot())
	if err != nil {
		t.Fatalf("SupersedeAndInsertArtifact: %v", err)
	}
	if fresh.Version != 2 {
		t.Fatalf("regenerated version: want 2, got %d", fresh.Version)
	}
	if fresh.Status != "ready" {
		t.Fatalf("regenerated status: want ready, got %s", fresh.Status)
	}

	// Prior is archived and linked forward to the regenerated asset.
	var status string
	var supersededBy *string
	if err := pool.QueryRow(ctx,
		`SELECT status, superseded_by_asset_id FROM visual_assets WHERE id = $1`, "art_prior",
	).Scan(&status, &supersededBy); err != nil {
		t.Fatalf("read prior: %v", err)
	}
	if status != "archived" {
		t.Fatalf("prior status: want archived, got %s", status)
	}
	if supersededBy == nil || *supersededBy != "art_fresh" {
		t.Fatalf("prior must link forward to art_fresh, got %v", supersededBy)
	}

	if got := readyArtifactCount(t, pool); got != 1 {
		t.Fatalf("want exactly one ready artifact after supersede, got %d", got)
	}
}

// TestIntegrationArtifactSupersedeConcurrent is the Phase 6A4 concurrency
// acceptance: two concurrent forced regenerations of the same slot serialize on
// the slot advisory lock, producing versions N+1 and N+2 (never duplicate
// versions); afterward exactly one ready asset remains (the latest) and all
// prior ready assets are archived and linked forward.
func TestIntegrationArtifactSupersedeConcurrent(t *testing.T) {
	pool, repo, _ := withFixtures(t)
	ctx := context.Background()

	// Prior ready artifact (version 1).
	if _, err := repo.Insert(ctx, artifactInsertParams("art_base")); err != nil {
		t.Fatalf("seed base artifact: %v", err)
	}

	var wg sync.WaitGroup
	versions := make([]int32, 2)
	errs := make([]error, 2)
	ids := []string{"art_c1", "art_c2"}
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			a, err := repo.SupersedeAndInsertArtifact(ctx, artifactInsertParams(ids[i]), artifactSupersedeSlot())
			errs[i] = err
			versions[i] = a.Version
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent supersede %d: %v", i, err)
		}
	}
	// The two regenerations took versions 2 and 3 (order non-deterministic), never
	// a duplicate.
	got := map[int32]bool{versions[0]: true, versions[1]: true}
	if !got[2] || !got[3] || versions[0] == versions[1] {
		t.Fatalf("concurrent versions must be 2 and 3 (distinct), got %v", versions)
	}

	// Exactly one ready asset remains: the latest (version 3).
	if n := readyArtifactCount(t, pool); n != 1 {
		t.Fatalf("want exactly one ready artifact after two concurrent regenerations, got %d", n)
	}
	var readyVersion int32
	if err := pool.QueryRow(ctx,
		`SELECT version FROM visual_assets
		 WHERE tenant_id = $1 AND asset_type = 'artifact' AND prompt_hash = $2 AND status = 'ready'`,
		itTenant, supersedeHash).Scan(&readyVersion); err != nil {
		t.Fatalf("read ready version: %v", err)
	}
	if readyVersion != 3 {
		t.Fatalf("surviving ready asset must be the latest (version 3), got %d", readyVersion)
	}

	// All prior ready assets are archived; every archived row links forward.
	var archivedUnlinked int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM visual_assets
		 WHERE tenant_id = $1 AND asset_type = 'artifact' AND prompt_hash = $2
		   AND status = 'archived' AND superseded_by_asset_id IS NULL`,
		itTenant, supersedeHash).Scan(&archivedUnlinked); err != nil {
		t.Fatalf("count archived unlinked: %v", err)
	}
	if archivedUnlinked != 0 {
		t.Fatalf("every archived asset must link forward, got %d unlinked", archivedUnlinked)
	}
}
