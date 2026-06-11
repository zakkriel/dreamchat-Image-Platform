package jobs

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"sync"
	"testing"
	"time"

	"github.com/zakkriel/drchat-image-platform/internal/assets"
	"github.com/zakkriel/drchat-image-platform/internal/providers"
	"github.com/zakkriel/drchat-image-platform/internal/providers/mock"
)

type fakeJobsRepo struct {
	mu               sync.Mutex
	jobs             map[string]Job
	attempts         []ProviderAttempt
	costEvents       []CostEventInsertParams
	markRunningCalls int
	markCompleted    []string
	markFailed       []string

	// Pack fan-out tracking (Phase 5A).
	packStatuses map[string][]string // packID -> status transitions
	packItems    map[string][]AssetPackItem
	packAssets   []assets.InsertParams
	// Pack completeness tracking (Phase 6A3): the last delivered/missing role
	// sets written for a pack.
	packDelivered map[string][]string
	packMissing   map[string][]string
	// failPackInsertFor makes InsertPackItemWithAsset fail N times for a
	// variant key, atomically (nothing recorded) — modelling a rolled-back
	// asset + item transaction.
	failPackInsertFor map[string]int
	// failNextMarkCompleted makes the next MarkCompleted fail once, to force
	// the asynq-retry path after a successful fan-out.
	failNextMarkCompleted bool

	// Phase 6A4 pack supersede modeling. packTable is an in-memory visual_assets
	// for pack-role slots; supersedeVariantCalls records each
	// InsertPackItemWithAssetSuperseding call for routing/slot assertions.
	packTable            []assets.VisualAsset
	supersedeVariantCall []supersedeVariantCall
}

type supersedeVariantCall struct {
	asset assets.InsertParams
	item  AssetPackItemInsertParams
	slot  assets.VariantSlot
}

func newFakeJobsRepo() *fakeJobsRepo {
	return &fakeJobsRepo{
		jobs:              map[string]Job{},
		packStatuses:      map[string][]string{},
		packItems:         map[string][]AssetPackItem{},
		packDelivered:     map[string][]string{},
		packMissing:       map[string][]string{},
		failPackInsertFor: map[string]int{},
	}
}

func (r *fakeJobsRepo) Insert(_ context.Context, params InsertParams) (Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	job := Job{
		ID:                 params.ID,
		TenantID:           params.TenantID,
		WorldID:            params.WorldID,
		JobType:            params.JobType,
		Status:             "queued",
		RequestedByTokenID: params.RequestedByTokenID,
		InputPayload:       params.InputPayload,
		FallbackPolicy:     params.FallbackPolicy,
		CacheResult:        params.CacheResult,
	}
	r.jobs[params.ID] = job
	return job, nil
}

func (r *fakeJobsRepo) GetByIDForTenant(_ context.Context, id, tenantID string) (Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	job, ok := r.jobs[id]
	if !ok || job.TenantID != tenantID {
		return Job{}, ErrNotFound
	}
	return job, nil
}

func (r *fakeJobsRepo) GetByID(_ context.Context, id string) (Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	job, ok := r.jobs[id]
	if !ok {
		return Job{}, ErrNotFound
	}
	return job, nil
}

func (r *fakeJobsRepo) MarkRunning(_ context.Context, id, tenantID string) (Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.markRunningCalls++
	job, ok := r.jobs[id]
	if !ok || job.TenantID != tenantID {
		return Job{}, ErrNotFound
	}
	job.Status = "running"
	r.jobs[id] = job
	return job, nil
}

func (r *fakeJobsRepo) MarkCompleted(_ context.Context, id, tenantID string, finalAssetIDs []string) (Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failNextMarkCompleted {
		r.failNextMarkCompleted = false
		return Job{}, errors.New("forced mark-completed failure")
	}
	job, ok := r.jobs[id]
	if !ok || job.TenantID != tenantID {
		return Job{}, ErrNotFound
	}
	job.Status = "completed"
	job.FinalAssetIds = finalAssetIDs
	r.jobs[id] = job
	r.markCompleted = append(r.markCompleted, id)
	return job, nil
}

