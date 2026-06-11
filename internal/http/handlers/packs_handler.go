package handlers

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/zakkriel/drchat-image-platform/internal/assets"
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
// artifacts handler it owns only authorization, validation, planning,
// retrieval-before-generation (Phase 6A3), and response shaping; pack fan-out
// itself is the worker's job (ADR-008).
type PacksHandler struct {
	Service    jobs.Creator
	Styles     styles.Repository
	Identities identities.Repository
	Provider   config.Provider
	// Retriever is the Phase 6A3 per-role reuse decision layer (implemented by
	// *assets.Retriever). When nil the handler skips reuse and prices/generates
	// the whole pack (the pre-6A3 behavior).
	Retriever RetrievalService
}

func NewPacksHandler(service jobs.Creator, stylesRepo styles.Repository, identitiesRepo identities.Repository, provider config.Provider) *PacksHandler {
	return &PacksHandler{
		Service:    service,
		Styles:     stylesRepo,
		Identities: identitiesRepo,
		Provider:   provider,
	}
}

// WithRetriever wires the per-role reuse decision layer (Phase 6A3). Optional;
// nil-safe (the handler generates the whole pack when it is unset).
func (h *PacksHandler) WithRetriever(retriever RetrievalService) *PacksHandler {
	h.Retriever = retriever
	return h
}

// packKind selects the per-entity constants of a pack request. The
// no-template default variant list is the PRD 04 §4.2/§5.2 minimum/starter
// pack (7 character roles, 6 place roles), derived from the named minimal
// template so the two can never diverge.
type packKind struct {
	ownerType       string // visual_identities.owner_type (also assets entity type)
	pathParam       string // chi URL param carrying the entity id
	payloadIDKey    string // input_payload key for the entity id
	jobType         string // generation_jobs.job_type
	packType        string // asset_packs.pack_type for the minimal default
	customPackType  string // asset_packs.pack_type when variant_keys override
	defaultVariants []string
}

var (
	characterPackKind = packKind{
		ownerType:      "character",
		pathParam:      "character_id",
		payloadIDKey:   "character_id",
		jobType:        jobs.JobTypeCharacterPack,
		packType:       assets.TemplateCharacterMinimalPortrait,
		customPackType: assets.PackTypeCharacterCustom,
		// The no-template default IS the PRD 04 §4.2 minimum/starter pack.
		// Deriving it from the template guarantees "minimal" means the same
		// thing whether selected explicitly or by omission.
		defaultVariants: minimalTemplateRoles("character", assets.TemplateCharacterMinimalPortrait),
	}
	placePackKind = packKind{
		ownerType:       "place",
		pathParam:       "place_id",
		payloadIDKey:    "place_id",
		jobType:         jobs.JobTypePlacePack,
		packType:        assets.TemplatePlaceMinimalScene,
		customPackType:  assets.PackTypePlaceCustom,
		defaultVariants: minimalTemplateRoles("place", assets.TemplatePlaceMinimalScene),
	}
)

// minimalTemplateRoles fetches a template's role set at init, panicking if the
// template is undefined — a programming error, since these are compile-time
// constants. Keeps the no-template default and the named minimal template in
// lock-step so they can never silently diverge.
func minimalTemplateRoles(entityType, template string) []string {
	roles, ok := assets.PackTemplateRoles(entityType, template)
	if !ok {
		panic("packs: undefined minimal pack template " + template)
	}
	return roles
}

// maxPackVariants caps the fan-out (and therefore the priced unit count) of
// a single pack request.
const maxPackVariants = 12

// errUnknownPackTemplate is returned by resolvePackPlan when pack_template
// names a template that is not defined for the entity → 400 invalid_request.
var errUnknownPackTemplate = errors.New("unknown pack_template")

