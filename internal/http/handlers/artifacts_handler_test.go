package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/zakkriel/drchat-image-platform/internal/config"
	"github.com/zakkriel/drchat-image-platform/internal/idempotency"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
	"github.com/zakkriel/drchat-image-platform/internal/styles"
)

var jobIDRe = regexp.MustCompile(`^job_[0-9a-f]{16}$`)

type stubJobsRepo struct {
	mu       sync.Mutex
	inserts  []jobs.InsertParams
	byID     map[string]jobs.Job
	insertEr error
	getErr   error
}

func newStubJobsRepo() *stubJobsRepo {
	return &stubJobsRepo{byID: map[string]jobs.Job{}}
}

func (s *stubJobsRepo) Insert(_ context.Context, params jobs.InsertParams) (jobs.Job, error) {
	if s.insertEr != nil {
		return jobs.Job{}, s.insertEr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inserts = append(s.inserts, params)
	job := jobs.Job{
		ID:           params.ID,
		TenantID:     params.TenantID,
		WorldID:      params.WorldID,
		JobType:      params.JobType,
		Status:       "queued",
		InputPayload: params.InputPayload,
	}
	s.byID[params.ID] = job
	return job, nil
}

func (s *stubJobsRepo) GetByIDForTenant(_ context.Context, id, tenantID string) (jobs.Job, error) {
	if s.getErr != nil {
		return jobs.Job{}, s.getErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.byID[id]
	if !ok || job.TenantID != tenantID {
		return jobs.Job{}, jobs.ErrNotFound
	}
	return job, nil
}

func (s *stubJobsRepo) GetByID(_ context.Context, id string) (jobs.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.byID[id]
	if !ok {
		return jobs.Job{}, jobs.ErrNotFound
	}
	return job, nil
}

func (s *stubJobsRepo) MarkRunning(_ context.Context, id, tenantID string) (jobs.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.byID[id]
	if !ok || job.TenantID != tenantID {
		return jobs.Job{}, jobs.ErrNotFound
	}
	job.Status = "running"
	s.byID[id] = job
	return job, nil
}

func (s *stubJobsRepo) MarkCompleted(_ context.Context, id, tenantID string, finalAssetIDs []string) (jobs.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.byID[id]
	if !ok || job.TenantID != tenantID {
		return jobs.Job{}, jobs.ErrNotFound
	}
	job.Status = "completed"
	job.FinalAssetIds = finalAssetIDs
	s.byID[id] = job
	return job, nil
}

func (s *stubJobsRepo) MarkFailed(_ context.Context, id, tenantID, errorCode, errorMessage string, retryable bool) (jobs.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.byID[id]
	if !ok || job.TenantID != tenantID {
		return jobs.Job{}, jobs.ErrNotFound
	}
	job.Status = "failed"
	ec := errorCode
	em := errorMessage
	rb := retryable
	job.ErrorCode = &ec
	job.ErrorMessage = &em
	job.Retryable = &rb
	s.byID[id] = job
	return job, nil
}

func (s *stubJobsRepo) InsertProviderAttempt(_ context.Context, params jobs.ProviderAttemptInsertParams) (jobs.ProviderAttempt, error) {
	return jobs.ProviderAttempt{
		ID:              params.ID,
		GenerationJobID: params.GenerationJobID,
		ProviderID:      params.ProviderID,
		AttemptNumber:   params.AttemptNumber,
		Status:          "started",
	}, nil
}

func (s *stubJobsRepo) MarkProviderAttemptSucceeded(context.Context, string, int32) error {
	return nil
}
func (s *stubJobsRepo) MarkProviderAttemptFailed(context.Context, string, string, string, int32) error {
	return nil
}
func (s *stubJobsRepo) CountProviderAttempts(context.Context, string) (int32, error) {
	return 1, nil
}
func (s *stubJobsRepo) InsertCostEvent(context.Context, jobs.CostEventInsertParams) error {
	return nil
}

type stubEnqueuer struct {
	mu     sync.Mutex
	jobIDs []string
	enqErr error
}

func (s *stubEnqueuer) EnqueueGenerateArtifact(_ context.Context, jobID string) error {
	if s.enqErr != nil {
		return s.enqErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobIDs = append(s.jobIDs, jobID)
	return nil
}

type stubIdempRepo struct {
	mu      sync.Mutex
	records map[string]idempotency.Record
}

func newStubIdempRepo() *stubIdempRepo {
	return &stubIdempRepo{records: map[string]idempotency.Record{}}
}

func idempKey(tokenID, key string) string { return tokenID + "|" + key }

func (s *stubIdempRepo) Get(_ context.Context, tokenID, key string) (idempotency.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[idempKey(tokenID, key)]
	if !ok {
		return idempotency.Record{}, idempotency.ErrNotFound
	}
	return rec, nil
}

func (s *stubIdempRepo) Insert(_ context.Context, rec idempotency.Record) (idempotency.Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := idempKey(rec.TokenID, rec.Key)
	if existing, ok := s.records[k]; ok {
		return existing, false, nil
	}
	s.records[k] = rec
	return rec, true, nil
}

func newArtifactsRouter(jobsRepo jobs.Repository, stylesRepo styles.Repository, enq Enqueuer, idemRepo idempotency.Repository, provider config.Provider) chi.Router {
	h := NewArtifactsHandler(jobsRepo, stylesRepo, enq, provider)
	r := chi.NewRouter()
	idemMW := idempotency.Middleware(idempotency.Deps{Repo: idemRepo})
	r.With(idemMW).Post("/v1/artifacts/{artifact_id}/generate", h.Generate)
	return r
}

func sendJSONWithHeaders(t *testing.T, h http.Handler, method, path, tenant string, scopes []string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var buf []byte
	if body != nil {
		if raw, ok := body.(json.RawMessage); ok {
			buf = raw
		} else {
			var err error
			buf, err = json.Marshal(body)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(buf)).WithContext(authedContext(tenant, scopes...))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestArtifactGenerateHappyPath(t *testing.T) {
	jobsRepo := newStubJobsRepo()
	stylesRepo := newStubStylesRepo()
	stylesRepo.seed(styles.StyleProfile{ID: "sty_ok", TenantID: tenantA, Status: "active"})
	enq := &stubEnqueuer{}
	idemRepo := newStubIdempRepo()

	router := newArtifactsRouter(jobsRepo, stylesRepo, enq, idemRepo, config.ProviderMock)
	body := map[string]any{
		"world_id":         "w1",
		"style_profile_id": "sty_ok",
		"description":      "A bronze key",
	}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_bronze_key/generate",
		tenantA, []string{"images:write"}, body, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	resp := decode[map[string]any](t, rec)
	jobID, _ := resp["job_id"].(string)
	if !jobIDRe.MatchString(jobID) {
		t.Fatalf("expected job_<16 hex>, got %q", jobID)
	}
	if resp["status"] != "queued" {
		t.Fatalf("expected status=queued, got %v", resp["status"])
	}
	if len(jobsRepo.inserts) != 1 {
		t.Fatalf("expected exactly one job insert, got %d", len(jobsRepo.inserts))
	}
	if len(enq.jobIDs) != 1 || enq.jobIDs[0] != jobID {
		t.Fatalf("expected exactly one enqueue for jobID %s, got %+v", jobID, enq.jobIDs)
	}
	if jobsRepo.inserts[0].TenantID != tenantA {
		t.Fatalf("expected tenant_a, got %s", jobsRepo.inserts[0].TenantID)
	}
	if fp := jobsRepo.inserts[0].FallbackPolicy; fp == nil || *fp != "compatible_only" {
		t.Fatalf("expected fallback_policy=compatible_only, got %v", fp)
	}
	if cr := jobsRepo.inserts[0].CacheResult; cr == nil || *cr != "generated_required" {
		t.Fatalf("expected cache_result=generated_required, got %v", cr)
	}
}

func TestArtifactGenerateMissingWorldIDReturns400(t *testing.T) {
	router := newArtifactsRouter(newStubJobsRepo(), seededStyles(), &stubEnqueuer{}, newStubIdempRepo(), config.ProviderMock)
	body := map[string]any{"style_profile_id": "sty_ok", "description": "x"}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_1/generate", tenantA, []string{"images:write"}, body, nil)
	assertError(t, rec, http.StatusBadRequest, "invalid_request")
}

func TestArtifactGenerateMissingStyleReturns400(t *testing.T) {
	router := newArtifactsRouter(newStubJobsRepo(), seededStyles(), &stubEnqueuer{}, newStubIdempRepo(), config.ProviderMock)
	body := map[string]any{"world_id": "w1", "description": "x"}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_1/generate", tenantA, []string{"images:write"}, body, nil)
	assertError(t, rec, http.StatusBadRequest, "invalid_request")
}

func TestArtifactGenerateMissingDescriptionReturns400(t *testing.T) {
	router := newArtifactsRouter(newStubJobsRepo(), seededStyles(), &stubEnqueuer{}, newStubIdempRepo(), config.ProviderMock)
	body := map[string]any{"world_id": "w1", "style_profile_id": "sty_ok"}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_1/generate", tenantA, []string{"images:write"}, body, nil)
	assertError(t, rec, http.StatusBadRequest, "invalid_request")
}

func TestArtifactGenerateBodyTenantIDReturns400(t *testing.T) {
	router := newArtifactsRouter(newStubJobsRepo(), seededStyles(), &stubEnqueuer{}, newStubIdempRepo(), config.ProviderMock)
	body := map[string]any{"tenant_id": "tenant_other", "world_id": "w1", "style_profile_id": "sty_ok", "description": "x"}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_1/generate", tenantA, []string{"images:write"}, body, nil)
	assertError(t, rec, http.StatusBadRequest, "invalid_request")
}

func TestArtifactGenerateUnknownStyleReturns422(t *testing.T) {
	jobsRepo := newStubJobsRepo()
	stylesRepo := newStubStylesRepo() // empty
	router := newArtifactsRouter(jobsRepo, stylesRepo, &stubEnqueuer{}, newStubIdempRepo(), config.ProviderMock)
	body := map[string]any{"world_id": "w1", "style_profile_id": "sty_ghost", "description": "x"}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_1/generate", tenantA, []string{"images:write"}, body, nil)
	assertError(t, rec, http.StatusUnprocessableEntity, "invalid_style_profile")
}

func TestArtifactGenerateBFLProviderReturns503BeforeAnyWrites(t *testing.T) {
	jobsRepo := newStubJobsRepo()
	enq := &stubEnqueuer{}
	idem := newStubIdempRepo()
	router := newArtifactsRouter(jobsRepo, seededStyles(), enq, idem, config.ProviderBFL)
	body := map[string]any{"world_id": "w1", "style_profile_id": "sty_ok", "description": "x"}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/artifacts/art_1/generate", tenantA, []string{"images:write"},
		body, map[string]string{idempotency.HeaderKey: "phase3-bfl-1"})
	assertError(t, rec, http.StatusServiceUnavailable, "provider_unavailable")
	if len(jobsRepo.inserts) != 0 {
		t.Fatalf("expected zero job inserts when provider unavailable, got %d", len(jobsRepo.inserts))
	}
	if len(enq.jobIDs) != 0 {
		t.Fatalf("expected zero enqueues when provider unavailable, got %d", len(enq.jobIDs))
	}
	if len(idem.records) != 0 {
		t.Fatalf("expected no idempotency rows written, got %d", len(idem.records))
	}
}

func seededStyles() *stubStylesRepo {
	repo := newStubStylesRepo()
	repo.seed(styles.StyleProfile{ID: "sty_ok", TenantID: tenantA, Status: "active"})
	return repo
}
