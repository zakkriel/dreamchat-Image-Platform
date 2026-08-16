package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/zakkriel/drchat-image-platform/internal/assets"
	"github.com/zakkriel/drchat-image-platform/internal/auth"
	"github.com/zakkriel/drchat-image-platform/internal/http/apigen"
	"github.com/zakkriel/drchat-image-platform/internal/httperr"
	"github.com/zakkriel/drchat-image-platform/internal/idempotency"
	"github.com/zakkriel/drchat-image-platform/internal/identities"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
	"github.com/zakkriel/drchat-image-platform/internal/providers/routing"
)

// CharacterBootstrapAnchorHandler generates and attaches one anchor image for a
// character visual identity. It keeps the flow intentionally lean: no
// preview-first, force-regenerate, grids, packs, or transforms.
type CharacterBootstrapAnchorHandler struct {
	Service  jobs.Creator
	Identity identities.Repository
	Resolver RouteResolver
	// ProviderPreference is the process-level IMAGE_PROVIDER tie-break preference.
	ProviderPreference string
	// Reuse is an optional exact-reuse lookup. When set, a matching ready render
	// is attached immediately (no job create/enqueue).
	Reuse ArtifactReuseLookup
	// Gate is the shared media-eligibility gate. Zero value = unwired (skip).
	Gate GovernanceGate
}

func NewCharacterBootstrapAnchorHandler(service jobs.Creator, identity identities.Repository, resolver RouteResolver, providerPreference string, reuse ArtifactReuseLookup) *CharacterBootstrapAnchorHandler {
	return &CharacterBootstrapAnchorHandler{
		Service:            service,
		Identity:           identity,
		Resolver:           resolver,
		ProviderPreference: providerPreference,
		Reuse:              reuse,
	}
}

type bootstrapAnchorRequest struct {
	Governance     *apigen.GovernanceEnvelope `json:"governance,omitempty"`
	WorldID        string                     `json:"world_id"`
	ProviderID     *string                    `json:"provider_id,omitempty"`
	QualityTier    *apigen.QualityTier        `json:"quality_tier,omitempty"`
	LatencyTier    *apigen.LatencyTier        `json:"latency_tier,omitempty"`
	FallbackPolicy *apigen.FallbackPolicy     `json:"fallback_policy,omitempty"`
}

type bootstrapAnchorAlreadyAnchoredResponse struct {
	Status           string   `json:"status"`
	VisualIdentityID string   `json:"visual_identity_id"`
	AnchorAssetIDs   []string `json:"anchor_asset_ids"`
}