// dedupVariants de-duplicates (order-preserving), rejects empty keys, and caps
// the list at maxPackVariants. Variant keys stay opaque — no semantic
// validation beyond empty/cap checks (5A contract, preserved in 5B).
func dedupVariants(source []string) ([]string, error) {
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

// planPackVariants resolves the variant list when no template is in play: an
// explicit non-empty variant_keys wins verbatim; otherwise the kind's fixed
// default applies. Retained for the no-template path and unit tests.
func planPackVariants(kind packKind, override []string) ([]string, error) {
	source := kind.defaultVariants
	if len(override) > 0 {
		source = override
	}
	return dedupVariants(source)
}

// resolvePackPlan applies the 5B resolution precedence —
// explicit variant_keys > pack_template > minimal default — and returns both
// the variant list to fan out and the pack_type to record on asset_packs.
//
//   - variant_keys (non-empty): win verbatim (opaque, de-duplicated, capped);
//     the pack is a custom pack, not the named template.
//   - pack_template: resolves to its documented role set; unknown → error.
//   - neither: the kind's minimal default.
func resolvePackPlan(kind packKind, override []string, template string) (keys []string, packType string, err error) {
	if len(override) > 0 {
		keys, err = dedupVariants(override)
		if err != nil {
			return nil, "", err
		}
		return keys, kind.customPackType, nil
	}
	if template != "" {
		roles, ok := assets.PackTemplateRoles(kind.ownerType, template)
		if !ok {
			return nil, "", fmt.Errorf("%w: %q", errUnknownPackTemplate, template)
		}
		keys, err = dedupVariants(roles)
		if err != nil {
			return nil, "", err
		}
		return keys, template, nil
	}
	keys, err = dedupVariants(kind.defaultVariants)
	if err != nil {
		return nil, "", err
	}
	return keys, kind.packType, nil
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
	template := ""
	if req.PackTemplate != nil {
		template = *req.PackTemplate
	}
	variantKeys, packType, err := resolvePackPlan(kind, override, template)
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

	// Resolve the effective quality tier once (default "standard"). The same
	// value feeds the reuse lookup and the stored/generated assets so a request
	// that omits quality_tier reuses and is reused consistently.
	quality := ""
	effectiveQuality := "standard"
	if req.QualityTier != nil {
		quality = string(*req.QualityTier)
		effectiveQuality = quality
	}

	// Everything the worker needs lives in input_payload so the queue task
	// carries only job_id (same contract as artifacts). variant_keys stays the
	// FULL required role set: the worker's existing-items skip then naturally
	// generates only the roles not already present as reused asset_pack_items.
	payload := map[string]any{
		kind.payloadIDKey:    ownerID,
		"world_id":           req.WorldId,
		"style_profile_id":   req.StyleProfileId,
		"variant_keys":       variantKeys,
		"visual_identity_id": identity.ID,
		"display_name":       identity.DisplayName,
		"fallback_policy":    fallback,
	}
	if req.QualityTier != nil {
		payload["quality_tier"] = quality
	}
	if req.LatencyTier != nil {
		payload["latency_tier"] = string(*req.LatencyTier)
	}

	// Phase 6A4: force_regenerate (default false) bypasses per-role reuse and
	// regenerates the whole pack. Carried on the payload so the worker supersedes
	// each role's slot.
	forceRegenerate := req.ForceRegenerate != nil && *req.ForceRegenerate
	if forceRegenerate {
		payload["force_regenerate"] = true
	}

	// Phase 6A3 retrieval-before-generation: resolve every required role through
	// the retrieval layer (exact → compatible → preview → generated_required,
	// gated by fallback_policy) before reserving cost. Roles a reusable asset
	// satisfies become reused items (persisted up front, no provider work); the
	// rest are the missing roles the worker generates and the only roles priced.
	//
	// Phase 6A4: when forced, retrieval is bypassed — every required role is
	// treated as missing (priced + generated) and no reused items are persisted,
	// so the existing partial path generates the whole pack with no misses-only
	// discount and no all-hits synchronous completion.
	var reuseItems []jobs.PackReuseItem
	var missing []string
	if forceRegenerate {
		missing = append([]string(nil), variantKeys...)
	} else {
		var ok bool
		reuseItems, missing, ok = h.planPackReuse(w, r, packReuseInput{
			tenantID:         principal.TenantID,
			worldID:          req.WorldId,
			visualIdentityID: identity.ID,
			entityType:       kind.ownerType,
			styleProfileID:   req.StyleProfileId,
			qualityTier:      effectiveQuality,
			fallbackPolicy:   fallback,
			roles:            variantKeys,
		})
		if !ok {
			return
		}
	}

	// Idempotency context is shared by the reuse and the generate paths so a
	// same-key replay returns the same pack job regardless of which path created it.
	idemKey := r.Header.Get(idempotency.HeaderKey)
	endpoint := r.Method + " " + r.URL.Path
	requestHash := jobs.HashRequestBody(raw)

	// All-hits: every required role was satisfied by reuse. Complete the pack
	// synchronously — no reservation, no provider attempt, no enqueue.
	if h.Retriever != nil && len(missing) == 0 {
		h.respondPackAllHits(w, r, packAllHitsInput{
			principal:        principal,
			jobType:          kind.jobType,
			worldID:          req.WorldId,
			packType:         packType,
			visualIdentityID: identity.ID,
			qualityTier:      quality,
			fallback:         fallback,
			payload:          payload,
			requiredRoles:    variantKeys,
			reuseItems:       reuseItems,
			idemKey:          idemKey,
			endpoint:         endpoint,
			requestHash:      requestHash,
		})
		return
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
			PackType:         packType,
			VisualIdentityID: identity.ID,
			QualityTier:      quality,
			RequiredRoles:    variantKeys,
			MissingRoles:     missing,
			ReusedItems:      reuseItems,
		},
		ProviderID:    artifactProviderID,
		ModelID:       artifactModelID,
		OperationType: artifactOperationType,
		// Misses-only pricing: only the roles with no reusable asset are
		// generated, so only they are priced. Zero misses never reaches here.
		Units: int32(len(missing)),
	}
	if idemKey != "" {
		params.IdempotencyKey = idemKey
		params.Endpoint = endpoint
		params.RequestHash = requestHash
	}

	result, err := h.Service.CreateAndEnqueue(r.Context(), params)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}

	h.writePackAccepted(w, result)
}

