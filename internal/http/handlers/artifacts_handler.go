package handlers

import (
	"bytes"
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/zakkriel/drchat-image-platform/internal/auth"
	"github.com/zakkriel/drchat-image-platform/internal/config"
	"github.com/zakkriel/drchat-image-platform/internal/http/apigen"
	"github.com/zakkriel/drchat-image-platform/internal/httperr"
	"github.com/zakkriel/drchat-image-platform/internal/idempotency"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
	"github.com/zakkriel/drchat-image-platform/internal/styles"
)

// ArtifactsHandler accepts artifact generation requests and delegates the
// transactional job-create + idempotency-row + enqueue work to a
// jobs.Creator service. The handler itself is responsible only for
// authorization, validation, and shaping the 202/4xx/5xx response.
type ArtifactsHandler struct {
	Service  jobs.Creator
	Styles   styles.Repository
	Provider config.Provider
}

func NewArtifactsHandler(service jobs.Creator, stylesRepo styles.Repository, provider config.Provider) *ArtifactsHandler {
	return &ArtifactsHandler{
		Service:  service,
		Styles:   stylesRepo,
		Provider: provider,
	}
}

type artifactGenerateResponse struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
}

func (h *ArtifactsHandler) Generate(w http.ResponseWriter, r *http.Request) {
	// Provider gate runs first. Per the Phase 3 corrections, this must
	// reject before any idempotency row, job row, or queue task is created
	// or attempted.
	if h.Provider != config.ProviderMock {
		httperr.Write(w, r, http.StatusServiceUnavailable, httperr.CodeProviderUnavailable, "configured image provider is not available in this phase")
		return
	}

	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "missing principal")
		return
	}

	artifactID := chi.URLParam(r, "artifact_id")
	if artifactID == "" {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "artifact_id is required")
		return
	}

	raw, ok := readRawJSONBody(w, r)
	if !ok {
		return
	}

	var req apigen.GenerateArtifactRequest
	if !decodeFromRaw(w, r, raw, &req) {
		return
	}

	if req.WorldId == "" {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "world_id is required")
		return
	}
	if req.StyleProfileId == "" {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "style_profile_id is required")
		return
	}
	if req.Description == "" {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "description is required")
		return
	}
	if req.QualityTier != nil && !validQualityTier(*req.QualityTier) {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "quality_tier must be one of draft, standard, high")
		return
	}
	if req.LatencyTier != nil && !validLatencyTier(*req.LatencyTier) {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "latency_tier must be one of fast, balanced, quality")
		return
	}
	if req.FallbackPolicy != nil && !validFallbackPolicy(*req.FallbackPolicy) {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "fallback_policy must be one of none, compatible_only, preview_allowed, any_existing")
		return
	}

	if _, err := h.Styles.GetByIDForTenant(r.Context(), req.StyleProfileId, principal.TenantID); err != nil {
		if errors.Is(err, styles.ErrNotFound) {
			httperr.Write(w, r, http.StatusUnprocessableEntity, httperr.CodeInvalidStyleProfile, "style profile not found for tenant")
			return
		}
		httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "could not validate style profile")
		return
	}

	fallback := string(apigen.CompatibleOnly)
	if req.FallbackPolicy != nil {
		fallback = string(*req.FallbackPolicy)
	}
	cacheResult := "generated_required"

	payload := map[string]any{
		"artifact_id":      artifactID,
		"world_id":         req.WorldId,
		"style_profile_id": req.StyleProfileId,
		"description":      req.Description,
		"fallback_policy":  fallback,
	}
	if req.QualityTier != nil {
		payload["quality_tier"] = string(*req.QualityTier)
	}
	if req.LatencyTier != nil {
		payload["latency_tier"] = string(*req.LatencyTier)
	}

	params := jobs.CreateAndEnqueueParams{
		TenantID:           principal.TenantID,
		RequestedByTokenID: principal.TokenID,
		JobType:            "artifact",
		WorldID:            req.WorldId,
		InputPayload:       payload,
		FallbackPolicy:     fallback,
		CacheResult:        cacheResult,
	}
	if key := r.Header.Get(idempotency.HeaderKey); key != "" {
		params.IdempotencyKey = key
		params.Endpoint = r.Method + " " + r.URL.Path
		params.RequestHash = jobs.HashRequestBody(raw)
	}

	result, err := h.Service.CreateAndEnqueue(r.Context(), params)
	if err != nil {
		switch {
		case errors.Is(err, jobs.ErrIdempotencyConflict):
			httperr.Write(w, r, http.StatusConflict, httperr.CodeIdempotencyConflict, "idempotency key reused with a different body or endpoint")
		case errors.Is(err, jobs.ErrEnqueueFailed):
			httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "could not enqueue generation job")
		default:
			httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "could not create generation job")
		}
		return
	}

	writeJSON(w, http.StatusAccepted, artifactGenerateResponse{JobID: result.JobID, Status: "queued"})
}

// readRawJSONBody is the body-reading half of readJSONBody — the handler
// needs the raw bytes for the idempotency hash, so it can't use the
// existing helper as-is.
func readRawJSONBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBodyBytes))
	if err != nil {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "could not read request body")
		return nil, false
	}
	if err := rejectBodyTenantID(raw); err != nil {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "tenant_id must not be set in request body")
		return nil, false
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "request body required")
		return nil, false
	}
	return raw, true
}

func decodeFromRaw(w http.ResponseWriter, r *http.Request, raw []byte, v any) bool {
	dec := newJSONDecoder(raw)
	if err := dec.Decode(v); err != nil {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "could not decode request body")
		return false
	}
	return true
}

func validLatencyTier(l apigen.LatencyTier) bool {
	switch l {
	case apigen.Fast, apigen.Balanced, apigen.Quality:
		return true
	}
	return false
}

func validFallbackPolicy(fp apigen.FallbackPolicy) bool {
	switch fp {
	case apigen.None, apigen.CompatibleOnly, apigen.PreviewAllowed, apigen.AnyExisting:
		return true
	}
	return false
}
