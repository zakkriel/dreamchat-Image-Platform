package jobs

import (
	"context"
	"strings"
	"testing"

	"github.com/zakkriel/drchat-image-platform/internal/identities"
)

// seedFalGenerationJob seeds a single-image combined-contract job (JobType
// "generation") whose resolved route points at the reference-conditioned fal
// provider, the way the generations handler persists it (identity_id + intent
// + description on the payload, resolved route stamped by the service).
func seedFalGenerationJob(repo *fakeJobsRepo, jobID string) {
	tokenID := "tok_test"
	_, _ = repo.Insert(context.Background(), InsertParams{
		ID:                 jobID,
		TenantID:           "tenant_a",
		JobType:            "generation",
		RequestedByTokenID: &tokenID,
		InputPayload: map[string]any{
			"description":       "Captain Mira",
			"identity_id":       "vi_test",
			"intent":            "commit",
			"provider_id":       "fal",
			"model_id":          "pm_fal_flux_kontext_multi",
			"provider_route_id": "route_fal_text_to_image_pack",
		},
	})
}

// TestSingleImageReferenceConditionedThreadsRefs proves the single-image path
// (the /v1/generations worker path) gathers the identity's anchor references
// and threads them into the provider request when the resolved route's
// provider requires reference conditioning — the same guarantee the pack path
// already had. Without this the canonical endpoint could resolve fal and then
// always fail closed with empty ReferenceURLs.
func TestSingleImageReferenceConditionedThreadsRefs(t *testing.T) {
	repo := newFakeJobsRepo()
	assetsRepo := &fakeAssetsRepo{}
	repo.assets = assetsRepo
	assetsRepo.seedAsset(readyAnchor("va_anchor_1", "tenant_a"))
	assetsRepo.seedAsset(readyAnchor("va_anchor_2", "tenant_a"))
	provider := &referenceProvider{}
	seedFalGenerationJob(repo, "job_gen_ref1")

	w := &Worker{
		Jobs:      repo,
		Assets:    assetsRepo,
		Storage:   &fakeStorage{},
		Providers: newFalRegistry(provider),
		Identities: &fakeIdentityReader{identity: identities.VisualIdentity{
			ID:             "vi_test",
			TenantID:       "tenant_a",
			AnchorAssetIds: []string{"va_anchor_1", "va_anchor_2"},
		}},
	}

	if err := w.Process(context.Background(), "job_gen_ref1", 0); err != nil {
		t.Fatalf("Process: %v", err)
	}

	calls := provider.calls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 provider call, got %d", len(calls))
	}
	if len(calls[0]) != 2 {
		t.Fatalf("expected 2 reference urls, got %v", calls[0])
	}
	// Provider-reachable origin, not the delivery origin — see the pack test.
	for _, u := range calls[0] {
		if !strings.Contains(u, "https://provider.example.test/") || !strings.Contains(u, "high") {
			t.Fatalf("reference url not a provider-signed high-res object: %q", u)
		}
	}
	if job := repo.jobs["job_gen_ref1"]; job.Status != "completed" {
		t.Fatalf("expected completed job, got %q", job.Status)
	}
}

// TestSingleImageWithoutReferencesFailsClosed proves a single-image job routed
// to a reference-conditioned provider fails terminally with
// missing_reference_assets (no provider call) when the identity has no
// anchors — never rendering a different character from the prompt alone.
func TestSingleImageWithoutReferencesFailsClosed(t *testing.T) {
	repo := newFakeJobsRepo()
	repo.assets = &fakeAssetsRepo{}
	provider := &referenceProvider{}
	seedFalGenerationJob(repo, "job_gen_ref2")

	w := &Worker{
		Jobs:      repo,
		Assets:    &fakeAssetsRepo{},
		Storage:   &fakeStorage{},
		Providers: newFalRegistry(provider),
		Identities: &fakeIdentityReader{identity: identities.VisualIdentity{
			ID:             "vi_test",
			TenantID:       "tenant_a",
			AnchorAssetIds: nil,
		}},
	}

	if err := w.Process(context.Background(), "job_gen_ref2", 0); err != nil {
		t.Fatalf("Process returned infra error: %v", err)
	}
	if n := len(provider.calls()); n != 0 {
		t.Fatalf("provider must not be called without references; got %d calls", n)
	}
	job := repo.jobs["job_gen_ref2"]
	if job.Status != "failed" {
		t.Fatalf("expected failed job, got %q", job.Status)
	}
	if job.ErrorCode == nil || *job.ErrorCode != errorCodeMissingReference {
		t.Fatalf("expected error code %q, got %v", errorCodeMissingReference, job.ErrorCode)
	}
}

// TestSingleImagePreviewFirstThreadsRefsToBothPhases proves a preview-first
// job routed to a reference-conditioned provider conditions BOTH the preview
// and the final render on the identity's anchors.
func TestSingleImagePreviewFirstThreadsRefsToBothPhases(t *testing.T) {
	repo := newFakeJobsRepo()
	assetsRepo := &fakeAssetsRepo{}
	repo.assets = assetsRepo
	assetsRepo.seedAsset(readyAnchor("va_anchor_1", "tenant_a"))
	provider := &referenceProvider{}

	tokenID := "tok_test"
	_, _ = repo.Insert(context.Background(), InsertParams{
		ID:                 "job_gen_ref3",
		TenantID:           "tenant_a",
		JobType:            "generation",
		RequestedByTokenID: &tokenID,
		InputPayload: map[string]any{
			"description":        "Captain Mira",
			"identity_id":        "vi_test",
			"provider_id":        "fal",
			"model_id":           "pm_fal_flux_kontext_multi",
			"provider_route_id":  "route_fal_text_to_image_pack",
			"delivery_mode":      "preview_first",
			"preview_capability": "true_preview",
		},
	})

	w := &Worker{
		Jobs:      repo,
		Assets:    assetsRepo,
		Storage:   &fakeStorage{},
		Providers: newFalRegistry(provider),
		Identities: &fakeIdentityReader{identity: identities.VisualIdentity{
			ID:             "vi_test",
			TenantID:       "tenant_a",
			AnchorAssetIds: []string{"va_anchor_1"},
		}},
	}

	if err := w.Process(context.Background(), "job_gen_ref3", 0); err != nil {
		t.Fatalf("Process: %v", err)
	}
	calls := provider.calls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 provider calls (preview + final), got %d", len(calls))
	}
	for i, refs := range calls {
		if len(refs) != 1 {
			t.Fatalf("call %d: expected 1 reference url, got %v", i, refs)
		}
	}
	if job := repo.jobs["job_gen_ref3"]; job.Status != "completed" {
		t.Fatalf("expected completed job, got %q", job.Status)
	}
}
