package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/zakkriel/drchat-image-platform/internal/auth"
	"github.com/zakkriel/drchat-image-platform/internal/config"
	"github.com/zakkriel/drchat-image-platform/internal/http/apigen"
	"github.com/zakkriel/drchat-image-platform/internal/httperr"
	"github.com/zakkriel/drchat-image-platform/internal/idempotency"
	"github.com/zakkriel/drchat-image-platform/internal/ids"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
	"github.com/zakkriel/drchat-image-platform/internal/styles"
)

// Enqueuer is the subset of the asynq client wrapper the handler needs.
type Enqueuer interface {
	EnqueueGenerateArtifact(ctx context.Context, jobID string) error
}

type ArtifactsHandler struct {
	Jobs     jobs.Repository
	Styles   styles.Repository
	Enqueuer Enqueuer
	Provider config.Provider
	NewID    func() string
}

func NewArtifactsHandler(jobsRepo jobs.Repository, stylesRepo styles.Repository, enqueuer Enqueuer, provider config.Provider) *ArtifactsHandler {
	return &ArtifactsHandler{
		Jobs:     jobsRepo,
		Styles:   stylesRepo,
		Enqueuer: enqueuer,
		Provider: provider,
		NewID:    ids.NewGenerationJobID,
	}
}

type artifactGenerateResponse struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
}

func (h *ArtifactsHandler) Generate(w http.ResponseWriter, r *http.Request) {
	// Bail out before any state changes for unsupported providers.
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

	var req apigen.GenerateArtifactRequest
	if !readJSONBody(w, r, &req) {
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

	jobID := idempotency.ReservedJobIDFromContext(r.Context())
	if jobID == "" {
		jobID = h.NewID()
	}

	fallback := string(apigen.CompatibleOnly)
	if req.FallbackPolicy != nil {
		fallback = string(*req.FallbackPolicy)
	}
	cacheResult := "generated_required"
	worldID := req.WorldId
	tokenID := principal.TokenID

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

	if _, err := h.Jobs.Insert(r.Context(), jobs.InsertParams{
		ID:                 jobID,
		TenantID:           principal.TenantID,
		WorldID:            &worldID,
		JobType:            "artifact",
		RequestedByTokenID: &tokenID,
		InputPayload:       payload,
		FallbackPolicy:     &fallback,
		CacheResult:        &cacheResult,
	}); err != nil {
		httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "could not create generation job")
		return
	}

	if err := h.Enqueuer.EnqueueGenerateArtifact(r.Context(), jobID); err != nil {
		httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "could not enqueue generation job")
		return
	}

	writeJSON(w, http.StatusAccepted, artifactGenerateResponse{JobID: jobID, Status: "queued"})
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