func (r *fakeJobsRepo) MarkFailed(_ context.Context, id, tenantID, errorCode, errorMessage string, retryable bool) (Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	job, ok := r.jobs[id]
	if !ok || job.TenantID != tenantID {
		return Job{}, ErrNotFound
	}
	job.Status = "failed"
	ec := errorCode
	em := errorMessage
	rb := retryable
	job.ErrorCode = &ec
	job.ErrorMessage = &em
	job.Retryable = &rb
	r.jobs[id] = job
	r.markFailed = append(r.markFailed, id)
	return job, nil
}

func (r *fakeJobsRepo) InsertProviderAttempt(_ context.Context, params ProviderAttemptInsertParams) (ProviderAttempt, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	att := ProviderAttempt{
		ID:              params.ID,
		GenerationJobID: params.GenerationJobID,
		ProviderID:      params.ProviderID,
		AttemptNumber:   params.AttemptNumber,
		Status:          "started",
	}
	r.attempts = append(r.attempts, att)
	return att, nil
}

func (r *fakeJobsRepo) MarkProviderAttemptSucceeded(context.Context, string, int32) error {
	return nil
}
func (r *fakeJobsRepo) MarkProviderAttemptFailed(context.Context, string, string, string, int32) error {
	return nil
}

func (r *fakeJobsRepo) CountProviderAttempts(_ context.Context, jobID string) (int32, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := int32(0)
	for _, a := range r.attempts {
		if a.GenerationJobID == jobID {
			count++
		}
	}
	return count, nil
}

func (r *fakeJobsRepo) InsertCostEvent(_ context.Context, params CostEventInsertParams) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.costEvents = append(r.costEvents, params)
	return nil
}

func (r *fakeJobsRepo) UpdateAssetPackStatus(_ context.Context, packID, status string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.packStatuses[packID] = append(r.packStatuses[packID], status)
	return nil
}

func (r *fakeJobsRepo) UpdateAssetPackCompleteness(_ context.Context, packID string, delivered, missing []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.packDelivered[packID] = append([]string(nil), delivered...)
	r.packMissing[packID] = append([]string(nil), missing...)
	return nil
}

func (r *fakeJobsRepo) InsertAssetPackItem(_ context.Context, params AssetPackItemInsertParams) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.insertPackItemLocked(params)
}

func (r *fakeJobsRepo) insertPackItemLocked(params AssetPackItemInsertParams) error {
	for _, item := range r.packItems[params.AssetPackID] {
		if item.VariantKey == params.VariantKey {
			return errors.New("duplicate variant_key for pack")
		}
	}
	r.packItems[params.AssetPackID] = append(r.packItems[params.AssetPackID], AssetPackItem(params))
	return nil
}

// InsertPackItemWithAsset mirrors the production semantics: the asset and
// the item are recorded together or not at all.
func (r *fakeJobsRepo) InsertPackItemWithAsset(_ context.Context, asset assets.InsertParams, item AssetPackItemInsertParams) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n := r.failPackInsertFor[item.VariantKey]; n > 0 {
		r.failPackInsertFor[item.VariantKey] = n - 1
		return errors.New("forced pack insert failure (atomic rollback)")
	}
	if err := r.insertPackItemLocked(item); err != nil {
		return err
	}
	r.packAssets = append(r.packAssets, asset)
	return nil
}

