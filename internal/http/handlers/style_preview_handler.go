package handlers

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/zakkriel/drchat-image-platform/internal/assets"
	"github.com/zakkriel/drchat-image-platform/internal/auth"
	"github.com/zakkriel/drchat-image-platform/internal/http/apigen"
	"github.com/zakkriel/drchat-image-platform/internal/httperr"
	"github.com/zakkriel/drchat-image-platform/internal/idempotency"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
	"github.com/zakkriel/drchat-image-platform/internal/providers/routing"
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
	Service            jobs.Creator
	Styles             styles.Repository
	Resolver           RouteResolver
	ProviderPreference string
}

func NewStylePreviewHandler(service jobs.Creator, stylesRepo styles.Repository, resolver RouteResolver, providerPreference string) *StylePreviewHandler {
	return &StylePreviewHandler{Service: service, Styles: stylesRepo, Resolver: resolver, ProviderPreference: providerPreference}
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
	if req.DeliveryMode != nil && !validDeliveryMode(*req.DeliveryMode) {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "delivery_mode must be one of final_only, preview_first")
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

	// Phase 7B: delivery_mode=preview_first opts the style-preview sample into
	// two-phase preview-first delivery (hard true_preview routing requirement).
	previewFirst := req.DeliveryMode != nil && *req.DeliveryMode == apigen.PreviewFirst

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
	if previewFirst {
		payload["delivery_mode"] = string(apigen.PreviewFirst)
	}

	idemKey := r.Header.Get(idempotency.HeaderKey)
	endpoint := r.Method + " " + r.URL.Path
	requestHash := jobs.HashRequestBody(raw)

	// Idempotency replay check FIRST (Phase 7A): a replay short-circuits before
	// route resolution + cost reservation.
	if idemKey != "" && handleReplay(w, r, h.Service, principal.TenantID, principal.TokenID, idemKey, endpoint, requestHash) {
		return
	}

	// Resolve the provider route once, before reserving cost.
	resolveReq := routing.ResolveRequest{
		TenantID:           principal.TenantID,
		OperationType:      artifactOperationType,
		QualityTier:        qualityTier,
		RequiredCapability: capabilitySceneCapable,
		ProviderPreference: h.ProviderPreference,
	}
	// Phase 7B: preview_first is a HARD true_preview requirement → 422
	// unsupported_capability before cost reservation when no true_preview route
	// exists. No downgrade, no derived_preview fallback.
	if previewFirst {
		resolveReq.RequiredPreviewCapability = previewCapabilityTruePreview
	}
	resolved, err := h.Resolver.Resolve(r.Context(), resolveReq)
	if err != nil {
		writeRouteError(w, r, err)
		return
	}

	params := jobs.CreateAndEnqueueParams{
		TenantID:           principal.TenantID,
		RequestedByTokenID: principal.TokenID,
		JobType:            "artifact",
		WorldID:            req.WorldId,
		InputPayload:       payload,
		FallbackPolicy:     "none",
		CacheResult:        "generated_required",
		Units:              artifactUnits,
		MaxConcurrentJobs:  principal.Limits.MaxConcurrentJobs,
	}
	applyResolvedRoute(&params, payload, resolved)
	// Phase 7C-4: resolve the ordered fallback chain with the same request and
	// thread the alternates (chain minus the applied primary) onto the params; the
	// jobs service keeps only the same-price subset. A ResolveChain error is
	// treated as "no fallbacks" — the primary already resolved successfully.
	applyFallbackChain(&params, resolveFallbackChain(r.Context(), h.Resolver, resolveReq))
	if idemKey != "" {
		params.IdempotencyKey = idemKey
		params.Endpoint = endpoint
		params.RequestHash = requestHash
	}

	result, err := h.Service.CreateAndEnqueue(r.Context(), params)
	if err != nil {
		if errors.Is(err, jobs.ErrConcurrentJobsExceeded) {
			setConcurrentHeaders(w, params.MaxConcurrentJobs, params.MaxConcurrentJobs)
		}
		writeJobServiceError(w, r, err)
		return
	}

	writeJobAccepted(w, result)
}
