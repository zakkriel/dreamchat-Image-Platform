// Package ratelimit implements Phase 7C-2 per-token request-rate limiting.
//
// The limiter uses a fixed-window Redis counter per token, per window type
// (requests-per-minute and requests-per-hour), keyed on an aligned time
// bucket:
//
//	rate_limit:token:<token_id>:rpm:<yyyyMMddHHmm>
//	rate_limit:token:<token_id>:rph:<yyyyMMddHH>
//
// Each request increments both windows (separate keys). The counter increment
// and its TTL are created atomically (see Store) so a connection drop between
// the two can never leave a permanent key.
//
// Fixed-window trade-off: a denied request still increments the counter, which
// is intentional and affects Remaining/Retry-After math (a burst near a window
// boundary can be served twice as fast as the nominal rate). This is the
// documented cost of a cheap, dependency-light fixed window — see
// docs/api/rate-limits.md.
//
// Request-rate limiting fails OPEN: if Redis is unreachable the request is
// allowed and rate-limit headers are omitted. The Postgres-backed concurrent
// cap (internal/jobs) is unaffected by a Redis outage.
package ratelimit

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/zakkriel/drchat-image-platform/internal/auth"
)

// Store is the minimal backing-store contract the limiter needs: a single
// atomic "increment this fixed-window counter, creating its TTL on first
// increment" operation. Kept tiny so unit tests can supply an in-memory fake
// and the limiter logic is exercised without Redis.
type Store interface {
	// Increment atomically increments the counter at key and, when the key is
	// created by this call (count transitions to 1), sets its TTL. It returns
	// the post-increment count.
	Increment(ctx context.Context, key string, ttl time.Duration) (count int64, err error)
}

// Limiter enforces per-token request-rate limits against a Store. The zero
// value and a nil *Limiter are both safe: Enabled reports false and Middleware
// passes every request through (dev/test without Redis).
type Limiter struct {
	store  Store
	now    func() time.Time
	logger *slog.Logger
}

// New builds a Limiter over the given Store. A nil store yields a disabled
// limiter (every request allowed) so callers do not need to special-case the
// no-Redis path. logger may be nil (falls back to slog.Default).
func New(store Store, logger *slog.Logger) *Limiter {
	if logger == nil {
		logger = slog.Default()
	}
	return &Limiter{store: store, now: time.Now, logger: logger}
}

// Enabled reports whether the limiter has a backing store. A nil receiver or a
// nil store is disabled.
func (l *Limiter) Enabled() bool {
	return l != nil && l.store != nil
}

// WindowResult is one fixed window's decision data, used for the response
// headers.
type WindowResult struct {
	Limit     int
	Remaining int
	Reset     int64 // unix seconds at which the current window resets
}

// Result is the outcome of an Allow decision across both windows.
type Result struct {
	Allowed bool
	Minute  WindowResult
	Hour    WindowResult
	// RetryAfter is seconds until the blocking window resets. Only meaningful
	// when Allowed is false.
	RetryAfter int
}

// Allow increments both fixed-window counters for the token and decides whether
// the request is within the per-token limits. Both windows are always
// incremented (a denied request still counts — the fixed-window trade-off).
// Any store error is returned so the caller can fail open.
func (l *Limiter) Allow(ctx context.Context, tokenID string, limits auth.Limits) (Result, error) {
	now := l.now().UTC()
	minReset := now.Truncate(time.Minute).Add(time.Minute)
	hourReset := now.Truncate(time.Hour).Add(time.Hour)

	minCount, err := l.store.Increment(ctx, minuteKey(tokenID, now), time.Minute)
	if err != nil {
		return Result{}, err
	}
	hourCount, err := l.store.Increment(ctx, hourKey(tokenID, now), time.Hour)
	if err != nil {
		return Result{}, err
	}

	res := Result{
		Minute: WindowResult{Limit: limits.RequestsPerMinute, Remaining: remaining(limits.RequestsPerMinute, minCount), Reset: minReset.Unix()},
		Hour:   WindowResult{Limit: limits.RequestsPerHour, Remaining: remaining(limits.RequestsPerHour, hourCount), Reset: hourReset.Unix()},
	}

	overMinute := minCount > int64(limits.RequestsPerMinute)
	overHour := hourCount > int64(limits.RequestsPerHour)
	res.Allowed = !overMinute && !overHour
	if !res.Allowed {
		// When the hour window is exceeded the request stays blocked until the
		// hour resets, regardless of the minute window — so Retry-After tracks
		// the window that clears the denial.
		reset := minReset
		if overHour {
			reset = hourReset
		}
		secs := int(reset.Unix() - now.Unix())
		if secs < 1 {
			secs = 1
		}
		res.RetryAfter = secs
	}
	return res, nil
}

func remaining(limit int, count int64) int {
	r := int64(limit) - count
	if r < 0 {
		return 0
	}
	return int(r)
}

func minuteKey(tokenID string, t time.Time) string {
	return fmt.Sprintf("rate_limit:token:%s:rpm:%s", tokenID, t.Format("200601021504"))
}

func hourKey(tokenID string, t time.Time) string {
	return fmt.Sprintf("rate_limit:token:%s:rph:%s", tokenID, t.Format("2006010215"))
}

// Header names for the request-rate limits (documented in
// docs/api/rate-limits.md and the OpenAPI spec).
const (
	HeaderRPM          = "X-RateLimit-Requests-Per-Minute"
	HeaderRPMRemaining = "X-RateLimit-Requests-Per-Minute-Remaining"
	HeaderRPMReset     = "X-RateLimit-Requests-Per-Minute-Reset"
	HeaderRPH          = "X-RateLimit-Requests-Per-Hour"
	HeaderRPHRemaining = "X-RateLimit-Requests-Per-Hour-Remaining"
	HeaderRPHReset     = "X-RateLimit-Requests-Per-Hour-Reset"
)

// WriteHeaders stamps the request-rate headers for both windows onto h. Reset
// is a Unix timestamp (seconds) at which the current fixed window resets.
func (res Result) WriteHeaders(h http.Header) {
	h.Set(HeaderRPM, strconv.Itoa(res.Minute.Limit))
	h.Set(HeaderRPMRemaining, strconv.Itoa(res.Minute.Remaining))
	h.Set(HeaderRPMReset, strconv.FormatInt(res.Minute.Reset, 10))
	h.Set(HeaderRPH, strconv.Itoa(res.Hour.Limit))
	h.Set(HeaderRPHRemaining, strconv.Itoa(res.Hour.Remaining))
	h.Set(HeaderRPHReset, strconv.FormatInt(res.Hour.Reset, 10))
}
