package handlers

import (
	"bytes"
	"context"
	"errors"
	"io"
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

// ArtifactReuseLookup is the narrow Phase 6A2 exact-reuse dependency: the
// deterministic artifact-render-hash lookup the generate path consults before
// reserving cost or enqueuing provider work. *assets.pgRepository satisfies it
// (via assets.Repository); keeping it a focused interface lets the handler be
// tested without a database and without pulling in the full asset repository.
type ArtifactReuseLookup interface {
	FindReadyArtifactByPromptHash(ctx context.Context, q assets.ArtifactLookup) (assets.VisualAsset, error)
}

// ArtifactsHandler accepts artifact generation requests and delegates the
// transactional job-create + idempotency-row + enqueue work to a
// jobs.Creator service. The handler itself is responsible only for
// authorization, validation, retrieval-before-generation (Phase 6A2), and
// shaping the 202/4xx/5xx response.
type ArtifactsHandler struct {
	Service  jobs.Creator
	Styles   styles.Repository
	Resolver RouteResolver
	// ProviderPreference is the per-process IMAGE_PROVIDER preference fed to the
	// resolver's tie-break; empty means "no preference".
	ProviderPreference string
	// Reuse is the Phase 6A2 exact-reuse lookup. When nil, the handler skips
	// retrieval and always generates (the pre-6A2 behavior).
	Reuse ArtifactReuseLookup
}

func NewArtifactsHandler(service jobs.Creator, stylesRepo styles.Repository, resolver RouteResolver, providerPreference string, reuse ArtifactReuseLookup) *ArtifactsHandler {
	return &ArtifactsHandler{
		Service:            service,
		Styles:             stylesRepo,
		Resolver:           resolver,
		ProviderPreference: providerPreference,
		Reuse:              reuse,
	}
}

// A single artifact request is one text_to_image image; the provider/model are
// resolved per request by the route resolver (Phase 7A), no longer hardcoded.
const (
	artifactOperationType = "text_to_image"
	artifactUnits         = 1
)

func (h *ArtifactsHandler) Generate(w http.ResponseWriter, r *http.Request) {
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
	if req.DeliveryMode != nil && !validDeliveryMode(*req.DeliveryMode) {
		httperr.Write(w, r, http.StatusBadRequest, httperr.CodeInvalidRequest, "delivery_mode must be one of final_only, preview_first")
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

	// Resolve the effective quality tier once. The same value feeds the render
	// hash, the reuse lookup, and the stored asset, so a request that omits
	// quality_tier (effective "standard") reuses and is reused consistently.
	qualityTier := "standard"
	if req.QualityTier != nil {
		qualityTier = string(*req.QualityTier)
	}

	// Phase 6A2: the deterministic artifact render hash. It is the asset's
	// prompt_hash (carried in the payload so the worker persists it on a miss)
	// and the key the exact-reuse lookup matches on.
	renderHash := assets.ArtifactRenderHash(assets.ArtifactHashInput{
		TenantID:       principal.TenantID,
		WorldID:        req.WorldId,
		ArtifactID:     artifactID,
		Description:    req.Description,
		StyleProfileID: req.StyleProfileId,
		QualityTier:    qualityTier,
	})

	// Phase 6A4: force_regenerate (default false) bypasses reuse and always
	// generates. It is carried on the payload so the worker supersedes the slot.
	forceRegenerate := req.ForceRegenerate != nil && *req.ForceRegenerate

	// Phase 7B: delivery_mode=preview_first opts the request into two-phase
	// preview-first delivery. It imposes a hard true_preview routing requirement
	// (resolved below) and is carried on the payload so the worker runs the
	// two-phase lifecycle. Default/final_only is the unchanged Phase 7A path.
	previewFirst := req.DeliveryMode != nil && *req.DeliveryMode == apigen.PreviewFirst

	payload := map[string]any{
		"artifact_id":      artifactID,
		"world_id":         req.WorldId,
		"style_profile_id": req.StyleProfileId,
		"description":      req.Description,
		"fallback_policy":  fallback,
		"quality_tier":     qualityTier,
		"prompt_hash":      renderHash,
	}
	if req.LatencyTier != nil {
		payload["latency_tier"] = string(*req.LatencyTier)
	}
	// Only set the key when forced, so a default request's payload stays
	// byte-for-byte the Phase 6A2 shape.
	if forceRegenerate {
		payload["force_regenerate"] = true
	}
	// Only set the key for preview_first, so a final_only/omitted request's
	// payload stays the Phase 7A shape.
	if previewFirst {
		payload["delivery_mode"] = string(apigen.PreviewFirst)
	}

	// Idempotency context is shared by both the reuse and the generate paths so
	// a same-key replay returns the same job regardless of which path created it.
	idemKey := r.Header.Get(idempotency.HeaderKey)
	endpoint := r.Method + " " + r.URL.Path
	requestHash := jobs.HashRequestBody(raw)

	// Idempotency replay check FIRST (Phase 7A lifecycle): a replay returns the
	// existing job without re-running reuse, route resolution, cost reservation,
	// or enqueue.
	if idemKey != "" && handleReplay(w, r, h.Service, principal.TenantID, principal.TokenID, idemKey, endpoint, requestHash) {
		return
	}

	// Phase 6A2 retrieval-before-generation: before reserving cost or enqueuing
	// provider work, look for an existing ready artifact with this exact render
	// hash. Exact reuse is allowed for EVERY fallback_policy (including none) —
	// fallback_policy gates compatible/preview fallback, not exact reuse.
	//
	// Phase 6A4: force_regenerate skips this lookup entirely → always reserve +
	// enqueue + generate, and the worker supersedes the slot.
	//
	// Phase 7B: preview_first also bypasses exact reuse and always generates a
	// fresh preview + final. A final-only ready asset has no preview, so reusing
	// it would never satisfy the preview_first contract — preview-first must
	// produce both tiers.
	if h.Reuse != nil && !forceRegenerate && !previewFirst {
		existing, err := h.Reuse.FindReadyArtifactByPromptHash(r.Context(), assets.ArtifactLookup{
			TenantID:       principal.TenantID,
			WorldID:        req.WorldId,
			StyleProfileID: req.StyleProfileId,
			QualityTier:    qualityTier,
			PromptHash:     renderHash,
		})
		switch {
		case err == nil:
			h.respondCacheHit(w, r, principal, req.WorldId, fallback, payload, existing.ID, idemKey, endpoint, requestHash)
			return
		case errors.Is(err, assets.ErrNotFound):
			// miss: fall through to the normal create/reserve/enqueue path.
		default:
			httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "could not check artifact reuse")
			return
		}
	}

	// Resolve the provider route ONCE, before reserving cost. The resolved
	// model becomes the pricing key and is persisted on the job for the worker.
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
	}
	// Phase 7B: preview_first is a HARD true_preview requirement. If no enabled
	// true_preview route can serve the request the resolver returns
	// ErrUnsupportedCapability → 422 BEFORE cost reservation, job creation, or
	// enqueue. There is no downgrade to final_only and no derived_preview fallback.
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
		FallbackPolicy:     fallback,
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
		h.writeServiceError(w, r, err)
		return
	}

	writeJobAccepted(w, result)
}