// InsertPackItemWithAssetSuperseding models the forced pack-role write: archive
// the prior ready asset of the exact variant slot, version the new one, and
// record the asset + item together (atomic).
func (r *fakeJobsRepo) InsertPackItemWithAssetSuperseding(_ context.Context, asset assets.InsertParams, item AssetPackItemInsertParams, slot assets.VariantSlot) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n := r.failPackInsertFor[item.VariantKey]; n > 0 {
		r.failPackInsertFor[item.VariantKey] = n - 1
		return errors.New("forced pack insert failure (atomic rollback)")
	}
	r.supersedeVariantCall = append(r.supersedeVariantCall, supersedeVariantCall{asset: asset, item: item, slot: slot})
	maxVersion := int32(0)
	for i := range r.packTable {
		if variantSlotMatch(r.packTable[i], slot) && r.packTable[i].Version > maxVersion {
			maxVersion = r.packTable[i].Version
		}
	}
	newID := asset.ID
	for i := range r.packTable {
		if variantSlotMatch(r.packTable[i], slot) && r.packTable[i].Status == "ready" {
			r.packTable[i].Status = "archived"
			r.packTable[i].SupersededByAssetID = &newID
		}
	}
	if err := r.insertPackItemLocked(item); err != nil {
		return err
	}
	stored := assetFromParams(asset, maxVersion+1)
	r.packTable = append(r.packTable, stored)
	r.packAssets = append(r.packAssets, asset)
	return nil
}

func variantSlotMatch(a assets.VisualAsset, slot assets.VariantSlot) bool {
	return a.TenantID == slot.TenantID &&
		a.WorldID == slot.WorldID &&
		ptrEq(a.VisualIdentityID, slot.VisualIdentityID) &&
		a.VariantKey == slot.VariantKey &&
		a.StateVersion == slot.StateVersion &&
		ptrEq(a.StyleProfileID, slot.StyleProfileID) &&
		a.QualityTier == slot.QualityTier
}

func (r *fakeJobsRepo) ListAssetPackItems(_ context.Context, packID string) ([]AssetPackItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]AssetPackItem(nil), r.packItems[packID]...), nil
}

func (r *fakeJobsRepo) lastPackStatus(packID string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	transitions := r.packStatuses[packID]
	if len(transitions) == 0 {
		return ""
	}
	return transitions[len(transitions)-1]
}

type fakeAssetsRepo struct {
	mu     sync.Mutex
	stored []assets.InsertParams

	// Phase 6A4 supersede modeling. table is an in-memory visual_assets so the
	// supersede path can faithfully archive prior ready rows and version the new
	// one; supersedeArtifactCalls records each SupersedeAndInsertArtifact call for
	// routing/slot assertions.
	table                 []assets.VisualAsset
	supersedeArtifactCall []supersedeArtifactCall
}

type supersedeArtifactCall struct {
	params assets.InsertParams
	slot   assets.ArtifactSlot
}

func (r *fakeAssetsRepo) GetByIDForTenant(context.Context, string, string) (assets.VisualAsset, error) {
	return assets.VisualAsset{}, assets.ErrNotFound
}

func (r *fakeAssetsRepo) FindExact(context.Context, assets.RetrievalQuery) (assets.VisualAsset, error) {
	return assets.VisualAsset{}, assets.ErrNotFound
}

func (r *fakeAssetsRepo) ListRetrievalCandidates(context.Context, assets.RetrievalQuery) ([]assets.VisualAsset, error) {
	return nil, nil
}

func (r *fakeAssetsRepo) ListRetrievalCandidatesByCompatTag(context.Context, assets.RetrievalQuery, []string) ([]assets.VisualAsset, error) {
	return nil, nil
}

func (r *fakeAssetsRepo) FindReadyArtifactByPromptHash(context.Context, assets.ArtifactLookup) (assets.VisualAsset, error) {
	return assets.VisualAsset{}, assets.ErrNotFound
}

func (r *fakeAssetsRepo) Insert(_ context.Context, params assets.InsertParams) (assets.VisualAsset, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stored = append(r.stored, params)
	asset := assetFromParams(params, 1)
	r.table = append(r.table, asset)
	return asset, nil
}

