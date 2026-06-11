package jobs

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/zakkriel/drchat-image-platform/internal/assets"
	"github.com/zakkriel/drchat-image-platform/internal/providers"
)

// selectiveProvider fails Generate when the prompt contains any of failOn;
// otherwise it returns one deterministic image. Used to drive partial pack
// failures.
type selectiveProvider struct {
	mu     sync.Mutex
	calls  []string
	failOn []string
}

func (p *selectiveProvider) Generate(_ context.Context, req providers.ProviderGenerateRequest) (providers.ProviderGenerateResult, error) {
	p.mu.Lock()
	p.calls = append(p.calls, req.Prompt)
	p.mu.Unlock()
	for _, marker := range p.failOn {
		if strings.Contains(req.Prompt, marker) {
			return providers.ProviderGenerateResult{}, errors.New("provider unavailable for " + marker)
		}
	}
	return providers.ProviderGenerateResult{
		Images:     []providers.ProviderImage{{Bytes: tinyPNGBytes(), ContentType: "image/png"}},
		PromptHash: "hash",
		Seed:       "seed",
	}, nil
}
func (p *selectiveProvider) PollStatus(context.Context, string) (providers.ProviderJobStatus, error) {
	return providers.ProviderJobStatus{}, providers.ErrNotApplicable
}
func (p *selectiveProvider) Upscale(context.Context, providers.ProviderUpscaleRequest) (providers.ProviderGenerateResult, error) {
	return providers.ProviderGenerateResult{}, providers.ErrNotImplemented
}
func (p *selectiveProvider) Capabilities() providers.ProviderCapabilities {
	return providers.ProviderCapabilities{ProviderID: "mock", ModelName: "mock-v1"}
}

