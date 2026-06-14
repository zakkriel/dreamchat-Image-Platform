package ratelimit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/zakkriel/drchat-image-platform/internal/auth"
)

// fakeStore is an in-memory Store for unit tests. It records, per key, the
// running count and how many times a TTL was set, so tests can assert the
// atomic "set TTL only on the first increment" contract without Redis.
type fakeStore struct {
	mu        sync.Mutex
	counts    map[string]int64
	ttls      map[string]time.Duration
	ttlSets   map[string]int
	err       error
	callCount int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		counts:  map[string]int64{},
		ttls:    map[string]time.Duration{},
		ttlSets: map[string]int{},
	}
}

func (f *fakeStore) Increment(_ context.Context, key string, ttl time.Duration) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callCount++
	if f.err != nil {
		return 0, f.err
	}
	f.counts[key]++
	c := f.counts[key]
	if c == 1 {
		f.ttls[key] = ttl
		f.ttlSets[key]++
	}
	return c, nil
}

func fixedNow(t time.Time) func() time.Time { return func() time.Time { return t } }

const tok = "tok_unit"

func limiterAt(store Store, at time.Time) *Limiter {
	l := New(store, nil)
	l.now = fixedNow(at)
	return l
}

func TestAllowUnderRPMLimit(t *testing.T) {
	store := newFakeStore()
	l := limiterAt(store, time.Date(2026, 6, 14, 12, 0, 30, 0, time.UTC))
	limits := auth.Limits{RequestsPerMinute: 3, RequestsPerHour: 1000}

	for i := 1; i <= 3; i++ {
		res, err := l.Allow(context.Background(), tok, limits)
		if err != nil {
			t.Fatalf("req %d: unexpected error: %v", i, err)
		}
		if !res.Allowed {
			t.Fatalf("req %d: expected allowed under rpm limit", i)
		}
	}
}

func TestDenyOverRPMLimit(t *testing.T) {
	store := newFakeStore()
	l := limiterAt(store, time.Date(2026, 6, 14, 12, 0, 30, 0, time.UTC))
	limits := auth.Limits{RequestsPerMinute: 2, RequestsPerHour: 1000}

	// First two are allowed (count 1, 2 <= 2); the third exceeds the cap.
	_, _ = l.Allow(context.Background(), tok, limits)
	_, _ = l.Allow(context.Background(), tok, limits)
	res, err := l.Allow(context.Background(), tok, limits)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Allowed {
		t.Fatal("expected denial when over rpm limit")
	}
	if res.RetryAfter <= 0 {
		t.Fatalf("expected positive Retry-After, got %d", res.RetryAfter)
	}
}

func TestAllowUnderRPHLimit(t *testing.T) {
	store := newFakeStore()
	l := limiterAt(store, time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC))
	limits := auth.Limits{RequestsPerMinute: 1000, RequestsPerHour: 2}

	res, err := l.Allow(context.Background(), tok, limits)
	if err != nil || !res.Allowed {
		t.Fatalf("expected allow under rph limit, got allowed=%v err=%v", res.Allowed, err)
	}
}

func TestDenyOverRPHLimit(t *testing.T) {
	store := newFakeStore()
	l := limiterAt(store, time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC))
	limits := auth.Limits{RequestsPerMinute: 1000, RequestsPerHour: 1}

	_, _ = l.Allow(context.Background(), tok, limits)
	res, err := l.Allow(context.Background(), tok, limits)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Allowed {
		t.Fatal("expected denial when over rph limit")
	}
	// Hour-window denial: Retry-After tracks the hour reset, not the minute.
	if res.RetryAfter != 3600 {
		t.Fatalf("expected Retry-After=3600 (full hour from :00:00), got %d", res.RetryAfter)
	}
}

func TestDeniedRequestStillIncrements(t *testing.T) {
	store := newFakeStore()
	l := limiterAt(store, time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC))
	limits := auth.Limits{RequestsPerMinute: 1, RequestsPerHour: 1000}

	_, _ = l.Allow(context.Background(), tok, limits) // count 1, allowed
	_, _ = l.Allow(context.Background(), tok, limits) // count 2, denied
	_, _ = l.Allow(context.Background(), tok, limits) // count 3, denied

	key := minuteKey(tok, time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC))
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.counts[key] != 3 {
		t.Fatalf("denied requests must still increment the counter: got %d, want 3", store.counts[key])
	}
}

func TestTTLSetAtomicallyOnFirstIncrementOnly(t *testing.T) {
	store := newFakeStore()
	l := limiterAt(store, time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC))
	limits := auth.Limits{RequestsPerMinute: 100, RequestsPerHour: 1000}

	for i := 0; i < 4; i++ {
		_, _ = l.Allow(context.Background(), tok, limits)
	}

	minKey := minuteKey(tok, time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC))
	hrKey := hourKey(tok, time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC))
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.ttlSets[minKey] != 1 {
		t.Fatalf("minute TTL must be set exactly once (on first increment), got %d", store.ttlSets[minKey])
	}
	if store.ttls[minKey] != time.Minute {
		t.Fatalf("minute TTL must equal the minute window, got %v", store.ttls[minKey])
	}
	if store.ttlSets[hrKey] != 1 {
		t.Fatalf("hour TTL must be set exactly once, got %d", store.ttlSets[hrKey])
	}
	if store.ttls[hrKey] != time.Hour {
		t.Fatalf("hour TTL must equal the hour window, got %v", store.ttls[hrKey])
	}
}

