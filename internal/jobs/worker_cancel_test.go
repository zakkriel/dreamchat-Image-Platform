package jobs

import (
	"context"
	"testing"

	"github.com/zakkriel/drchat-image-platform/internal/providers"
)

// setJobStatus flips a fake job's status, modeling an admin cancel that lands
// out of band (the unit tests don't run the adminjobs service).
func (r *fakeJobsRepo) setJobStatus(id, status string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	job := r.jobs[id]
	job.Status = status
	r.jobs[id] = job
}

func (r *fakeJobsRepo) status(id string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.jobs[id].Status
}

// cancelDuringGenerateProvider flips the job to cancelled the moment the
// provider is asked to generate — modeling a cancel that lands while provider
// work is in flight, before the worker persists output. It otherwise returns a
// valid image like the mock provider.
type cancelDuringGenerateProvider struct {
	repo  *fakeJobsRepo
	jobID string
}

func (p cancelDuringGenerateProvider) Generate(_ context.Context, _ providers.ProviderGenerateRequest) (providers.ProviderGenerateResult, error) {
	p.repo.setJobStatus(p.jobID, statusCancelled)
	return providers.ProviderGenerateResult{
		Images:     []providers.ProviderImage{{Bytes: tinyPNGBytes()}},
		PromptHash: "h",
		Seed:       "s",
	}, nil
}

func (cancelDuringGenerateProvider) PollStatus(context.Context, string) (providers.ProviderJobStatus, error) {
	return providers.ProviderJobStatus{}, providers.ErrNotImplemented
}
func (cancelDuringGenerateProvider) Upscale(context.Context, providers.ProviderUpscaleRequest) (providers.ProviderGenerateResult, error) {
	return providers.ProviderGenerateResult{}, providers.ErrNotImplemented
}
func (cancelDuringGenerateProvider) Capabilities() providers.ProviderCapabilities {
	return providers.ProviderCapabilities{ProviderID: "mock", ModelName: "mock-v1"}
}

func newCancelledFixtureJob(t *testing.T, id string) (*fakeJobsRepo, *fakeAssetsRepo) {
	t.Helper()
	jobsRepo := newFakeJobsRepo()
	assetsRepo := &fakeAssetsRepo{}
	jobsRepo.assets = assetsRepo
	worldID := "w1"
	tokenID := "tok_test"
	_, _ = jobsRepo.Insert(context.Background(), InsertParams{
		ID: id, TenantID: "tenant_a", WorldID: &worldID, JobType: "artifact",
		RequestedByTokenID: &tokenID,
		InputPayload:       map[string]any{"description": "x"},
	})
	return jobsRepo, assetsRepo
}

// A cancelled job is terminal: the worker calls no provider, uploads nothing,
// inserts no asset, commits no cost, and releases the reservation idempotently.
func TestWorkerCancelledJobShortCircuits(t *testing.T) {
	jobsRepo, assetsRepo := newCancelledFixtureJob(t, "job_cancelled")
	jobsRepo.setJobStatus("job_cancelled", statusCancelled)
	prov := &countingProvider{}
	storage := &fakeStorage{}
	fin := &fakeFinalizer{}
	w := &Worker{Jobs: jobsRepo, Assets: assetsRepo, Storage: storage, Providers: testRegistry(prov), Finalizer: fin}

	if err := w.Process(context.Background(), "job_cancelled", 0); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if prov.calls != 0 {
		t.Fatalf("cancelled job must not call the provider, called=%d", prov.calls)
	}
	if len(storage.keys) != 0 {
		t.Fatalf("cancelled job must not upload, keys=%d", len(storage.keys))
	}
	if len(assetsRepo.stored) != 0 || len(assetsRepo.previewStored) != 0 {
		t.Fatalf("cancelled job must not insert an asset")
	}
	if len(jobsRepo.markCompleted) != 0 {
		t.Fatalf("cancelled job must not be marked completed")
	}
	if len(fin.committed) != 0 {
		t.Fatalf("cancelled job must not commit cost, committed=%+v", fin.committed)
	}
	if len(fin.released) != 1 || fin.released[0] != "job_cancelled" {
		t.Fatalf("cancelled job must release the reservation idempotently, got %+v", fin.released)
	}
	if jobsRepo.status("job_cancelled") != statusCancelled {
		t.Fatalf("job must stay cancelled")
	}
}

// A cancel that lands after the provider returns but before the guarded final
// persist must leave no asset attached, keep the job cancelled, release the
// reservation, and never commit cost.
func TestWorkerCancelDuringProviderWorkSkipsPersist(t *testing.T) {
	jobsRepo, assetsRepo := newCancelledFixtureJob(t, "job_race")
	prov := cancelDuringGenerateProvider{repo: jobsRepo, jobID: "job_race"}
	storage := &fakeStorage{}
	fin := &fakeFinalizer{}
	w := &Worker{Jobs: jobsRepo, Assets: assetsRepo, Storage: storage, Providers: testRegistry(prov), Finalizer: fin}

	if err := w.Process(context.Background(), "job_race", 0); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(assetsRepo.stored) != 0 {
		t.Fatalf("no final asset must be recorded for a job cancelled before persist, got %d", len(assetsRepo.stored))
	}
	if len(jobsRepo.markCompleted) != 0 {
		t.Fatalf("a cancelled job must not be marked completed")
	}
	if jobsRepo.status("job_race") != statusCancelled {
		t.Fatalf("job must remain cancelled, got %s", jobsRepo.status("job_race"))
	}
	if len(fin.committed) != 0 {
		t.Fatalf("cost must not be committed for a cancelled job, got %+v", fin.committed)
	}
	if len(fin.released) != 1 {
		t.Fatalf("reservation must be released exactly once, got %+v", fin.released)
	}
}

// Preview-first: a cancel before the preview persist records no preview asset
// and leaves the job cancelled.
func TestWorkerCancelDuringPreviewSkipsPersist(t *testing.T) {
	jobsRepo := newFakeJobsRepo()
	assetsRepo := &fakeAssetsRepo{}
	jobsRepo.assets = assetsRepo
	worldID := "w1"
	tokenID := "tok_test"
	_, _ = jobsRepo.Insert(context.Background(), InsertParams{
		ID: "job_pf", TenantID: "tenant_a", WorldID: &worldID, JobType: "artifact",
		RequestedByTokenID: &tokenID,
		InputPayload: map[string]any{
			"description":        "x",
			"delivery_mode":      deliveryModePreviewFirst,
			"preview_capability": string(providers.PreviewCapabilityTrue),
		},
	})
	prov := cancelDuringGenerateProvider{repo: jobsRepo, jobID: "job_pf"}
	storage := &fakeStorage{}
	fin := &fakeFinalizer{}
	w := &Worker{Jobs: jobsRepo, Assets: assetsRepo, Storage: storage, Providers: testRegistry(prov), Finalizer: fin}

	if err := w.Process(context.Background(), "job_pf", 0); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(assetsRepo.previewStored) != 0 {
		t.Fatalf("no preview asset must be recorded for a job cancelled before preview persist, got %d", len(assetsRepo.previewStored))
	}
	if len(jobsRepo.markPreviewReady) != 0 {
		t.Fatalf("a cancelled preview-first job must not be marked preview_ready")
	}
	if jobsRepo.status("job_pf") != statusCancelled {
		t.Fatalf("job must remain cancelled, got %s", jobsRepo.status("job_pf"))
	}
	if len(fin.committed) != 0 {
		t.Fatalf("cost must not be committed, got %+v", fin.committed)
	}
}
