package handlers

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/zakkriel/drchat-image-platform/internal/jobs"
)

// stubJobsRepo is a tiny in-memory implementation of jobs.Repository used
// by the GET /v1/jobs/{job_id} tests. Only the read path is exercised.
type stubJobsRepo struct {
	byID map[string]jobs.Job
}

func newStubJobsRepo() *stubJobsRepo {
	return &stubJobsRepo{byID: map[string]jobs.Job{}}
}

func (s *stubJobsRepo) Insert(context.Context, jobs.InsertParams) (jobs.Job, error) {
	return jobs.Job{}, nil
}
func (s *stubJobsRepo) GetByIDForTenant(_ context.Context, id, tenantID string) (jobs.Job, error) {
	job, ok := s.byID[id]
	if !ok || job.TenantID != tenantID {
		return jobs.Job{}, jobs.ErrNotFound
	}
	return job, nil
}
func (s *stubJobsRepo) GetByID(_ context.Context, id string) (jobs.Job, error) {
	job, ok := s.byID[id]
	if !ok {
		return jobs.Job{}, jobs.ErrNotFound
	}
	return job, nil
}
func (s *stubJobsRepo) MarkRunning(context.Context, string, string) (jobs.Job, error) {
	return jobs.Job{}, nil
}
func (s *stubJobsRepo) MarkCompleted(context.Context, string, string, []string) (jobs.Job, error) {
	return jobs.Job{}, nil
}
func (s *stubJobsRepo) MarkFailed(context.Context, string, string, string, string, bool) (jobs.Job, error) {
	return jobs.Job{}, nil
}
func (s *stubJobsRepo) InsertProviderAttempt(context.Context, jobs.ProviderAttemptInsertParams) (jobs.ProviderAttempt, error) {
	return jobs.ProviderAttempt{}, nil
}
func (s *stubJobsRepo) MarkProviderAttemptSucceeded(context.Context, string, int32) error { return nil }
func (s *stubJobsRepo) MarkProviderAttemptFailed(context.Context, string, string, string, int32) error {
	return nil
}
func (s *stubJobsRepo) CountProviderAttempts(context.Context, string) (int32, error) { return 0, nil }
func (s *stubJobsRepo) InsertCostEvent(context.Context, jobs.CostEventInsertParams) error {
	return nil
}
func (s *stubJobsRepo) UpdateAssetPackStatus(context.Context, string, string) error { return nil }
func (s *stubJobsRepo) InsertAssetPackItem(context.Context, jobs.AssetPackItemInsertParams) error {
	return nil
}
func (s *stubJobsRepo) ListAssetPackItems(context.Context, string) ([]jobs.AssetPackItem, error) {
	return nil, nil
}

func newJobsRouter(repo jobs.Repository) chi.Router {
	h := NewJobsHandler(repo)
	r := chi.NewRouter()
	r.Get("/v1/jobs/{job_id}", h.Get)
	return r
}

func TestJobsGetSameTenant(t *testing.T) {
	repo := newStubJobsRepo()
	now := time.Now()
	repo.byID["job_aaaa"] = jobs.Job{
		ID:            "job_aaaa",
		TenantID:      tenantA,
		JobType:       "artifact",
		Status:        "completed",
		FinalAssetIds: []string{"asset_1"},
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	rec := sendJSON(t, newJobsRouter(repo), http.MethodGet, "/v1/jobs/job_aaaa", tenantA, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := decode[map[string]any](t, rec)
	if body["id"] != "job_aaaa" {
		t.Fatalf("expected id=job_aaaa, got %v", body["id"])
	}
	if body["job_type"] != "artifact" {
		t.Fatalf("expected job_type=artifact, got %v", body["job_type"])
	}
	if body["status"] != "completed" {
		t.Fatalf("expected status=completed, got %v", body["status"])
	}
	finalIDs, _ := body["final_asset_ids"].([]any)
	if len(finalIDs) != 1 || finalIDs[0] != "asset_1" {
		t.Fatalf("expected final_asset_ids=[asset_1], got %v", body["final_asset_ids"])
	}
}

func TestJobsGetCrossTenantReturns404(t *testing.T) {
	repo := newStubJobsRepo()
	now := time.Now()
	repo.byID["job_aaaa"] = jobs.Job{
		ID:        "job_aaaa",
		TenantID:  tenantA,
		JobType:   "artifact",
		Status:    "queued",
		CreatedAt: now,
		UpdatedAt: now,
	}
	rec := sendJSON(t, newJobsRouter(repo), http.MethodGet, "/v1/jobs/job_aaaa", tenantB, nil)
	assertError(t, rec, http.StatusNotFound, "not_found")
}

func TestJobsGetUnknownReturns404(t *testing.T) {
	rec := sendJSON(t, newJobsRouter(newStubJobsRepo()), http.MethodGet, "/v1/jobs/job_ghost", tenantA, nil)
	assertError(t, rec, http.StatusNotFound, "not_found")
}
