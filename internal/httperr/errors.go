// Package httperr renders the platform's standard error body to an HTTP
// response. Kept as a leaf package so both internal/http and internal/auth
// can import it without circling each other.
package httperr

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/zakkriel/drchat-image-platform/internal/telemetry"
)

const (
	CodeUnauthorized        = "unauthorized"
	CodeForbidden           = "forbidden"
	CodeNotFound            = "not_found"
	CodeInternalError       = "internal_error"
	CodeInvalidRequest      = "invalid_request"
	CodeInvalidStyleProfile = "invalid_style_profile"
	CodeIdempotencyConflict = "idempotency_conflict"
	CodeProviderUnavailable = "provider_unavailable"

	// Admin job control (Phase 7C-1). Returned when cancel/retry is requested
	// from a status the action does not allow (HTTP 409).
	CodeInvalidState = "invalid_state"

	// Rate limiting (Phase 7C-2). Both surface as HTTP 429. rate_limit_exceeded
	// is the per-token request-rate cap (carries Retry-After);
	// concurrent_jobs_exceeded is the per-token hard cap on live generation jobs
	// (no Retry-After — concurrency clears at a terminal state, not a fixed
	// window). Cost-budget denials stay 422 and are NOT moved here.
	CodeRateLimitExceeded      = "rate_limit_exceeded"
	CodeConcurrentJobsExceeded = "concurrent_jobs_exceeded"

	// Cost-control pre-flight (docs/architecture/cost-control.md §5). Both
	// surface as HTTP 422 in Phase 4 (see Phase 4 corrections 1, 2, 6, 7).
	CodeNoPriceEntry   = "no_price_entry"
	CodeBudgetExceeded = "budget_exceeded"

	// Provider route resolution (Phase 7A). All three surface as HTTP 422: the
	// request was well-formed but no provider route can serve it. They replace
	// the pre-7A 503 provider_unavailable gate.
	CodeNoRoute                     = "no_route"
	CodeUnsupportedCapability       = "unsupported_capability"
	CodeProviderUnavailableForRoute = "provider_unavailable_for_route"

	// CodeProviderPreferenceUnavailable surfaces (HTTP 422) when a request pins a
	// provider via provider_id (the per-request provider preference) that is not
	// configured/available in this process. Distinct from
	// provider_unavailable_for_route (a route's provider not being wired): here the
	// caller named a provider the deployment does not run, so the request fails
	// closed instead of silently resolving the default provider.
	CodeProviderPreferenceUnavailable = "provider_preference_unavailable"

	// Provider capability reconciliation (PRD 03 §8). Surfaces as HTTP 422: a
	// route matched the request but its provider adapter cannot actually back the
	// capability the route claims (config drift), so the platform fails closed
	// rather than route identity/pack work to an unsuitable provider. Distinct
	// from unsupported_capability (no route exists for the capability) and
	// provider_unavailable_for_route (provider not wired in this process).
	CodeRouteCapabilityMismatch = "route_capability_mismatch"

	// CodeInvalidAnchorAsset surfaces (HTTP 422) when attaching anchor assets to a
	// visual identity and a supplied asset cannot serve as a reference: not owned
	// by the tenant, not ready, missing a high-res object, or already bound to a
	// different identity (ADR-017).
	CodeInvalidAnchorAsset = "invalid_anchor_asset"

	// Chunk 2: deferred-behavior rejections (HTTP 501) — the request is
	// well-formed but actively invokes behavior not implemented yet, and routing
	// it through the single-image path would mis-cost (transform_only) or
	// mis-count (grid) the result.
	CodeTransformOnlyNotSupported = "transform_only_not_supported"
	CodeGridNotSupported          = "grid_not_supported"
	// CodeGovernanceBlocked surfaces (HTTP 403) when GOVERNANCE_ENFORCEMENT=enforce
	// and the media-eligibility envelope fails verification (Chunk 2).
	CodeGovernanceBlocked = "governance_blocked"
)

type Body struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

func Write(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	body := Body{
		Code:      code,
		Message:   message,
		RequestID: telemetry.RequestIDFromContext(r.Context()),
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Default().LogAttrs(r.Context(), slog.LevelError, "write_error_failed",
			slog.String("request_id", body.RequestID),
			slog.String("error", err.Error()),
		)
	}
}