// SupersedeAndInsertArtifact models the production semantics in memory: under
// the (notional) slot lock, archive every prior ready row of the exact artifact
// slot, link it forward to the new asset, and insert the new asset ready with
// version = prior_max + 1.
func (r *fakeAssetsRepo) SupersedeAndInsertArtifact(_ context.Context, params assets.InsertParams, slot assets.ArtifactSlot) (assets.VisualAsset, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.supersedeArtifactCall = append(r.supersedeArtifactCall, supersedeArtifactCall{params: params, slot: slot})
	maxVersion := int32(0)
	for i := range r.table {
		if !artifactSlotMatch(r.table[i], slot) {
			continue
		}
		if r.table[i].Version > maxVersion {
			maxVersion = r.table[i].Version
		}
	}
	newID := params.ID
	for i := range r.table {
		if artifactSlotMatch(r.table[i], slot) && r.table[i].Status == "ready" {
			r.table[i].Status = "archived"
			r.table[i].SupersededByAssetID = &newID
		}
	}
	asset := assetFromParams(params, maxVersion+1)
	r.stored = append(r.stored, params)
	r.table = append(r.table, asset)
	return asset, nil
}

func assetFromParams(params assets.InsertParams, version int32) assets.VisualAsset {
	if params.Version != 0 {
		version = params.Version
	}
	return assets.VisualAsset{
		ID:               params.ID,
		TenantID:         params.TenantID,
		WorldID:          params.WorldID,
		VisualIdentityID: params.VisualIdentityID,
		AssetType:        params.AssetType,
		VariantKey:       params.VariantKey,
		Version:          version,
		StateVersion:     1, // visual_assets.state_version DEFAULT 1
		StyleProfileID:   params.StyleProfileID,
		QualityTier:      params.QualityTier,
		Status:           "ready",
		PromptHash:       params.PromptHash,
		LowResUrl:        params.LowResUrl,
		HighResUrl:       params.HighResUrl,
		ThumbnailUrl:     params.ThumbnailUrl,
	}
}

func ptrEq(a *string, b string) bool {
	return a != nil && *a == b
}

func artifactSlotMatch(a assets.VisualAsset, slot assets.ArtifactSlot) bool {
	return a.TenantID == slot.TenantID &&
		a.WorldID == slot.WorldID &&
		a.AssetType == "artifact" &&
		a.VariantKey == "default" &&
		ptrEq(a.StyleProfileID, slot.StyleProfileID) &&
		a.QualityTier == slot.QualityTier &&
		ptrEq(a.PromptHash, slot.PromptHash)
}

type fakeStorage struct {
	mu     sync.Mutex
	keys   []string
	failOn string
}

func (s *fakeStorage) Put(_ context.Context, key string, _ []byte, _ string) (string, error) {
	if s.failOn != "" && key == s.failOn {
		return "", errors.New("storage: forced failure")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = append(s.keys, key)
	return "s3://bucket/" + key, nil
}

func (s *fakeStorage) Presign(_ context.Context, key string, _ time.Duration) (string, error) {
	return "https://example.test/" + key + "?sig=test", nil
}

// tinyPNGBytes is a valid 8x8 PNG used by provider stubs so the worker's
// downscale (imaging.EncodeTiers) can decode the "provider output". Tiny
// sources are not upscaled, so the three tiers come out identical — pack/cost
// tests assert status and pack items, not tier dimensions.
func tinyPNGBytes() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x * 16), G: uint8(y * 16), B: 0x40, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

type errorProvider struct{}

func (errorProvider) Generate(context.Context, providers.ProviderGenerateRequest) (providers.ProviderGenerateResult, error) {
	return providers.ProviderGenerateResult{}, errors.New("provider unavailable")
}
func (errorProvider) PollStatus(context.Context, string) (providers.ProviderJobStatus, error) {
	return providers.ProviderJobStatus{}, providers.ErrNotApplicable
}
func (errorProvider) Upscale(context.Context, providers.ProviderUpscaleRequest) (providers.ProviderGenerateResult, error) {
	return providers.ProviderGenerateResult{}, providers.ErrNotImplemented
}
func (errorProvider) Capabilities() providers.ProviderCapabilities {
	return providers.ProviderCapabilities{ProviderID: "error", ModelName: "error-v1"}
}

