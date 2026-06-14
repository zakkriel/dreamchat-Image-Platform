package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/zakkriel/drchat-image-platform/internal/httperr"
	"github.com/zakkriel/drchat-image-platform/internal/telemetry"
)

const bearerPrefix = "Bearer "

// touchTimeout caps the best-effort last_used_at update so a slow DB never
// stalls the goroutine.
const touchTimeout = time.Second

type RejectReason string

const (
	ReasonMissing            RejectReason = "missing_authorization"
	ReasonMalformed          RejectReason = "malformed_authorization"
	ReasonUnknownToken       RejectReason = "unknown_token"
	ReasonHashMismatch       RejectReason = "hash_mismatch"
	ReasonInactive           RejectReason = "inactive_token"
	ReasonExpired            RejectReason = "expired_token"
	ReasonEnvironmentInvalid RejectReason = "environment_mismatch"
	ReasonBackendError       RejectReason = "backend_error"
)

// RejectedError is returned by Verify when authentication should fail. It
// carries the token prefix (when known and non-secret) and a structured
// reason. The raw secret portion is never embedded in this error.
type RejectedError struct {
	Reason RejectReason
	Prefix string
	Err    error
}

func (e *RejectedError) Error() string {
	if e.Err != nil {
		return "auth rejected: " + string(e.Reason) + ": " + e.Err.Error()
	}
	return "auth rejected: " + string(e.Reason)
}

func (e *RejectedError) Unwrap() error { return e.Err }

// Verify performs the full token-validation pipeline against the repository
// and returns either a populated Principal or a RejectedError. Callers that
// need raw control over the response (e.g. the docs-gating wrapper that
// reports 404 instead of 401) should use this directly. Verify never logs
// or returns the raw bearer token or secret portion.
func Verify(ctx context.Context, repo Repository, authorization, pepper, environment string) (*Principal, *RejectedError) {
	if authorization == "" {
		return nil, &RejectedError{Reason: ReasonMissing}
	}
	if !strings.HasPrefix(authorization, bearerPrefix) {
		return nil, &RejectedError{Reason: ReasonMalformed}
	}
	raw := strings.TrimSpace(strings.TrimPrefix(authorization, bearerPrefix))
	if raw == "" {
		return nil, &RejectedError{Reason: ReasonMalformed}
	}
	prefix, secret, ok := splitToken(raw)
	if !ok {
		return nil, &RejectedError{Reason: ReasonMalformed}
	}

	token, err := repo.GetActiveAPITokenByPrefix(ctx, prefix)
	if err != nil {
		if errors.Is(err, ErrTokenNotFound) {
			return nil, &RejectedError{Reason: ReasonUnknownToken, Prefix: prefix}
		}
		return nil, &RejectedError{Reason: ReasonBackendError, Prefix: prefix, Err: err}
	}

	computed := hashSecret(secret, pepper)
	expected, decodeErr := hex.DecodeString(token.TokenHash)
	if decodeErr != nil || subtle.ConstantTimeCompare(computed, expected) != 1 {
		return nil, &RejectedError{Reason: ReasonHashMismatch, Prefix: prefix}
	}

	if token.Status != "active" {
		return nil, &RejectedError{Reason: ReasonInactive, Prefix: prefix}
	}
	if token.ExpiresAt != nil && !token.ExpiresAt.After(time.Now()) {
		return nil, &RejectedError{Reason: ReasonExpired, Prefix: prefix}
	}
	if token.Environment != environment {
		return nil, &RejectedError{Reason: ReasonEnvironmentInvalid, Prefix: prefix}
	}

	go touchLastUsed(repo, token.ID)

	return &Principal{
		TokenID:     token.ID,
		TenantID:    token.TenantID,
		Scopes:      token.Scopes,
		Environment: token.Environment,
		Limits:      effectiveLimits(token),
	}, nil
}

// effectiveLimits resolves a token's per-token limit overrides (Phase 7C-2)
// against the platform defaults: a non-nil, positive override wins; otherwise
// the default applies. A non-positive or NULL override falls back to the
// default so a misconfigured 0 can never silently block every request.
func effectiveLimits(t Token) Limits {
	l := DefaultLimits()
	if t.RateLimitRPM != nil && *t.RateLimitRPM > 0 {
		l.RequestsPerMinute = int(*t.RateLimitRPM)
	}
	if t.RateLimitRPH != nil && *t.RateLimitRPH > 0 {
		l.RequestsPerHour = int(*t.RateLimitRPH)
	}
	if t.MaxConcurrentJobs != nil && *t.MaxConcurrentJobs > 0 {
		l.MaxConcurrentJobs = int(*t.MaxConcurrentJobs)
	}
	return l
}

// Middleware returns a chi-compatible middleware that authenticates incoming
// requests against the api_tokens table. The middleware attaches the
// resolved Principal to the request context on success and writes a 401
// problem+json response on any failure.
func Middleware(repo Repository, pepper, environment string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, rej := Verify(r.Context(), repo, r.Header.Get("Authorization"), pepper, environment)
			if rej != nil {
				logRejected(r, rej)
				if rej.Reason == ReasonBackendError {
					httperr.Write(w, r, http.StatusInternalServerError, httperr.CodeInternalError, "authentication backend unavailable")
					return
				}
				httperr.Write(w, r, http.StatusUnauthorized, httperr.CodeUnauthorized, "invalid or missing bearer token")
				return
			}
			telemetry.RequestLogFromContext(r.Context()).SetIdentity(principal.TenantID, principal.TokenID)
			next.ServeHTTP(w, r.WithContext(ContextWithPrincipal(r.Context(), principal)))
		})
	}
}

// RequireScopes returns a middleware that allows the request through only if
// the resolved Principal carries every scope in the required set.
func RequireScopes(scopes ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal := PrincipalFromContext(r.Context())
			if principal == nil {
				httperr.Write(w, r, http.StatusForbidden, httperr.CodeForbidden, "missing principal for scope check")
				return
			}
			for _, scope := range scopes {
				if !principal.HasScope(scope) {
					httperr.Write(w, r, http.StatusForbidden, httperr.CodeForbidden, "token missing required scope")
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func splitToken(raw string) (prefix, secret string, ok bool) {
	idx := strings.LastIndex(raw, "_")
	if idx <= 0 || idx == len(raw)-1 {
		return "", "", false
	}
	return raw[:idx], raw[idx+1:], true
}

func hashSecret(secret, pepper string) []byte {
	sum := sha256.Sum256([]byte(secret + pepper))
	return sum[:]
}

func logRejected(r *http.Request, rej *RejectedError) {
	attrs := []slog.Attr{
		slog.String("request_id", telemetry.RequestIDFromContext(r.Context())),
		slog.String("reason", string(rej.Reason)),
		slog.String("token_prefix", rej.Prefix),
	}
	if rej.Reason == ReasonBackendError && rej.Err != nil {
		attrs = append(attrs, slog.String("error", rej.Err.Error()))
		slog.Default().LogAttrs(r.Context(), slog.LevelError, "auth_lookup_failed", attrs...)
		return
	}
	slog.Default().LogAttrs(r.Context(), slog.LevelInfo, "auth_rejected", attrs...)
}

func touchLastUsed(repo Repository, id string) {
	ctx, cancel := context.WithTimeout(context.Background(), touchTimeout)
	defer cancel()
	if err := repo.TouchAPITokenLastUsed(ctx, id); err != nil {
		slog.Default().LogAttrs(ctx, slog.LevelWarn, "token_touch_failed",
			slog.String("token_id", id),
			slog.String("error", err.Error()),
		)
	}
}
