//go:build integration

package jobs_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zakkriel/drchat-image-platform/internal/assets"
	"github.com/zakkriel/drchat-image-platform/internal/auth"
	"github.com/zakkriel/drchat-image-platform/internal/cost"
	"github.com/zakkriel/drchat-image-platform/internal/http/handlers"
	"github.com/zakkriel/drchat-image-platform/internal/identities"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
	"github.com/zakkriel/drchat-image-platform/internal/providers"
	"github.com/zakkriel/drchat-image-platform/internal/providers/mock"
	"github.com/zakkriel/drchat-image-platform/internal/styles"
	"github.com/zakkriel/drchat-image-platform/internal/telemetry"
)

// Phase 5A pack fan-out, end to end against Postgres: handler → create
// transaction (job + pack + reservation) → worker fan-out → terminal pack
// status + cost finalization. Storage is in-process (memStorage); the cost
// and pack bookkeeping, not S3, are under test.

const (
	itCharacterID = "char_it_pack"
	itPlaceID     = "place_it_pack"
	itIdentityCh  = "vi_it_pack_char"
	itIdentityPl  = "vi_it_pack_place"
)

func seedPackIdentities(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	for _, row := range []struct{ id, ownerType, ownerID, name string }{
		{itIdentityCh, "character", itCharacterID, "Captain Mira"},
		{itIdentityPl, "place", itPlaceID, "The Old Dock"},
	} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO visual_identities (id, tenant_id, world_id, owner_type, owner_id, display_name, style_profile_id)
			 VALUES ($1, $2, 'w1', $3, $4, $5, $6)`,
			row.id, itTenant, row.ownerType, row.ownerID, row.name, itStyleID,
		); err != nil {
			t.Fatalf("seed identity %s: %v", row.id, err)
		}
	}
}

func mountPackTestRouter(svc jobs.Creator, pool *pgxpool.Pool, jobsRepo jobs.Repository) *chi.Mux {
	// Phase 6A3: wire the real per-role reuse decision layer so pack creation is
	// retrieval-first against Postgres (the same wiring router.go uses).
	packs := handlers.NewPacksHandler(svc, styles.NewRepository(pool), identities.NewRepository(pool), itResolver(pool), "mock").
		WithRetriever(assets.NewRetriever(assets.NewRepository(pool)))
	jobsH := handlers.NewJobsHandler(jobsRepo)
	r := chi.NewRouter()
	r.Post("/v1/characters/{character_id}/generate-pack", packs.GenerateCharacterPack)
	r.Post("/v1/places/{place_id}/generate-pack", packs.GeneratePlacePack)
	r.Get("/v1/jobs/{job_id}", jobsH.Get)
	return r
}

func sendPackRequest(t *testing.T, r http.Handler, path string, body map[string]any, idemKey string) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	ctx := auth.ContextWithPrincipal(req.Context(), &auth.Principal{
		TokenID:  itTokenID,
		TenantID: itTenant,
		Scopes:   []string{"images:write"},
	})
	ctx = telemetry.ContextWithRequestID(ctx, "req_test")
	ctx = telemetry.ContextWithRequestLog(ctx, &telemetry.RequestLog{})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func newPackTestWorker(pool *pgxpool.Pool, provider providers.ImageProvider) *jobs.Worker {
	return &jobs.Worker{
		Jobs:      jobs.NewRepository(pool),
		Assets:    assets.NewRepository(pool),
		Storage:   memStorage{},
		Providers: registryFor(provider),
		Finalizer: cost.NewLifecycle(pool, nil),
	}
}

// variantFailingProvider fails Generate when the prompt contains the marker;
// other variants succeed with a real mock render.
type variantFailingProvider struct {
	inner  *mock.Provider
	failOn string
}

func (p *variantFailingProvider) Generate(ctx context.Context, req providers.ProviderGenerateRequest) (providers.ProviderGenerateResult, error) {
	if p.failOn != "" && strings.Contains(req.Prompt, p.failOn) {
		return providers.ProviderGenerateResult{}, errors.New("provider unavailable for " + p.failOn)
	}
	return p.inner.Generate(ctx, req)
}
func (p *variantFailingProvider) PollStatus(ctx context.Context, id string) (providers.ProviderJobStatus, error) {
	return p.inner.PollStatus(ctx, id)
}
func (p *variantFailingProvider) Upscale(ctx context.Context, req providers.ProviderUpscaleRequest) (providers.ProviderGenerateResult, error) {
	return p.inner.Upscale(ctx, req)
}
func (p *variantFailingProvider) Capabilities() providers.ProviderCapabilities {
	return p.inner.Capabilities()
}

func TestEndToEndCharacterPackGeneration(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	seedPackIdentities(t, pool)
	seedBudget(t, pool, "bud_pack_ok", "tenant", itTenant, "active", "1.0000")

	jobsRepo := jobs.NewRepository(pool)
	enq := newRecordingEnqueuer()
	svc := newCostService(pool, enq)
	r := mountPackTestRouter(svc, pool, jobsRepo)

	rec := sendPackRequest(t, r, "/v1/characters/"+itCharacterID+"/generate-pack", map[string]any{
		"world_id":         "w1",
		"style_profile_id": itStyleID,
	}, "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	jobID, _ := resp["job_id"].(string)
	packID, _ := resp["asset_pack_id"].(string)
	if jobID == "" || packID == "" {
		t.Fatalf("expected job_id + asset_pack_id, got %v", resp)
	}
	// PRD 04 §4.2 starter character pack = 7 variants → estimate 7 × 0.0100.
	if resp["estimated_cost_usd"] != "0.0700" {
		t.Fatalf("expected estimated_cost_usd=0.0700, got %v", resp["estimated_cost_usd"])
	}
	if resp["cost_reservation_id"] == "" || resp["cost_reservation_id"] == nil {
		t.Fatalf("expected cost_reservation_id, got %v", resp)
	}
	if got := enq.packSnapshot(); len(got) != 1 || got[0] != jobID {
		t.Fatalf("expected exactly one pack enqueue for %s, got %v", jobID, got)
	}
	if got := scalar(t, pool, `SELECT status FROM asset_packs WHERE id = $1`, packID); got != "planned" {
		t.Fatalf("pre-worker pack status: expected planned, got %s", got)
	}
	if reserved, spent := budgetAmounts(t, pool, "bud_pack_ok"); reserved != "0.0700" || spent != "0.0000" {
		t.Fatalf("pre-worker budget: expected reserved 0.0700 / spent 0, got %s / %s", reserved, spent)
	}

	w := newPackTestWorker(pool, mock.New())
	if err := w.ProcessPack(context.Background(), jobID); err != nil {
		t.Fatalf("worker pack process: %v", err)
	}

	// Job surface: completed, 7 final assets, asset_pack_id visible on GET.
	getReq := httptest.NewRequest(http.MethodGet, "/v1/jobs/"+jobID, nil)
	getReq = getReq.WithContext(auth.ContextWithPrincipal(
		telemetry.ContextWithRequestLog(telemetry.ContextWithRequestID(getReq.Context(), "req_test"), &telemetry.RequestLog{}),
		&auth.Principal{TokenID: itTokenID, TenantID: itTenant, Scopes: []string{"jobs:read"}},
	))
	getRec := httptest.NewRecorder()
	r.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET job expected 200, got %d body=%s", getRec.Code, getRec.Body.String())
	}
	var jobBody map[string]any
	_ = json.Unmarshal(getRec.Body.Bytes(), &jobBody)
	if jobBody["status"] != "completed" {
		t.Fatalf("expected job completed, got %v", jobBody["status"])
	}
	if jobBody["asset_pack_id"] != packID {
		t.Fatalf("expected asset_pack_id=%s on job GET, got %v", packID, jobBody["asset_pack_id"])
	}
	finalIDs, _ := jobBody["final_asset_ids"].([]any)
	if len(finalIDs) != 7 {
		t.Fatalf("expected 7 final_asset_ids, got %v", finalIDs)
	}

	// Pack + items.
	if got := scalar(t, pool, `SELECT status FROM asset_packs WHERE id = $1`, packID); got != "completed" {
		t.Fatalf("pack status: expected completed, got %s", got)
	}
	if got := scalar(t, pool, `SELECT count(*) FROM asset_pack_items WHERE asset_pack_id = $1`, packID); got != "7" {
		t.Fatalf("expected 7 asset_pack_items, got %s", got)
	}
	// Every asset carries provenance + the identity link.
	if got := scalar(t, pool,
		`SELECT count(*) FROM visual_assets
		 WHERE generation_job_id = $1 AND provider_id = 'mock' AND model_id = 'pm_mock_v1' AND visual_identity_id = $2 AND asset_type = 'character_portrait'`,
		jobID, itIdentityCh); got != "7" {
		t.Fatalf("expected 7 provenance-stamped character_portrait assets, got %s", got)
	}
	// Variant keys are the PRD 04 §4.2 starter pack roles, in order.
	if got := scalar(t, pool,
		`SELECT string_agg(variant_key, ',' ORDER BY sort_order) FROM asset_pack_items WHERE asset_pack_id = $1`, packID,
	); got != "neutral_front_portrait,neutral_three_quarter_portrait,side_angle_portrait,warm_or_smiling_expression,serious_or_tense_expression,angry_or_defensive_expression,surprised_or_shocked_expression" {
		t.Fatalf("unexpected variant keys: %s", got)
	}

	// Cost lifecycle: reservation committed, budget reserved → spent by 0.0700.
	if got := scalar(t, pool, `SELECT status FROM cost_reservations WHERE generation_job_id = $1`, jobID); got != "committed" {
		t.Fatalf("reservation: expected committed, got %s", got)
	}
	if reserved, spent := budgetAmounts(t, pool, "bud_pack_ok"); reserved != "0.0000" || spent != "0.0700" {
		t.Fatalf("post-commit budget: expected reserved 0 / spent 0.0700, got %s / %s", reserved, spent)
	}
	if got := scalar(t, pool, `SELECT actual_cost_usd::text FROM generation_jobs WHERE id = $1`, jobID); got != "0.0700" {
		t.Fatalf("job actual_cost_usd: expected 0.0700, got %s", got)
	}
}

func TestEndToEndPlacePackGeneration(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	seedPackIdentities(t, pool)

	jobsRepo := jobs.NewRepository(pool)
	enq := newRecordingEnqueuer()
	svc := newCostService(pool, enq)
	r := mountPackTestRouter(svc, pool, jobsRepo)

	rec := sendPackRequest(t, r, "/v1/places/"+itPlaceID+"/generate-pack", map[string]any{
		"world_id":         "w1",
		"style_profile_id": itStyleID,
	}, "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	jobID, _ := resp["job_id"].(string)
	packID, _ := resp["asset_pack_id"].(string)
	// PRD 04 §5.2 starter place pack = 6 variants → estimate 6 × 0.0100.
	if resp["estimated_cost_usd"] != "0.0600" {
		t.Fatalf("expected estimated_cost_usd=0.0600, got %v", resp["estimated_cost_usd"])
	}

	w := newPackTestWorker(pool, mock.New())
	if err := w.ProcessPack(context.Background(), jobID); err != nil {
		t.Fatalf("worker pack process: %v", err)
	}

	if got := scalar(t, pool, `SELECT status FROM asset_packs WHERE id = $1`, packID); got != "completed" {
		t.Fatalf("pack status: expected completed, got %s", got)
	}
	if got := scalar(t, pool, `SELECT pack_type FROM asset_packs WHERE id = $1`, packID); got != "place_minimal_scene_pack" {
		t.Fatalf("pack_type: expected place_minimal_scene_pack, got %s", got)
	}
	if got := scalar(t, pool, `SELECT count(*) FROM asset_pack_items WHERE asset_pack_id = $1`, packID); got != "6" {
		t.Fatalf("expected 6 asset_pack_items, got %s", got)
	}
	if got := scalar(t, pool,
		`SELECT count(*) FROM visual_assets WHERE generation_job_id = $1 AND asset_type = 'place_scene' AND visual_identity_id = $2`,
		jobID, itIdentityPl); got != "6" {
		t.Fatalf("expected 6 place_scene assets, got %s", got)
	}
}

// TestEndToEndCharacterExpressionPackClassification (Phase 5B): a pack_template
// request fans out the template role set; every generated visual_assets row
// carries a populated variant_family, the right compatibility_tags, structured
// metadata tags, and the correct fallback_allowed flag (strong emotion off,
// generic presence on).
func TestEndToEndCharacterExpressionPackClassification(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	seedPackIdentities(t, pool)
	seedBudget(t, pool, "bud_pack_expr", "tenant", itTenant, "active", "1.0000")

	jobsRepo := jobs.NewRepository(pool)
	enq := newRecordingEnqueuer()
	svc := newCostService(pool, enq)
	r := mountPackTestRouter(svc, pool, jobsRepo)

	rec := sendPackRequest(t, r, "/v1/characters/"+itCharacterID+"/generate-pack", map[string]any{
		"world_id":         "w1",
		"style_profile_id": itStyleID,
		"pack_template":    "character_expression_pack",
	}, "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	jobID, _ := resp["job_id"].(string)
	packID, _ := resp["asset_pack_id"].(string)
	// character_expression_pack = 5 variants → estimate 5 × 0.0100.
	if resp["estimated_cost_usd"] != "0.0500" {
		t.Fatalf("expected estimated_cost_usd=0.0500, got %v", resp["estimated_cost_usd"])
	}
	// The pack carries the template name as its pack_type.
	if got := scalar(t, pool, `SELECT pack_type FROM asset_packs WHERE id = $1`, packID); got != "character_expression_pack" {
		t.Fatalf("pack_type: expected character_expression_pack, got %s", got)
	}

	w := newPackTestWorker(pool, mock.New())
	if err := w.ProcessPack(context.Background(), jobID); err != nil {
		t.Fatalf("worker pack process: %v", err)
	}

	if got := scalar(t, pool, `SELECT status FROM asset_packs WHERE id = $1`, packID); got != "completed" {
		t.Fatalf("pack status: expected completed, got %s", got)
	}
	// Every generated asset has a populated (non-unknown, non-null) family.
	if got := scalar(t, pool,
		`SELECT count(*) FROM visual_assets WHERE generation_job_id = $1 AND variant_family IS NOT NULL AND variant_family <> 'unknown'`,
		jobID); got != "5" {
		t.Fatalf("expected 5 assets with a meaningful variant_family, got %s", got)
	}
	// Neutral portrait: family neutral, generic_presence compatibility tag,
	// fallback allowed, metadata angle tag.
	if got := scalar(t, pool,
		`SELECT variant_family FROM visual_assets WHERE generation_job_id = $1 AND variant_key = 'neutral_front_portrait'`,
		jobID); got != "neutral" {
		t.Fatalf("neutral family: expected neutral, got %s", got)
	}
	if got := scalar(t, pool,
		`SELECT ('generic_presence' = ANY(compatibility_tags))::text FROM visual_assets WHERE generation_job_id = $1 AND variant_key = 'neutral_front_portrait'`,
		jobID); got != "true" {
		t.Fatalf("neutral must carry generic_presence compatibility tag, got %s", got)
	}
	if got := scalar(t, pool,
		`SELECT fallback_allowed::text FROM visual_assets WHERE generation_job_id = $1 AND variant_key = 'neutral_front_portrait'`,
		jobID); got != "true" {
		t.Fatalf("neutral fallback_allowed: expected true, got %s", got)
	}
	if got := scalar(t, pool,
		`SELECT metadata->'variant_tags'->>'angle' FROM visual_assets WHERE generation_job_id = $1 AND variant_key = 'neutral_front_portrait'`,
		jobID); got != "front" {
		t.Fatalf("neutral metadata angle: expected front, got %s", got)
	}
	// Warm expression: family warm, metadata expression tag, fallback allowed.
	if got := scalar(t, pool,
		`SELECT variant_family FROM visual_assets WHERE generation_job_id = $1 AND variant_key = 'expression_warm'`,
		jobID); got != "warm" {
		t.Fatalf("warm family: expected warm, got %s", got)
	}
	if got := scalar(t, pool,
		`SELECT metadata->'variant_tags'->>'expression' FROM visual_assets WHERE generation_job_id = $1 AND variant_key = 'expression_warm'`,
		jobID); got != "warm" {
		t.Fatalf("warm metadata expression: expected warm, got %s", got)
	}
	// Strong-emotion expression: family strong_emotion, fallback NOT allowed,
	// no compatibility tags.
	if got := scalar(t, pool,
		`SELECT variant_family FROM visual_assets WHERE generation_job_id = $1 AND variant_key = 'expression_angry'`,
		jobID); got != "strong_emotion" {
		t.Fatalf("angry family: expected strong_emotion, got %s", got)
	}
	if got := scalar(t, pool,
		`SELECT fallback_allowed::text FROM visual_assets WHERE generation_job_id = $1 AND variant_key = 'expression_angry'`,
		jobID); got != "false" {
		t.Fatalf("angry fallback_allowed: expected false, got %s", got)
	}
	if got := scalar(t, pool,
		`SELECT cardinality(compatibility_tags)::text FROM visual_assets WHERE generation_job_id = $1 AND variant_key = 'expression_angry'`,
		jobID); got != "0" {
		t.Fatalf("angry must carry no compatibility tags, got %s", got)
	}

	// Query sanity: compatibility_tags is populated and queryable (GIN overlap).
	// Exactly one row in this pack carries generic_presence (neutral_front).
	if got := scalar(t, pool,
		`SELECT count(*) FROM visual_assets WHERE generation_job_id = $1 AND compatibility_tags && ARRAY['generic_presence']`,
		jobID); got != "1" {
		t.Fatalf("expected 1 generic_presence asset via array overlap, got %s", got)
	}
}

// TestEndToEndPlaceTimeOfDayPackClassification (Phase 5B): a place
// time-of-day template stamps the time_of_day metadata and time_of_day family
// on each generated scene.
func TestEndToEndPlaceTimeOfDayPackClassification(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	seedPackIdentities(t, pool)
	seedBudget(t, pool, "bud_pack_tod", "tenant", itTenant, "active", "1.0000")

	jobsRepo := jobs.NewRepository(pool)
	enq := newRecordingEnqueuer()
	svc := newCostService(pool, enq)
	r := mountPackTestRouter(svc, pool, jobsRepo)

	rec := sendPackRequest(t, r, "/v1/places/"+itPlaceID+"/generate-pack", map[string]any{
		"world_id":         "w1",
		"style_profile_id": itStyleID,
		"pack_template":    "place_time_of_day_pack",
	}, "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	jobID, _ := resp["job_id"].(string)
	packID, _ := resp["asset_pack_id"].(string)
	if resp["estimated_cost_usd"] != "0.0400" {
		t.Fatalf("expected estimated_cost_usd=0.0400 (4 variants), got %v", resp["estimated_cost_usd"])
	}

	w := newPackTestWorker(pool, mock.New())
	if err := w.ProcessPack(context.Background(), jobID); err != nil {
		t.Fatalf("worker pack process: %v", err)
	}

	if got := scalar(t, pool, `SELECT pack_type FROM asset_packs WHERE id = $1`, packID); got != "place_time_of_day_pack" {
		t.Fatalf("pack_type: expected place_time_of_day_pack, got %s", got)
	}
	// All four rows carry the time_of_day family.
	if got := scalar(t, pool,
		`SELECT count(*) FROM visual_assets WHERE generation_job_id = $1 AND variant_family = 'time_of_day'`,
		jobID); got != "4" {
		t.Fatalf("expected 4 time_of_day assets, got %s", got)
	}
	// Each carries its time_of_day metadata tag.
	for _, tc := range []struct{ key, tod string }{
		{"day_view", "day"},
		{"night_view", "night"},
		{"dawn_view", "dawn"},
		{"dusk_view", "dusk"},
	} {
		if got := scalar(t, pool,
			`SELECT metadata->'variant_tags'->>'time_of_day' FROM visual_assets WHERE generation_job_id = $1 AND variant_key = $2`,
			jobID, tc.key); got != tc.tod {
			t.Fatalf("%s metadata time_of_day: expected %s, got %s", tc.key, tc.tod, got)
		}
	}
	// day_view is the fallback-safe daylight; night/dawn/dusk are strict.
	if got := scalar(t, pool,
		`SELECT count(*) FROM visual_assets WHERE generation_job_id = $1 AND fallback_allowed = true`,
		jobID); got != "1" {
		t.Fatalf("expected exactly 1 fallback-allowed time-of-day asset (day), got %s", got)
	}
}

func TestPackPartialFailureCompletesWithWarnings(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	seedPackIdentities(t, pool)
	seedBudget(t, pool, "bud_pack_warn", "tenant", itTenant, "active", "1.0000")

	jobsRepo := jobs.NewRepository(pool)
	enq := newRecordingEnqueuer()
	svc := newCostService(pool, enq)
	r := mountPackTestRouter(svc, pool, jobsRepo)

	rec := sendPackRequest(t, r, "/v1/characters/"+itCharacterID+"/generate-pack", map[string]any{
		"world_id":         "w1",
		"style_profile_id": itStyleID,
	}, "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	jobID, _ := resp["job_id"].(string)
	packID, _ := resp["asset_pack_id"].(string)

	w := newPackTestWorker(pool, &variantFailingProvider{inner: mock.New(), failOn: "side_angle_portrait"})
	if err := w.ProcessPack(context.Background(), jobID); err != nil {
		t.Fatalf("worker pack process: %v", err)
	}

	if got := scalar(t, pool, `SELECT status FROM asset_packs WHERE id = $1`, packID); got != "completed_with_warnings" {
		t.Fatalf("pack status: expected completed_with_warnings, got %s", got)
	}
	if got := scalar(t, pool, `SELECT status FROM generation_jobs WHERE id = $1`, jobID); got != "completed" {
		t.Fatalf("job status: expected completed, got %s", got)
	}
	// 7-variant starter pack, one variant (side_angle_portrait) fails → 6 delivered.
	if got := scalar(t, pool, `SELECT count(*) FROM asset_pack_items WHERE asset_pack_id = $1`, packID); got != "6" {
		t.Fatalf("expected 6 delivered items, got %s", got)
	}
	// Atomicity invariant: one visual asset per delivered item, no orphans —
	// the asset and its pack item commit in a single transaction.
	if got := scalar(t, pool, `SELECT count(*) FROM visual_assets WHERE generation_job_id = $1`, jobID); got != "6" {
		t.Fatalf("expected exactly 6 visual_assets (no orphans), got %s", got)
	}
	// Cost rule for 5A: partial success still commits the full N × price
	// hold (the provider was called N times).
	if got := scalar(t, pool, `SELECT status FROM cost_reservations WHERE generation_job_id = $1`, jobID); got != "committed" {
		t.Fatalf("reservation: expected committed on partial success, got %s", got)
	}
	if reserved, spent := budgetAmounts(t, pool, "bud_pack_warn"); reserved != "0.0000" || spent != "0.0700" {
		t.Fatalf("budget: expected reserved 0 / spent 0.0700, got %s / %s", reserved, spent)
	}
	// The failed variant left a failed provider_attempt behind.
	if got := scalar(t, pool, `SELECT count(*) FROM provider_attempts WHERE generation_job_id = $1 AND status = 'failed'`, jobID); got != "1" {
		t.Fatalf("expected 1 failed provider attempt, got %s", got)
	}
	// Phase 6A3 completeness: the failed role stays in missing_roles, the other
	// six are delivered, required is the full 7-role starter set.
	if got := scalar(t, pool, `SELECT array_to_string(missing_roles, ',') FROM asset_packs WHERE id = $1`, packID); got != "side_angle_portrait" {
		t.Fatalf("missing_roles: expected side_angle_portrait, got %q", got)
	}
	if got := scalar(t, pool, `SELECT cardinality(delivered_roles)::text FROM asset_packs WHERE id = $1`, packID); got != "6" {
		t.Fatalf("delivered_roles: expected 6, got %s", got)
	}
	if got := scalar(t, pool, `SELECT cardinality(required_roles)::text FROM asset_packs WHERE id = $1`, packID); got != "7" {
		t.Fatalf("required_roles: expected 7, got %s", got)
	}
}

// TestPackRegenerationAllHitsReusesAndChargesNothing is the Phase 6A3 headline:
// generate a pack, then regenerate the SAME pack. Every role exact-matches an
// existing ready asset, so the second request completes synchronously with no
// new visual_assets, no new provider_attempts, no new cost_reservations, no
// enqueue, and zero spend — and the new pack records full completeness.
func TestPackRegenerationAllHitsReusesAndChargesNothing(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	seedPackIdentities(t, pool)
	seedBudget(t, pool, "bud_pack_reuse", "tenant", itTenant, "active", "1.0000")

	jobsRepo := jobs.NewRepository(pool)
	enq := newRecordingEnqueuer()
	svc := newCostService(pool, enq)
	r := mountPackTestRouter(svc, pool, jobsRepo)
	body := map[string]any{"world_id": "w1", "style_profile_id": itStyleID}

	// First generation: zero priors → full pack, generated normally.
	rec1 := sendPackRequest(t, r, "/v1/characters/"+itCharacterID+"/generate-pack", body, "")
	if rec1.Code != http.StatusAccepted {
		t.Fatalf("first POST expected 202, got %d body=%s", rec1.Code, rec1.Body.String())
	}
	var resp1 map[string]any
	_ = json.Unmarshal(rec1.Body.Bytes(), &resp1)
	job1, _ := resp1["job_id"].(string)
	w := newPackTestWorker(pool, mock.New())
	if err := w.ProcessPack(context.Background(), job1); err != nil {
		t.Fatalf("worker pack process: %v", err)
	}

	// Baseline after the first generation.
	assetsBefore := scalar(t, pool, `SELECT count(*) FROM visual_assets WHERE tenant_id = $1`, itTenant)
	attemptsBefore := scalar(t, pool, `SELECT count(*) FROM provider_attempts`)
	reservationsBefore := scalar(t, pool, `SELECT count(*) FROM cost_reservations`)
	_, spentBefore := budgetAmounts(t, pool, "bud_pack_reuse")
	if assetsBefore != "7" {
		t.Fatalf("expected 7 generated assets after first run, got %s", assetsBefore)
	}

	// Regenerate the SAME pack (fresh request, no idempotency key).
	rec2 := sendPackRequest(t, r, "/v1/characters/"+itCharacterID+"/generate-pack", body, "")
	if rec2.Code != http.StatusAccepted {
		t.Fatalf("regenerate POST expected 202, got %d body=%s", rec2.Code, rec2.Body.String())
	}
	var resp2 map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp2)
	job2, _ := resp2["job_id"].(string)
	pack2, _ := resp2["asset_pack_id"].(string)
	if job2 == job1 {
		t.Fatalf("regeneration must create a distinct job, got the same %s", job1)
	}
	// All-hits: free, no reservation in the response.
	if resp2["estimated_cost_usd"] != "0.0000" {
		t.Fatalf("all-hits estimated_cost_usd: expected 0.0000, got %v", resp2["estimated_cost_usd"])
	}
	if _, found := resp2["cost_reservation_id"]; found {
		t.Fatalf("all-hits response must carry no cost_reservation_id, got %v", resp2)
	}
	// The all-hits pack job is never enqueued (only the first job was).
	if got := enq.packSnapshot(); len(got) != 1 || got[0] != job1 {
		t.Fatalf("expected exactly one pack enqueue (the first job), got %v", got)
	}

	// The second job is already completed via reuse.
	if got := scalar(t, pool, `SELECT status FROM generation_jobs WHERE id = $1`, job2); got != "completed" {
		t.Fatalf("job2 status: expected completed, got %s", got)
	}
	if got := scalar(t, pool, `SELECT cache_result FROM generation_jobs WHERE id = $1`, job2); got != "exact_match" {
		t.Fatalf("job2 cache_result: expected exact_match, got %s", got)
	}
	if got := scalar(t, pool, `SELECT actual_cost_usd::text FROM generation_jobs WHERE id = $1`, job2); got != "0.0000" {
		t.Fatalf("job2 actual_cost_usd: expected 0.0000, got %s", got)
	}
	if got := scalar(t, pool, `SELECT coalesce(cost_reservation_id, '') FROM generation_jobs WHERE id = $1`, job2); got != "" {
		t.Fatalf("job2 must have no cost_reservation_id, got %s", got)
	}

	// The reused pack: completed, full completeness, items pointing at the FIRST
	// job's assets (no new assets minted).
	if got := scalar(t, pool, `SELECT status FROM asset_packs WHERE id = $1`, pack2); got != "completed" {
		t.Fatalf("pack2 status: expected completed, got %s", got)
	}
	if got := scalar(t, pool, `SELECT cardinality(delivered_roles)::text FROM asset_packs WHERE id = $1`, pack2); got != "7" {
		t.Fatalf("pack2 delivered_roles: expected 7, got %s", got)
	}
	if got := scalar(t, pool, `SELECT cardinality(missing_roles)::text FROM asset_packs WHERE id = $1`, pack2); got != "0" {
		t.Fatalf("pack2 missing_roles: expected 0, got %s", got)
	}
	if got := scalar(t, pool,
		`SELECT count(*) FROM asset_pack_items api JOIN visual_assets va ON va.id = api.visual_asset_id
		 WHERE api.asset_pack_id = $1 AND va.generation_job_id = $2`, pack2, job1); got != "7" {
		t.Fatalf("expected all 7 reused items to point at the first job's assets, got %s", got)
	}

	// Nothing new was generated, attempted, reserved, or spent.
	if got := scalar(t, pool, `SELECT count(*) FROM visual_assets WHERE tenant_id = $1`, itTenant); got != assetsBefore {
		t.Fatalf("reuse minted new assets: %s -> %s", assetsBefore, got)
	}
	if got := scalar(t, pool, `SELECT count(*) FROM provider_attempts`); got != attemptsBefore {
		t.Fatalf("reuse made new provider attempts: %s -> %s", attemptsBefore, got)
	}
	if got := scalar(t, pool, `SELECT count(*) FROM cost_reservations`); got != reservationsBefore {
		t.Fatalf("reuse made new cost reservations: %s -> %s", reservationsBefore, got)
	}
	if _, spentAfter := budgetAmounts(t, pool, "bud_pack_reuse"); spentAfter != spentBefore {
		t.Fatalf("reuse changed spend: %s -> %s", spentBefore, spentAfter)
	}
}

// TestPackPartialReuseChargesMissesOnly: after generating the 7-role minimal
// pack, a full-reference pack (9 roles) reuses what the matrix allows — the 3
// portrait roles exact-match, and the warm/serious expression roles
// compatible-match the minimal pack's warm/serious expressions (5 reused) — and
// generates only the remaining 4 roles, priced misses-only.
func TestPackPartialReuseChargesMissesOnly(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	seedPackIdentities(t, pool)
	seedBudget(t, pool, "bud_pack_partial", "tenant", itTenant, "active", "1.0000")

	jobsRepo := jobs.NewRepository(pool)
	enq := newRecordingEnqueuer()
	svc := newCostService(pool, enq)
	r := mountPackTestRouter(svc, pool, jobsRepo)

	// First: the 7-role minimal starter pack.
	rec1 := sendPackRequest(t, r, "/v1/characters/"+itCharacterID+"/generate-pack",
		map[string]any{"world_id": "w1", "style_profile_id": itStyleID}, "")
	if rec1.Code != http.StatusAccepted {
		t.Fatalf("first POST expected 202, got %d body=%s", rec1.Code, rec1.Body.String())
	}
	var resp1 map[string]any
	_ = json.Unmarshal(rec1.Body.Bytes(), &resp1)
	job1, _ := resp1["job_id"].(string)
	w := newPackTestWorker(pool, mock.New())
	if err := w.ProcessPack(context.Background(), job1); err != nil {
		t.Fatalf("first worker process: %v", err)
	}
	_, spentAfterFirst := budgetAmounts(t, pool, "bud_pack_partial")
	if spentAfterFirst != "0.0700" {
		t.Fatalf("spend after first pack: expected 0.0700, got %s", spentAfterFirst)
	}

	// Second: the full-reference pack (9 roles). The 3 portrait roles exact-match
	// the minimal pack's portraits; expression_warm/expression_serious
	// compatible-match its warm/serious expressions (default policy is
	// compatible_only). That is 5 reused; the remaining 4 roles generate.
	rec2 := sendPackRequest(t, r, "/v1/characters/"+itCharacterID+"/generate-pack",
		map[string]any{"world_id": "w1", "style_profile_id": itStyleID, "pack_template": "character_full_reference_pack"}, "")
	if rec2.Code != http.StatusAccepted {
		t.Fatalf("second POST expected 202, got %d body=%s", rec2.Code, rec2.Body.String())
	}
	var resp2 map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp2)
	job2, _ := resp2["job_id"].(string)
	pack2, _ := resp2["asset_pack_id"].(string)
	// Misses-only pricing: 4 missing roles × 0.0100.
	if resp2["estimated_cost_usd"] != "0.0400" {
		t.Fatalf("partial pack estimate: expected 0.0400 (4 misses), got %v", resp2["estimated_cost_usd"])
	}
	if resp2["cost_reservation_id"] == nil || resp2["cost_reservation_id"] == "" {
		t.Fatalf("partial pack must carry a cost_reservation_id, got %v", resp2)
	}
	// Pre-worker completeness: 9 required, 5 already delivered (reused), 4 missing.
	if got := scalar(t, pool, `SELECT status FROM asset_packs WHERE id = $1`, pack2); got != "planned" {
		t.Fatalf("pre-worker pack2 status: expected planned, got %s", got)
	}
	if got := scalar(t, pool, `SELECT cardinality(required_roles)::text FROM asset_packs WHERE id = $1`, pack2); got != "9" {
		t.Fatalf("pack2 required_roles: expected 9, got %s", got)
	}
	if got := scalar(t, pool, `SELECT cardinality(delivered_roles)::text FROM asset_packs WHERE id = $1`, pack2); got != "5" {
		t.Fatalf("pack2 delivered_roles (pre-worker, reused): expected 5, got %s", got)
	}
	if got := scalar(t, pool, `SELECT cardinality(missing_roles)::text FROM asset_packs WHERE id = $1`, pack2); got != "4" {
		t.Fatalf("pack2 missing_roles: expected 4, got %s", got)
	}
	// The 5 reused items already point at the first job's assets.
	if got := scalar(t, pool,
		`SELECT count(*) FROM asset_pack_items api JOIN visual_assets va ON va.id = api.visual_asset_id
		 WHERE api.asset_pack_id = $1 AND va.generation_job_id = $2`, pack2, job1); got != "5" {
		t.Fatalf("expected 5 reused items pointing at the first job, got %s", got)
	}
	if reserved, _ := budgetAmounts(t, pool, "bud_pack_partial"); reserved != "0.0400" {
		t.Fatalf("partial reservation: expected reserved 0.0400, got %s", reserved)
	}

	if err := w.ProcessPack(context.Background(), job2); err != nil {
		t.Fatalf("second worker process: %v", err)
	}

	// The worker generated only the 4 missing roles (4 new assets, 4 attempts).
	if got := scalar(t, pool, `SELECT count(*) FROM visual_assets WHERE generation_job_id = $1`, job2); got != "4" {
		t.Fatalf("expected 4 newly generated assets for job2, got %s", got)
	}
	if got := scalar(t, pool, `SELECT count(*) FROM provider_attempts WHERE generation_job_id = $1`, job2); got != "4" {
		t.Fatalf("expected 4 provider attempts for job2 (misses only), got %s", got)
	}
	// The pack now has all 9 items and full completeness.
	if got := scalar(t, pool, `SELECT count(*) FROM asset_pack_items WHERE asset_pack_id = $1`, pack2); got != "9" {
		t.Fatalf("expected 9 pack items, got %s", got)
	}
	if got := scalar(t, pool, `SELECT status FROM asset_packs WHERE id = $1`, pack2); got != "completed" {
		t.Fatalf("pack2 status: expected completed, got %s", got)
	}
	if got := scalar(t, pool, `SELECT cardinality(delivered_roles)::text FROM asset_packs WHERE id = $1`, pack2); got != "9" {
		t.Fatalf("pack2 delivered_roles after worker: expected 9, got %s", got)
	}
	if got := scalar(t, pool, `SELECT cardinality(missing_roles)::text FROM asset_packs WHERE id = $1`, pack2); got != "0" {
		t.Fatalf("pack2 missing_roles after worker: expected 0, got %s", got)
	}
	// Budget spend rose by misses-only: 0.0700 + 0.0400 = 0.1100.
	if _, spent := budgetAmounts(t, pool, "bud_pack_partial"); spent != "0.1100" {
		t.Fatalf("total spend: expected 0.1100 (misses-only), got %s", spent)
	}
}

func TestPackTotalFailureFailsAndReleasesBudget(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	seedPackIdentities(t, pool)
	seedBudget(t, pool, "bud_pack_fail", "tenant", itTenant, "active", "1.0000")

	jobsRepo := jobs.NewRepository(pool)
	enq := newRecordingEnqueuer()
	svc := newCostService(pool, enq)
	r := mountPackTestRouter(svc, pool, jobsRepo)

	rec := sendPackRequest(t, r, "/v1/characters/"+itCharacterID+"/generate-pack", map[string]any{
		"world_id":         "w1",
		"style_profile_id": itStyleID,
	}, "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	jobID, _ := resp["job_id"].(string)
	packID, _ := resp["asset_pack_id"].(string)

	w := newPackTestWorker(pool, failingProvider{})
	if err := w.ProcessPack(context.Background(), jobID); err != nil {
		t.Fatalf("worker pack process (total failure is terminal): %v", err)
	}

	if got := scalar(t, pool, `SELECT status FROM asset_packs WHERE id = $1`, packID); got != "failed" {
		t.Fatalf("pack status: expected failed, got %s", got)
	}
	var status string
	var retryable *bool
	if err := pool.QueryRow(context.Background(),
		`SELECT status, retryable FROM generation_jobs WHERE id = $1`, jobID).Scan(&status, &retryable); err != nil {
		t.Fatalf("read job: %v", err)
	}
	if status != "failed" || retryable == nil || *retryable {
		t.Fatalf("expected failed/retryable=false, got %s/%v", status, retryable)
	}
	if got := scalar(t, pool, `SELECT count(*) FROM asset_pack_items WHERE asset_pack_id = $1`, packID); got != "0" {
		t.Fatalf("expected 0 items, got %s", got)
	}
	// Reservation released, budget refunded in full.
	if got := scalar(t, pool, `SELECT status FROM cost_reservations WHERE generation_job_id = $1`, jobID); got != "released" {
		t.Fatalf("reservation: expected released, got %s", got)
	}
	if reserved, spent := budgetAmounts(t, pool, "bud_pack_fail"); reserved != "0.0000" || spent != "0.0000" {
		t.Fatalf("budget: expected full refund, got reserved %s / spent %s", reserved, spent)
	}
}

func TestPackPreflightBudgetExceededIsNeverEnqueued(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	seedPackIdentities(t, pool)
	// Budget covers one image but not the 7-variant starter pack (7 × 0.0100).
	seedBudget(t, pool, "bud_pack_tight", "tenant", itTenant, "active", "0.0200")

	jobsRepo := jobs.NewRepository(pool)
	enq := newRecordingEnqueuer()
	svc := newCostService(pool, enq)
	r := mountPackTestRouter(svc, pool, jobsRepo)

	rec := sendPackRequest(t, r, "/v1/characters/"+itCharacterID+"/generate-pack", map[string]any{
		"world_id":         "w1",
		"style_profile_id": itStyleID,
	}, "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d body=%s", rec.Code, rec.Body.String())
	}
	var errBody map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &errBody)
	if errBody["code"] != "budget_exceeded" {
		t.Fatalf("expected budget_exceeded, got %v", errBody)
	}
	if got := enq.packSnapshot(); len(got) != 0 {
		t.Fatalf("expected no enqueue on denied pre-flight, got %v", got)
	}
	// The failed reservation carries the full pack estimate (N × price).
	var rStatus, rEst, rReserved string
	if err := pool.QueryRow(context.Background(),
		`SELECT status, estimated_amount::text, reserved_amount::text FROM cost_reservations WHERE tenant_id = $1 ORDER BY created_at DESC LIMIT 1`,
		itTenant).Scan(&rStatus, &rEst, &rReserved); err != nil {
		t.Fatalf("read reservation: %v", err)
	}
	if rStatus != "failed" || rEst != "0.0700" || rReserved != "0.0000" {
		t.Fatalf("reservation: expected failed/0.0700/0, got %s/%s/%s", rStatus, rEst, rReserved)
	}
	if reserved, _ := budgetAmounts(t, pool, "bud_pack_tight"); reserved != "0.0000" {
		t.Fatalf("budget must hold nothing on denial, got reserved %s", reserved)
	}
	// A denied pre-flight must not leave an asset pack behind: the pack row
	// is only inserted after the reservation succeeds, so nothing can sit at
	// status=planned for a job that will never run.
	if got := scalar(t, pool, `SELECT count(*) FROM asset_packs WHERE tenant_id = $1`, itTenant); got != "0" {
		t.Fatalf("expected no asset_packs row on denied pre-flight, got %s", got)
	}
	if got := scalar(t, pool, `SELECT count(*) FROM asset_packs WHERE tenant_id = $1 AND status = 'planned'`, itTenant); got != "0" {
		t.Fatalf("no asset_pack may remain planned, got %s", got)
	}
	// The 422 body carries no asset_pack_id (none exists).
	var errResp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &errResp)
	if _, found := errResp["asset_pack_id"]; found {
		t.Fatalf("denied pre-flight response must not carry asset_pack_id: %v", errResp)
	}
	// The failed job has no pack link either.
	var packLink *string
	if err := pool.QueryRow(context.Background(),
		`SELECT asset_pack_id FROM generation_jobs WHERE tenant_id = $1 ORDER BY created_at DESC LIMIT 1`,
		itTenant).Scan(&packLink); err != nil {
		t.Fatalf("read job pack link: %v", err)
	}
	if packLink != nil {
		t.Fatalf("denied job must not link an asset pack, got %v", *packLink)
	}
}

func TestPackEnqueueFailureFailsPackAndReleasesReservation(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	seedPackIdentities(t, pool)
	seedBudget(t, pool, "bud_pack_enq", "tenant", itTenant, "active", "1.0000")

	jobsRepo := jobs.NewRepository(pool)
	enq := newRecordingEnqueuer()
	enq.failOn["*"] = true
	svc := newCostService(pool, enq).WithFinalizer(cost.NewLifecycle(pool, nil))
	r := mountPackTestRouter(svc, pool, jobsRepo)

	rec := sendPackRequest(t, r, "/v1/characters/"+itCharacterID+"/generate-pack", map[string]any{
		"world_id":         "w1",
		"style_profile_id": itStyleID,
	}, "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 on enqueue failure, got %d body=%s", rec.Code, rec.Body.String())
	}

	// Job failed with enqueue_failed.
	var jobID, jobStatus string
	var errorCode *string
	if err := pool.QueryRow(context.Background(),
		`SELECT id, status, error_code FROM generation_jobs WHERE tenant_id = $1 ORDER BY created_at DESC LIMIT 1`,
		itTenant).Scan(&jobID, &jobStatus, &errorCode); err != nil {
		t.Fatalf("read job: %v", err)
	}
	if jobStatus != "failed" || errorCode == nil || *errorCode != "enqueue_failed" {
		t.Fatalf("expected failed/enqueue_failed, got %s/%v", jobStatus, errorCode)
	}
	// Reservation released, budget refunded.
	if got := scalar(t, pool, `SELECT status FROM cost_reservations WHERE generation_job_id = $1`, jobID); got != "released" {
		t.Fatalf("reservation: expected released, got %s", got)
	}
	if reserved, spent := budgetAmounts(t, pool, "bud_pack_enq"); reserved != "0.0000" || spent != "0.0000" {
		t.Fatalf("budget: expected full refund, got reserved %s / spent %s", reserved, spent)
	}
	// The pack (created after the successful pre-flight) is failed, never
	// stuck at planned.
	if got := scalar(t, pool, `SELECT status FROM asset_packs WHERE created_by_job_id = $1`, jobID); got != "failed" {
		t.Fatalf("pack: expected failed after enqueue failure, got %s", got)
	}
	if got := scalar(t, pool, `SELECT count(*) FROM asset_packs WHERE tenant_id = $1 AND status = 'planned'`, itTenant); got != "0" {
		t.Fatalf("no asset_pack may remain planned, got %s", got)
	}
}

func TestPackIdempotencyReplayReturnsSameJobAndPack(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	seedPackIdentities(t, pool)

	jobsRepo := jobs.NewRepository(pool)
	enq := newRecordingEnqueuer()
	svc := newCostService(pool, enq)
	r := mountPackTestRouter(svc, pool, jobsRepo)

	body := map[string]any{"world_id": "w1", "style_profile_id": itStyleID}
	const key = "phase5a-pack-replay-1"

	first := sendPackRequest(t, r, "/v1/characters/"+itCharacterID+"/generate-pack", body, key)
	if first.Code != http.StatusAccepted {
		t.Fatalf("first: expected 202, got %d body=%s", first.Code, first.Body.String())
	}
	var firstBody map[string]any
	_ = json.Unmarshal(first.Body.Bytes(), &firstBody)

	second := sendPackRequest(t, r, "/v1/characters/"+itCharacterID+"/generate-pack", body, key)
	if second.Code != http.StatusAccepted {
		t.Fatalf("replay: expected 202, got %d body=%s", second.Code, second.Body.String())
	}
	var secondBody map[string]any
	_ = json.Unmarshal(second.Body.Bytes(), &secondBody)

	if firstBody["job_id"] != secondBody["job_id"] {
		t.Fatalf("replay: expected same job_id, got %v vs %v", firstBody["job_id"], secondBody["job_id"])
	}
	if firstBody["asset_pack_id"] == nil || firstBody["asset_pack_id"] != secondBody["asset_pack_id"] {
		t.Fatalf("replay: expected same asset_pack_id, got %v vs %v", firstBody["asset_pack_id"], secondBody["asset_pack_id"])
	}

	// No duplicate rows, exactly one enqueue.
	if got := scalar(t, pool, `SELECT count(*) FROM generation_jobs WHERE tenant_id = $1`, itTenant); got != "1" {
		t.Fatalf("expected one job row, got %s", got)
	}
	if got := scalar(t, pool, `SELECT count(*) FROM asset_packs WHERE tenant_id = $1`, itTenant); got != "1" {
		t.Fatalf("expected one asset_packs row, got %s", got)
	}
	if got := enq.packSnapshot(); len(got) != 1 {
		t.Fatalf("expected exactly one pack enqueue, got %v", got)
	}

	// Replay after the worker completes the pack must not duplicate items.
	w := newPackTestWorker(pool, mock.New())
	jobID, _ := firstBody["job_id"].(string)
	if err := w.ProcessPack(context.Background(), jobID); err != nil {
		t.Fatalf("worker pack process: %v", err)
	}
	third := sendPackRequest(t, r, "/v1/characters/"+itCharacterID+"/generate-pack", body, key)
	if third.Code != http.StatusAccepted {
		t.Fatalf("post-completion replay: expected 202, got %d body=%s", third.Code, third.Body.String())
	}
	var thirdBody map[string]any
	_ = json.Unmarshal(third.Body.Bytes(), &thirdBody)
	if thirdBody["status"] != "completed" {
		t.Fatalf("post-completion replay must echo live status, got %v", thirdBody["status"])
	}
	packID, _ := firstBody["asset_pack_id"].(string)
	if got := scalar(t, pool, `SELECT count(*) FROM asset_pack_items WHERE asset_pack_id = $1`, packID); got != "7" {
		t.Fatalf("expected 7 items after replay, got %s", got)
	}
}

// TestPackWorkerRetryAfterCompletionDoesNotRefanOut drives ProcessPack twice
// (as an asynq retry would) and asserts the terminal short-circuit: no new
// provider attempts, items, or budget movement on the second run.
func TestPackWorkerRetryAfterCompletionDoesNotRefanOut(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	seedPackIdentities(t, pool)
	seedBudget(t, pool, "bud_pack_retry", "tenant", itTenant, "active", "1.0000")

	jobsRepo := jobs.NewRepository(pool)
	enq := newRecordingEnqueuer()
	svc := newCostService(pool, enq)
	r := mountPackTestRouter(svc, pool, jobsRepo)

	rec := sendPackRequest(t, r, "/v1/places/"+itPlaceID+"/generate-pack", map[string]any{
		"world_id":         "w1",
		"style_profile_id": itStyleID,
	}, "")
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	jobID, _ := resp["job_id"].(string)
	packID, _ := resp["asset_pack_id"].(string)

	w := newPackTestWorker(pool, mock.New())
	if err := w.ProcessPack(context.Background(), jobID); err != nil {
		t.Fatalf("first pack process: %v", err)
	}
	attemptsAfterFirst := scalar(t, pool, `SELECT count(*) FROM provider_attempts WHERE generation_job_id = $1`, jobID)

	if err := w.ProcessPack(context.Background(), jobID); err != nil {
		t.Fatalf("second (retry) pack process: %v", err)
	}
	if got := scalar(t, pool, `SELECT count(*) FROM provider_attempts WHERE generation_job_id = $1`, jobID); got != attemptsAfterFirst {
		t.Fatalf("retry re-fanned out: attempts %s -> %s", attemptsAfterFirst, got)
	}
	// 6-variant starter place pack.
	if got := scalar(t, pool, `SELECT count(*) FROM asset_pack_items WHERE asset_pack_id = $1`, packID); got != "6" {
		t.Fatalf("expected 6 items after retry, got %s", got)
	}
	// Budget moved exactly once.
	if reserved, spent := budgetAmounts(t, pool, "bud_pack_retry"); reserved != "0.0000" || spent != "0.0600" {
		t.Fatalf("budget after retry: expected reserved 0 / spent 0.0600, got %s / %s", reserved, spent)
	}
}

// TestEndToEndGeneratedPackAssetIsRetrievable proves the 6A1 provenance fix:
// an asset produced by the real pack generation path persists style_profile_id
// and is therefore findable by the retrieval layer (which matches on a
// concrete style_profile_id). Before the fix, generated rows had
// style_profile_id = NULL and retrieval could never find them — only manually
// seeded rows worked. This test deliberately uses NO manual visual_assets
// insert; the rows come entirely from the worker.
func TestEndToEndGeneratedPackAssetIsRetrievable(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	seedPackIdentities(t, pool)
	seedBudget(t, pool, "bud_pack_retrieve", "tenant", itTenant, "active", "1.0000")

	jobsRepo := jobs.NewRepository(pool)
	enq := newRecordingEnqueuer()
	svc := newCostService(pool, enq)
	r := mountPackTestRouter(svc, pool, jobsRepo)

	rec := sendPackRequest(t, r, "/v1/characters/"+itCharacterID+"/generate-pack", map[string]any{
		"world_id":         "w1",
		"style_profile_id": itStyleID,
	}, "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	jobID, _ := resp["job_id"].(string)
	if jobID == "" {
		t.Fatalf("expected job_id, got %v", resp)
	}

	w := newPackTestWorker(pool, mock.New())
	if err := w.ProcessPack(context.Background(), jobID); err != nil {
		t.Fatalf("worker pack process: %v", err)
	}

	// Provenance: every generated asset persists the requested style_profile_id
	// (non-null), so the next assertion's retrieval can match on it.
	if got := scalar(t, pool,
		`SELECT count(*) FROM visual_assets WHERE generation_job_id = $1 AND style_profile_id = $2`,
		jobID, itStyleID); got != "7" {
		t.Fatalf("expected 7 generated assets with style_profile_id=%s, got %s", itStyleID, got)
	}
	if got := scalar(t, pool,
		`SELECT count(*) FROM visual_assets WHERE generation_job_id = $1 AND style_profile_id IS NULL`,
		jobID); got != "0" {
		t.Fatalf("no generated asset should have NULL style_profile_id, got %s", got)
	}

	// Retrieval against a generated row (NOT a manual seed): exact match on the
	// platform-produced neutral_front_portrait, scoped by the requested style.
	retriever := assets.NewRetriever(assets.NewRepository(pool))
	res, err := retriever.Retrieve(context.Background(), assets.RetrievalQuery{
		TenantID:         itTenant,
		WorldID:          "w1",
		VisualIdentityID: itIdentityCh,
		EntityType:       assets.EntityCharacter,
		VariantKey:       "neutral_front_portrait",
		StyleProfileID:   itStyleID,
		StateVersion:     1,
		QualityTier:      "standard",
		FallbackPolicy:   assets.FallbackPolicyCompatibleOnly,
	})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if res.MatchType != assets.OutcomeExactMatch {
		t.Fatalf("want exact_match on a generated asset, got %s", res.MatchType)
	}
	if res.Asset == nil {
		t.Fatal("expected a returned asset")
	}
	if res.Asset.StyleProfileID == nil || *res.Asset.StyleProfileID != itStyleID {
		t.Fatalf("returned asset style_profile_id: want %s, got %v", itStyleID, res.Asset.StyleProfileID)
	}
	// The returned asset is one of the rows this job generated.
	if got := scalar(t, pool,
		`SELECT count(*) FROM visual_assets WHERE id = $1 AND generation_job_id = $2`,
		res.Asset.ID, jobID); got != "1" {
		t.Fatalf("returned asset %s is not one of the generated rows for job %s", res.Asset.ID, jobID)
	}
}

// TestPackForceRegenerateSupersedesAndChargesFullPack is the Phase 6A4 pack
// acceptance test: a forced regeneration of a pack whose every role has a
// reusable asset still prices and generates the WHOLE pack (no misses-only
// discount, no all-hits shortcut), the prior per-role assets are archived and
// linked forward, the new pack is all-delivered with all-new ready assets, and
// budget spend increases by the full pack cost.
func TestPackForceRegenerateSupersedesAndChargesFullPack(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	seedPackIdentities(t, pool)
	seedBudget(t, pool, "bud_pack_force", "tenant", itTenant, "active", "5.0000")

	jobsRepo := jobs.NewRepository(pool)
	enq := newRecordingEnqueuer()
	svc := newCostService(pool, enq)
	r := mountPackTestRouter(svc, pool, jobsRepo)
	body := map[string]any{"world_id": "w1", "style_profile_id": itStyleID}

	// First generation: zero priors → full 7-role pack, generated normally.
	rec1 := sendPackRequest(t, r, "/v1/characters/"+itCharacterID+"/generate-pack", body, "")
	if rec1.Code != http.StatusAccepted {
		t.Fatalf("first POST expected 202, got %d body=%s", rec1.Code, rec1.Body.String())
	}
	var resp1 map[string]any
	_ = json.Unmarshal(rec1.Body.Bytes(), &resp1)
	job1, _ := resp1["job_id"].(string)
	w := newPackTestWorker(pool, mock.New())
	if err := w.ProcessPack(context.Background(), job1); err != nil {
		t.Fatalf("worker pack process: %v", err)
	}
	if got := scalar(t, pool, `SELECT count(*) FROM visual_assets WHERE tenant_id = $1`, itTenant); got != "7" {
		t.Fatalf("expected 7 generated assets after first run, got %s", got)
	}
	_, spentAfterFirst := budgetAmounts(t, pool, "bud_pack_force")

	// Force regenerate the SAME pack — every role is reusable, but force bypasses.
	forcedBody := map[string]any{"world_id": "w1", "style_profile_id": itStyleID, "force_regenerate": true}
	rec2 := sendPackRequest(t, r, "/v1/characters/"+itCharacterID+"/generate-pack", forcedBody, "")
	if rec2.Code != http.StatusAccepted {
		t.Fatalf("forced POST expected 202, got %d body=%s", rec2.Code, rec2.Body.String())
	}
	var resp2 map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp2)
	job2, _ := resp2["job_id"].(string)
	pack2, _ := resp2["asset_pack_id"].(string)
	if job2 == job1 {
		t.Fatalf("forced regeneration must create a distinct job")
	}
	// A forced pack is priced (whole pack) and enqueued — not an all-hits shortcut.
	if _, found := resp2["cost_reservation_id"]; !found {
		t.Fatalf("forced pack must reserve cost (cost_reservation_id in response), got %v", resp2)
	}
	if resp2["estimated_cost_usd"] == "0.0000" {
		t.Fatalf("forced pack must be priced for the whole pack, got estimated_cost_usd=0.0000")
	}
	if got := enq.packSnapshot(); len(got) != 2 {
		t.Fatalf("forced pack must be enqueued (expected 2 pack enqueues total), got %v", got)
	}
	// Pricing covers all 7 roles (no misses-only discount): the reservation's
	// estimate equals the first full-pack generation's estimate.
	estForced := scalar(t, pool, `SELECT cost_estimate_usd::text FROM generation_jobs WHERE id = $1`, job2)
	estFirst := scalar(t, pool, `SELECT cost_estimate_usd::text FROM generation_jobs WHERE id = $1`, job1)
	if estForced != estFirst {
		t.Fatalf("forced pack estimate must equal a full cold pack (%s), got %s", estFirst, estForced)
	}

	if err := w.ProcessPack(context.Background(), job2); err != nil {
		t.Fatalf("worker forced pack process: %v", err)
	}

	// 7 prior assets archived + linked; 7 new ready assets (version 2) for job2.
	if got := scalar(t, pool, `SELECT count(*) FROM visual_assets WHERE tenant_id = $1`, itTenant); got != "14" {
		t.Fatalf("expected 14 total assets (7 archived + 7 regenerated), got %s", got)
	}
	if got := scalar(t, pool,
		`SELECT count(*) FROM visual_assets WHERE generation_job_id = $1 AND status = 'archived' AND superseded_by_asset_id IS NOT NULL`, job1); got != "7" {
		t.Fatalf("expected 7 prior assets archived + linked forward, got %s", got)
	}
	if got := scalar(t, pool,
		`SELECT count(*) FROM visual_assets WHERE generation_job_id = $1 AND status = 'ready' AND version = 2`, job2); got != "7" {
		t.Fatalf("expected 7 regenerated ready assets at version 2, got %s", got)
	}
	// Exactly 7 ready pack assets remain for the identity (one per role).
	if got := scalar(t, pool,
		`SELECT count(*) FROM visual_assets WHERE visual_identity_id = $1 AND status = 'ready'`, itIdentityCh); got != "7" {
		t.Fatalf("expected exactly 7 ready assets for the identity after supersede, got %s", got)
	}

	// The forced pack is complete with all-new assets (none reused from job1).
	if got := scalar(t, pool, `SELECT status FROM asset_packs WHERE id = $1`, pack2); got != "completed" {
		t.Fatalf("pack2 status: expected completed, got %s", got)
	}
	if got := scalar(t, pool, `SELECT cardinality(delivered_roles)::text FROM asset_packs WHERE id = $1`, pack2); got != "7" {
		t.Fatalf("pack2 delivered_roles: expected 7, got %s", got)
	}
	if got := scalar(t, pool, `SELECT cardinality(missing_roles)::text FROM asset_packs WHERE id = $1`, pack2); got != "0" {
		t.Fatalf("pack2 missing_roles: expected 0, got %s", got)
	}
	if got := scalar(t, pool,
		`SELECT count(*) FROM asset_pack_items api JOIN visual_assets va ON va.id = api.visual_asset_id
		 WHERE api.asset_pack_id = $1 AND va.generation_job_id = $2`, pack2, job2); got != "7" {
		t.Fatalf("forced pack must point at all-new job2 assets, got %s", got)
	}
	if got := scalar(t, pool,
		`SELECT count(*) FROM asset_pack_items WHERE asset_pack_id = $1`, pack2); got != "7" {
		t.Fatalf("forced pack must have exactly 7 items (none reused), got %s", got)
	}

	// Budget spend increased by the full pack cost (no misses-only discount).
	_, spentAfterForced := budgetAmounts(t, pool, "bud_pack_force")
	if spentAfterForced == spentAfterFirst {
		t.Fatalf("forced pack must increase spend by the full pack cost: still %s", spentAfterForced)
	}
}
