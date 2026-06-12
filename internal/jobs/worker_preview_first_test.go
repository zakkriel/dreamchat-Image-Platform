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

	"github.com/zakkriel/drchat-image-platform/internal/assets"
	"github.com/zakkriel/drchat-image-platform/internal/providers"
)

// previewFirstPayload is the input_payload a handler persists for a preview_first
// artifact job: the two-phase opt-in (delivery_mode) plus the resolved route's
// preview capability (preview_capability), alongside the normal artifact fields.
func previewFirstPayload() map[string]any {
	return map[string]any{
		"world_id":           "w1",
		"description":        "bronze key",
		"style_profile_id":   "sty_ok",
		"quality_tier":       "standard",
		"prompt_hash":        "render_hash_pf",
		"delivery_mode":      "preview_first",
		"preview_capability": "true_preview",
	}
}

func seedPreviewFirstJob(repo *fakeJobsRepo, id string, payload map[string]any) {
	worldID := "w1"
	tokenID := "tok_test"
	_, _ = repo.Insert(context.Background(), InsertParams{
		ID:                 id,
		TenantID:           "tenant_a",
		WorldID:            &worldID,
		JobType:            "artifact",
		RequestedByTokenID: &tokenID,
		InputPayload:       payload,
	})
}

// pngOfEdge renders a decodable edge×edge PNG whose bytes depend on the edge, so
// a 512px preview render and a 1024px final render are genuinely distinct bytes.
func pngOfEdge(edge int) []byte {
	if edge <= 0 {
		edge = 8
	}
	img := image.NewRGBA(image.Rect(0, 0, edge, edge))
	for y := 0; y < edge; y++ {
		for x := 0; x < edge; x++ {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: uint8((x + y) % 256), A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// previewFirstProvider records the dimensions of each Generate call (so a test
// can prove the preview render is lower-resolution than the final), can fail a
// chosen call (failOnCall, 1-based; 0 = never), and — on the final (2nd) call —
// snapshots the job's committed state so a test can prove preview_ready was
// committed BEFORE final generation began.
type previewFirstProvider struct {
	mu         sync.Mutex
	widths     []int
	calls      int
	failOnCall int

	// Ordering probe: populated at the start of the final call.
	repo                 *fakeJobsRepo
	jobID                string
	finalSawStatus       string
	finalSawPreviewCount int
}

func (p *previewFirstProvider) Generate(_ context.Context, req providers.ProviderGenerateRequest) (providers.ProviderGenerateResult, error) {
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.widths = append(p.widths, req.Width)
	p.mu.Unlock()

	// On the final call, observe what another process would see: the preview must
	// already be committed (job preview_ready + preview_asset_ids populated).
	if call == 2 && p.repo != nil {
		job, _ := p.repo.GetByID(context.Background(), p.jobID)
		p.mu.Lock()
		p.finalSawStatus = job.Status
		p.finalSawPreviewCount = len(job.PreviewAssetIds)
		p.mu.Unlock()
	}

	if p.failOnCall == call {
		return providers.ProviderGenerateResult{}, errors.New("provider failure")
	}
	return providers.ProviderGenerateResult{
		Images:     []providers.ProviderImage{{Bytes: pngOfEdge(req.Width), ContentType: "image/png", Width: req.Width, Height: req.Height}},
		PromptHash: "provider_hash_pf",
		Seed:       "seed_pf",
	}, nil
}

func (p *previewFirstProvider) PollStatus(context.Context, string) (providers.ProviderJobStatus, error) {
	return providers.ProviderJobStatus{}, providers.ErrNotApplicable
}
func (p *previewFirstProvider) Upscale(context.Context, providers.ProviderUpscaleRequest) (providers.ProviderGenerateResult, error) {
	return providers.ProviderGenerateResult{}, providers.ErrNotImplemented
}
func (p *previewFirstProvider) Capabilities() providers.ProviderCapabilities {
	return providers.ProviderCapabilities{ProviderID: "mock", ModelName: "mock-v1"}
}

func (p *previewFirstProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// TestPreviewFirstTwoPhaseLifecycle exercises the whole happy path: preview
// asset (status=preview_ready), job flip to preview_ready with preview_asset_ids
// committed before final generation, final asset (status=ready), job completed
// with final_asset_ids, both assets carrying resolved provenance, and the cost
// reservation committed exactly once.
func TestPreviewFirstTwoPhaseLifecycle(t *testing.T) {
	jobsRepo := newFakeJobsRepo()
	assetsRepo := &fakeAssetsRepo{}
	storage := &fakeStorage{}
	fin := &fakeFinalizer{}
	seedPreviewFirstJob(jobsRepo, "job_pf", previewFirstPayload())
	provider := &previewFirstProvider{repo: jobsRepo, jobID: "job_pf"}

	w := &Worker{Jobs: jobsRepo, Assets: assetsRepo, Storage: storage, Providers: testRegistry(provider), Finalizer: fin}
	if err := w.Process(context.Background(), "job_pf", 0); err != nil {
		t.Fatalf("Process: %v", err)
	}

	// 1. preview asset stored with status=preview_ready.
	if len(assetsRepo.previewStored) != 1 {
		t.Fatalf("expected exactly one preview asset, got %d", len(assetsRepo.previewStored))
	}
	preview := assetsRepo.previewStored[0]
	// preview tier carries the preview_safe compatibility tag.
	if len(preview.CompatibilityTags) != 1 || preview.CompatibilityTags[0] != "preview_safe" {
		t.Fatalf("preview asset must be tagged preview_safe, got %v", preview.CompatibilityTags)
	}

	// 4. final asset stored (status=ready via the normal Insert path).
	if len(assetsRepo.stored) != 1 {
		t.Fatalf("expected exactly one final asset, got %d", len(assetsRepo.stored))
	}
	final := assetsRepo.stored[0]

	// 7. distinct rows.
	if preview.ID == final.ID {
		t.Fatalf("preview and final must be distinct asset rows, both %q", preview.ID)
	}

	// preview render is lower-resolution than the final render.
	if len(provider.widths) != 2 {
		t.Fatalf("expected two provider calls (preview + final), got %d", provider.callCount())
	}
	if provider.widths[0] >= provider.widths[1] {
		t.Fatalf("preview render must request smaller dimensions than final: preview=%d final=%d", provider.widths[0], provider.widths[1])
	}

	// 3. preview committed BEFORE final generation began.
	if provider.finalSawStatus != "preview_ready" {
		t.Fatalf("final generation must start only after job committed preview_ready, saw %q", provider.finalSawStatus)
	}
	if provider.finalSawPreviewCount != 1 {
		t.Fatalf("preview_asset_ids must be committed before final generation, saw %d", provider.finalSawPreviewCount)
	}

	// 2 & 5. job flipped to preview_ready then completed with the right id arrays.
	if len(jobsRepo.markPreviewReady) != 1 {
		t.Fatalf("expected exactly one MarkPreviewReady, got %d", len(jobsRepo.markPreviewReady))
	}
	job := jobsRepo.jobs["job_pf"]
	if job.Status != "completed" {
		t.Fatalf("expected completed, got %q", job.Status)
	}
	if len(job.PreviewAssetIds) != 1 || job.PreviewAssetIds[0] != preview.ID {
		t.Fatalf("preview_asset_ids must point at the preview asset, got %v", job.PreviewAssetIds)
	}
	if len(job.FinalAssetIds) != 1 || job.FinalAssetIds[0] != final.ID {
		t.Fatalf("final_asset_ids must point at the final asset, got %v", job.FinalAssetIds)
	}

	// 6. both assets carry resolved provider/model/route provenance.
	assertProvenance(t, "preview", preview)
	assertProvenance(t, "final", final)

	// 7. cost committed exactly once, never released.
	if len(fin.committed) != 1 || fin.committed[0] != "job_pf" {
		t.Fatalf("cost must commit exactly once after final success, got %+v", fin.committed)
	}
	if len(fin.released) != 0 {
		t.Fatalf("cost must not be released on success, got %+v", fin.released)
	}
}

func assertProvenance(t *testing.T, tier string, p assets.InsertParams) {
	t.Helper()
	if p.ProviderID == nil || *p.ProviderID != "mock" {
		t.Fatalf("%s asset missing provider_id=mock, got %v", tier, p.ProviderID)
	}
	if p.ModelID == nil || *p.ModelID != "pm_mock_v1" {
		t.Fatalf("%s asset missing model_id=pm_mock_v1, got %v", tier, p.ModelID)
	}
	if p.ProviderRouteID == nil || *p.ProviderRouteID != "route_mock_text_to_image_standard" {
		t.Fatalf("%s asset missing provider_route_id, got %v", tier, p.ProviderRouteID)
	}
	if p.GenerationJobID == nil || *p.GenerationJobID == "" {
		t.Fatalf("%s asset missing generation_job_id", tier)
	}
}

// 8. preview-phase failure releases the reservation and creates no asset.
func TestPreviewFirstPreviewFailureReleasesAndCreatesNoAsset(t *testing.T) {
	jobsRepo := newFakeJobsRepo()
	assetsRepo := &fakeAssetsRepo{}
	fin := &fakeFinalizer{}
	seedPreviewFirstJob(jobsRepo, "job_pf_fail1", previewFirstPayload())
	provider := &previewFirstProvider{failOnCall: 1}

	w := &Worker{Jobs: jobsRepo, Assets: assetsRepo, Storage: &fakeStorage{}, Providers: testRegistry(provider), Finalizer: fin}
	// Final attempt → terminal failure path.
	if err := w.Process(context.Background(), "job_pf_fail1", int32(MaxAttempts-1)); err == nil {
		t.Fatalf("expected error from preview-phase provider failure")
	}
	if len(assetsRepo.previewStored) != 0 || len(assetsRepo.stored) != 0 {
		t.Fatalf("preview-phase failure must create no asset, got preview=%d final=%d", len(assetsRepo.previewStored), len(assetsRepo.stored))
	}
	if len(jobsRepo.markPreviewReady) != 0 {
		t.Fatalf("preview-phase failure must not mark preview_ready")
	}
	job := jobsRepo.jobs["job_pf_fail1"]
	if job.Status != "failed" {
		t.Fatalf("expected failed, got %q", job.Status)
	}
	if len(fin.released) != 1 {
		t.Fatalf("preview-phase terminal failure must release the reservation, got %+v", fin.released)
	}
	if len(fin.committed) != 0 {
		t.Fatalf("preview-phase failure must not commit cost, got %+v", fin.committed)
	}
}

// 9. final-phase failure after the preview was delivered releases the
// reservation, keeps the preview readable (preview_ready, preview_asset_ids
// intact), and leaves final_asset_ids empty.
func TestPreviewFirstFinalFailureKeepsPreviewReadable(t *testing.T) {
	jobsRepo := newFakeJobsRepo()
	assetsRepo := &fakeAssetsRepo{}
	fin := &fakeFinalizer{}
	seedPreviewFirstJob(jobsRepo, "job_pf_fail2", previewFirstPayload())
	provider := &previewFirstProvider{failOnCall: 2}

	w := &Worker{Jobs: jobsRepo, Assets: assetsRepo, Storage: &fakeStorage{}, Providers: testRegistry(provider), Finalizer: fin}
	if err := w.Process(context.Background(), "job_pf_fail2", int32(MaxAttempts-1)); err == nil {
		t.Fatalf("expected error from final-phase provider failure")
	}

	// Preview was delivered and remains readable.
	if len(assetsRepo.previewStored) != 1 {
		t.Fatalf("expected the preview asset to remain, got %d", len(assetsRepo.previewStored))
	}
	if len(assetsRepo.stored) != 0 {
		t.Fatalf("final-phase failure must create no final asset, got %d", len(assetsRepo.stored))
	}
	job := jobsRepo.jobs["job_pf_fail2"]
	if job.Status != "failed" {
		t.Fatalf("expected failed, got %q", job.Status)
	}
	if len(job.PreviewAssetIds) != 1 {
		t.Fatalf("preview_asset_ids must survive a final-phase failure, got %v", job.PreviewAssetIds)
	}
	if len(job.FinalAssetIds) != 0 {
		t.Fatalf("final_asset_ids must stay empty on final-phase failure, got %v", job.FinalAssetIds)
	}
	if len(fin.released) != 1 || len(fin.committed) != 0 {
		t.Fatalf("final-phase terminal failure must release (not commit): committed=%+v released=%+v", fin.committed, fin.released)
	}
}

// 10. retry of a preview_ready job resumes at final and never regenerates the
// preview or recharges.
func TestPreviewFirstRetryResumesFinalWithoutDuplicatingPreview(t *testing.T) {
	jobsRepo := newFakeJobsRepo()
	assetsRepo := &fakeAssetsRepo{}
	fin := &fakeFinalizer{}
	seedPreviewFirstJob(jobsRepo, "job_pf_resume", previewFirstPayload())
	// Simulate a prior attempt that already committed the preview: the job is
	// preview_ready with a preview_asset_id, exactly as MarkPreviewReady leaves it.
	jobsRepo.mu.Lock()
	job := jobsRepo.jobs["job_pf_resume"]
	job.Status = "preview_ready"
	job.PreviewAssetIds = []string{"asset_preview_prior"}
	jobsRepo.jobs["job_pf_resume"] = job
	jobsRepo.mu.Unlock()

	provider := &previewFirstProvider{}
	w := &Worker{Jobs: jobsRepo, Assets: assetsRepo, Storage: &fakeStorage{}, Providers: testRegistry(provider), Finalizer: fin}
	if err := w.Process(context.Background(), "job_pf_resume", 1); err != nil {
		t.Fatalf("retry Process: %v", err)
	}

	// Resume must NOT regenerate the preview.
	if len(assetsRepo.previewStored) != 0 {
		t.Fatalf("resume must not regenerate the preview, got %d preview asset(s)", len(assetsRepo.previewStored))
	}
	if len(jobsRepo.markPreviewReady) != 0 {
		t.Fatalf("resume must not re-flip preview_ready")
	}
	// Only the final provider call happens.
	if provider.callCount() != 1 {
		t.Fatalf("resume must call the provider once (final only), got %d", provider.callCount())
	}
	if provider.widths[0] != deliveryRenderEdge {
		t.Fatalf("the single resume call must be the final render (%dpx), got %d", deliveryRenderEdge, provider.widths[0])
	}
	// Final completes; preview_asset_ids preserved; cost commits once.
	finalJob := jobsRepo.jobs["job_pf_resume"]
	if finalJob.Status != "completed" {
		t.Fatalf("expected completed after resume, got %q", finalJob.Status)
	}
	if len(finalJob.PreviewAssetIds) != 1 || finalJob.PreviewAssetIds[0] != "asset_preview_prior" {
		t.Fatalf("resume must preserve the prior preview_asset_ids, got %v", finalJob.PreviewAssetIds)
	}
	if len(assetsRepo.stored) != 1 {
		t.Fatalf("resume must produce exactly one final asset, got %d", len(assetsRepo.stored))
	}
	if len(fin.committed) != 1 {
		t.Fatalf("resume must commit cost exactly once, got %+v", fin.committed)
	}
}

// 11. a job without delivery_mode=preview_first stays single-phase: no preview
// asset, no preview_ready transition.
func TestPreviewFirstSinglePhaseUnchangedWhenNotOptedIn(t *testing.T) {
	jobsRepo := newFakeJobsRepo()
	assetsRepo := &fakeAssetsRepo{}
	fin := &fakeFinalizer{}
	// Same payload but final_only (no delivery_mode / preview_capability).
	seedPreviewFirstJob(jobsRepo, "job_single", map[string]any{
		"world_id":         "w1",
		"description":      "bronze key",
		"style_profile_id": "sty_ok",
		"quality_tier":     "standard",
		"prompt_hash":      "render_hash_single",
	})
	provider := &previewFirstProvider{}
	w := &Worker{Jobs: jobsRepo, Assets: assetsRepo, Storage: &fakeStorage{}, Providers: testRegistry(provider), Finalizer: fin}
	if err := w.Process(context.Background(), "job_single", 0); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(assetsRepo.previewStored) != 0 {
		t.Fatalf("single-phase job must not create a preview asset, got %d", len(assetsRepo.previewStored))
	}
	if len(jobsRepo.markPreviewReady) != 0 {
		t.Fatalf("single-phase job must not enter preview_ready")
	}
	if provider.callCount() != 1 {
		t.Fatalf("single-phase job must call the provider once, got %d", provider.callCount())
	}
	if len(assetsRepo.stored) != 1 {
		t.Fatalf("single-phase job must store exactly one final asset, got %d", len(assetsRepo.stored))
	}
}
