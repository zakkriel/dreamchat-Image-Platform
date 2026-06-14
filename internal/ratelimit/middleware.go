package ratelimit

import (
	"log/slog"
	"net/http"
	"strconv"

	"github.com/zakkriel/drchat-image-platform/internal/auth"
	"github.com/zakkriel/drchat-image-platform/internal/httperr"
	"github.com/zakkriel/drchat-image-platform/internal/telemetry"
)

// Middleware returns a chi-compatible middleware that enforces per-token
// request-rate limits. It MUST be mounted after the auth middleware so the
// authenticated Principal (and its effective limits) is on the context.
//
// Behavior:
//   - A disabled limiter (nil, or no Redis store configured) passes every
//     request through — dev/test without Redis is supported without forcing
//     Redis on every test.
//   - A missing principal (auth did not run / failed) passes through; auth is
//     responsible for rejecting unauthenticated requests before this point.
//   - On a Redis error the request is ALLOWED (fail open), a warning is logged,
//     and rate-limit headers are omitted.
//   - On an allow the request-rate headers are set for both windows.
//   - On an over-limit decision the request is rejected with
//     429 rate_limit_exceeded, carrying Retry-After and the rate-limit headers.
func Middleware(l *Limiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !l.Enabled() {
				next.ServeHTTP(w, r)
				return
			}
			principal := auth.PrincipalFromContext(r.Context())
			if principal == nil {
				next.ServeHTTP(w, r)
				return
			}

			res, err := l.Allow(r.Context(), principal.TokenID, principal.Limits)
			if err != nil {
				// Fail open: a Redis outage degrades request-rate limiting only;
				// the request proceeds and headers are omitted.
				l.logger.LogAttrs(r.Context(), slog.LevelWarn, "rate_limit_store_error",
					slog.String("request_id", telemetry.RequestIDFromContext(r.Context())),
					slog.String("token_id", principal.TokenID),
					slog.String("error", err.Error()),
				)
				next.ServeHTTP(w, r)
				return
			}

			res.WriteHeaders(w.Header())
			if !res.Allowed {
				w.Header().Set("Retry-After", strconv.Itoa(res.RetryAfter))
				httperr.Write(w, r, http.StatusTooManyRequests, httperr.CodeRateLimitExceeded, "request rate limit exceeded")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
