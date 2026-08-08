//go:build integration

package jobs_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zakkriel/drchat-image-platform/internal/cost"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
)

func strptr(s string) *string        { return &s }
func boolptr(b bool) *bool           { return &b }
func timeptr(t time.Time) *time.Time { return &t }

func TestInsertPersistsGovernanceColumns(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	seedBudget(t, pool, "bud_gov_test", "tenant", itTenant, "active", "1.0000")

	enq := newRecordingEnqueuer()
	svc := jobs.NewService(pool, enq, cost.NewService(nil))

	p := baseParams()
	p.ClassificationID = strptr("c1")
	p.Intent = strptr("draft")
	p.GovernanceEnvelope = []byte(`{"schema_version":"1"}`)

	res, err := svc.CreateAndEnqueue(context.Background(), p)
	if err != nil {
		t.Fatalf("CreateAndEnqueue: %v", err)
	}
	if res.JobID == "" {
		t.Fatalf("expected job_id, got empty")
	}

	var classID, intent *string
	var govEnv []byte
	if err := pool.QueryRow(context.Background(),
		`SELECT classification_id, intent, governance_envelope FROM generation_jobs WHERE id = $1`,
		res.JobID,
	).Scan(&classID, &intent, &govEnv); err != nil {
		t.Fatalf("query job: %v", err)
	}
	if classID == nil || *classID != "c1" {
		t.Fatalf("expected classification_id=c1, got %v", classID)
	}
	if intent == nil || *intent != "draft" {
		t.Fatalf("expected intent=draft, got %v", intent)
	}
	// Postgres JSONB normalizes whitespace, so compare via round-trip decode.
	var govParsed map[string]any
	if err := json.Unmarshal(govEnv, &govParsed); err != nil {
		t.Fatalf("unmarshal governance_envelope: %v", err)
	}
	if v, ok := govParsed["schema_version"]; !ok || v != "1" {
		t.Fatalf("expected governance_envelope.schema_version=1, got %v", govParsed)
	}
}

// seedGenerationsIdentity inserts the visual_identity the cache-hit job's
// visual_identity_id FK points at (generation_jobs.visual_identity_id references
// visual_identities since Wave 3).
func seedGenerationsIdentity(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO visual_identities (id, tenant_id, world_id, owner_type, owner_id, display_name, style_profile_id)
		 VALUES ($1, $2, 'w1', 'character', 'char_it_generation', 'Captain Mira', $3)`,
		"vi_it_generation", itTenant, itStyleID,
	); err != nil {
		t.Fatalf("seed generations identity: %v", err)
	}
}

// generationsCacheHitParams mirrors exactly what GenerationsHandler.respondCacheHit
// builds for an exact reuse on POST /v1/generations.
func generationsCacheHitParams() jobs.CreateCacheHitParams {
	identityID := "vi_it_generation"
	return jobs.CreateCacheHitParams{
		TenantID:           itTenant,
		RequestedByTokenID: itTokenID,
		JobType:            "generation",
		WorldID:            "w1",
		VisualIdentityID:   &identityID,
		InputPayload: map[string]any{
			"identity_id": identityID,
			"intent":      "commit",
			"prompt_hash": "hash_it_generation",
		},
		FallbackPolicy: "compatible_only",
		FinalAssetID:   "asset_it_generation_cached",
	}
}

// TestGenerationsCacheHitJobInserts is the DB-level regression test for the
// exact-reuse 500 on POST /v1/generations. The handler omitted FallbackPolicy, so
// the column arrived as "" and the INSERT violated
// generation_jobs_fallback_policy_check — turning every cache hit into a
// 500 internal_error. The pre-existing cache-hit coverage used a stub creator and
// therefore never executed an INSERT; this test does.
func TestGenerationsCacheHitJobInserts(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	seedGenerationsIdentity(t, pool)

	svc := jobs.NewService(pool, newRecordingEnqueuer(), cost.NewService(nil))

	res, err := svc.CreateCompletedCacheHitJob(context.Background(), generationsCacheHitParams())
	if err != nil {
		t.Fatalf("CreateCompletedCacheHitJob with the handler's params must insert: %v", err)
	}
	if res.Status != "completed" {
		t.Fatalf("expected a completed cache-hit job, got %q", res.Status)
	}

	var fallbackPolicy, jobType string
	var worldID, identityID *string
	var finalAssetIDs []string
	if err := pool.QueryRow(context.Background(),
		`SELECT fallback_policy, job_type, world_id, visual_identity_id, final_asset_ids
		   FROM generation_jobs WHERE id = $1`,
		res.JobID,
	).Scan(&fallbackPolicy, &jobType, &worldID, &identityID, &finalAssetIDs); err != nil {
		t.Fatalf("query cache-hit job: %v", err)
	}
	if fallbackPolicy != "compatible_only" {
		t.Fatalf("expected fallback_policy=compatible_only, got %q", fallbackPolicy)
	}
	if jobType != "generation" {
		t.Fatalf("expected job_type=generation, got %q", jobType)
	}
	// Lineage parity: a reused job must carry the same world + subject a
	// generated one would, so a consumer cannot tell them apart by lineage.
	if worldID == nil || *worldID != "w1" {
		t.Fatalf("expected world_id=w1, got %v", worldID)
	}
	if identityID == nil || *identityID != "vi_it_generation" {
		t.Fatalf("expected visual_identity_id=vi_it_generation, got %v", identityID)
	}
	if len(finalAssetIDs) != 1 || finalAssetIDs[0] != "asset_it_generation_cached" {
		t.Fatalf("expected the reused asset in final_asset_ids, got %v", finalAssetIDs)
	}
}

// TestCacheHitEmptyFallbackPolicyRejected proves the constraint that produced the
// 500 is real: an empty fallback_policy cannot reach the table. This is what
// makes the assertion in TestGenerationsCacheHitJobInserts load-bearing rather
// than decorative.
func TestCacheHitEmptyFallbackPolicyRejected(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	seedGenerationsIdentity(t, pool)

	svc := jobs.NewService(pool, newRecordingEnqueuer(), cost.NewService(nil))

	p := generationsCacheHitParams()
	p.FallbackPolicy = ""
	if _, err := svc.CreateCompletedCacheHitJob(context.Background(), p); err == nil {
		t.Fatal("an empty fallback_policy must be rejected by generation_jobs_fallback_policy_check")
	}
}
