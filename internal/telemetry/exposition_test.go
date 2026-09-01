package telemetry

import (
	"strings"
	"testing"
)

// The exposition is a scrape contract: a name or type change silently breaks
// every dashboard and alert built on it.
func TestWriteExpositionEmitsNamedTypedCounters(t *testing.T) {
	var m Metrics
	m.RecordCacheHit()
	m.RecordCacheHit()
	m.RecordCacheMiss()
	m.RecordUsableImage()
	m.RecordProviderCall()
	m.RecordPolicyReject()
	m.RecordFallbackAttempt()
	m.RecordFallbackSuccess()
	m.RecordCost("0.0100", "0.0075")

	var sb strings.Builder
	if err := m.Snapshot().WriteExposition(&sb); err != nil {
		t.Fatalf("write exposition: %v", err)
	}
	out := sb.String()

	for _, want := range []string{
		"# TYPE asset_cache_hit_count counter\nasset_cache_hit_count 2\n",
		"asset_cache_miss_count 1\n",
		"generation_usable_image_count 1\n",
		"provider_call_count 1\n",
		"provider_policy_reject_count 1\n",
		"provider_fallback_attempt_count 1\n",
		"provider_fallback_success_count 1\n",
		"estimated_cost_usd 0.010000\n",
		"actual_cost_usd 0.007500\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("exposition missing %q\ngot:\n%s", want, out)
		}
	}
	// Every metric must carry HELP and TYPE, or promtool rejects the scrape.
	if got := strings.Count(out, "# HELP "); got != 9 {
		t.Fatalf("expected 9 HELP lines, got %d", got)
	}
	if got := strings.Count(out, "# TYPE "); got != 9 {
		t.Fatalf("expected 9 TYPE lines, got %d", got)
	}
}

// Money totals must be gauges. Late reconciliation applies signed deltas, so an
// actual total can decrease; a decreasing counter reads as a process restart and
// corrupts every rate() over it.
func TestWriteExpositionTypesMoneyAsGaugeAndHandlesNegativeDelta(t *testing.T) {
	var m Metrics
	m.RecordCost("1.0000", "1.0000")
	m.RecordActualDelta("-2.5000")

	var sb strings.Builder
	if err := m.Snapshot().WriteExposition(&sb); err != nil {
		t.Fatalf("write exposition: %v", err)
	}
	out := sb.String()

	if !strings.Contains(out, "# TYPE actual_cost_usd gauge") {
		t.Fatalf("actual_cost_usd must be a gauge, got:\n%s", out)
	}
	if !strings.Contains(out, "# TYPE estimated_cost_usd gauge") {
		t.Fatalf("estimated_cost_usd must be a gauge, got:\n%s", out)
	}
	if !strings.Contains(out, "actual_cost_usd -1.500000\n") {
		t.Fatalf("expected a negative actual total, got:\n%s", out)
	}
}

// Money is carried as micro-USD integers so a large cumulative total does not
// lose cents on the way to the scraper.
func TestFormatMicrosUSDIsExactAtScale(t *testing.T) {
	for _, tc := range []struct {
		micros int64
		want   string
	}{
		{0, "0.000000"},
		{1, "0.000001"},
		{10_000, "0.010000"},
		{1_234_567, "1.234567"},
		{-1, "-0.000001"},
		{123_456_789_012_345, "123456789.012345"},
	} {
		if got := formatMicrosUSD(tc.micros); got != tc.want {
			t.Fatalf("formatMicrosUSD(%d) = %q, want %q", tc.micros, got, tc.want)
		}
	}
}
