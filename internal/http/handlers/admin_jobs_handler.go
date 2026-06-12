package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/zakkriel/drchat-image-platform/internal/adminjobs"
	"github.com/zakkriel/drchat-image-platform/internal/auth"
	"github.com/zakkriel/drchat-image-platform/internal/cost"
	"github.com/zakkriel/drchat-image-platform/internal/httperr"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
)

// AdminJobsService is the handler-facing slice of the Phase 7C-1 admin
// job-control surface. Tests stub this.
type AdminJobsService interface {
	CancelJob(ctx context.Context, tenantID, jobID string) (jobs.Job, error)
	RetryJob(ctx context.Context, tenantID, jobID string) (jobs.Job, error)
}

// AdminJobsHandler serves POST /v1/admin/jobs/{job_id}/cancel and
// /v1/admin/jobs/{job_id}/retry. Authorization (admin:jobs) is enforced by
// route middleware; tenant is taken from the authenticated principal — never
// from the path or body.
type AdminJobsHandler struct {
	Service AdminJobsService
}

func NewAdminJobsHandler(svc AdminJobsService) *AdminJobsHandler {
	return &AdminJobsHandler{Service: svc}
}

func (h *AdminJobsHandler) Cancel(w http.ResponseWriter, r *http.Request) {
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
	job, err := h.Service.CancelJob(r.Context(), principal.TenantID, jobID)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toGenerationJobAPI(job))
}

func (h *AdminJobsHandler) Retry(w http.ResponseWriter, r *http.Request) {
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
	job, err := h.Service.RetryJob(r.Context(), principal.TenantID, jobID)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toGenerationJobAPI(job))
}

func (h *AdminJobsHandler) writeErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, adminjobs.ErrNotFound):
		httperr.Write(w, r, http.StatusNotFound, httperr.CodeNotFound, "job not found")
	case errors.Is(err, adminjobs.ErrInvalidState):
		httperr.Write(w, r, http.StatusConflict, httperr.CodeInvalidState, "job is not in a valid state for this action")
	case errors.Is(err, cost.ErrNoPriceEntry):
		httperr.Write(w, r, http.StatusUnprocessableEntity, httperr.CodeNoPriceEntry, "no active price entry for the persisted provider/model/operation")
	case errors.Is(err, cost.ErrBudgetExceeded):
		httperr.Write(w, r, http.StatusUnprocessableEntity, httperr.CodeBudgetExceeded, "cost budget exceeded for this retry")
	case errors.Is(err, jobs.ErrEnqueueFailed):
		httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "job reopened but could not be enqueued")
	default:
		httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "admin job operation failed")
	}
}
