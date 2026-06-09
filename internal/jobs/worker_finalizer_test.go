package jobs

import (
	"context"
	"sync"
	"testing"

	"github.com/zakkriel/drchat-image-platform/internal/providers/mock"
)

// fakeFinalizer records which terminal transition the worker invoked.
type fakeFinalizer struct {
	mu        sync.Mutex
	committed []string
	released  []string
}

func (f *fakeFinalizer) Commit(_ context.Context, jobID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.committed = append(f.committed, jobID)
	return nil
}

func (f *fakeFinalizer) Release(_ context.Context, jobID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.released = append(f.released, jobID)
	return nil
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
