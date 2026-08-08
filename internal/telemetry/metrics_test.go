package telemetry

import "testing"

func TestMetricsSnapshotAndDerivedValues(t *testing.T) {
	var metrics Metrics
	metrics.RecordCacheHit()
	metrics.RecordCacheMiss()
	metrics.RecordProviderCall()
	metrics.RecordPolicyReject()
	metrics.RecordFallbackAttempt()
	metrics.RecordFallbackSuccess()
	metrics.RecordUsableImage()
	metrics.RecordCost("1.250000", "0.875000")

	snapshot := metrics.Snapshot()
	if snapshot.CacheHits != 1 || snapshot.CacheMisses != 1 {
		t.Fatalf("cache counters = %+v", snapshot)
	}
	if snapshot.ProviderCalls != 1 || snapshot.PolicyRejects != 1 {
		t.Fatalf("provider counters = %+v", snapshot)
	}
	if snapshot.FallbackAttempts != 1 || snapshot.FallbackSuccesses != 1 {
		t.Fatalf("fallback counters = %+v", snapshot)
	}
	if snapshot.UsableImages != 1 {
		t.Fatalf("usable images = %d, want 1", snapshot.UsableImages)
	}
	if snapshot.EstimatedCostMicrosUSD != 1_250_000 || snapshot.ActualCostMicrosUSD != 875_000 {
		t.Fatalf("cost totals = %+v", snapshot)
	}
	if got := snapshot.CacheHitRate(); got != 0.5 {
		t.Fatalf("cache hit rate = %v, want 0.5", got)
	}
	if got := snapshot.ActualEstimateVarianceMicrosUSD(); got != -375_000 {
		t.Fatalf("cost variance = %d, want -375000", got)
	}
	if got := snapshot.ActualCostPerUsableImageMicrosUSD(); got != 875_000 {
		t.Fatalf("cost per usable image = %v, want 875000", got)
	}
}

func TestMetricsRecordActualDeltaSupportsSignedReconciliation(t *testing.T) {
	var metrics Metrics
	metrics.RecordCost("", "0.0300")
	metrics.RecordActualDelta("-0.0250")
	if got := metrics.Snapshot().ActualCostMicrosUSD; got != 5_000 {
		t.Fatalf("reconciled actual total = %d, want 5000", got)
	}
	metrics.RecordActualDelta("not-a-number")
	if got := metrics.Snapshot().ActualCostMicrosUSD; got != 5_000 {
		t.Fatalf("invalid delta changed actual total to %d", got)
	}
}

func TestMetricsIgnoresInvalidCostValues(t *testing.T) {
	var metrics Metrics
	metrics.RecordCost("not-a-number", "0.01")
	metrics.RecordCost("0.02", "")
	metrics.RecordCost("NaN", "Inf")
	metrics.RecordCost("1e30", "1e30")

	snapshot := metrics.Snapshot()
	if snapshot.EstimatedCostMicrosUSD != 20_000 {
		t.Fatalf("estimated cost = %d, want 20000", snapshot.EstimatedCostMicrosUSD)
	}
	if snapshot.ActualCostMicrosUSD != 10_000 {
		t.Fatalf("actual cost = %d, want 10000", snapshot.ActualCostMicrosUSD)
	}
}
