package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/zakkriel/drchat-image-platform/internal/http/apigen"
	"github.com/zakkriel/drchat-image-platform/internal/httperr"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
	"github.com/zakkriel/drchat-image-platform/internal/providers/routing"
)

// Capability requirements each generation path imposes on route resolution
// (Phase 7A). These select the provider-route `required_capability` the resolver
// must match.
const (
	// capabilitySceneCapable is required by single-image generation (artifact +
	// style preview): a provider able to render a coherent scene/subject.
	capabilitySceneCapable = "scene_capable"
	// capabilityPackCapable is required by pack generation: a provider able to
	// produce an identity-consistent multi-role pack. Mock advertises it; BFL
	// (conservative floor) does not, so BFL cannot serve packs in 7A.
	capabilityPackCapable = "pack_capable"
	// previewCapabilityTruePreview is the hard preview requirement a
	// delivery_mode=preview_first request imposes on route resolution (Phase 7B):
	// only a route whose preview_capability is true_preview can serve it. Mock
	// advertises true_preview; BFL advertises no_preview, so BFL is excluded from
	// preview-first. There is no derived_preview fallback in 7B.
	previewCapabilityTruePreview = "true_preview"
)

// RouteResolver is the handler-facing view of the provider route resolver
// (internal/providers/routing). The handler resolves a route ONCE, at request
// time, before reserving cost — the worker consumes the persisted result and
// never re-resolves. Kept a narrow interface so handler tests can supply an
// in-memory resolver.
type RouteResolver interface {
	Resolve(ctx context.Context, req routing.ResolveRequest) (routing.ResolvedRoute, error)
}

// writeRouteError maps a routing failure to its 422 error code. All routing
// failures are 422 (the request was well-formed; no route can serve it) — this
// replaces the pre-7A 503 provider_unavailable gate.
func writeRouteError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, routing.ErrUnsupportedCapability):
		httperr.Write(w, r, http.StatusUnprocessableEntity, httperr.CodeUnsupportedCapability, "no provider route satisfies the requested capability")
	case errors.Is(err, routing.ErrProviderUnavailableForRoute):
		httperr.Write(w, r, http.StatusUnprocessableEntity, httperr.CodeProviderUnavailableForRoute, "the matching provider route is not available")
	case errors.Is(err, routing.ErrNoRoute):
		httperr.Write(w, r, http.StatusUnprocessableEntity, httperr.CodeNoRoute, "no provider route satisfies this request")
	default:
		httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "could not resolve provider route")
	}
}

// handleReplay runs the idempotency replay pre-check (Phase 7A): it executes
// BEFORE route resolution and cost reservation. It returns handled=true when the
// request was a replay (existing job echoed), a conflict, or a previously-denied
// job — a response has already been written and the caller must return.
// handled=false means it is a new request the caller should proceed to resolve
// and create.
func handleReplay(w http.ResponseWriter, r *http.Request, svc jobs.Creator, tokenID, idemKey, endpoint, requestHash string) bool {
	result, found, err := svc.LookupReplay(r.Context(), jobs.ReplayLookup{
		TokenID:     tokenID,
		Key:         idemKey,
		Endpoint:    endpoint,
		RequestHash: requestHash,
	})
	if err != nil {
		// Conflict (409) or a re-raised pre-flight denial (422); both mapped here.
		writeJobServiceError(w, r, err)
		return true
	}
	if !found {
		return false
	}
	writeJobAccepted(w, result)
	return true
}

// writeJobAccepted shapes the 202 acceptance envelope shared by the artifact and
// style-preview create + replay paths (the pack handler has its own shaper that
// also carries asset_pack_id).
func writeJobAccepted(w http.ResponseWriter, result jobs.CreateResult) {
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

// applyResolvedRoute stamps the resolved provider/model/route onto the cost
// pre-flight params AND the job input payload, so cost reservation prices the
// resolved model and the worker consumes the persisted resolved route
// (generation_jobs has no provider_id/model_id columns, so the payload is the
// carrier — Phase 7A job-persistence rule).
func applyResolvedRoute(params *jobs.CreateAndEnqueueParams, payload map[string]any, resolved routing.ResolvedRoute) {
	params.ProviderID = resolved.ProviderID
	params.ModelID = resolved.ProviderModelID
	params.ProviderRouteID = resolved.ProviderRouteID
	params.OperationType = resolved.OperationType

	// The jobs.Service is the authoritative persister (it stamps these onto the
	// payload from the params for every caller); writing them here too keeps the
	// values identical and lets handler-level tests observe the persisted route.
	payload["provider_id"] = resolved.ProviderID
	payload["model_id"] = resolved.ProviderModelID
	payload["provider_route_id"] = resolved.ProviderRouteID
	// Phase 7B: persist the resolved route's preview capability as provenance so
	// the worker can confirm a preview_first job resolved a true_preview route
	// without re-resolving. Harmless for final-only/pack jobs (the worker only
	// two-phases when delivery_mode == preview_first AND this == true_preview).
	if resolved.PreviewCapability != "" {
		payload["preview_capability"] = resolved.PreviewCapability
	}
}
