package handlers

import (
	"net/http"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/zakkriel/drchat-image-platform/internal/jobs"
)

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
