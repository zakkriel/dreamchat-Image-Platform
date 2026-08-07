package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/zakkriel/drchat-image-platform/internal/assets"
	"github.com/zakkriel/drchat-image-platform/internal/audit"
	"github.com/zakkriel/drchat-image-platform/internal/auth"
	"github.com/zakkriel/drchat-image-platform/internal/governance"
	"github.com/zakkriel/drchat-image-platform/internal/http/apigen"
	"github.com/zakkriel/drchat-image-platform/internal/httperr"
	"github.com/zakkriel/drchat-image-platform/internal/idempotency"
	"github.com/zakkriel/drchat-image-platform/internal/identities"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
	"github.com/zakkriel/drchat-image-platform/internal/providers/routing"
)

// AuditSink is the handler-facing audit-emission seam. Implementations open a
// tenant-scoped DB connection (appdb.WithTenant + audit.Emit) and close it;
// unit tests use a fake that records events in memory.
//
// tenantID is threaded in separately (not embedded in the event) so the sink
// can scope its DB queries without requiring the handler to know about the DB.
type AuditSink interface {
	Emit(ctx context.Context, tenantID string, ev audit.Event) error
}

// GenerationReuseLookup is the narrow combined-contract exact-reuse dependency
// (the /v1/generations analogue of ArtifactReuseLookup): the deterministic
// generation-render-hash lookup the handler consults after the governance gate
// clears and before route resolution or cost reservation. assets.Repository
// satisfies it; nil disables reuse (the handler generates as before).
type GenerationReuseLookup interface {
	FindReadyGenerationByPromptHash(ctx context.Context, tenantID, promptHash string) (assets.VisualAsset, error)
}

// platformCeiling is the maximum megapixel value the platform will accept
// from a caller. Requests above this are clamped to this ceiling.
const platformCeiling float64 = 4.0

// capabilityIdentityCapable is required by the POST /v1/generations endpoint:
// a provider able to render an identity-consistent image.
const capabilityIdentityCapable = "identity_capable"

// generationsOperationType is the operation type for the /v1/generations endpoint.
const generationsOperationType = "text_to_image"

// GenerationsHandler handles POST /v1/generations requests. It is the
// chokepoint for the combined contract (governance + subject + render + grid),
// running validation, 501 gates, idempotency reconcile, identity fetch,
// governance verification, cost-routing, MP clamp, payload seeding, and
// CreateAndEnqueue.
type GenerationsHandler struct {
	Service    jobs.Creator
	Resolver   RouteResolver
	Identities identities.Repository

	// Verifier is the governance.Verifier applied at the chokepoint (Task 8).
	// When nil the gate is skipped (log_only no-op); wired in production by router.go.
	Verifier governance.Verifier
	// Mode is governance.ModeEnforce or ModeLogOnly. Only consulted when Verifier
	// is non-nil.
	Mode governance.Mode
	// Audit emits media.eligibility_verified / media.eligibility_blocked events.
	// Required when Verifier is non-nil; may be nil-safe (events are best-effort).
	Audit AuditSink

	// Reuse is the combined-contract exact-reuse lookup (retrieval before
	// generation, ADR-009). Optional; nil-safe (reuse is skipped when unwired).
	Reuse GenerationReuseLookup
}

// NewGenerationsHandler wires a GenerationsHandler.
func NewGenerationsHandler(svc jobs.Creator, resolver RouteResolver, idRepo identities.Repository) *GenerationsHandler {
	return &GenerationsHandler{
		Service:    svc,
		Resolver:   resolver,
		Identities: idRepo,
	}
}