// writePackAccepted shapes the 202 acceptance envelope for a pack create.
func (h *PacksHandler) writePackAccepted(w http.ResponseWriter, result jobs.CreateResult) {
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

// packReuseStateVersion is the state version pack retrieval queries on. Pack
// assets are generated at the entity's default state (state_version = 1, the
// visual_assets default), so reuse must look for that same state to find a
// previously-generated pack asset. There is no per-state pack request yet.
const packReuseStateVersion = 1

// packReuseInput is the per-role retrieval context derived from the pack request.
type packReuseInput struct {
	tenantID         string
	worldID          string
	visualIdentityID string
	entityType       string // character | place
	styleProfileID   string
	qualityTier      string
	fallbackPolicy   string
	roles            []string // the full required role set, in order
}

// planPackReuse resolves every required role through the retrieval decision
// layer and splits the roles into reused (an existing ready asset the policy
// allows) and missing (generated_required, or a hit the policy disallows — both
// surface from Retrieve as a non-reusable outcome). It returns ok=false after
// writing a 500 on a retrieval error.
//
// When the retriever is unwired the whole pack is missing (the pre-6A3 "generate
// everything" behavior). A reused asset is claimed at most once per pack: the
// asset_pack_items UNIQUE(asset_pack_id, visual_asset_id) constraint forbids the
// same asset backing two roles, so a second role that resolves to an already-
// claimed asset is treated as missing and generated fresh.
func (h *PacksHandler) planPackReuse(w http.ResponseWriter, r *http.Request, in packReuseInput) ([]jobs.PackReuseItem, []string, bool) {
	if h.Retriever == nil {
		return nil, append([]string(nil), in.roles...), true
	}
	var reuseItems []jobs.PackReuseItem
	var missing []string
	claimed := make(map[string]bool, len(in.roles))
	for i, role := range in.roles {
		res, err := h.Retriever.Retrieve(r.Context(), assets.RetrievalQuery{
			TenantID:         in.tenantID,
			WorldID:          in.worldID,
			VisualIdentityID: in.visualIdentityID,
			EntityType:       in.entityType,
			VariantKey:       role,
			StyleProfileID:   in.styleProfileID,
			StateVersion:     packReuseStateVersion,
			QualityTier:      in.qualityTier,
			FallbackPolicy:   in.fallbackPolicy,
		})
		if err != nil {
			httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "could not evaluate pack reuse")
			return nil, nil, false
		}
		if res.MatchType != assets.OutcomeGeneratedRequired && res.Asset != nil && !claimed[res.Asset.ID] {
			claimed[res.Asset.ID] = true
			reuseItems = append(reuseItems, jobs.PackReuseItem{
				VariantKey: role,
				AssetID:    res.Asset.ID,
				MatchType:  res.MatchType,
				SortOrder:  int32(i),
			})
			continue
		}
		missing = append(missing, role)
	}
	return reuseItems, missing, true
}

