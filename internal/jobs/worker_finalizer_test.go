package jobs

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/zakkriel/drchat-image-platform/internal/providers"
	"github.com/zakkriel/drchat-image-platform/internal/providers/mock"
)

// fakeFinalizer records which terminal transition the worker invoked.
// failNextCommit makes the next Commit fail once (then succeed), to model a
// transient finalization failure after the job is already completed.
type fakeFinalizer struct {
	mu             sync.Mutex
	committed      []string
	released       []string
	failNextCommit bool
}

func (f *fakeFinalizer) Commit(_ context.Context, jobID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNextCommit {
		f.failNextCommit = false
		return errors.New("commit failed")
	}
	f.committed = append(f.committed, jobID)
	return nil
}

func (f *fakeFinalizer) Release(_ context.Context, jobID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.released = append(f.released, jobID)
	return nil
}

// countingProvider records how many times Generate is called and returns a
// single deterministic image so the worker's upload + asset insert succeed.
type countingProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *countingProvider) Generate(context.Context, providers.ProviderGenerateRequest) (providers.ProviderGenerateResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return providers.ProviderGenerateResult{
		Images:     []providers.ProviderImage{{Bytes: tinyPNGBytes(), ContentType: "image/png"}},
		PromptHash: "hash",
		Seed:       "seed",
	}, nil
}
func (p *countingProvider) PollStatus(context.Context, string) (providers.ProviderJobStatus, error) {
	return providers.ProviderJobStatus{}, providers.ErrNotApplicable
}
func (p *countingProvider) Upscale(context.Context, providers.ProviderUpscaleRequest) (providers.ProviderGenerateResult, error) {
	return providers.ProviderGenerateResult{}, providers.ErrNotImplemented
}
func (p *countingProvider) Capabilities() providers.ProviderCapabilities {
	return providers.ProviderCapabilities{ProviderID: "mock", ModelName: "mock-v1"}
}