// Create handles POST /v1/generations. Steps:
//  1. Extract principal (tenant, token).
//  2. readRawJSONBody + rejectBodyTenantID (covered by shared helper).
//  3. Decode with DisallowUnknownFields → 422 on unknown fields or decode error.
//  4. Validate required fields (governance, subject.identity_id, intent enum,
//     transform schema_version if present, grid shape) → 422.
//  5. 501 checks (transform_only / grid.enabled) — before identity fetch to avoid
//     a wasted DB round-trip on well-formed-but-deferred requests.
//  6. Idempotency reconcile: body key required; header≠body → 422; header-only → 422.
//  7. Fetch the subject identity (existence + description source) → 422 on ErrNotFound.
//  8. handleReplay pre-check.
//  9. Build ResolveRequest (Intent + identity_capable floor, QualityTier EMPTY, ProviderID pin).
//
// 10. Resolve → writeRouteError on error; applyResolvedRoute.
// 11. MP clamp; seed payload["description"]=identity.DisplayName; map params; CreateAndEnqueue → 202.
func (h *GenerationsHandler) Create(w http.ResponseWriter, r *http.Request) {
	// Step 1: extract principal.
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "missing principal")
		return
	}
	tenantID := principal.TenantID

	// Step 2: read raw body (for idempotency hash) + reject body tenant_id.
	raw, ok := readRawJSONBody(w, r)
	if !ok {
		return
	}

	// Step 3: decode with DisallowUnknownFields → 422 (not 400) per contract.
	var req apigen.GenerationRequest
	dec := newJSONDecoder(raw)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		httperr.Write(w, r, http.StatusUnprocessableEntity, httperr.CodeInvalidRequest, "invalid or unknown field in request body: "+err.Error())
		return
	}

	// Step 4: validate required fields.

	// Governance: all required fields must be non-empty, including schema_version.
	gov := req.Governance
	if gov.SchemaVersion == "" {
		httperr.Write(w, r, http.StatusUnprocessableEntity, httperr.CodeInvalidRequest, "governance.schema_version is required")
		return
	}
	if gov.ClassificationId == "" {
		httperr.Write(w, r, http.StatusUnprocessableEntity, httperr.CodeInvalidRequest, "governance.classification_id is required")
		return
	}
	if gov.Visibility == "" {
		httperr.Write(w, r, http.StatusUnprocessableEntity, httperr.CodeInvalidRequest, "governance.visibility is required")
		return
	}
	if gov.ContentClass == "" {
		httperr.Write(w, r, http.StatusUnprocessableEntity, httperr.CodeInvalidRequest, "governance.content_class is required")
		return
	}
	if gov.AuthorizedBy == "" {
		httperr.Write(w, r, http.StatusUnprocessableEntity, httperr.CodeInvalidRequest, "governance.authorized_by is required")
		return
	}
	if gov.Signature == "" {
		httperr.Write(w, r, http.StatusUnprocessableEntity, httperr.CodeInvalidRequest, "governance.signature is required")
		return
	}

	// Subject: identity_id required.
	if req.Subject.IdentityId == "" {
		httperr.Write(w, r, http.StatusUnprocessableEntity, httperr.CodeInvalidRequest, "subject.identity_id is required")
		return
	}

	// Render: intent must be "draft" or "commit".
	if req.Render.Intent != apigen.IntentDraft && req.Render.Intent != apigen.IntentCommit {
		httperr.Write(w, r, http.StatusUnprocessableEntity, httperr.CodeInvalidRequest, "render.intent must be one of: draft, commit")
		return
	}

	// Transform: if present, requires schema_version inside the map (D-4).
	if req.Render.Transform != nil {
		m := *req.Render.Transform
		sv, hasVersion := m["schema_version"]
		svStr, _ := sv.(string)
		if !hasVersion || svStr == "" {
			httperr.Write(w, r, http.StatusUnprocessableEntity, httperr.CodeInvalidRequest, "render.transform requires schema_version when present")
			return
		}
	}

	// Grid shape: no specific field validation beyond the 501 check below, but
	// cells must not be present without enabled (guard nil before enabled check).

	// Step 5: 501 checks — well-formed but deferred behavior. Checked BEFORE
	// the identity DB fetch to avoid a wasted round-trip on deferred requests.
	if req.Render.TransformOnly != nil && *req.Render.TransformOnly {
		httperr.Write(w, r, http.StatusNotImplemented, httperr.CodeTransformOnlyNotSupported, "transform_only is not supported in this version")
		return
	}
	if req.Grid != nil && req.Grid.Enabled {
		httperr.Write(w, r, http.StatusNotImplemented, httperr.CodeGridNotSupported, "grid is not supported in this version")
		return
	}

	// Step 6: idempotency reconcile. Body key is required; header, if present,
	// must match body key (the caller canonicalizes one key, not two).
	bodyKey := req.IdempotencyKey
	if bodyKey == "" {
		httperr.Write(w, r, http.StatusUnprocessableEntity, httperr.CodeInvalidRequest, "idempotency_key is required in the request body")
		return
	}
	headerKey := r.Header.Get(idempotency.HeaderKey)
	if headerKey != "" && headerKey != bodyKey {
		httperr.Write(w, r, http.StatusUnprocessableEntity, httperr.CodeInvalidRequest, "Idempotency-Key header must match body idempotency_key when both are present")
		return
	}

	endpoint := r.Method + " " + r.URL.Path
	requestHash := jobs.HashRequestBody(raw)

	// Step 7: fetch subject identity (existence + description source).
	// Done after 501 checks so deferred requests do not incur a DB round-trip.
	identity, err := h.Identities.GetByIDForTenant(r.Context(), req.Subject.IdentityId, tenantID)
	if err != nil {
		if errors.Is(err, identities.ErrNotFound) {
			httperr.Write(w, r, http.StatusUnprocessableEntity, httperr.CodeInvalidRequest, "subject.identity_id not found")
			return
		}
		httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "could not fetch subject identity")
		return
	}

	// Step 8: handleReplay pre-check (the effective key is the body key).
	if handleReplay(w, r, h.Service, tenantID, principal.TokenID, bodyKey, endpoint, requestHash) {
		return
	}

	// Step 8b: governance gate — runs AFTER idempotency and BEFORE route
	// resolution and cost reservation. A block in enforce mode short-circuits
	// here: neither the resolver nor CreateAndEnqueue is called, so no
	// cost_reservations row is ever written for a blocked request.
	//
	// governanceVerifiedAt is non-nil when the gate passes (res.OK=true); it
	// is stamped onto CreateAndEnqueueParams below so the worker knows the
	// envelope was verified at request time.
	var governanceVerifiedAt *time.Time
	if h.Verifier != nil {
		env := governance.Envelope{
			SchemaVersion:    req.Governance.SchemaVersion,
			ClassificationID: req.Governance.ClassificationId,
			Visibility:       req.Governance.Visibility,
			ContentClass:     req.Governance.ContentClass,
			AuthorizedBy:     req.Governance.AuthorizedBy,
			IssuedAt:         req.Governance.IssuedAt,
			Signature:        req.Governance.Signature,
		}
		// SubjectMeta carries ONLY the identity ID refs — never the fetched
		// identity's DisplayName, traits, or any prompt/description text.
		subj := governance.SubjectMeta{
			IdentityID:    req.Subject.IdentityId,
			AnchorAssetID: deref(req.Subject.AnchorAssetId),
			DeriveFrom:    deref(req.Subject.DeriveFrom),
		}
		res := h.Verifier.Verify(r.Context(), env, subj)
		proceed, eventType := governance.Decide(h.Mode, res)
		if h.Audit != nil {
			_ = h.Audit.Emit(r.Context(), tenantID, audit.Event{
				EventType:    eventType,
				TenantID:     tenantID,
				ActorTokenID: principal.TokenID,
				ResourceType: "generation",
				Metadata: map[string]any{
					"reason":            res.Reason,
					"classification_id": req.Governance.ClassificationId,
					"content_class":     req.Governance.ContentClass, // opaque: stored/logged, never parsed
					"mode":              string(h.Mode),
				},
			})
		}
		if !proceed {
			httperr.Write(w, r, http.StatusForbidden, httperr.CodeGovernanceBlocked, "governance verification failed: "+res.Reason)
			return
		}
		if res.OK {
			now := time.Now()
			governanceVerifiedAt = &now
		}
	}

	// Combined-contract exact reuse (retrieval-before-generation, ADR-009).
	// The deterministic generation render hash is computed from the request's
	// render-determining fields (identity, display name, anchors, derive_from,
	// intent, transform) and doubles as the produced asset's prompt_hash, so
	// every /v1/generations output joins the reuse economy. A hit completes
	// synchronously with zero cost — no route resolution, no reservation, no
	// provider call. Runs AFTER the governance gate (eligibility is verified
	// and audited even for reused output) and after idempotency replay.
	transformJSON := ""
	if req.Render.Transform != nil {
		if b, merr := json.Marshal(req.Render.Transform); merr == nil {
			transformJSON = string(b)
		}
	}
	renderHash := assets.GenerationRenderHash(assets.GenerationHashInput{
		TenantID:      tenantID,
		IdentityID:    req.Subject.IdentityId,
		DisplayName:   identity.DisplayName,
		AnchorAssetID: deref(req.Subject.AnchorAssetId),
		DeriveFrom:    deref(req.Subject.DeriveFrom),
		Intent:        string(req.Render.Intent),
		TransformJSON: transformJSON,
	})
	if h.Reuse != nil {
		existing, rerr := h.Reuse.FindReadyGenerationByPromptHash(r.Context(), tenantID, renderHash)
		switch {
		case rerr == nil:
			h.respondCacheHit(w, r, tenantID, principal.TokenID, req, renderHash, existing.ID, bodyKey, endpoint, requestHash)
			return
		case errors.Is(rerr, assets.ErrNotFound):
			// miss: fall through to the normal resolve/reserve/enqueue path.
		default:
			httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "could not check generation reuse")
			return
		}
	}

	// Step 9: build ResolveRequest.
	// Floor: identity_capable (identity_id is required by this endpoint, so the
	// floor is always identity_capable; the conditional is for clarity/futureproofing).
	floor := capabilityIdentityCapable
	if req.Subject.IdentityId == "" {
		floor = capabilitySceneCapable
	}

	// ProviderID pin from the render options (may be nil → empty → no pin).
	requestedProvider := ""
	if req.Render.ProviderId != nil {
		requestedProvider = *req.Render.ProviderId
	}

	resolveReq := routing.ResolveRequest{
		TenantID:      tenantID,
		OperationType: generationsOperationType,
		// QualityTier intentionally left EMPTY: a set QualityTier hard-filters
		// BEFORE intent ranking and would defeat draft/commit selection (Task 6).
		Intent:             string(req.Render.Intent),
		RequiredCapability: floor,
		ProviderID:         requestedProvider,
	}

	// Step 10: resolve provider route.
	resolved, err := h.Resolver.Resolve(r.Context(), resolveReq)
	if err != nil {
		writeRouteError(w, r, err)
		return
	}

	// Build the job payload and params. applyResolvedRoute stamps the resolved
	// provider/model/route onto both params and payload.
	payload := map[string]any{}
	params := jobs.CreateAndEnqueueParams{
		TenantID:           tenantID,
		RequestedByTokenID: principal.TokenID,
		JobType:            "generation",
		InputPayload:       payload,
		CacheResult:        "generated_required",
		Units:              1,
		MaxConcurrentJobs:  principal.Limits.MaxConcurrentJobs,
	}
	applyResolvedRoute(&params, payload, resolved)

	// Step 11: MP clamp + identity-derived description + contract objects → params.

	// Clamp max_megapixels (nil → ceiling).
	clamped := clampMegapixels(req.Render.MaxMegapixels, platformCeiling)
	params.MaxMegapixels = &clamped

	// Seed payload["description"] from the fetched identity (identity-derived prompt).
	// This mirrors the pack flow's identity.DisplayName → payload["display_name"] path.
	payload["description"] = identity.DisplayName
	// Stamp the deterministic render hash so the worker persists it as the
	// produced asset's prompt_hash (the reuse cache key) and the effective
	// quality tier from the resolved route (the intent-driven path resolves
	// without a hard quality filter, so the route's tier IS the effective tier).
	payload["prompt_hash"] = renderHash
	if resolved.QualityTier != "" {
		payload["quality_tier"] = resolved.QualityTier
	}

	// Store raw contract objects in the payload for worker observability.
	payload["identity_id"] = req.Subject.IdentityId
	payload["intent"] = string(req.Render.Intent)
	if req.Subject.AnchorAssetId != nil {
		payload["anchor_asset_id"] = *req.Subject.AnchorAssetId
	}
	if req.Subject.DeriveFrom != nil {
		payload["derive_from"] = *req.Subject.DeriveFrom
	}
	if req.Lazy != nil {
		payload["lazy"] = *req.Lazy
	}
	payload["max_megapixels"] = clamped

	// Map governance/subject/render fields onto CreateAndEnqueueParams.
	govJSON, _ := json.Marshal(gov)
	params.GovernanceEnvelope = govJSON

	classID := gov.ClassificationId
	params.ClassificationID = &classID

	vis := gov.Visibility
	params.Visibility = &vis

	cc := gov.ContentClass
	params.ContentClass = &cc

	ab := gov.AuthorizedBy
	params.AuthorizedBy = &ab

	intent := string(req.Render.Intent)
	params.Intent = &intent

	if req.Render.TransformOnly != nil {
		params.TransformOnly = req.Render.TransformOnly
	}
	if req.Render.Transform != nil {
		transformJSON, _ := json.Marshal(req.Render.Transform)
		params.Transform = transformJSON
	}
	if req.Lazy != nil {
		params.Lazy = req.Lazy
	}
	if req.Subject.AnchorAssetId != nil {
		params.AnchorAssetID = req.Subject.AnchorAssetId
	}
	if req.Subject.DeriveFrom != nil {
		params.DeriveFrom = req.Subject.DeriveFrom
	}

	// Idempotency.
	params.IdempotencyKey = bodyKey
	params.Endpoint = endpoint
	params.RequestHash = requestHash

	// GovernanceVerifiedAt: set when the governance gate passed (res.OK=true).
	params.GovernanceVerifiedAt = governanceVerifiedAt

	result, err := h.Service.CreateAndEnqueue(r.Context(), params)
	if err != nil {
		writeJobServiceError(w, r, err)
		return
	}

	writeJobAccepted(w, result)
}