// packAllHitsInput carries what respondPackAllHits needs to land a completed
// all-hits pack job.
type packAllHitsInput struct {
	principal        *auth.Principal
	jobType          string
	worldID          string
	packType         string
	visualIdentityID string
	qualityTier      string
	fallback         string
	payload          map[string]any
	requiredRoles    []string
	reuseItems       []jobs.PackReuseItem
	idemKey          string
	endpoint         string
	requestHash      string
}

// respondPackAllHits lands an already-completed pack job (no cost reservation,
// no provider attempt, no enqueue) and shapes the 202. As with the artifact
// cache hit the envelope status stays "queued" (the schema's only accepted
// value); the synchronously-completed state, the aggregate cache_result, and
// final_asset_ids are observed via GET /v1/jobs/{job_id}. estimated_cost_usd is
// "0.0000" to signal the reuse is free.
func (h *PacksHandler) respondPackAllHits(w http.ResponseWriter, r *http.Request, in packAllHitsInput) {
	params := jobs.CreatePackReuseParams{
		TenantID:           in.principal.TenantID,
		RequestedByTokenID: in.principal.TokenID,
		JobType:            in.jobType,
		WorldID:            in.worldID,
		InputPayload:       in.payload,
		FallbackPolicy:     in.fallback,
		CacheResult:        aggregatePackCacheResult(in.reuseItems),
		PackType:           in.packType,
		VisualIdentityID:   in.visualIdentityID,
		QualityTier:        in.qualityTier,
		RequiredRoles:      in.requiredRoles,
		ReusedItems:        in.reuseItems,
	}
	if in.idemKey != "" {
		params.IdempotencyKey = in.idemKey
		params.Endpoint = in.endpoint
		params.RequestHash = in.requestHash
	}

	result, err := h.Service.CreateCompletedPackReuseJob(r.Context(), params)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}

	est := "0.0000"
	if result.EstimatedCostUSD != "" {
		est = result.EstimatedCostUSD
	}
	// A fresh all-hits completion uses the accepted envelope's only schema value
	// ("queued") — its completed state is observed via GET /v1/jobs/{job_id}. A
	// replay echoes the existing job's live status (e.g. "completed"), preserving
	// the idempotency contract that a replay reports the prior job's real state.
	status := apigen.GenerationJobAcceptedStatusQueued
	if result.Replayed && result.Status != "" {
		status = apigen.GenerationJobAcceptedStatus(result.Status)
	}
	resp := apigen.GenerationJobAccepted{
		JobId:            result.JobID,
		Status:           status,
		EstimatedCostUsd: &est,
	}
	if result.AssetPackID != "" {
		pid := result.AssetPackID
		resp.AssetPackId = &pid
	}
	writeJSON(w, http.StatusAccepted, resp)
}

// aggregatePackCacheResult summarizes an all-hits pack's per-role reuse outcomes
// into the single job-level cache_result enum: the weakest reuse tier across the
// roles (exact_match > compatible_match > preview_fallback). "All roles reused
// at least at a compatible match" honestly reads as compatible_match. An empty
// set (never the all-hits case) defaults to exact_match.
func aggregatePackCacheResult(items []jobs.PackReuseItem) string {
	best := assets.OutcomeExactMatch
	rank := func(m string) int {
		switch m {
		case assets.OutcomeExactMatch:
			return 3
		case assets.OutcomeCompatibleMatch:
			return 2
		case assets.OutcomePreviewFallback:
			return 1
		default:
			return 0
		}
	}
	min := rank(best)
	for _, item := range items {
		if rank(item.MatchType) < min {
			min = rank(item.MatchType)
			best = item.MatchType
		}
	}
	return best
}

// writeServiceError maps a jobs.Creator error to the matching HTTP status.
func (h *PacksHandler) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
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
}
