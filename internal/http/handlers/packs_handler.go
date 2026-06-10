package handlers

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/zakkriel/drchat-image-platform/internal/auth"
	"github.com/zakkriel/drchat-image-platform/internal/config"
	"github.com/zakkriel/drchat-image-platform/internal/http/apigen"
	"github.com/zakkriel/drchat-image-platform/internal/httperr"
	"github.com/zakkriel/drchat-image-platform/internal/idempotency"
	"github.com/zakkriel/drchat-image-platform/internal/identities"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
	"github.com/zakkriel/drchat-image-platform/internal/styles"
)

// PacksHandler accepts the two generate-pack requests (PRD 04 §4/§5) and
// delegates the transactional job + pack create to jobs.Creator. Like the
// artifacts handler it owns only authorization, validation, planning, and
// response shaping; pack fan-out itself is the worker's job (ADR-008).
type PacksHandler struct {
	Service    jobs.Creator
	Styles     styles.Repository
	Identities identities.Repository
	Provider   config.Provider
}

func NewPacksHandler(service jobs.Creator, stylesRepo styles.Repository, identitiesRepo identities.Repository, provider config.Provider) *PacksHandler {
	return &PacksHandler{
		Service:    service,
		Styles:     stylesRepo,
		Identities: identitiesRepo,
		Provider:   provider,
	}
}

// packKind selects the per-entity constants of a pack request. 5A ships the
// two starter-pack kinds; their default variant lists are deliberately
// minimal (5B expands them with expression/angle/state semantics).
type packKind struct {
	ownerType       string // visual_identities.owner_type
	pathParam       string // chi URL param carrying the entity id
	payloadIDKey    string // input_payload key for the entity id
	jobType         string // generation_jobs.job_type
	packType        string // asset_packs.pack_type (PRD 04 names)
	defaultVariants []string
}

var (
	characterPackKind = packKind{
		ownerType:    "character",
		pathParam:    "character_id",
		payloadIDKey: "character_id",
		jobType:      jobs.JobTypeCharacterPack,
		packType:     "character_minimal_portrait_pack",
		defaultVariants: []string{
			"neutral_front_portrait",
			"neutral_three_quarter_portrait",
			"side_angle_portrait",
		},
	}
	placePackKind = packKind{
		ownerType:    "place",
		pathParam:    "place_id",
		payloadIDKey: "place_id",
		jobType:      jobs.JobTypePlacePack,
		packType:     "place_minimal_scene_pack",
		defaultVariants: []string{
			"establishing_wide_view",
			"closer_atmospheric_view",
		},
	}
)

// maxPackVariants caps the fan-out (and therefore the priced unit count) of
// a single pack request.
const maxPackVariants = 12