func TestWorkerProcessHappyPath(t *testing.T) {
	jobsRepo := newFakeJobsRepo()
	assetsRepo := &fakeAssetsRepo{}
	storage := &fakeStorage{}

	worldID := "w1"
	tokenID := "tok_test"
	_, _ = jobsRepo.Insert(context.Background(), InsertParams{
		ID:                 "job_test1",
		TenantID:           "tenant_a",
		WorldID:            &worldID,
		JobType:            "artifact",
		RequestedByTokenID: &tokenID,
		InputPayload: map[string]any{
			"world_id":    "w1",
			"description": "bronze key",
		},
	})

	w := &Worker{
		Jobs:     jobsRepo,
		Assets:   assetsRepo,
		Storage:  storage,
		Provider: mock.New(),
	}
	if err := w.Process(context.Background(), "job_test1", 0); err != nil {
		t.Fatalf("Process: %v", err)
	}

	if jobsRepo.markRunningCalls != 1 {
		t.Fatalf("expected MarkRunning to be called once, got %d", jobsRepo.markRunningCalls)
	}
	if len(jobsRepo.markCompleted) != 1 || jobsRepo.markCompleted[0] != "job_test1" {
		t.Fatalf("expected job_test1 marked completed, got %+v", jobsRepo.markCompleted)
	}
	if len(jobsRepo.attempts) != 1 || jobsRepo.attempts[0].AttemptNumber != 1 {
		t.Fatalf("expected one attempt with number=1, got %+v", jobsRepo.attempts)
	}
	if len(assetsRepo.stored) != 1 {
		t.Fatalf("expected one asset stored, got %d", len(assetsRepo.stored))
	}
	asset := assetsRepo.stored[0]
	if asset.LowResUrl == nil || asset.HighResUrl == nil || asset.ThumbnailUrl == nil {
		t.Fatalf("expected three URLs populated, got %+v", asset)
	}
	if asset.ProviderID == nil || *asset.ProviderID != "mock" {
		t.Fatalf("expected provider_id=mock, got %v", asset.ProviderID)
	}
	if asset.ModelID == nil || *asset.ModelID != "pm_mock_v1" {
		t.Fatalf("expected model_id=pm_mock_v1, got %v", asset.ModelID)
	}
	if len(storage.keys) != 3 {
		t.Fatalf("expected three S3 keys, got %d", len(storage.keys))
	}
	if len(jobsRepo.costEvents) != 1 || jobsRepo.costEvents[0].Status != "completed" {
		t.Fatalf("expected one completed cost event, got %+v", jobsRepo.costEvents)
	}
}

func TestWorkerProcessPersistsRequestQualityTierAndRenderHash(t *testing.T) {
	jobsRepo := newFakeJobsRepo()
	assetsRepo := &fakeAssetsRepo{}
	storage := &fakeStorage{}

	worldID := "w1"
	tokenID := "tok_test"
	_, _ = jobsRepo.Insert(context.Background(), InsertParams{
		ID:                 "job_quality",
		TenantID:           "tenant_a",
		WorldID:            &worldID,
		JobType:            "artifact",
		RequestedByTokenID: &tokenID,
		InputPayload: map[string]any{
			"world_id":         "w1",
			"description":      "bronze key",
			"style_profile_id": "sty_ok",
			"quality_tier":     "high",
			"prompt_hash":      "render_hash_abc",
		},
	})

	w := &Worker{Jobs: jobsRepo, Assets: assetsRepo, Storage: storage, Provider: mock.New()}
	if err := w.Process(context.Background(), "job_quality", 0); err != nil {
		t.Fatalf("Process: %v", err)
	}

	if len(assetsRepo.stored) != 1 {
		t.Fatalf("expected one asset stored, got %d", len(assetsRepo.stored))
	}
	asset := assetsRepo.stored[0]
	// quality_tier comes from the request payload, not a hardcoded "standard".
	if asset.QualityTier != "high" {
		t.Fatalf("expected quality_tier=high from payload, got %q", asset.QualityTier)
	}
	// The primary prompt_hash is the deterministic artifact render hash.
	if asset.PromptHash == nil || *asset.PromptHash != "render_hash_abc" {
		t.Fatalf("expected prompt_hash=render_hash_abc (the render hash), got %v", asset.PromptHash)
	}
	// The provider's own hash is provenance in metadata, never the primary key.
	pph, ok := asset.Metadata["provider_prompt_hash"].(string)
	if !ok || pph == "" {
		t.Fatalf("expected metadata.provider_prompt_hash to carry the provider hash, got %v", asset.Metadata)
	}
	if pph == "render_hash_abc" {
		t.Fatalf("provider_prompt_hash must be the provider's hash, not the render hash")
	}
}