func TestWindowResetAllowsAgain(t *testing.T) {
	store := newFakeStore()
	limits := auth.Limits{RequestsPerMinute: 1, RequestsPerHour: 1000}

	at := time.Date(2026, 6, 14, 12, 0, 30, 0, time.UTC)
	l := limiterAt(store, at)
	_, _ = l.Allow(context.Background(), tok, limits) // count 1, allowed
	denied, _ := l.Allow(context.Background(), tok, limits)
	if denied.Allowed {
		t.Fatal("expected denial within the same minute window")
	}

	// Advance into the next minute: a fresh bucket key, count resets.
	l.now = fixedNow(at.Add(time.Minute))
	res, _ := l.Allow(context.Background(), tok, limits)
	if !res.Allowed {
		t.Fatal("expected allow after the minute window reset")
	}
}

func TestRetryAfterCalculation(t *testing.T) {
	store := newFakeStore()
	// 30s into the minute -> 30s until the minute boundary resets.
	l := limiterAt(store, time.Date(2026, 6, 14, 12, 0, 30, 0, time.UTC))
	limits := auth.Limits{RequestsPerMinute: 1, RequestsPerHour: 1000}

	_, _ = l.Allow(context.Background(), tok, limits)
	res, _ := l.Allow(context.Background(), tok, limits)
	if res.Allowed {
		t.Fatal("expected denial")
	}
	if res.RetryAfter != 30 {
		t.Fatalf("expected Retry-After=30, got %d", res.RetryAfter)
	}
}

func TestHeaderMath(t *testing.T) {
	store := newFakeStore()
	at := time.Date(2026, 6, 14, 12, 0, 30, 0, time.UTC)
	l := limiterAt(store, at)
	limits := auth.Limits{RequestsPerMinute: 60, RequestsPerHour: 1000}

	res, _ := l.Allow(context.Background(), tok, limits)
	if res.Minute.Limit != 60 || res.Minute.Remaining != 59 {
		t.Fatalf("minute header math: limit=%d remaining=%d, want 60/59", res.Minute.Limit, res.Minute.Remaining)
	}
	if res.Hour.Limit != 1000 || res.Hour.Remaining != 999 {
		t.Fatalf("hour header math: limit=%d remaining=%d, want 1000/999", res.Hour.Limit, res.Hour.Remaining)
	}
	wantMinReset := time.Date(2026, 6, 14, 12, 1, 0, 0, time.UTC).Unix()
	if res.Minute.Reset != wantMinReset {
		t.Fatalf("minute reset: got %d, want %d", res.Minute.Reset, wantMinReset)
	}
	wantHourReset := time.Date(2026, 6, 14, 13, 0, 0, 0, time.UTC).Unix()
	if res.Hour.Reset != wantHourReset {
		t.Fatalf("hour reset: got %d, want %d", res.Hour.Reset, wantHourReset)
	}
}

func TestRemainingNeverNegative(t *testing.T) {
	store := newFakeStore()
	l := limiterAt(store, time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC))
	limits := auth.Limits{RequestsPerMinute: 1, RequestsPerHour: 1000}

	_, _ = l.Allow(context.Background(), tok, limits)
	res, _ := l.Allow(context.Background(), tok, limits) // over limit
	if res.Minute.Remaining != 0 {
		t.Fatalf("remaining must clamp at 0, got %d", res.Minute.Remaining)
	}
}

func TestStoreErrorBubbles(t *testing.T) {
	store := newFakeStore()
	store.err = errors.New("redis down")
	l := limiterAt(store, time.Now())
	_, err := l.Allow(context.Background(), tok, auth.DefaultLimits())
	if err == nil {
		t.Fatal("expected the store error to bubble so the middleware can fail open")
	}
}

func TestPerTokenOverrideBeatsDefault(t *testing.T) {
	store := newFakeStore()
	l := limiterAt(store, time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC))

	// A tight override (1 rpm) denies the second request...
	tight := auth.Limits{RequestsPerMinute: 1, RequestsPerHour: 1000}
	_, _ = l.Allow(context.Background(), "tok_tight", tight)
	res, _ := l.Allow(context.Background(), "tok_tight", tight)
	if res.Allowed {
		t.Fatal("tight override should deny the second request")
	}

	// ...while a generous override (100 rpm) on a different token allows it,
	// proving the limit value is honored per call rather than a fixed default.
	loose := auth.Limits{RequestsPerMinute: 100, RequestsPerHour: 1000}
	_, _ = l.Allow(context.Background(), "tok_loose", loose)
	res2, _ := l.Allow(context.Background(), "tok_loose", loose)
	if !res2.Allowed {
		t.Fatal("generous override should allow the second request")
	}
}

func TestNilAndDisabledLimiter(t *testing.T) {
	var nilLimiter *Limiter
	if nilLimiter.Enabled() {
		t.Fatal("nil limiter must report disabled")
	}
	disabled := New(nil, nil)
	if disabled.Enabled() {
		t.Fatal("limiter with nil store must report disabled")
	}
}