func (h *CharacterBootstrapAnchorHandler) BootstrapCharacterAnchor(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "missing principal")
		return
	}

	characterID := chi.URLParam(r, "character_id")
	if characterID == "" {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "character_id is required")
		return
	}

	raw, ok := readRawJSONBody(w, r)
	if !ok {
		return
	}

	var req bootstrapAnchorRequest
	if !decodeFromRaw(w, r, raw, &req) {
		return
	}
	if req.WorldID == "" {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "world_id is required")
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

	identity, err := h.Identity.GetByOwner(r.Context(), principal.TenantID, req.WorldID, string(apigen.OwnerTypeCharacter), characterID)
	if err != nil {
		if errors.Is(err, identities.ErrNotFound) {
			httperr.Write(w, r, http.StatusNotFound, httperr.CodeNotFound, "visual identity not found")
			return
		}
		httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "could not load visual identity")
		return
	}
	if len(identity.AnchorAssetIds) > 0 {
		writeJSON(w, http.StatusOK, bootstrapAnchorAlreadyAnchoredResponse{
			Status:           "already_anchored",
			VisualIdentityID: identity.ID,
			AnchorAssetIDs:   append([]string(nil), identity.AnchorAssetIds...),
		})
		return
	}

	fallback := string(apigen.CompatibleOnly)
	if req.FallbackPolicy != nil {
		fallback = string(*req.FallbackPolicy)
	}
	if !requireAdminForAnyExisting(w, r, principal.HasScope("admin:read"), fallback) {
		return
	}

	qualityTier := string(apigen.QualityTierStandard)
	if req.QualityTier != nil {
		qualityTier = string(*req.QualityTier)
	}

	description := identity.DisplayName
	renderHash := assets.ArtifactRenderHash(assets.ArtifactHashInput{
		TenantID:       principal.TenantID,
		WorldID:        req.WorldID,
		ArtifactID:     "bootstrap_anchor:" + identity.ID,
		Description:    description,
		StyleProfileID: identity.StyleProfileID,
		QualityTier:    qualityTier,
	})

	requestedProvider := ""
	if req.ProviderID != nil {
		requestedProvider = strings.TrimSpace(*req.ProviderID)
	}

	payload := map[string]any{
		"artifact_id":             "bootstrap_anchor:" + identity.ID,
		"world_id":                req.WorldID,
		"style_profile_id":        identity.StyleProfileID,
		"description":             description,
		"fallback_policy":         fallback,
		"quality_tier":            qualityTier,
		"prompt_hash":             renderHash,
		"anchor_for_identity_id":  identity.ID,
		"anchor_for_character_id": characterID,
	}
	if requestedProvider != "" {
		payload["requested_provider_id"] = requestedProvider
	}
	if req.LatencyTier != nil {
		payload["latency_tier"] = string(*req.LatencyTier)
	}

	idemKey := r.Header.Get(idempotency.HeaderKey)
	endpoint := r.Method + " " + r.URL.Path
	requestHash := jobs.HashRequestBody(raw)
	if idemKey != "" && handleReplay(w, r, h.Service, principal.TenantID, principal.TokenID, idemKey, endpoint, requestHash) {
		return
	}

	gov, ok := h.Gate.run(w, r, principal.TenantID, principal.TokenID, req.Governance)
	if !ok {
		return
	}

	if h.Reuse != nil {
		existing, rerr := h.Reuse.FindReadyArtifactByPromptHash(r.Context(), assets.ArtifactLookup{
			TenantID:       principal.TenantID,
			WorldID:        req.WorldID,
			StyleProfileID: identity.StyleProfileID,
			QualityTier:    qualityTier,
			PromptHash:     renderHash,
		})
		switch {
		case rerr == nil:
			updated, uerr := h.Identity.SetAnchorAssets(r.Context(), identity.ID, principal.TenantID, []string{existing.ID})
			if uerr != nil {
				httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "could not attach reused anchor")
				return
			}
			writeJSON(w, http.StatusOK, bootstrapAnchorAlreadyAnchoredResponse{
				Status:           "already_anchored",
				VisualIdentityID: updated.ID,
				AnchorAssetIDs:   append([]string(nil), updated.AnchorAssetIds...),
			})
			return
		case errors.Is(rerr, assets.ErrNotFound):
			// miss: continue with normal generation path.
		default:
			httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "could not check bootstrap anchor reuse")
			return
		}
	}

	latencyTier := ""
	if req.LatencyTier != nil {
		latencyTier = string(*req.LatencyTier)
	}
	resolveReq := routing.ResolveRequest{
		TenantID:           principal.TenantID,
		OperationType:      artifactOperationType,
		QualityTier:        qualityTier,
		LatencyTier:        latencyTier,
		RequiredCapability: capabilitySceneCapable,
		ProviderPreference: h.ProviderPreference,
		ProviderID:         requestedProvider,
	}
	resolved, err := h.Resolver.Resolve(r.Context(), resolveReq)
	if err != nil {
		writeRouteError(w, r, err)
		return
	}

	identityID := identity.ID
	params := jobs.CreateAndEnqueueParams{
		TenantID:           principal.TenantID,
		RequestedByTokenID: principal.TokenID,
		JobType:            "artifact",
		WorldID:            req.WorldID,
		VisualIdentityID:   &identityID,
		InputPayload:       payload,
		FallbackPolicy:     fallback,
		CacheResult:        "generated_required",
		Units:              artifactUnits,
		MaxConcurrentJobs:  principal.Limits.MaxConcurrentJobs,
	}
	gov.apply(&params)
	applyResolvedRoute(&params, payload, resolved)
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
