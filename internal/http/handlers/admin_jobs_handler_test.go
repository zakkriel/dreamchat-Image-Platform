package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/zakkriel/drchat-image-platform/internal/adminjobs"
	"github.com/zakkriel/drchat-image-platform/internal/auth"
	"github.com/zakkriel/drchat-image-platform/internal/cost"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
)

// stubAdminJobsService returns canned results so the handler's scope gating and
// error→status mapping can be tested without a database.
type stubAdminJobsService struct {
	cancelJob   jobs.Job
	cancelErr   error
	cancelCalls int
	retryJob    jobs.Job
	retryErr    error
	retryCalls  int
}

func (s *stubAdminJobsService) CancelJob(_ context.Context, _, jobID string) (jobs.Job, error) {
	s.cancelCalls++
	if s.cancelErr != nil {
		return jobs.Job{}, s.cancelErr
	}
	j := s.cancelJob
	if j.ID == "" {
		j = jobs.Job{ID: jobID, JobType: "artifact", Status: "cancelled"}
	}
	return j, nil
}

func (s *stubAdminJobsService) RetryJob(_ context.Context, _, jobID string) (jobs.Job, error) {
	s.retryCalls++
	if s.retryErr != nil {
		return jobs.Job{}, s.retryErr
	}
	j := s.retryJob
	if j.ID == "" {
		j = jobs.Job{ID: jobID, JobType: "artifact", Status: "queued"}
	}
	return j, nil
}

func newAdminJobsRouter(svc AdminJobsService) chi.Router {
	h := NewAdminJobsHandler(svc)
	r := chi.NewRouter()
	r.Route("/v1/admin/jobs", func(a chi.Router) {
		a.Use(auth.RequireScopes("admin:jobs"))
		a.Post("/{job_id}/cancel", h.Cancel)
		a.Post("/{job_id}/retry", h.Retry)
	})
	return r
}

func sendAdminJob(t *testing.T, h http.Handler, path string, scopes []string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, nil).WithContext(authedContext(tenantA, scopes...))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func errCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v (raw=%s)", err, rec.Body.String())
	}
	return body.Code
}

func TestAdminJobCancelRequiresScope(t *testing.T) {
	svc := &stubAdminJobsService{}
	r := newAdminJobsRouter(svc)

	rec := sendAdminJob(t, r, "/v1/admin/jobs/job_1/cancel", []string{"images:write"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without admin:jobs, got %d", rec.Code)
	}
	if svc.cancelCalls != 0 {
		t.Fatalf("service must not be called without scope")
	}

	rec = sendAdminJob(t, r, "/v1/admin/jobs/job_1/cancel", []string{"admin:jobs"})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with admin:jobs, got %d body=%s", rec.Code, rec.Body.String())
	}
	if svc.cancelCalls != 1 {
		t.Fatalf("expected cancel called once, got %d", svc.cancelCalls)
	}
}

func TestAdminJobRetryRequiresScope(t *testing.T) {
	svc := &stubAdminJobsService{}
	r := newAdminJobsRouter(svc)

	rec := sendAdminJob(t, r, "/v1/admin/jobs/job_1/retry", []string{"admin:costs"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without admin:jobs, got %d", rec.Code)
	}
	rec = sendAdminJob(t, r, "/v1/admin/jobs/job_1/retry", []string{"admin:jobs"})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with admin:jobs, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminJobCancelErrorMapping(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode int
		wantBody string
	}{
		{"invalid_state", adminjobs.ErrInvalidState, http.StatusConflict, "invalid_state"},
		{"not_found", adminjobs.ErrNotFound, http.StatusNotFound, "not_found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &stubAdminJobsService{cancelErr: tc.err}
			r := newAdminJobsRouter(svc)
			rec := sendAdminJob(t, r, "/v1/admin/jobs/job_1/cancel", []string{"admin:jobs"})
			if rec.Code != tc.wantCode {
				t.Fatalf("expected %d, got %d body=%s", tc.wantCode, rec.Code, rec.Body.String())
			}
			if got := errCode(t, rec); got != tc.wantBody {
				t.Fatalf("expected code %q, got %q", tc.wantBody, got)
			}
		})
	}
}

func TestAdminJobRetryErrorMapping(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode int
		wantBody string
	}{
		{"invalid_state", adminjobs.ErrInvalidState, http.StatusConflict, "invalid_state"},
		{"not_found", adminjobs.ErrNotFound, http.StatusNotFound, "not_found"},
		{"no_price_entry", cost.ErrNoPriceEntry, http.StatusUnprocessableEntity, "no_price_entry"},
		{"budget_exceeded", cost.ErrBudgetExceeded, http.StatusUnprocessableEntity, "budget_exceeded"},
		{"enqueue_failed", jobs.ErrEnqueueFailed, http.StatusInternalServerError, "internal_error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &stubAdminJobsService{retryErr: tc.err}
			r := newAdminJobsRouter(svc)
			rec := sendAdminJob(t, r, "/v1/admin/jobs/job_1/retry", []string{"admin:jobs"})
			if rec.Code != tc.wantCode {
				t.Fatalf("expected %d, got %d body=%s", tc.wantCode, rec.Code, rec.Body.String())
			}
			if got := errCode(t, rec); got != tc.wantBody {
				t.Fatalf("expected code %q, got %q", tc.wantBody, got)
			}
		})
	}
}

func TestAdminJobCancelReturnsJob(t *testing.T) {
	svc := &stubAdminJobsService{cancelJob: jobs.Job{ID: "job_x", JobType: "artifact", Status: "cancelled"}}
	r := newAdminJobsRouter(svc)
	rec := sendAdminJob(t, r, "/v1/admin/jobs/job_x/cancel", []string{"admin:jobs"})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var body struct {
		Id     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Id != "job_x" || body.Status != "cancelled" {
		t.Fatalf("unexpected job body: %+v", body)
	}
}
