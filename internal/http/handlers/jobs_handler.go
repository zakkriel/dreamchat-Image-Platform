package handlers

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/zakkriel/drchat-image-platform/internal/auth"
	"github.com/zakkriel/drchat-image-platform/internal/http/apigen"
	"github.com/zakkriel/drchat-image-platform/internal/httperr"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
)

type JobsHandler struct {
	Repo jobs.Repository
}

func NewJobsHandler(repo jobs.Repository) *JobsHandler {
	return &JobsHandler{Repo: repo}
}

func (h *JobsHandler) Get(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "missing principal")
		return
	}

	jobID := chi.URLParam(r, "job_id")
	if jobID == "" {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "job_id is required")
		return
	}

	job, err := h.Repo.GetByIDForTenant(r.Context(), jobID, principal.TenantID)
	if err != nil {
		if errors.Is(err, jobs.ErrNotFound) {
			httperr.Write(w, r, http.StatusNotFound, httperr.CodeNotFound, "job not found")
			return
		}
		httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "could not load job")
		return
	}

	writeJSON(w, http.StatusOK, toGenerationJobAPI(job))
}

func toGenerationJobAPI(job jobs.Job) apigen.GenerationJob {
	out := apigen.GenerationJob{
		Id:        job.ID,
		JobType:   job.JobType,
		Status:    apigen.GenerationJobStatus(job.Status),
		CreatedAt: job.CreatedAt,
		UpdatedAt: job.UpdatedAt,
	}
	if len(job.PreviewAssetIds) > 0 {
		ids := append([]string(nil), job.PreviewAssetIds...)
		out.PreviewAssetIds = &ids
	}
	if len(job.FinalAssetIds) > 0 {
		ids := append([]string(nil), job.FinalAssetIds...)
		out.FinalAssetIds = &ids
	}
	if job.ErrorCode != nil {
		ec := *job.ErrorCode
		out.ErrorCode = &ec
	}
	if job.ErrorMessage != nil {
		em := *job.ErrorMessage
		out.ErrorMessage = &em
	}
	if job.Retryable != nil {
		rb := *job.Retryable
		out.Retryable = &rb
	}
	return out
}