// respondCacheHit lands an already-completed cache-hit job (no cost
// reservation, no provider attempt, no enqueue) and shapes the 202 response.
// The 202 stays an acceptance envelope for API compatibility (status "queued"
// — the schema's only accepted-status value): the synchronously-completed
// state, cache_result=exact_match, and final_asset_ids are observed via
// GET /v1/jobs/{job_id}. estimated_cost_usd is "0.0000" to signal the reuse is
// free.
func (h *ArtifactsHandler) respondCacheHit(w http.ResponseWriter, r *http.Request, principal *auth.Principal, worldID, fallback string, payload map[string]any, assetID, idemKey, endpoint, requestHash string) {
	params := jobs.CreateCacheHitParams{
		TenantID:           principal.TenantID,
		RequestedByTokenID: principal.TokenID,
		JobType:            "artifact",
		WorldID:            worldID,
		InputPayload:       payload,
		FallbackPolicy:     fallback,
		FinalAssetID:       assetID,
	}
	if idemKey != "" {
		params.IdempotencyKey = idemKey
		params.Endpoint = endpoint
		params.RequestHash = requestHash
	}

	result, err := h.Service.CreateCompletedCacheHitJob(r.Context(), params)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}

	est := "0.0000"
	if result.EstimatedCostUSD != "" {
		est = result.EstimatedCostUSD
	}
	resp := apigen.GenerationJobAccepted{
		JobId:            result.JobID,
		Status:           apigen.GenerationJobAcceptedStatusQueued,
		EstimatedCostUsd: &est,
	}
	writeJSON(w, http.StatusAccepted, resp)
}

// writeServiceError maps a jobs.Creator error to the matching HTTP status.
func (h *ArtifactsHandler) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	writeJobServiceError(w, r, err)
}

// writeJobServiceError maps a jobs.Creator error to the matching HTTP status.
// Shared by the artifact generate and style-preview paths.
func writeJobServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, jobs.ErrConcurrentJobsExceeded):
		httperr.Write(w, r, http.StatusTooManyRequests, httperr.CodeConcurrentJobsExceeded, "too many concurrent generation jobs for this token")
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

func validDeliveryMode(d apigen.DeliveryMode) bool {
	switch d {
	case apigen.FinalOnly, apigen.PreviewFirst:
		return true
	}
	return false
}