// respondCacheHit lands an already-completed cache-hit job for a combined-
// contract exact reuse (no route resolution, no cost reservation, no provider
// attempt, no enqueue) and shapes the 202 acceptance envelope. Mirrors the
// artifact cache-hit path: the synchronously-completed state,
// cache_result=exact_match, and final_asset_ids are observed via
// GET /v1/jobs/{job_id}; estimated_cost_usd is "0.0000" — the reuse is free.
func (h *GenerationsHandler) respondCacheHit(w http.ResponseWriter, r *http.Request, tenantID, tokenID string, req apigen.GenerationRequest, renderHash, assetID, idemKey, endpoint, requestHash string) {
	result, err := h.Service.CreateCompletedCacheHitJob(r.Context(), jobs.CreateCacheHitParams{
		TenantID:           tenantID,
		RequestedByTokenID: tokenID,
		JobType:            "generation",
		InputPayload: map[string]any{
			"identity_id": req.Subject.IdentityId,
			"intent":      string(req.Render.Intent),
			"prompt_hash": renderHash,
		},
		FinalAssetID:   assetID,
		IdempotencyKey: idemKey,
		Endpoint:       endpoint,
		RequestHash:    requestHash,
	})
	if err != nil {
		writeJobServiceError(w, r, err)
		return
	}
	est := "0.0000"
	if result.EstimatedCostUSD != "" {
		est = result.EstimatedCostUSD
	}
	writeJSON(w, http.StatusAccepted, apigen.GenerationJobAccepted{
		JobId:            result.JobID,
		Status:           apigen.GenerationJobAcceptedStatusQueued,
		EstimatedCostUsd: &est,
	})
}

// clampMegapixels returns the lesser of the requested value and ceiling. When p
// is nil (omitted by the caller), the platform ceiling is returned.
func clampMegapixels(p *float32, ceiling float64) float64 {
	if p == nil {
		return ceiling
	}
	requested := float64(*p)
	if requested > ceiling {
		return ceiling
	}
	return requested
}
