//go:build integration

package assets_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zakkriel/drchat-image-platform/internal/assets"
)

// Phase 6A1 retrieval substrate against Postgres. Seeds visual_assets directly
// (no generation path) and exercises repository SQL + the retrieval decision
// layer end to end.
//
// To run:
//
//	POSTGRES_DSN=postgres://image_platform:image_platform@localhost:5432/image_platform?sslmode=disable \
//	go test -tags=integration ./internal/assets/...
//
// It cleans up the rows it touches before and after each run; it never drops
// or recreates tables (table-count assertion stays unchanged — no migration).

const (
	itTenant      = "tenant_it_retrieval"
	itTenantOther = "tenant_it_retrieval_other"
	itWorld       = "world_it_retrieval"
	itStyle       = "sty_it_retrieval"
	itIdentity    = "vi_it_retrieval"
	itOwner       = "char_it_retrieval"
)

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

func cleanupRetrieval(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	for _, tenant := range []string{itTenant, itTenantOther} {
		if _, err := pool.Exec(ctx, `DELETE FROM visual_assets WHERE tenant_id = $1`, tenant); err != nil {
			t.Fatalf("cleanup assets: %v", err)
		}
		if _, err := pool.Exec(ctx, `DELETE FROM visual_identities WHERE tenant_id = $1`, tenant); err != nil {
			t.Fatalf("cleanup identities: %v", err)
		}
		if _, err := pool.Exec(ctx, `DELETE FROM style_profiles WHERE tenant_id = $1`, tenant); err != nil {
			t.Fatalf("cleanup styles: %v", err)
		}
	}
}

// styleID / identityID are tenant-scoped so the two seeded tenants don't
// collide on the primary keys of style_profiles / visual_identities.
func styleID(tenant string) string    { return itStyle + "_" + tenant }
func identityID(tenant string) string { return itIdentity + "_" + tenant }

func seedRetrievalFixtures(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	for _, tenant := range []string{itTenant, itTenantOther} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO style_profiles (id, tenant_id, name, style_mode, positive_prompt, default_quality_tier, status)
			 VALUES ($1, $2, 'retrieval style', 'open_prompt', 'watercolor', 'standard', 'active')`,
			styleID(tenant), tenant,
		); err != nil {
			t.Fatalf("seed style (%s): %v", tenant, err)
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO visual_identities (id, tenant_id, world_id, owner_type, owner_id, display_name, style_profile_id)
			 VALUES ($1, $2, $3, 'character', $4, 'Captain Mira', $5)`,
			identityID(tenant), tenant, itWorld, itOwner, styleID(tenant),
		); err != nil {
			t.Fatalf("seed identity (%s): %v", tenant, err)
		}
	}
}

// seedAsset inserts a visual_assets row with the Phase 5B classification of the
// variant_key, under the given tenant and status.
func seedAsset(t *testing.T, pool *pgxpool.Pool, id, tenant, variantKey, status string) {
	t.Helper()
	cv := assets.ClassifyVariant(assets.EntityCharacter, variantKey)
	_, err := pool.Exec(context.Background(),
		`INSERT INTO visual_assets
		   (id, tenant_id, world_id, visual_identity_id, asset_type, variant_key,
		    variant_family, state_version, style_profile_id, quality_tier, status,
		    compatibility_tags, fallback_allowed, fallback_rank, is_identity_anchor)
		 VALUES ($1, $2, $3, $4, 'character_portrait', $5,
		         $6, 1, $7, 'standard', $8,
		         $9, $10, $11, false)`,
		id, tenant, itWorld, identityID(tenant), variantKey,
		cv.Family, styleID(tenant), status,
		cv.CompatibilityTags, cv.FallbackAllowed, cv.FallbackRank,
	)
	if err != nil {
		t.Fatalf("seed asset %s: %v", id, err)
	}
}

func itQuery(variantKey, policy string) assets.RetrievalQuery {
	return assets.RetrievalQuery{
		TenantID:         itTenant,
		WorldID:          itWorld,
		VisualIdentityID: identityID(itTenant),
		EntityType:       assets.EntityCharacter,
		VariantKey:       variantKey,
		StyleProfileID:   styleID(itTenant),
		StateVersion:     1,
		QualityTier:      "standard",
		FallbackPolicy:   policy,
	}
}

func withFixtures(t *testing.T) (*pgxpool.Pool, assets.Repository, *assets.Retriever) {
	t.Helper()
	pool := openTestPool(t)
	cleanupRetrieval(t, pool)
	t.Cleanup(func() { cleanupRetrieval(t, pool); pool.Close() })
	seedRetrievalFixtures(t, pool)
	repo := assets.NewRepository(pool)
	return pool, repo, assets.NewRetriever(repo)
}

func TestIntegrationExactMatch(t *testing.T) {
	pool, _, rt := withFixtures(t)
	seedAsset(t, pool, "a1", itTenant, "neutral_front_portrait", "ready")

	res, err := rt.Retrieve(context.Background(), itQuery("neutral_front_portrait", assets.FallbackPolicyCompatibleOnly))
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if res.MatchType != assets.OutcomeExactMatch {
		t.Fatalf("want exact_match, got %s", res.MatchType)
	}
	if res.Asset == nil || res.Asset.ID != "a1" {
		t.Fatalf("want asset a1, got %+v", res.Asset)
	}
}