func (p *countingProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// seedJob inserts a job and forces it to a given terminal status.
func seedJob(repo *fakeJobsRepo, id, status string) {
	worldID := "w1"
	_, _ = repo.Insert(context.Background(), InsertParams{
		ID: id, TenantID: "tenant_a", WorldID: &worldID, JobType: "artifact",
		InputPayload: map[string]any{"description": "x"},
	})
	repo.mu.Lock()
	job := repo.jobs[id]
	job.Status = status
	repo.jobs[id] = job
	repo.mu.Unlock()
}

func TestWorkerCompletedJobOnlyFinalizesNoRegeneration(t *testing.T) {
	jobsRepo := newFakeJobsRepo()
	seedJob(jobsRepo, "job_done", "completed")
	assetsRepo := &fakeAssetsRepo{}
	provider := &countingProvider{}
	fin := &fakeFinalizer{}
	w := &Worker{Jobs: jobsRepo, Assets: assetsRepo, Storage: &fakeStorage{}, Provider: provider, Finalizer: fin}

	if err := w.Process(context.Background(), "job_done", 0); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if provider.callCount() != 0 {
		t.Fatalf("expected provider not called for completed job, got %d", provider.callCount())
	}
	if len(assetsRepo.stored) != 0 {
		t.Fatalf("expected no asset insert for completed job, got %d", len(assetsRepo.stored))
	}
	if len(fin.committed) != 1 || fin.committed[0] != "job_done" {
		t.Fatalf("expected Commit for completed job, got %+v", fin.committed)
	}
}

func TestWorkerFailedJobOnlyReleasesNoRegeneration(t *testing.T) {
	jobsRepo := newFakeJobsRepo()
	seedJob(jobsRepo, "job_gone", "failed")
	assetsRepo := &fakeAssetsRepo{}
	provider := &countingProvider{}
	fin := &fakeFinalizer{}
	w := &Worker{Jobs: jobsRepo, Assets: assetsRepo, Storage: &fakeStorage{}, Provider: provider, Finalizer: fin}

	if err := w.Process(context.Background(), "job_gone", 0); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if provider.callCount() != 0 {
		t.Fatalf("expected provider not called for failed job, got %d", provider.callCount())
	}
	if len(assetsRepo.stored) != 0 {
		t.Fatalf("expected no asset insert for failed job, got %d", len(assetsRepo.stored))
	}
	if len(fin.released) != 1 || fin.released[0] != "job_gone" {
		t.Fatalf("expected Release for failed job, got %+v", fin.released)
	}
}

func TestWorkerRetryAfterCommitFailureDoesNotRegenerate(t *testing.T) {
	jobsRepo := newFakeJobsRepo()
	worldID := "w1"
	_, _ = jobsRepo.Insert(context.Background(), InsertParams{
		ID: "job_retry", TenantID: "tenant_a", WorldID: &worldID, JobType: "artifact",
		InputPayload: map[string]any{"description": "x"},
	})
	assetsRepo := &fakeAssetsRepo{}
	provider := &countingProvider{}
	fin := &fakeFinalizer{failNextCommit: true}
	w := &Worker{Jobs: jobsRepo, Assets: assetsRepo, Storage: &fakeStorage{}, Provider: provider, Finalizer: fin}

	// First attempt: generation succeeds, job marked completed, then Commit
	// fails → Process returns an error (asynq will retry).
	if err := w.Process(context.Background(), "job_retry", 0); err == nil {
		t.Fatalf("expected error when commit fails after completion")
	}
	if provider.callCount() != 1 || len(assetsRepo.stored) != 1 {
		t.Fatalf("first attempt should generate exactly once: calls=%d assets=%d", provider.callCount(), len(assetsRepo.stored))
	}

	// Retry: the job is now completed; the worker must only re-run the
	// (now-succeeding) finalization, never the provider or asset insert.
	if err := w.Process(context.Background(), "job_retry", 1); err != nil {
		t.Fatalf("retry Process: %v", err)
	}
	if provider.callCount() != 1 {
		t.Fatalf("retry must not call provider again, got %d calls", provider.callCount())
	}
	if len(assetsRepo.stored) != 1 {
		t.Fatalf("retry must not insert a second asset, got %d", len(assetsRepo.stored))
	}
	if len(fin.committed) != 1 || fin.committed[0] != "job_retry" {
		t.Fatalf("expected exactly one successful Commit on retry, got %+v", fin.committed)
	}
}

func TestWorkerCommitsReservationOnSuccess(t *testing.T) {
	jobsRepo := newFakeJobsRepo()
	worldID := "w1"
	_, _ = jobsRepo.Insert(context.Background(), InsertParams{
		ID: "job_fin1", TenantID: "tenant_a", WorldID: &worldID, JobType: "artifact",
		InputPayload: map[string]any{"description": "x"},
	})
	fin := &fakeFinalizer{}
	w := &Worker{
		Jobs: jobsRepo, Assets: &fakeAssetsRepo{}, Storage: &fakeStorage{},
		Provider: mock.New(), Finalizer: fin,
	}
	if err := w.Process(context.Background(), "job_fin1", 0); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(fin.committed) != 1 || fin.committed[0] != "job_fin1" {
		t.Fatalf("expected commit for job_fin1, got %+v", fin.committed)
	}
	if len(fin.released) != 0 {
		t.Fatalf("expected no release on success, got %+v", fin.released)
	}
}

func TestWorkerReleasesReservationOnTerminalFailure(t *testing.T) {
	jobsRepo := newFakeJobsRepo()
	worldID := "w1"
	_, _ = jobsRepo.Insert(context.Background(), InsertParams{
		ID: "job_fin2", TenantID: "tenant_a", WorldID: &worldID, JobType: "artifact",
		InputPayload: map[string]any{"description": "x"},
	})
	fin := &fakeFinalizer{}
	w := &Worker{
		Jobs: jobsRepo, Assets: &fakeAssetsRepo{}, Storage: &fakeStorage{},
		Provider: errorProvider{}, Finalizer: fin,
	}
	// Final attempt → terminal failure → release.
	if err := w.Process(context.Background(), "job_fin2", int32(MaxAttempts-1)); err == nil {
		t.Fatalf("expected error on final attempt")
	}
	if len(fin.released) != 1 || fin.released[0] != "job_fin2" {
		t.Fatalf("expected release for job_fin2, got %+v", fin.released)
	}
	if len(fin.committed) != 0 {
		t.Fatalf("expected no commit on failure, got %+v", fin.committed)
	}
}

func TestWorkerDoesNotReleaseOnEarlyFailure(t *testing.T) {
	jobsRepo := newFakeJobsRepo()
	worldID := "w1"
	_, _ = jobsRepo.Insert(context.Background(), InsertParams{
		ID: "job_fin3", TenantID: "tenant_a", WorldID: &worldID, JobType: "artifact",
		InputPayload: map[string]any{"description": "x"},
	})
	fin := &fakeFinalizer{}
	w := &Worker{
		Jobs: jobsRepo, Assets: &fakeAssetsRepo{}, Storage: &fakeStorage{},
		Provider: errorProvider{}, Finalizer: fin,
	}
	// Early attempt → not terminal → no release (job stays for retry).
	if err := w.Process(context.Background(), "job_fin3", 0); err == nil {
		t.Fatalf("expected error on early attempt")
	}
	if len(fin.released) != 0 || len(fin.committed) != 0 {
		t.Fatalf("expected no finalize on early failure, got committed=%+v released=%+v", fin.committed, fin.released)
	}
}