// planPackVariants resolves the variant list at request time: an explicit
// non-empty variant_keys wins verbatim (opaque strings, de-duplicated,
// order-preserving); otherwise the kind's fixed default applies. Exceeding
// maxPackVariants is a caller error (400).
func planPackVariants(kind packKind, override []string) ([]string, error) {
	source := kind.defaultVariants
	if len(override) > 0 {
		source = override
	}
	seen := make(map[string]struct{}, len(source))
	out := make([]string, 0, len(source))
	for _, key := range source {
		if key == "" {
			return nil, errors.New("variant_keys must not contain empty strings")
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	if len(out) > maxPackVariants {
		return nil, fmt.Errorf("variant_keys must contain at most %d distinct keys", maxPackVariants)
	}
	return out, nil
}

func (h *PacksHandler) GenerateCharacterPack(w http.ResponseWriter, r *http.Request) {
	h.generate(w, r, characterPackKind)
}

func (h *PacksHandler) GeneratePlacePack(w http.ResponseWriter, r *http.Request) {
	h.generate(w, r, placePackKind)
}

func (h *PacksHandler) generate(w http.ResponseWriter, r *http.Request, kind packKind) {
	// Provider gate first, before any row or queue task exists (same Phase 3
	// rule the artifacts handler follows).
	if h.Provider != config.ProviderMock {
		httperr.Write(w, r, http.StatusServiceUnavailable, httperr.CodeProviderUnavailable, "configured image provider is not available in this phase")
		return
	}

	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "missing principal")
		return
	}

	ownerID := chi.URLParam(r, kind.pathParam)
	if ownerID == "" {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, kind.pathParam+" is required")
		return
	}

	raw, ok := readRawJSONBody(w, r)
	if !ok {
		return
	}

	// The two generated request types are structurally identical; decode
	// into the character shape for both kinds.
	var req apigen.GenerateCharacterPackRequest
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

	var override []string
	if req.VariantKeys != nil {
		override = *req.VariantKeys
	}
	variantKeys, err := planPackVariants(kind, override)
	if err != nil {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, err.Error())
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

	// A pack hangs off an existing visual identity; 5A never creates one
	// implicitly (Phase 2 owns identity creation).
	identity, err := h.Identities.GetByOwner(r.Context(), principal.TenantID, req.WorldId, kind.ownerType, ownerID)
	if err != nil {
		if errors.Is(err, identities.ErrNotFound) {
			httperr.Write(w, r, http.StatusUnprocessableEntity, httperr.CodeInvalidRequest, "pack requires an existing visual identity")
			return
		}
		httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "could not resolve visual identity")
		return
	}

	fallback := string(apigen.CompatibleOnly)
	if req.FallbackPolicy != nil {
		fallback = string(*req.FallbackPolicy)
	}

	// Everything the worker needs lives in input_payload so the queue task
	// carries only job_id (same contract as artifacts).
	payload := map[string]any{
		kind.payloadIDKey:    ownerID,
		"world_id":           req.WorldId,
		"style_profile_id":   req.StyleProfileId,
		"variant_keys":       variantKeys,
		"visual_identity_id": identity.ID,
		"display_name":       identity.DisplayName,
		"fallback_policy":    fallback,
	}
	quality := ""
	if req.QualityTier != nil {
		quality = string(*req.QualityTier)
		payload["quality_tier"] = quality
	}
	if req.LatencyTier != nil {
		payload["latency_tier"] = string(*req.LatencyTier)
	}

	params := jobs.CreateAndEnqueueParams{
		TenantID:           principal.TenantID,
		RequestedByTokenID: principal.TokenID,
		JobType:            kind.jobType,
		WorldID:            req.WorldId,
		InputPayload:       payload,
		FallbackPolicy:     fallback,
		CacheResult:        "generated_required",
		AssetPack: &jobs.AssetPackSpec{
			PackType:         kind.packType,
			VisualIdentityID: identity.ID,
			QualityTier:      quality,
		},
		ProviderID:    artifactProviderID,
		ModelID:       artifactModelID,
		OperationType: artifactOperationType,
		// The variant list is the unit of fan-out and the unit of pricing:
		// N variants = N text_to_image images.
		Units: int32(len(variantKeys)),
	}
	if key := r.Header.Get(idempotency.HeaderKey); key != "" {
		params.IdempotencyKey = key
		params.Endpoint = r.Method + " " + r.URL.Path
		params.RequestHash = jobs.HashRequestBody(raw)
	}

	result, err := h.Service.CreateAndEnqueue(r.Context(), params)
	if err != nil {
		switch {
		case errors.Is(err, jobs.ErrNoPriceEntry):
			httperr.Write(w, r, http.StatusUnprocessableEntity, httperr.CodeNoPriceEntry, "no active price entry for the selected provider/model/operation")
		case errors.Is(err, jobs.ErrBudgetExceeded):
			httperr.Write(w, r, http.StatusUnprocessableEntity, httperr.CodeBudgetExceeded, "cost budget exceeded for this request")
		case errors.Is(err, jobs.ErrIdempotencyConflict):
			httperr.Write(w, r, http.StatusConflict, httperr.CodeIdempotencyConflict, "idempotency key reused with a different body or endpoint")
		case errors.Is(err, jobs.ErrEnqueueFailed):
			httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "could not enqueue generation job")
		default:
			httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "could not create generation job")
		}
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
	if result.AssetPackID != "" {
		pid := result.AssetPackID
		resp.AssetPackId = &pid
	}
	writeJSON(w, http.StatusAccepted, resp)
}