func TestIntegrationCompatibleMatch(t *testing.T) {
	pool, _, rt := withFixtures(t)
	seedAsset(t, pool, "a1", itTenant, "neutral_front_portrait", "ready")

	// Requesting expression_warm with a neutral_front candidate → compatible
	// per the 5B matrix (neutral is fallback-safe for a mild expression).
	res, err := rt.Retrieve(context.Background(), itQuery("expression_warm", assets.FallbackPolicyCompatibleOnly))
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if res.MatchType != assets.OutcomeCompatibleMatch {
		t.Fatalf("want compatible_match, got %s", res.MatchType)
	}
	if res.Asset == nil || res.Asset.ID != "a1" {
		t.Fatalf("want asset a1, got %+v", res.Asset)
	}
}

func TestIntegrationDayNightGeneratedRequired(t *testing.T) {
	pool, _, rt := withFixtures(t)
	seedAsset(t, pool, "p1", itTenant, "day_view", "ready")

	q := itQuery("night_view", assets.FallbackPolicyPreviewAllowed)
	q.EntityType = assets.EntityPlace
	res, err := rt.Retrieve(context.Background(), q)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if res.MatchType != assets.OutcomeGeneratedRequired {
		t.Fatalf("day→night: want generated_required, got %s", res.MatchType)
	}
}

func TestIntegrationFailedArchivedNotReturned(t *testing.T) {
	pool, _, rt := withFixtures(t)
	seedAsset(t, pool, "a_failed", itTenant, "neutral_front_portrait", "failed")
	seedAsset(t, pool, "a_archived", itTenant, "neutral_three_quarter_portrait", "archived")
	seedAsset(t, pool, "a_pending", itTenant, "neutral_bust", "pending")

	// Exact request against the failed asset must not match.
	res, err := rt.Retrieve(context.Background(), itQuery("neutral_front_portrait", assets.FallbackPolicyPreviewAllowed))
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if res.MatchType != assets.OutcomeGeneratedRequired {
		t.Fatalf("non-ready assets must not match, got %s (asset=%+v)", res.MatchType, res.Asset)
	}
}

func TestIntegrationOtherTenantNotReturned(t *testing.T) {
	pool, _, rt := withFixtures(t)
	// Ready asset belongs to the OTHER tenant only.
	seedAsset(t, pool, "a1", itTenantOther, "neutral_front_portrait", "ready")

	res, err := rt.Retrieve(context.Background(), itQuery("neutral_front_portrait", assets.FallbackPolicyPreviewAllowed))
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if res.MatchType != assets.OutcomeGeneratedRequired {
		t.Fatalf("cross-tenant asset must not be returned, got %s (asset=%+v)", res.MatchType, res.Asset)
	}
}

func TestIntegrationCandidateOrderingDeterministic(t *testing.T) {
	pool, _, rt := withFixtures(t)
	// Several neutral candidates qualify as compatible for a 3q request.
	// Winner is deterministic: lowest fallback_rank then lowest id.
	seedAsset(t, pool, "a_bust", itTenant, "neutral_bust", "ready") // tertiary rank
	seedAsset(t, pool, "a_front2", itTenant, "neutral_front_portrait", "ready")
	// Second front via the alias key (same family/rank, different key+id).
	seedAsset(t, pool, "a_front1", itTenant, "neutral_front", "ready")

	for i := 0; i < 5; i++ {
		res, err := rt.Retrieve(context.Background(), itQuery("neutral_three_quarter_portrait", assets.FallbackPolicyCompatibleOnly))
		if err != nil {
			t.Fatalf("Retrieve: %v", err)
		}
		if res.MatchType != assets.OutcomeCompatibleMatch {
			t.Fatalf("want compatible_match, got %s", res.MatchType)
		}
		// Both fronts share rank primary; lowest id "a_front1" wins.
		if res.Asset == nil || res.Asset.ID != "a_front1" {
			t.Fatalf("run %d: want deterministic winner a_front1, got %+v", i, res.Asset)
		}
	}
}

func TestIntegrationCompatTagPath(t *testing.T) {
	pool, repo, _ := withFixtures(t)
	seedAsset(t, pool, "a1", itTenant, "neutral_front_portrait", "ready") // has generic_presence
	seedAsset(t, pool, "a2", itTenant, "expression_warm", "ready")        // preview_safe only

	// The GIN-backed compatibility_tags query returns only assets whose tags
	// overlap {generic_presence}.
	got, err := repo.ListRetrievalCandidatesByCompatTag(
		context.Background(),
		itQuery("neutral_front_portrait", assets.FallbackPolicyCompatibleOnly),
		[]string{assets.TagGenericPresence},
	)
	if err != nil {
		t.Fatalf("ListRetrievalCandidatesByCompatTag: %v", err)
	}
	if len(got) != 1 || got[0].ID != "a1" {
		t.Fatalf("want only a1 (generic_presence), got %+v", got)
	}
}