func TestWorkerProcessProviderErrorOnFinalAttemptMarksFailed(t *testing.T) {
	jobsRepo := newFakeJobsRepo()
	assetsRepo := &fakeAssetsRepo{}
	storage := &fakeStorage{}
	worldID := "w1"
	_, _ = jobsRepo.Insert(context.Background(), InsertParams{
		ID:           "job_test2",
		TenantID:     "tenant_a",
		WorldID:      &worldID,
		JobType:      "artifact",
		InputPayload: map[string]any{"world_id": "w1", "description": "fail"},
	})

	w := &Worker{
		Jobs:     jobsRepo,
		Assets:   assetsRepo,
		Storage:  storage,
		Provider: errorProvider{},
	}

	// Simulate MaxAttempts-1 (the last attempt → finalAttempt=true).
	if err := w.Process(context.Background(), "job_test2", int32(MaxAttempts-1)); err == nil {
		t.Fatalf("expected error from final attempt")
	}
	if len(jobsRepo.markFailed) != 1 {
		t.Fatalf("expected job marked failed on final attempt, got %+v", jobsRepo.markFailed)
	}
	job := jobsRepo.jobs["job_test2"]
	if job.Status != "failed" {
		t.Fatalf("expected status=failed, got %s", job.Status)
	}
	if job.Retryable == nil || *job.Retryable {
		t.Fatalf("expected retryable=false on final failure, got %v", job.Retryable)
	}
}

func TestWorkerProcessProviderErrorOnEarlyAttemptDoesNotMarkFailed(t *testing.T) {
	jobsRepo := newFakeJobsRepo()
	assetsRepo := &fakeAssetsRepo{}
	storage := &fakeStorage{}
	worldID := "w1"
	_, _ = jobsRepo.Insert(context.Background(), InsertParams{
		ID:           "job_test3",
		TenantID:     "tenant_a",
		WorldID:      &worldID,
		JobType:      "artifact",
		InputPayload: map[string]any{"world_id": "w1", "description": "fail"},
	})

	w := &Worker{
		Jobs:     jobsRepo,
		Assets:   assetsRepo,
		Storage:  storage,
		Provider: errorProvider{},
	}
	// Early attempt → finalAttempt=false; job stays for retry.
	if err := w.Process(context.Background(), "job_test3", 0); err == nil {
		t.Fatalf("expected error on early attempt")
	}
	if len(jobsRepo.markFailed) != 0 {
		t.Fatalf("expected job not marked failed on early attempt, got %+v", jobsRepo.markFailed)
	}
}

func TestWorkerProcessAttemptNumberMatchesRetryCount(t *testing.T) {
	jobsRepo := newFakeJobsRepo()
	assetsRepo := &fakeAssetsRepo{}
	storage := &fakeStorage{}
	worldID := "w1"
	_, _ = jobsRepo.Insert(context.Background(), InsertParams{
		ID:           "job_test4",
		TenantID:     "tenant_a",
		WorldID:      &worldID,
		JobType:      "artifact",
		InputPayload: map[string]any{"description": "x"},
	})
	w := &Worker{
		Jobs:     jobsRepo,
		Assets:   assetsRepo,
		Storage:  storage,
		Provider: mock.New(),
	}
	if err := w.Process(context.Background(), "job_test4", 1); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(jobsRepo.attempts) != 1 || jobsRepo.attempts[0].AttemptNumber != 2 {
		t.Fatalf("expected attempt_number=2 for retryCount=1, got %+v", jobsRepo.attempts)
	}
}

