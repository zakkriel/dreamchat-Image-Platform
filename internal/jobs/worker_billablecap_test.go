package jobs

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/zakkriel/drchat-image-platform/internal/providers"
)

// countingErrorProvider always fails with a NON-content-policy error (the shape
// of a transient provider outage) and counts every billable call. One instance is
// registered under both provider ids so the count is the whole job's spend.
type countingErrorProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *countingErrorProvider) Generate(context.Context, providers.ProviderGenerateRequest) (providers.ProviderGenerateResult, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	return providers.ProviderGenerateResult{}, errors.New("provider unavailable")
}

func (p *countingErrorProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func (p *countingErrorProvider) PollStatus(context.Context, string) (providers.ProviderJobStatus, error) {
	return providers.ProviderJobStatus{}, providers.ErrNotApplicable
}

func (p *countingErrorProvider) Upscale(context.Context, providers.ProviderUpscaleRequest) (providers.ProviderGenerateResult, error) {
	return providers.ProviderGenerateResult{}, providers.ErrNotImplemented
}

func (p *countingErrorProvider) Capabilities() providers.ProviderCapabilities {
	return providers.ProviderCapabilities{ProviderID: "mock", ModelName: "mock-v1"}
}

// TestBillableCallCapBoundsSpendAcrossAsynqAttempts is the Wave 3.5 cost guard.
// A one-image job (units = 1) with one same-price fallback route walks
// MaxAttempts x (1 + 1) = 6 full-price provider calls without the cap — $0.24 of
// billable spend against a reservation priced for ONE $0.0400 image. With the
// cap the same job bills exactly MaxBillableCallsPerUnit = 3 calls however they
// distribute across attempts and routes, and the attempt that finds the cap
// already spent fails the job terminally instead of burning another asynq retry
// on a deterministic refusal.
func TestBillableCallCapBoundsSpendAcrossAsynqAttempts(t *testing.T) {
	jobsRepo := newFakeJobsRepo()
	assetsRepo := &fakeAssetsRepo{}
	jobsRepo.assets = assetsRepo
	fin := &fakeFinalizer{}
	provider := &countingErrorProvider{}

	worldID := "w1"
	tokenID := "tok_test"
	payload := fallbackPayload("uncapped fanout", map[string]any{
		"provider_id":       "bfl",
		"model_id":          "pm_bfl_v1",
		"provider_route_id": "route_bfl_text_to_image_standard",
	})
	// The handler stamps the priced unit count on the payload when it reserves
	// (service.go withCostContextPayload); one image is one unit.
	payload["units"] = float64(1)
	_, _ = jobsRepo.Insert(context.Background(), InsertParams{
		ID:                 "job_cap1",
		TenantID:           "tenant_a",
		WorldID:            &worldID,
		JobType:            "artifact",
		RequestedByTokenID: &tokenID,
		InputPayload:       payload,
	})

	w := &Worker{
		Jobs:    jobsRepo,
		Assets:  assetsRepo,
		Storage: &fakeStorage{},
		// Both the primary (mock) and the same-price fallback (bfl) fail, and both
		// share one counter: calls here are the job's whole billable spend.
		Providers: multiRegistry(map[string]providers.ImageProvider{
			"mock": provider,
			"bfl":  provider,
		}),
		Finalizer: fin,
	}

	// Drive every asynq attempt asynq would deliver.
	for retryCount := int32(0); retryCount < MaxAttempts; retryCount++ {
		err := w.Process(context.Background(), "job_cap1", retryCount)
		if retryCount < MaxAttempts-1 && err == nil && jobsRepo.jobs["job_cap1"].Status != "failed" {
			t.Fatalf("attempt %d: expected the failure to propagate for an asynq retry", retryCount)
		}
	}

	if got := provider.callCount(); got != MaxBillableCallsPerUnit {
		t.Fatalf("expected exactly %d billable provider calls across every attempt, got %d",
			MaxBillableCallsPerUnit, got)
	}
	// Every billable call is one persisted provider_attempts row: the cap's counter
	// and the spend agree.
	if len(jobsRepo.attempts) != MaxBillableCallsPerUnit {
		t.Fatalf("expected %d provider attempts, got %d", MaxBillableCallsPerUnit, len(jobsRepo.attempts))
	}
	billableEvents := 0
	for _, ce := range jobsRepo.costEvents {
		if ce.Status == "failed" {
			billableEvents++
		}
		if ce.Status == "completed" {
			t.Fatalf("no call succeeded; a completed cost event is wrong: %+v", jobsRepo.costEvents)
		}
	}
	if billableEvents != MaxBillableCallsPerUnit {
		t.Fatalf("expected %d failed cost events (one per billed call), got %d", MaxBillableCallsPerUnit, billableEvents)
	}

	// Terminal: the job is failed, non-retryable, and the reservation is released
	// exactly once rather than left reserved.
	job := jobsRepo.jobs["job_cap1"]
	if job.Status != "failed" {
		t.Fatalf("expected the capped job to end failed, got %q", job.Status)
	}
	if job.Retryable == nil || *job.Retryable {
		t.Fatalf("a capped job must be non-retryable, got %v", job.Retryable)
	}
	if len(fin.released) != 1 || fin.released[0] != "job_cap1" {
		t.Fatalf("expected the reservation released exactly once, got %+v", fin.released)
	}
	if len(fin.committed) != 0 {
		t.Fatalf("nothing succeeded; expected no commit, got %+v", fin.committed)
	}
}

// TestBillableCallCapIsTerminalBeforeTheFinalAttempt pins the second half of the
// cap: once the persisted count is exhausted, the job fails NOW even with asynq
// attempts left. Retrying could not bill anything, so it would only re-walk the
// chain to refuse each route again and leave the reservation held meanwhile. The
// chain here is primary + two same-price fallbacks, so the very first attempt
// spends the whole cap and the SECOND attempt (not the final one) is the one
// that must end the job.
func TestBillableCallCapIsTerminalBeforeTheFinalAttempt(t *testing.T) {
	jobsRepo := newFakeJobsRepo()
	assetsRepo := &fakeAssetsRepo{}
	jobsRepo.assets = assetsRepo
	fin := &fakeFinalizer{}
	provider := &countingErrorProvider{}

	worldID := "w1"
	payload := fallbackPayload("cap spent on the first attempt",
		map[string]any{
			"provider_id":       "bfl",
			"model_id":          "pm_bfl_v1",
			"provider_route_id": "route_bfl_text_to_image_standard",
		},
		map[string]any{
			"provider_id":       "fal",
			"model_id":          "pm_fal_v1",
			"provider_route_id": "route_fal_text_to_image_standard",
		},
	)
	payload["units"] = float64(1)
	_, _ = jobsRepo.Insert(context.Background(), InsertParams{
		ID:           "job_cap2",
		TenantID:     "tenant_a",
		WorldID:      &worldID,
		JobType:      "artifact",
		InputPayload: payload,
	})

	w := &Worker{
		Jobs:    jobsRepo,
		Assets:  assetsRepo,
		Storage: &fakeStorage{},
		Providers: multiRegistry(map[string]providers.ImageProvider{
			"mock": provider,
			"bfl":  provider,
			"fal":  provider,
		}),
		Finalizer: fin,
	}

	// Attempt 1 of 3: three routes, three billed calls, cap spent. Not terminal
	// yet — a real provider failure is what asynq should retry.
	if err := w.Process(context.Background(), "job_cap2", 0); err == nil {
		t.Fatalf("attempt 1 must return its provider failure so asynq retries")
	}
	if got := provider.callCount(); got != MaxBillableCallsPerUnit {
		t.Fatalf("attempt 1: expected %d billed calls, got %d", MaxBillableCallsPerUnit, got)
	}

	// Attempt 2 of 3 — NOT the final attempt. The cap is already spent, so the
	// job must end here rather than returning an error asynq would retry.
	if err := w.Process(context.Background(), "job_cap2", 1); err != nil {
		t.Fatalf("attempt 2: a spent cap is terminal; expected nil (no asynq retry), got %v", err)
	}
	if got := provider.callCount(); got != MaxBillableCallsPerUnit {
		t.Fatalf("attempt 2 must bill nothing further, got %d calls", got)
	}
	job := jobsRepo.jobs["job_cap2"]
	if job.Status != "failed" {
		t.Fatalf("attempt 2 must fail the job terminally, got %q", job.Status)
	}
	if job.Retryable == nil || *job.Retryable {
		t.Fatalf("a capped job must be non-retryable, got %v", job.Retryable)
	}
	if len(fin.released) != 1 || fin.released[0] != "job_cap2" {
		t.Fatalf("the reservation must be released when the cap ends the job, got %+v", fin.released)
	}
}

// TestBillableCallCapScalesWithReservedUnits pins the cap to what the
// reservation actually priced. A flat per-job cap would refuse calls a caller
// already paid for: a pack reserves one unit per missing role
// (service.go worstCaseBillableUnits) and a preview-first job reserves two
// phases, so both must be allowed more calls than a single image.
func TestBillableCallCapScalesWithReservedUnits(t *testing.T) {
	w := &Worker{}
	cases := []struct {
		name    string
		payload map[string]any
		want    int
	}{
		{"single image", map[string]any{"units": float64(1)}, 3},
		{"no units persisted falls back to one unit", map[string]any{}, 3},
		{"nonsense units cannot widen the cap", map[string]any{"units": float64(-4)}, 3},
		{"preview-first reserves two phases", map[string]any{
			"units":              float64(2),
			"delivery_mode":      deliveryModePreviewFirst,
			"preview_capability": string(providers.PreviewCapabilityTrue),
		}, 6},
		{"preview-first without persisted units still gets both phases", map[string]any{
			"delivery_mode":      deliveryModePreviewFirst,
			"preview_capability": string(providers.PreviewCapabilityTrue),
		}, 6},
		{"pack gets one unit per role", map[string]any{
			"units":        float64(3),
			"variant_keys": []any{"a", "b", "c"},
		}, 9},
		{"pack without persisted units falls back to its role count", map[string]any{
			"variant_keys": []any{"a", "b", "c"},
		}, 9},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := w.billableCallCap(Job{InputPayload: tc.payload}); got != tc.want {
				t.Fatalf("expected cap %d, got %d", tc.want, got)
			}
		})
	}
}
