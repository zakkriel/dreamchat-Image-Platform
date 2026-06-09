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
	CodeUnauthorized  = "unauthorized"
	CodeForbidden     = "forbidden"
	CodeNotFound      = "not_found"
	CodeInternalError = "internal_error"
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
