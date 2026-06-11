package handlers

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/zakkriel/drchat-image-platform/internal/assets"
	"github.com/zakkriel/drchat-image-platform/internal/auth"
	"github.com/zakkriel/drchat-image-platform/internal/config"
	"github.com/zakkriel/drchat-image-platform/internal/http/apigen"
	"github.com/zakkriel/drchat-image-platform/internal/httperr"
	"github.com/zakkriel/drchat-image-platform/internal/idempotency"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
	"github.com/zakkriel/drchat-image-platform/internal/styles"
)

// StylePreviewHandler serves POST /v1/styles/{style_id}/preview (Phase 6B): it
// reserves + enqueues a single sample image so a creator can see a style before
// using it. The sample rides the normal artifact generate path — the worker
// produces an ordinary delivered visual_asset (asset_type=artifact) through the
// same storage + presigned-tier machinery — so the preview is read back via
// GET /v1/jobs/{job_id}/assets like any other delivery. The handler owns only
// authorization, validation, payload assembly, and the 202/4xx/5xx shaping.
type StylePreviewHandler struct {
	Service  jobs.Creator
	Styles   styles.Repository
	Provider config.Provider
}

func NewStylePreviewHandler(service jobs.Creator, stylesRepo styles.Repository, provider config.Provider) *StylePreviewHandler {
	return &StylePreviewHandler{Service: service, Styles: stylesRepo, Provider: provider}
}

// stylePreviewFallbackDescription is the provider prompt when a style carries
// no positive prompt to render from.
const stylePreviewFallbackDescription = "style preview sample"

// stylePreviewArtifactID namespaces the synthetic artifact id a style preview
// renders under, so a preview render hash never collides with a real artifact.
func stylePreviewArtifactID(styleID string) string {
	return "style_preview:" + styleID
}

func (h *StylePreviewHandler) GeneratePreview(w http.ResponseWriter, r *http.Request) {
	// Provider gate first, before any idempotency/job/queue work (mirrors the
	// artifact generate path): the only provider that renders end-to-end today
	// is the mock.
	if h.Provider != config.ProviderMock {
		httperr.Write(w, r, http.StatusServiceUnavailable, httperr.CodeProviderUnavailable, "configured image provider is not available in this phase")
		return
	}

	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "missing principal")
		return
	}

	styleID := chi.URLParam(r, "style_id")
	if styleID == "" {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "style_id is required")
		return
	}

	raw, ok := readRawJSONBody(w, r)
	if !ok {
		return
	}
	var req apigen.StylePreviewRequest
	if !decodeFromRaw(w, r, raw, &req) {
		return
	}

	// world_id is required because generated visual assets are world-scoped.
	if req.WorldId == "" {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "world_id is required")
		return
	}
	if req.QualityTier != nil && !validQualityTier(*req.QualityTier) {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "quality_tier must be one of draft, standard, high")
		return
	}

	style, err := h.Styles.GetByIDForTenant(r.Context(), styleID, principal.TenantID)
	if err != nil {
		if errors.Is(err, styles.ErrNotFound) {
			httperr.Write(w, r, http.StatusUnprocessableEntity, httperr.CodeInvalidStyleProfile, "style profile not found for tenant")
			return
		}
		httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "could not validate style profile")
		return
	}

	// Effective quality tier: explicit request wins, else the style's default,
	// else "standard".
	qualityTier := "standard"
	switch {
	case req.QualityTier != nil:
		qualityTier = string(*req.QualityTier)
	case style.DefaultQualityTier != "":
		qualityTier = style.DefaultQualityTier
	}

	// Render the style's own positive prompt so the sample actually reflects it.
	description := stylePreviewFallbackDescription
	if style.PositivePrompt != "" {
		description = style.PositivePrompt
	}

	renderHash := assets.ArtifactRenderHash(assets.ArtifactHashInput{
		TenantID:       principal.TenantID,
		WorldID:        req.WorldId,
		ArtifactID:     stylePreviewArtifactID(styleID),
		Description:    description,
		StyleProfileID: styleID,
		QualityTier:    qualityTier,
	})

	payload := map[string]any{
		"artifact_id":      stylePreviewArtifactID(styleID),
		"world_id":         req.WorldId,
		"style_profile_id": styleID,
		"description":      description,
		"fallback_policy":  "none",
		"quality_tier":     qualityTier,
		"prompt_hash":      renderHash,
		// Provenance marker so the produced asset is identifiable as a preview.
		"preview_kind": "style_preview",
	}

	params := jobs.CreateAndEnqueueParams{
		TenantID:           principal.TenantID,
		RequestedByTokenID: principal.TokenID,
		JobType:            "artifact",
		WorldID:            req.WorldId,
		InputPayload:       payload,
		FallbackPolicy:     "none",
		CacheResult:        "generated_required",
		ProviderID:         artifactProviderID,
		ModelID:            artifactModelID,
		OperationType:      artifactOperationType,
		Units:              artifactUnits,
	}
	if idemKey := r.Header.Get(idempotency.HeaderKey); idemKey != "" {
		params.IdempotencyKey = idemKey
		params.Endpoint = r.Method + " " + r.URL.Path
		params.RequestHash = jobs.HashRequestBody(raw)
	}

	result, err := h.Service.CreateAndEnqueue(r.Context(), params)
	if err != nil {
		writeJobServiceError(w, r, err)
		return
	}

	status := result.Status
	if status == "" {
		status = "queued"
	}
	resp := apigen.GenerationJobAccepted{
		JobId:  result.JobID,
		Status: apigen.GenerationJobAcceptedStatus(status),
	}
	if result.EstimatedCostUSD != "" {
		est := result.EstimatedCostUSD
		resp.EstimatedCostUsd = &est
	}
	if result.Currency != "" {
		cur := result.Currency
		resp.Currency = &cur
	}
	if result.CostReservationID != "" {
		rid := result.CostReservationID
		resp.CostReservationId = &rid
	}
	writeJSON(w, http.StatusAccepted, resp)
}
