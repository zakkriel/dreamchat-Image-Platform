package telemetry

import (
	"math"
	"strconv"
	"sync/atomic"
)

// Metrics is the process-local counter surface for generation economics. It is
// intentionally dependency-free: deployments can periodically export Snapshot
// to Prometheus/OTel, while tests can inspect the same truth without a running
// collector. Money is retained in micro-USD to avoid float accumulation.
type Metrics struct {
	cacheHits         atomic.Int64
	cacheMisses       atomic.Int64
	usableImages      atomic.Int64
	providerCalls     atomic.Int64
	policyRejects     atomic.Int64
	fallbackAttempts  atomic.Int64
	fallbackSuccesses atomic.Int64
	estimatedMicros   atomic.Int64
	actualMicros      atomic.Int64
}

// MetricsSnapshot is an immutable point-in-time view of Metrics.
type MetricsSnapshot struct {
	CacheHits              int64
	CacheMisses            int64
	UsableImages           int64
	ProviderCalls          int64
	PolicyRejects          int64
	FallbackAttempts       int64
	FallbackSuccesses      int64
	EstimatedCostMicrosUSD int64
	ActualCostMicrosUSD    int64
}

var defaultMetrics Metrics

// DefaultMetrics is the process-wide collector used by the HTTP and worker
// paths. Callers that need isolation (for example a benchmark) may instantiate
// Metrics directly and pass it to Record methods.
func DefaultMetrics() *Metrics { return &defaultMetrics }

func (m *Metrics) RecordCacheHit()        { m.cacheHits.Add(1) }
func (m *Metrics) RecordCacheMiss()       { m.cacheMisses.Add(1) }
func (m *Metrics) RecordUsableImage()     { m.usableImages.Add(1) }
func (m *Metrics) RecordProviderCall()    { m.providerCalls.Add(1) }
func (m *Metrics) RecordPolicyReject()    { m.policyRejects.Add(1) }
func (m *Metrics) RecordFallbackAttempt() { m.fallbackAttempts.Add(1) }
func (m *Metrics) RecordFallbackSuccess() { m.fallbackSuccesses.Add(1) }

// RecordCost records decimal USD strings. Invalid or empty values are ignored
// rather than turning telemetry into a source of request failures.
func (m *Metrics) RecordCost(estimatedUSD, actualUSD string) {
	if v, ok := micros(estimatedUSD); ok {
		m.estimatedMicros.Add(v)
	}
	if v, ok := micros(actualUSD); ok {
		m.actualMicros.Add(v)
	}
}

// RecordActualDelta adjusts the cumulative actual total for a late
// reconciliation. Deltas may be negative when an estimate fallback is
// replaced by a lower provider-reported amount.
func (m *Metrics) RecordActualDelta(actualDeltaUSD string) {
	if v, ok := signedMicros(actualDeltaUSD); ok {
		m.actualMicros.Add(v)
	}
}

func (m *Metrics) Snapshot() MetricsSnapshot {
	return MetricsSnapshot{
		CacheHits:              m.cacheHits.Load(),
		CacheMisses:            m.cacheMisses.Load(),
		UsableImages:           m.usableImages.Load(),
		ProviderCalls:          m.providerCalls.Load(),
		PolicyRejects:          m.policyRejects.Load(),
		FallbackAttempts:       m.fallbackAttempts.Load(),
		FallbackSuccesses:      m.fallbackSuccesses.Load(),
		EstimatedCostMicrosUSD: m.estimatedMicros.Load(),
		ActualCostMicrosUSD:    m.actualMicros.Load(),
	}
}

func (s MetricsSnapshot) CacheHitRate() float64 {
	total := s.CacheHits + s.CacheMisses
	if total == 0 {
		return 0
	}
	return float64(s.CacheHits) / float64(total)
}

func (s MetricsSnapshot) ActualEstimateVarianceMicrosUSD() int64 {
	return s.ActualCostMicrosUSD - s.EstimatedCostMicrosUSD
}

func (s MetricsSnapshot) ActualCostPerUsableImageMicrosUSD() float64 {
	if s.UsableImages == 0 {
		return 0
	}
	return float64(s.ActualCostMicrosUSD) / float64(s.UsableImages)
}

func micros(value string) (int64, bool) {
	f, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
		return 0, false
	}
	scaled := f*1_000_000 + 0.5
	// Avoid implementation-dependent float-to-int overflow for syntactically
	// valid but unrepresentable telemetry values. Provider costs are bounded
	// much lower, but this keeps the metrics surface fail-safe on direct calls.
	if math.IsInf(scaled, 0) || scaled >= float64(1<<63-1) {
		return 0, false
	}
	return int64(scaled), true
}

func signedMicros(value string) (int64, bool) {
	f, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	scaled := f * 1_000_000
	if scaled >= 0 {
		scaled += 0.5
	} else {
		scaled -= 0.5
	}
	if math.IsInf(scaled, 0) ||
		scaled > float64(1<<63-1) ||
		scaled < -float64(1<<63) {
		return 0, false
	}
	return int64(scaled), true
}
