package telemetry

import (
	"fmt"
	"io"
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

// exposition describes one metric in the Prometheus text exposition format.
// Cumulative money totals are gauges rather than counters: late provider
// reconciliation applies signed deltas, so an actual total can legitimately go
// down, and a counter that decreases is a lie a scraper will misread as a
// process restart.
type expositionMetric struct {
	name  string
	help  string
	kind  string
	value string
}

// WriteExposition renders the snapshot in the Prometheus text exposition format
// (version 0.0.4). It is written by hand precisely so the telemetry package
// stays dependency-free: the format is a handful of lines, and pulling a client
// library in would make every consumer of this package carry it.
//
// Metric names follow docs/architecture/observability.md. Counters are raw;
// derived figures (cache-hit rate, $/usable image, estimate variance) are
// deliberately NOT exposed here - they are ratios a query layer computes
// correctly across instances, whereas a per-process ratio cannot be summed.
func (s MetricsSnapshot) WriteExposition(w io.Writer) error {
	for _, m := range []expositionMetric{
		{"asset_cache_hit_count", "Reuse lookups served from an existing ready asset.", "counter", strconv.FormatInt(s.CacheHits, 10)},
		{"asset_cache_miss_count", "Reuse lookups that required a fresh generation.", "counter", strconv.FormatInt(s.CacheMisses, 10)},
		{"generation_usable_image_count", "Images persisted and delivered to a caller.", "counter", strconv.FormatInt(s.UsableImages, 10)},
		{"provider_call_count", "Provider generate calls attempted.", "counter", strconv.FormatInt(s.ProviderCalls, 10)},
		{"provider_policy_reject_count", "Provider content-policy rejections, surfaced verbatim.", "counter", strconv.FormatInt(s.PolicyRejects, 10)},
		{"provider_fallback_attempt_count", "Same-price fallback routes attempted after a primary failure.", "counter", strconv.FormatInt(s.FallbackAttempts, 10)},
		{"provider_fallback_success_count", "Fallback routes that produced a usable result.", "counter", strconv.FormatInt(s.FallbackSuccesses, 10)},
		{"estimated_cost_usd", "Cumulative reserved estimate in USD.", "gauge", formatMicrosUSD(s.EstimatedCostMicrosUSD)},
		{"actual_cost_usd", "Cumulative provider-reported actual in USD, including late reconciliation deltas.", "gauge", formatMicrosUSD(s.ActualCostMicrosUSD)},
	} {
		if _, err := fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n%s %s\n", m.name, m.help, m.name, m.kind, m.name, m.value); err != nil {
			return err
		}
	}
	return nil
}

// formatMicrosUSD renders a micro-USD integer as plain decimal USD without
// going through float64, so a large cumulative total cannot lose precision on
// its way to the scraper.
func formatMicrosUSD(micros int64) string {
	sign := ""
	if micros < 0 {
		sign = "-"
		micros = -micros
	}
	return fmt.Sprintf("%s%d.%06d", sign, micros/1_000_000, micros%1_000_000)
}