// TestWorkerProcessForceRegenerateSupersedesArtifactSlot (Phase 6A4): a forced
// artifact job routes the write through SupersedeAndInsertArtifact (not Insert),
// with the EXACT artifact slot; the prior ready asset of that slot is archived
// and linked forward, and the new asset's version is prior + 1.
func TestWorkerProcessForceRegenerateSupersedesArtifactSlot(t *testing.T) {
	jobsRepo := newFakeJobsRepo()
	assetsRepo := &fakeAssetsRepo{}
	storage := &fakeStorage{}

	world := "w1"
	style := "sty_ok"
	quality := "standard"
	hash := "render_hash_force"

	// Seed the prior ready artifact (version 1) for the slot.
	priorID := "asset_prior"
	_, _ = assetsRepo.Insert(context.Background(), assets.InsertParams{
		ID:             priorID,
		TenantID:       "tenant_a",
		WorldID:        world,
		AssetType:      "artifact",
		VariantKey:     "default",
		StyleProfileID: &style,
		QualityTier:    quality,
		PromptHash:     &hash,
	})

	tokenID := "tok_test"
	_, _ = jobsRepo.Insert(context.Background(), InsertParams{
		ID:                 "job_force",
		TenantID:           "tenant_a",
		WorldID:            &world,
		JobType:            "artifact",
		RequestedByTokenID: &tokenID,
		InputPayload: map[string]any{
			"world_id":         world,
			"description":      "bronze key",
			"style_profile_id": style,
			"quality_tier":     quality,
			"prompt_hash":      hash,
			"force_regenerate": true,
		},
	})

	w := &Worker{Jobs: jobsRepo, Assets: assetsRepo, Storage: storage, Provider: mock.New()}
	if err := w.Process(context.Background(), "job_force", 0); err != nil {
		t.Fatalf("Process: %v", err)
	}

	// Routed through supersede, not a plain insert.
	if len(assetsRepo.supersedeArtifactCall) != 1 {
		t.Fatalf("forced artifact must call SupersedeAndInsertArtifact exactly once, got %d", len(assetsRepo.supersedeArtifactCall))
	}
	call := assetsRepo.supersedeArtifactCall[0]
	wantSlot := assets.ArtifactSlot{TenantID: "tenant_a", WorldID: world, StyleProfileID: style, QualityTier: quality, PromptHash: hash}
	if call.slot != wantSlot {
		t.Fatalf("supersede slot mismatch: want %+v, got %+v", wantSlot, call.slot)
	}

	// Prior asset archived and linked forward; new asset is the single ready row,
	// version prior+1.
	var prior, fresh *assets.VisualAsset
	for i := range assetsRepo.table {
		switch assetsRepo.table[i].ID {
		case priorID:
			prior = &assetsRepo.table[i]
		default:
			fresh = &assetsRepo.table[i]
		}
	}
	if prior == nil || fresh == nil {
		t.Fatalf("expected both the prior and the regenerated asset in the table, got %+v", assetsRepo.table)
	}
	if prior.Status != "archived" {
		t.Fatalf("prior asset must be archived, got %q", prior.Status)
	}
	if prior.SupersededByAssetID == nil || *prior.SupersededByAssetID != fresh.ID {
		t.Fatalf("prior asset must link forward to the regenerated asset %q, got %v", fresh.ID, prior.SupersededByAssetID)
	}
	if fresh.Status != "ready" {
		t.Fatalf("regenerated asset must be ready, got %q", fresh.Status)
	}
	if fresh.Version != prior.Version+1 || fresh.Version != 2 {
		t.Fatalf("regenerated version must be prior+1 (=2), got prior=%d fresh=%d", prior.Version, fresh.Version)
	}
}
