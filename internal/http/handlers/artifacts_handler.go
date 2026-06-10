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
	"github.com/zakkriel/drchat-image-platform/internal/config"
	"github.com/zakkriel/drchat-image-platform/internal/http/apigen"
	"github.com/zakkriel/drchat-image-platform/internal/httperr"
	"github.com/zakkriel/drchat-image-platform/internal/idempotency"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
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
	Provider config.Provider
	// Reuse is the Phase 6A2 exact-reuse lookup. When nil, the handler skips
	// retrieval and always generates (the pre-6A2 behavior).
	Reuse ArtifactReuseLookup
}

func NewArtifactsHandler(service jobs.Creator, stylesRepo styles.Repository, provider config.Provider, reuse ArtifactReuseLookup) *ArtifactsHandler {
	return &ArtifactsHandler{
		Service:  service,
		Styles:   stylesRepo,
		Provider: provider,
		Reuse:    reuse,
	}
}

// Phase 4 has no provider router yet, so artifact generation resolves to the
// seeded mock route (migrations/0002_seed_mock_provider.up.sql) for pricing.
// A single artifact request is one text_to_image image.
const (
	artifactProviderID    = "mock"
	artifactModelID       = "pm_mock_v1"
	artifactOperationType = "text_to_image"
	artifactUnits         = 1
)

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

	// Idempotency context is shared by both the reuse and the generate paths so
	// a same-key replay returns the same job regardless of which path created it.
	idemKey := r.Header.Get(idempotency.HeaderKey)
	endpoint := r.Method + " " + r.URL.Path
	requestHash := jobs.HashRequestBody(raw)

	// Phase 6A2 retrieval-before-generation: before reserving cost or enqueuing
	// provider work, look for an existing ready artifact with this exact render
	// hash. Exact reuse is allowed for EVERY fallback_policy (including none) —
	// fallback_policy gates compatible/preview fallback, not exact reuse.
	if h.Reuse != nil {
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

	params := jobs.CreateAndEnqueueParams{
		TenantID:           principal.TenantID,
		RequestedByTokenID: principal.TokenID,
		JobType:            "artifact",
		WorldID:            req.WorldId,
		InputPayload:       payload,
		FallbackPolicy:     fallback,
		CacheResult:        "generated_required",
		ProviderID:         artifactProviderID,
		ModelID:            artifactModelID,
		OperationType:      artifactOperationType,
		Units:              artifactUnits,
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
