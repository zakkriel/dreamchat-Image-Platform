package jobs

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/zakkriel/drchat-image-platform/internal/providers"
)

// unpaidProvider models a provider whose ACCOUNT has no credit: BFL answers 402
// on submit. The remedy is a payment, never another attempt.
type unpaidProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *unpaidProvider) Generate(context.Context, providers.ProviderGenerateRequest) (providers.ProviderGenerateResult, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	return providers.ProviderGenerateResult{}, fmt.Errorf("bfl: %w: submit returned status 402", providers.ErrProviderUnpaid)
}

func (p *unpaidProvider) PollStatus(context.Context, string) (providers.ProviderJobStatus, error) {
	return providers.ProviderJobStatus{}, providers.ErrNotApplicable
}

func (p *unpaidProvider) Upscale(context.Context, providers.ProviderUpscaleRequest) (providers.ProviderGenerateResult, error) {
	return providers.ProviderGenerateResult{}, providers.ErrNotImplemented
}

func (p *unpaidProvider) Capabilities() providers.ProviderCapabilities {
	return providers.ProviderCapabilities{ProviderID: "mock", ModelName: "mock-v1"}
}

// An unpaid account is terminal for the job: asynq must not re-attempt it, and
// the code must SAY unpaid rather than the generic provider_failure, because the
// consumer's correct response differs — a transient failure should be retried on
// the next sweep, an unpaid one must not be until someone pays.
//
// Measured 2026-09-01: 875 artifact jobs failed this way in 24h. Each was a
// fresh submit from the world backend's 2-minute reconciler, and together they
// drained the platform's 1000-requests/hour token budget. The asset READ path
// shares that budget, so a billing failure on one provider made every
// already-rendered picture in the product unfetchable.
func TestUnpaidAccountIsTerminalWithItsOwnCode(t *testing.T) {
	repo := newFakeJobsRepo()
	assetsRepo := &fakeAssetsRepo{}
	repo.assets = assetsRepo
	unpaid := &unpaidProvider{}

	worldID := "w1"
	tokenID := "tok_test"
	_, _ = repo.Insert(context.Background(), InsertParams{
		ID:                 "job_unpaid1",
		TenantID:           "tenant_a",
		WorldID:            &worldID,
		JobType:            "artifact",
		RequestedByTokenID: &tokenID,
		InputPayload:       fallbackPayload("a harbour at dusk", nil),
	})

	w := &Worker{
		Jobs:      repo,
		Assets:    assetsRepo,
		Storage:   &fakeStorage{},
		Providers: multiRegistry(map[string]providers.ImageProvider{"mock": unpaid}),
		Finalizer: &fakeFinalizer{},
	}

	// retryCount 0 → not the final asynq attempt. Terminal anyway.
	if err := w.Process(context.Background(), "job_unpaid1", 0); err != nil {
		t.Fatalf("expected nil (terminal, no asynq retry), got %v", err)
	}

	job := repo.jobs["job_unpaid1"]
	if job.Status != "failed" {
		t.Fatalf("expected failed, got %q", job.Status)
	}
	if job.Retryable == nil || *job.Retryable {
		t.Fatalf("an unpaid account must be non-retryable, got %v", job.Retryable)
	}
	if job.ErrorCode == nil || *job.ErrorCode != errorCodeProviderUnpaid {
		t.Fatalf("expected error code %q so the consumer can stop re-commissioning, got %v", errorCodeProviderUnpaid, job.ErrorCode)
	}
}

// Unlike a content-policy rejection, an unpaid account MUST still walk the
// fallback routes. Walking around a moderation decision would circumvent the
// rejecting provider's policy; walking around an unpaid invoice just uses a
// provider that IS paid, which is the entire point of having more than one.
// This is the half most likely to be broken by a later "treat all terminal
// errors alike" tidy-up.
func TestUnpaidAccountStillWalksToAPaidProvider(t *testing.T) {
	repo := newFakeJobsRepo()
	assetsRepo := &fakeAssetsRepo{}
	repo.assets = assetsRepo
	unpaid := &unpaidProvider{}
	paid := &selectiveProvider{}

	worldID := "w1"
	tokenID := "tok_test"
	_, _ = repo.Insert(context.Background(), InsertParams{
		ID:                 "job_unpaid2",
		TenantID:           "tenant_a",
		WorldID:            &worldID,
		JobType:            "artifact",
		RequestedByTokenID: &tokenID,
		InputPayload: fallbackPayload("a harbour at dusk", map[string]any{
			"provider_id":       "bfl",
			"model_id":          "pm_bfl_v1",
			"provider_route_id": "route_bfl_text_to_image_standard",
		}),
	})

	w := &Worker{
		Jobs:    repo,
		Assets:  assetsRepo,
		Storage: &fakeStorage{},
		Providers: multiRegistry(map[string]providers.ImageProvider{
			"mock": unpaid, // primary
			"bfl":  paid,   // fallback: MUST be tried
		}),
		Finalizer: &fakeFinalizer{},
	}

	_ = w.Process(context.Background(), "job_unpaid2", 0)

	paid.mu.Lock()
	paidCalls := len(paid.calls)
	paid.mu.Unlock()
	if paidCalls == 0 {
		t.Fatal("an unpaid primary must fall back to a paid provider; the walk was skipped")
	}
}
