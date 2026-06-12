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