func (p *selectiveProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

// seedPackJob places a queued pack job (with its pack link and payload) into
// the fake repo, the way the service's create transaction would.
func seedPackJob(repo *fakeJobsRepo, jobID, packID, jobType string, variantKeys []string) {
	worldID := "w1"
	tokenID := "tok_test"
	keys := make([]any, 0, len(variantKeys))
	for _, k := range variantKeys {
		keys = append(keys, k)
	}
	_, _ = repo.Insert(context.Background(), InsertParams{
		ID:                 jobID,
		TenantID:           "tenant_a",
		WorldID:            &worldID,
		JobType:            jobType,
		RequestedByTokenID: &tokenID,
		InputPayload: map[string]any{
			"world_id":           "w1",
			"style_profile_id":   "sty_ok",
			"variant_keys":       keys,
			"visual_identity_id": "vi_test",
			"display_name":       "Captain Mira",
		},
	})
	job := repo.jobs[jobID]
	pid := packID
	job.AssetPackID = &pid
	repo.jobs[jobID] = job
}

func newPackWorker(repo *fakeJobsRepo, assetsRepo *fakeAssetsRepo, provider providers.ImageProvider, fin *fakeFinalizer) *Worker {
	w := &Worker{
		Jobs:      repo,
		Assets:    assetsRepo,
		Storage:   &fakeStorage{},
		Providers: testRegistry(provider),
	}
	if fin != nil {
		w.Finalizer = fin
	}
	return w
}

func TestProcessPackFanOutHappyPath(t *testing.T) {
	repo := newFakeJobsRepo()
	assetsRepo := &fakeAssetsRepo{}
	provider := &selectiveProvider{}
	fin := &fakeFinalizer{}
	variants := []string{"neutral_front_portrait", "neutral_three_quarter_portrait", "side_angle_portrait"}
	seedPackJob(repo, "job_pack1", "pack_1", JobTypeCharacterPack, variants)

	w := newPackWorker(repo, assetsRepo, provider, fin)
	if err := w.ProcessPack(context.Background(), "job_pack1"); err != nil {
		t.Fatalf("ProcessPack: %v", err)
	}

	if len(repo.packAssets) != 3 {
		t.Fatalf("expected 3 assets, got %d", len(repo.packAssets))
	}
	items := repo.packItems["pack_1"]
	if len(items) != 3 {
		t.Fatalf("expected 3 asset_pack_items, got %d", len(items))
	}
	for i, item := range items {
		if item.VariantKey != variants[i] {
			t.Fatalf("item %d: expected variant %q, got %q", i, variants[i], item.VariantKey)
		}
		if item.SortOrder != int32(i) {
			t.Fatalf("item %d: expected sort_order %d, got %d", i, i, item.SortOrder)
		}
	}
	for _, a := range repo.packAssets {
		if a.AssetType != "character_portrait" {
			t.Fatalf("expected asset_type=character_portrait, got %q", a.AssetType)
		}
		if a.VisualIdentityID == nil || *a.VisualIdentityID != "vi_test" {
			t.Fatalf("expected visual_identity_id=vi_test, got %v", a.VisualIdentityID)
		}
		if a.ProviderID == nil || *a.ProviderID != "mock" || a.ModelID == nil || *a.ModelID != "pm_mock_v1" {
			t.Fatalf("expected mock/pm_mock_v1 provenance, got %v/%v", a.ProviderID, a.ModelID)
		}
	}
	job := repo.jobs["job_pack1"]
	if job.Status != "completed" || len(job.FinalAssetIds) != 3 {
		t.Fatalf("expected completed job with 3 final assets, got %s/%v", job.Status, job.FinalAssetIds)
	}
	if got := repo.lastPackStatus("pack_1"); got != "completed" {
		t.Fatalf("expected pack completed, got %q", got)
	}
	if len(fin.committed) != 1 || len(fin.released) != 0 {
		t.Fatalf("expected one commit / zero releases, got %v / %v", fin.committed, fin.released)
	}
	if len(repo.costEvents) != 1 || repo.costEvents[0].Status != "completed" {
		t.Fatalf("expected one completed pack cost event, got %+v", repo.costEvents)
	}
}

// TestProcessPackStampsVariantClassification pins Phase 5B: each generated
// pack asset carries the deterministic variant classification — family,
// compatibility tags, fallback flags, and structured metadata.
func TestProcessPackStampsVariantClassification(t *testing.T) {
	repo := newFakeJobsRepo()
	assetsRepo := &fakeAssetsRepo{}
	// A neutral portrait (generic-safe), a warm expression (fallback-able),
	// and a strong emotion (strict, no fallback).
	variants := []string{"neutral_front_portrait", "expression_warm", "expression_angry"}
	seedPackJob(repo, "job_classify", "pack_cl", JobTypeCharacterPack, variants)

	w := newPackWorker(repo, assetsRepo, &selectiveProvider{}, nil)
	if err := w.ProcessPack(context.Background(), "job_classify"); err != nil {
		t.Fatalf("ProcessPack: %v", err)
	}

	byKey := map[string]assets.InsertParams{}
	for _, a := range repo.packAssets {
		byKey[a.VariantKey] = a
	}

	neutral := byKey["neutral_front_portrait"]
	if neutral.VariantFamily == nil || *neutral.VariantFamily != assets.FamilyNeutral {
		t.Fatalf("neutral: expected family neutral, got %v", neutral.VariantFamily)
	}
	if !containsString(neutral.CompatibilityTags, assets.TagGenericPresence) {
		t.Fatalf("neutral: expected generic_presence tag, got %v", neutral.CompatibilityTags)
	}
	if !neutral.FallbackAllowed {
		t.Fatalf("neutral: expected fallback_allowed=true")
	}
	if neutral.Metadata == nil || neutral.Metadata["variant_family"] != assets.FamilyNeutral {
		t.Fatalf("neutral: expected metadata variant_family, got %v", neutral.Metadata)
	}
	tags, _ := neutral.Metadata["variant_tags"].(map[string]any)
	if tags == nil || tags["angle"] != "front" {
		t.Fatalf("neutral: expected metadata variant_tags angle=front, got %v", neutral.Metadata["variant_tags"])
	}

	warm := byKey["expression_warm"]
	if warm.VariantFamily == nil || *warm.VariantFamily != assets.FamilyWarm {
		t.Fatalf("warm: expected family warm, got %v", warm.VariantFamily)
	}
	if !warm.FallbackAllowed {
		t.Fatalf("warm: expected fallback_allowed=true")
	}

	angry := byKey["expression_angry"]
	if angry.VariantFamily == nil || *angry.VariantFamily != assets.FamilyStrongEmotion {
		t.Fatalf("angry: expected family strong_emotion, got %v", angry.VariantFamily)
	}
	if angry.FallbackAllowed {
		t.Fatalf("angry (strong emotion): expected fallback_allowed=false")
	}
	if len(angry.CompatibilityTags) != 0 {
		t.Fatalf("angry: expected no compatibility tags, got %v", angry.CompatibilityTags)
	}
}

func containsString(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}

func TestProcessPackPlaceUsesPlaceSceneAssetType(t *testing.T) {
	repo := newFakeJobsRepo()
	assetsRepo := &fakeAssetsRepo{}
	seedPackJob(repo, "job_pack_place", "pack_pl", JobTypePlacePack, []string{"establishing_wide_view", "closer_atmospheric_view"})

	w := newPackWorker(repo, assetsRepo, &selectiveProvider{}, nil)
	if err := w.ProcessPack(context.Background(), "job_pack_place"); err != nil {
		t.Fatalf("ProcessPack: %v", err)
	}
	if len(repo.packAssets) != 2 {
		t.Fatalf("expected 2 assets, got %d", len(repo.packAssets))
	}
	for _, a := range repo.packAssets {
		if a.AssetType != "place_scene" {
			t.Fatalf("expected asset_type=place_scene, got %q", a.AssetType)
		}
	}
}

func TestProcessPackPartialFailureCompletesWithWarnings(t *testing.T) {
	repo := newFakeJobsRepo()
	assetsRepo := &fakeAssetsRepo{}
	provider := &selectiveProvider{failOn: []string{"side_angle_portrait"}}
	fin := &fakeFinalizer{}
	seedPackJob(repo, "job_pack2", "pack_2", JobTypeCharacterPack,
		[]string{"neutral_front_portrait", "side_angle_portrait", "neutral_three_quarter_portrait"})

	w := newPackWorker(repo, assetsRepo, provider, fin)
	if err := w.ProcessPack(context.Background(), "job_pack2"); err != nil {
		t.Fatalf("ProcessPack: %v", err)
	}

	if len(repo.packAssets) != 2 || len(repo.packItems["pack_2"]) != 2 {
		t.Fatalf("expected 2 assets + 2 items, got %d/%d", len(repo.packAssets), len(repo.packItems["pack_2"]))
	}
	job := repo.jobs["job_pack2"]
	if job.Status != "completed" || len(job.FinalAssetIds) != 2 {
		t.Fatalf("expected completed job with 2 final assets, got %s/%v", job.Status, job.FinalAssetIds)
	}
	if got := repo.lastPackStatus("pack_2"); got != "completed_with_warnings" {
		t.Fatalf("expected completed_with_warnings, got %q", got)
	}
	// Cost rule for 5A: a partial pack still incurred N provider calls, so
	// the reservation commits in full.
	if len(fin.committed) != 1 || len(fin.released) != 0 {
		t.Fatalf("expected commit on partial success, got %v / %v", fin.committed, fin.released)
	}
	// The failing variant must not abort the batch: all three were attempted.
	if provider.callCount() != 3 {
		t.Fatalf("expected 3 provider calls, got %d", provider.callCount())
	}
}

func TestProcessPackTotalFailureFailsPackAndReleases(t *testing.T) {
	repo := newFakeJobsRepo()
	assetsRepo := &fakeAssetsRepo{}
	fin := &fakeFinalizer{}
	seedPackJob(repo, "job_pack3", "pack_3", JobTypeCharacterPack,
		[]string{"neutral_front_portrait", "side_angle_portrait"})

	w := newPackWorker(repo, assetsRepo, errorProvider{}, fin)
	if err := w.ProcessPack(context.Background(), "job_pack3"); err != nil {
		t.Fatalf("ProcessPack (total failure is terminal, not retryable): %v", err)
	}

	if len(repo.packAssets) != 0 || len(repo.packItems["pack_3"]) != 0 {
		t.Fatalf("expected no assets/items, got %d/%d", len(repo.packAssets), len(repo.packItems["pack_3"]))
	}
	job := repo.jobs["job_pack3"]
	if job.Status != "failed" {
		t.Fatalf("expected failed job, got %s", job.Status)
	}
	if job.Retryable == nil || *job.Retryable {
		t.Fatalf("expected retryable=false, got %v", job.Retryable)
	}
	if got := repo.lastPackStatus("pack_3"); got != "failed" {
		t.Fatalf("expected pack failed, got %q", got)
	}
	if len(fin.released) != 1 || len(fin.committed) != 0 {
		t.Fatalf("expected one release / zero commits, got %v / %v", fin.released, fin.committed)
	}
	if len(repo.costEvents) != 1 || repo.costEvents[0].Status != "failed" {
		t.Fatalf("expected one failed pack cost event, got %+v", repo.costEvents)
	}
}

func TestProcessPackTerminalCompletedJobOnlyCommits(t *testing.T) {
	repo := newFakeJobsRepo()
	assetsRepo := &fakeAssetsRepo{}
	provider := &selectiveProvider{}
	fin := &fakeFinalizer{}
	seedPackJob(repo, "job_pack4", "pack_4", JobTypeCharacterPack, []string{"a", "b"})
	job := repo.jobs["job_pack4"]
	job.Status = "completed"
	repo.jobs["job_pack4"] = job

	w := newPackWorker(repo, assetsRepo, provider, fin)
	if err := w.ProcessPack(context.Background(), "job_pack4"); err != nil {
		t.Fatalf("ProcessPack: %v", err)
	}
	if provider.callCount() != 0 {
		t.Fatalf("terminal job must never re-fan-out, got %d provider calls", provider.callCount())
	}
	if len(repo.packAssets) != 0 {
		t.Fatalf("expected no new assets on terminal job, got %d", len(repo.packAssets))
	}
	if len(fin.committed) != 1 || len(fin.released) != 0 {
		t.Fatalf("expected commit-only, got %v / %v", fin.committed, fin.released)
	}
	if repo.markRunningCalls != 0 {
		t.Fatalf("terminal job must not be re-marked running, got %d", repo.markRunningCalls)
	}
}

func TestProcessPackTerminalFailedJobOnlyReleases(t *testing.T) {
	repo := newFakeJobsRepo()
	provider := &selectiveProvider{}
	fin := &fakeFinalizer{}
	seedPackJob(repo, "job_pack5", "pack_5", JobTypeCharacterPack, []string{"a"})
	job := repo.jobs["job_pack5"]
	job.Status = "failed"
	repo.jobs["job_pack5"] = job

	w := newPackWorker(repo, &fakeAssetsRepo{}, provider, fin)
	if err := w.ProcessPack(context.Background(), "job_pack5"); err != nil {
		t.Fatalf("ProcessPack: %v", err)
	}
	if provider.callCount() != 0 {
		t.Fatalf("terminal job must never re-fan-out, got %d provider calls", provider.callCount())
	}
	if len(fin.released) != 1 || len(fin.committed) != 0 {
		t.Fatalf("expected release-only, got %v / %v", fin.committed, fin.released)
	}
}

func TestProcessPackSkipsAlreadyDeliveredVariants(t *testing.T) {
	repo := newFakeJobsRepo()
	assetsRepo := &fakeAssetsRepo{}
	provider := &selectiveProvider{}
	seedPackJob(repo, "job_pack6", "pack_6", JobTypeCharacterPack, []string{"a", "b", "c"})
	// Simulate a prior attempt that delivered "b" before the terminal write
	// failed: the retry must not re-generate it.
	_ = repo.InsertAssetPackItem(context.Background(), AssetPackItemInsertParams{
		ID: "pki_prior", AssetPackID: "pack_6", VisualAssetID: "asset_prior", VariantKey: "b", SortOrder: 1,
	})

	w := newPackWorker(repo, assetsRepo, provider, nil)
	if err := w.ProcessPack(context.Background(), "job_pack6"); err != nil {
		t.Fatalf("ProcessPack: %v", err)
	}
	if provider.callCount() != 2 {
		t.Fatalf("expected 2 provider calls (b already delivered), got %d", provider.callCount())
	}
	job := repo.jobs["job_pack6"]
	if job.Status != "completed" || len(job.FinalAssetIds) != 3 {
		t.Fatalf("expected completed with 3 final assets (incl. prior), got %s/%v", job.Status, job.FinalAssetIds)
	}
	if got := repo.lastPackStatus("pack_6"); got != "completed" {
		t.Fatalf("expected completed (prior delivery is not a warning), got %q", got)
	}
}

// TestProcessPackReusedRolesNotRegenerated pins the Phase 6A3 worker contract:
// roles persisted as reused asset_pack_items at creation time are NOT
// regenerated (no provider call), they appear in final_asset_ids, and the pack
// records full completeness (all delivered, none missing).
func TestProcessPackReusedRolesNotRegenerated(t *testing.T) {
	repo := newFakeJobsRepo()
	provider := &selectiveProvider{}
	roles := []string{"neutral_front_portrait", "side_angle_portrait", "warm_or_smiling_expression"}
	seedPackJob(repo, "job_reuse", "pack_reuse", JobTypeCharacterPack, roles)
	// The create transaction persisted a retrieval hit for the middle role,
	// pointing at an asset a previous job generated.
	_ = repo.InsertAssetPackItem(context.Background(), AssetPackItemInsertParams{
		ID: "pki_reused", AssetPackID: "pack_reuse", VisualAssetID: "reused_asset", VariantKey: "side_angle_portrait", SortOrder: 1,
	})

	w := newPackWorker(repo, &fakeAssetsRepo{}, provider, nil)
	if err := w.ProcessPack(context.Background(), "job_reuse"); err != nil {
		t.Fatalf("ProcessPack: %v", err)
	}

	// Only the two missing roles hit the provider; the reused role did not.
	if provider.callCount() != 2 {
		t.Fatalf("expected 2 provider calls (reused role skipped), got %d", provider.callCount())
	}
	if len(repo.packAssets) != 2 {
		t.Fatalf("expected 2 newly generated assets, got %d", len(repo.packAssets))
	}
	// final_asset_ids carries the reused asset plus the two freshly generated.
	job := repo.jobs["job_reuse"]
	if job.Status != "completed" || len(job.FinalAssetIds) != 3 {
		t.Fatalf("expected completed with 3 final assets, got %s/%v", job.Status, job.FinalAssetIds)
	}
	if !containsString(job.FinalAssetIds, "reused_asset") {
		t.Fatalf("final_asset_ids must include the reused asset, got %v", job.FinalAssetIds)
	}
	// Completeness: every required role delivered, nothing missing.
	delivered := append([]string(nil), repo.packDelivered["pack_reuse"]...)
	sortStrings(delivered)
	want := append([]string(nil), roles...)
	sortStrings(want)
	if !equalStrings(delivered, want) {
		t.Fatalf("delivered roles: expected %v, got %v", want, delivered)
	}
	if len(repo.packMissing["pack_reuse"]) != 0 {
		t.Fatalf("expected no missing roles, got %v", repo.packMissing["pack_reuse"])
	}
	if got := repo.lastPackStatus("pack_reuse"); got != "completed" {
		t.Fatalf("expected pack completed, got %q", got)
	}
}

// TestProcessPackWarningsRecordsMissingRole: a role that fails generation stays
// in the pack's missing_roles, and the pack completes with warnings.
func TestProcessPackWarningsRecordsMissingRole(t *testing.T) {
	repo := newFakeJobsRepo()
	provider := &selectiveProvider{failOn: []string{"side_angle_portrait"}}
	roles := []string{"neutral_front_portrait", "side_angle_portrait", "warm_or_smiling_expression"}
	seedPackJob(repo, "job_warn", "pack_warn", JobTypeCharacterPack, roles)

	w := newPackWorker(repo, &fakeAssetsRepo{}, provider, nil)
	if err := w.ProcessPack(context.Background(), "job_warn"); err != nil {
		t.Fatalf("ProcessPack: %v", err)
	}
	if got := repo.lastPackStatus("pack_warn"); got != "completed_with_warnings" {
		t.Fatalf("expected completed_with_warnings, got %q", got)
	}
	missing := repo.packMissing["pack_warn"]
	if len(missing) != 1 || missing[0] != "side_angle_portrait" {
		t.Fatalf("expected missing=[side_angle_portrait], got %v", missing)
	}
	delivered := append([]string(nil), repo.packDelivered["pack_warn"]...)
	sortStrings(delivered)
	want := []string{"neutral_front_portrait", "warm_or_smiling_expression"}
	if !equalStrings(delivered, want) {
		t.Fatalf("delivered roles: expected %v, got %v", want, delivered)
	}
}

func sortStrings(s []string) {
	sort.Strings(s)
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestProcessPackMissingPackLinkFailsTerminally(t *testing.T) {
	repo := newFakeJobsRepo()
	fin := &fakeFinalizer{}
	worldID := "w1"
	_, _ = repo.Insert(context.Background(), InsertParams{
		ID:       "job_pack7",
		TenantID: "tenant_a",
		WorldID:  &worldID,
		JobType:  JobTypeCharacterPack,
		InputPayload: map[string]any{
			"variant_keys":       []any{"a"},
			"visual_identity_id": "vi_test",
		},
	})

	w := newPackWorker(repo, &fakeAssetsRepo{}, &selectiveProvider{}, fin)
	if err := w.ProcessPack(context.Background(), "job_pack7"); err != nil {
		t.Fatalf("ProcessPack: %v", err)
	}
	job := repo.jobs["job_pack7"]
	if job.Status != "failed" || job.ErrorCode == nil || *job.ErrorCode != "pack_invalid_job" {
		t.Fatalf("expected failed/pack_invalid_job, got %s/%v", job.Status, job.ErrorCode)
	}
	if len(fin.released) != 1 {
		t.Fatalf("expected reservation released for invalid pack job, got %v", fin.released)
	}
}

// TestProcessPackItemInsertFailureRollsBackAtomically pins the Blocker 2
// fix: the visual_assets insert and the asset_pack_items insert commit
// together or not at all. Run 1 has variant "b"'s combined insert fail
// (atomic rollback — no orphan asset) and the terminal MarkCompleted fail
// (forcing the asynq retry path). The retry must skip the delivered
// variants ("a", "c") via asset_pack_items, re-attempt only "b", and end
// with exactly one asset per variant — no duplicates, items consistent.
func TestProcessPackItemInsertFailureRollsBackAtomically(t *testing.T) {
	repo := newFakeJobsRepo()
	provider := &selectiveProvider{}
	fin := &fakeFinalizer{}
	seedPackJob(repo, "job_pack8", "pack_8", JobTypeCharacterPack, []string{"a", "b", "c"})
	repo.failPackInsertFor["b"] = 1
	repo.failNextMarkCompleted = true

	w := newPackWorker(repo, &fakeAssetsRepo{}, provider, fin)

	// Run 1: a delivered, b rolled back (counted as item failure), c
	// delivered; the terminal job write fails → error → asynq would retry.
	if err := w.ProcessPack(context.Background(), "job_pack8"); err == nil {
		t.Fatalf("expected error from forced terminal-write failure")
	}
	if len(repo.packAssets) != 2 || len(repo.packItems["pack_8"]) != 2 {
		t.Fatalf("run 1: expected 2 atomically-committed asset+item pairs, got %d/%d",
			len(repo.packAssets), len(repo.packItems["pack_8"]))
	}
	if provider.callCount() != 3 {
		t.Fatalf("run 1: expected 3 provider calls, got %d", provider.callCount())
	}

	// Run 2 (retry): job is still running, so fan-out re-enters; "a" and
	// "c" are visible in asset_pack_items and must not hit the provider
	// again; "b" is retried and now succeeds.
	if err := w.ProcessPack(context.Background(), "job_pack8"); err != nil {
		t.Fatalf("retry ProcessPack: %v", err)
	}
	if provider.callCount() != 4 {
		t.Fatalf("retry must call the provider only for the undelivered variant: expected 4 total calls, got %d", provider.callCount())
	}

	// No duplicate visual assets: exactly one per variant.
	if len(repo.packAssets) != 3 {
		t.Fatalf("expected exactly 3 assets after retry, got %d", len(repo.packAssets))
	}
	seen := map[string]int{}
	for _, a := range repo.packAssets {
		seen[a.VariantKey]++
	}
	for variant, n := range seen {
		if n != 1 {
			t.Fatalf("variant %q has %d assets, expected exactly 1", variant, n)
		}
	}
	// asset_pack_items is eventually correct: one item per variant, each
	// pointing at the asset committed alongside it.
	items := repo.packItems["pack_8"]
	if len(items) != 3 {
		t.Fatalf("expected 3 items after retry, got %d", len(items))
	}
	job := repo.jobs["job_pack8"]
	if job.Status != "completed" || len(job.FinalAssetIds) != 3 {
		t.Fatalf("expected completed job with 3 final assets, got %s/%v", job.Status, job.FinalAssetIds)
	}
	if len(fin.committed) != 1 {
		t.Fatalf("expected exactly one commit across both runs, got %v", fin.committed)
	}
}

// TestProcessPackForceRegenerateSupersedesRoleSlots (Phase 6A4): a forced pack
// routes every role's write through InsertPackItemWithAssetSuperseding with the
// EXACT pack-role slot, and a prior ready asset for a role's slot is archived,
// linked forward, and the regenerated asset is versioned prior+1.
func TestProcessPackForceRegenerateSupersedesRoleSlots(t *testing.T) {
	repo := newFakeJobsRepo()
	assetsRepo := &fakeAssetsRepo{}
	provider := &selectiveProvider{}
	variants := []string{"neutral_front_portrait", "side_angle_portrait"}
	seedPackJob(repo, "job_force_pack", "pack_force", JobTypeCharacterPack, variants)
	// Mark the job forced (the handler would carry this on the payload).
	job := repo.jobs["job_force_pack"]
	job.InputPayload["force_regenerate"] = true
	repo.jobs["job_force_pack"] = job

	style := "sty_ok"
	identity := "vi_test"
	// Seed a prior ready asset (version 1) for the first role's slot.
	priorID := "asset_prior_role0"
	repo.packTable = append(repo.packTable, assets.VisualAsset{
		ID:               priorID,
		TenantID:         "tenant_a",
		WorldID:          "w1",
		VisualIdentityID: &identity,
		VariantKey:       variants[0],
		Version:          1,
		StateVersion:     1,
		StyleProfileID:   &style,
		QualityTier:      "standard",
		Status:           "ready",
	})

	w := newPackWorker(repo, assetsRepo, provider, nil)
	if err := w.ProcessPack(context.Background(), "job_force_pack"); err != nil {
		t.Fatalf("ProcessPack: %v", err)
	}

	// Every role took the supersede path (a forced pack has no reused items).
	if len(repo.supersedeVariantCall) != len(variants) {
		t.Fatalf("forced pack must supersede every role: want %d calls, got %d", len(variants), len(repo.supersedeVariantCall))
	}
	wantSlot0 := assets.VariantSlot{
		TenantID: "tenant_a", WorldID: "w1", VisualIdentityID: identity,
		VariantKey: variants[0], StateVersion: 1, StyleProfileID: style, QualityTier: "standard",
	}
	if repo.supersedeVariantCall[0].slot != wantSlot0 {
		t.Fatalf("role 0 slot mismatch: want %+v, got %+v", wantSlot0, repo.supersedeVariantCall[0].slot)
	}

	// The prior asset for role 0 is archived + linked; its replacement is version 2.
	var prior, fresh *assets.VisualAsset
	for i := range repo.packTable {
		if repo.packTable[i].ID == priorID {
			prior = &repo.packTable[i]
		} else if repo.packTable[i].VariantKey == variants[0] {
			fresh = &repo.packTable[i]
		}
	}
	if prior == nil || fresh == nil {
		t.Fatalf("expected prior + regenerated role-0 assets, got %+v", repo.packTable)
	}
	if prior.Status != "archived" || prior.SupersededByAssetID == nil || *prior.SupersededByAssetID != fresh.ID {
		t.Fatalf("prior role asset must be archived and linked to %q, got status=%q link=%v", fresh.ID, prior.Status, prior.SupersededByAssetID)
	}
	if fresh.Version != 2 {
		t.Fatalf("regenerated role asset version must be prior+1 (=2), got %d", fresh.Version)
	}
}
