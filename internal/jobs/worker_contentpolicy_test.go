package jobs

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/zakkriel/drchat-image-platform/internal/providers"
)

// contentPolicyProvider models a provider that rejects the request on
// content-policy grounds (the BFL "Content Moderated" terminal status shape).
type contentPolicyProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *contentPolicyProvider) Generate(context.Context, providers.ProviderGenerateRequest) (providers.ProviderGenerateResult, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	return providers.ProviderGenerateResult{}, fmt.Errorf("bfl: %w: provider returned terminal status %q", providers.ErrContentPolicyRejected, "Content Moderated")
}

func (p *contentPolicyProvider) PollStatus(context.Context, string) (providers.ProviderJobStatus, error) {
	return providers.ProviderJobStatus{}, providers.ErrNotApplicable
}

func (p *contentPolicyProvider) Upscale(context.Context, providers.ProviderUpscaleRequest) (providers.ProviderGenerateResult, error) {
	return providers.ProviderGenerateResult{}, providers.ErrNotImplemented
}

func (p *contentPolicyProvider) Capabilities() providers.ProviderCapabilities {
	return providers.ProviderCapabilities{ProviderID: "mock", ModelName: "mock-v1"}
}

// TestContentPolicyRejectionIsTerminalAndSkipsFallback proves the two
// non-censorship invariants of the worker: (1) a provider content-policy
// rejection is never walked around via same-price fallback routes — the
// rejecting provider's decision is surfaced, not circumvented; (2) it is
// terminal immediately (no asynq retry re-billing a deterministic rejection),
// with the distinct provider_content_rejected error code visible to callers.
func TestContentPolicyRejectionIsTerminalAndSkipsFallback(t *testing.T) {
	repo := newFakeJobsRepo()
	assetsRepo := &fakeAssetsRepo{}
	repo.assets = assetsRepo
	rejecting := &contentPolicyProvider{}
	fallback := &selectiveProvider{}

	worldID := "w1"
	tokenID := "tok_test"
	_, _ = repo.Insert(context.Background(), InsertParams{
		ID:                 "job_cp1",
		TenantID:           "tenant_a",
		WorldID:            &worldID,
		JobType:            "artifact",
		RequestedByTokenID: &tokenID,
		InputPayload: fallbackPayload("forbidden prompt", map[string]any{
			"provider_id":       "bfl",
			"model_id":          "pm_bfl_v1",
			"provider_route_id": "route_bfl_text_to_image_standard",
		}),
	})

	fin := &fakeFinalizer{}
	w := &Worker{
		Jobs:    repo,
		Assets:  assetsRepo,
		Storage: &fakeStorage{},
		Providers: multiRegistry(map[string]providers.ImageProvider{
			"mock": rejecting, // primary route (withDefaultResolvedRoute)
			"bfl":  fallback,  // same-price fallback: must NOT be tried
		}),
		Finalizer: fin,
	}

	// retryCount 0 → NOT the final asynq attempt; the rejection must still be
	// terminal (returning nil so asynq never retries).
	if err := w.Process(context.Background(), "job_cp1", 0); err != nil {
		t.Fatalf("expected nil (terminal, no retry), got %v", err)
	}

	fallback.mu.Lock()
	fallbackCalls := len(fallback.calls)
	fallback.mu.Unlock()
	if fallbackCalls != 0 {
		t.Fatalf("fallback provider must never be called past a content-policy rejection; got %d calls", fallbackCalls)
	}
	if rejecting.calls != 1 {
		t.Fatalf("expected exactly 1 primary call, got %d", rejecting.calls)
	}

	job := repo.jobs["job_cp1"]
	if job.Status != "failed" {
		t.Fatalf("expected failed job, got %q", job.Status)
	}
	if job.ErrorCode == nil || *job.ErrorCode != errorCodeContentRejected {
		t.Fatalf("expected error code %q, got %v", errorCodeContentRejected, job.ErrorCode)
	}
	if job.Retryable == nil || *job.Retryable {
		t.Fatalf("content-policy rejection must be non-retryable, got %v", job.Retryable)
	}
	if len(repo.attempts) != 1 {
		t.Fatalf("expected exactly one provider attempt recorded, got %d", len(repo.attempts))
	}
	if len(fin.released) != 1 {
		t.Fatalf("expected the cost reservation released exactly once, got %d", len(fin.released))
	}
}

// TestErrorCodeForContentPolicyAndReference pins the error-code vocabulary:
// content-policy rejections and missing references map to their distinct
// documented codes, not the generic provider_failure.
func TestErrorCodeForContentPolicyAndReference(t *testing.T) {
	if got := errorCodeFor(fmt.Errorf("wrap: %w", providers.ErrContentPolicyRejected)); got != errorCodeContentRejected {
		t.Fatalf("content policy: expected %q, got %q", errorCodeContentRejected, got)
	}
	if got := errorCodeFor(fmt.Errorf("wrap: %w", providers.ErrReferenceRequired)); got != errorCodeMissingReference {
		t.Fatalf("reference required: expected %q, got %q", errorCodeMissingReference, got)
	}
	if got := errorCodeFor(fmt.Errorf("boom")); got != errorCodeProviderFailure {
		t.Fatalf("generic: expected %q, got %q", errorCodeProviderFailure, got)
	}
}
