package jobs

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/zakkriel/drchat-image-platform/internal/assets"
	"github.com/zakkriel/drchat-image-platform/internal/identities"
	"github.com/zakkriel/drchat-image-platform/internal/providers"
	"github.com/zakkriel/drchat-image-platform/internal/storage"
)

// referenceProvider is a reference-conditioned stub: it advertises a real
// identity/pack-capable provider that REQUIRES reference images, and records the
// ReferenceURLs it is handed on each Generate so tests can assert the worker
// threaded them through.
type referenceProvider struct {
	mu       sync.Mutex
	refSeen  [][]string
	callPrmt []string
}

func (p *referenceProvider) Generate(_ context.Context, req providers.ProviderGenerateRequest) (providers.ProviderGenerateResult, error) {
	p.mu.Lock()
	p.refSeen = append(p.refSeen, req.ReferenceURLs)
	p.callPrmt = append(p.callPrmt, req.Prompt)
	p.mu.Unlock()
	if len(req.ReferenceURLs) == 0 {
		return providers.ProviderGenerateResult{}, providers.ErrReferenceRequired
	}
	return providers.ProviderGenerateResult{
		Images:     []providers.ProviderImage{{Bytes: tinyPNGBytes(), ContentType: "image/png"}},
		PromptHash: "hash",
		Seed:       "seed",
	}, nil
}

func (p *referenceProvider) PollStatus(context.Context, string) (providers.ProviderJobStatus, error) {
	return providers.ProviderJobStatus{}, providers.ErrNotApplicable
}

func (p *referenceProvider) Upscale(context.Context, providers.ProviderUpscaleRequest) (providers.ProviderGenerateResult, error) {
	return providers.ProviderGenerateResult{}, providers.ErrNotImplemented
}

func (p *referenceProvider) Capabilities() providers.ProviderCapabilities {
	return providers.ProviderCapabilities{
		ProviderID: "fal",
		ModelName:  "flux-pro-kontext-multi",
		Capabilities: []providers.Capability{
			providers.CapabilitySceneCapable,
			providers.CapabilityIdentityCapable,
			providers.CapabilityPackCapable,
		},
		RequiresReferenceImage: true,
	}
}

func (p *referenceProvider) calls() [][]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.refSeen
}

// fakeIdentityReader returns a single identity keyed by id, modelling the worker's
// IdentityReader dependency.
type fakeIdentityReader struct {
	identity identities.VisualIdentity
	err      error
}

func (r *fakeIdentityReader) GetByIDForTenant(_ context.Context, _, _ string) (identities.VisualIdentity, error) {
	if r.err != nil {
		return identities.VisualIdentity{}, r.err
	}
	return r.identity, nil
}

// seedFalPackJob seeds a pack job whose resolved route points at the fal provider
// (the reference-conditioned real provider), the way the handler would persist it.
func seedFalPackJob(repo *fakeJobsRepo, jobID, packID string, variantKeys []string) {
	worldID := "w1"
	tokenID := "tok_test"
	keys := make([]any, 0, len(variantKeys))
	for _, k := range variantKeys {
		keys = append(keys, k)
	}
	_, _ = repo.Insert(context.Background(), InsertParams{
		ID:                 jobID,
		TenantID:           "tenant_a",
		WorldID:            &worldID,
		JobType:            JobTypeCharacterPack,
		RequestedByTokenID: &tokenID,
		InputPayload: map[string]any{
			"world_id":           "w1",
			"style_profile_id":   "sty_ok",
			"variant_keys":       keys,
			"visual_identity_id": "vi_test",
			"display_name":       "Captain Mira",
			"provider_id":        "fal",
			"model_id":           "pm_fal_flux_kontext_multi",
			"provider_route_id":  "route_fal_text_to_image_pack",
		},
	})
	job := repo.jobs[jobID]
	pid := packID
	job.AssetPackID = &pid
	repo.jobs[jobID] = job
}

func newFalRegistry(p providers.ImageProvider) *providers.Registry {
	reg := providers.NewRegistry()
	reg.Register("fal", p)
	return reg
}

// TestPackWithReferenceAssetsBuildsReferenceURLs proves a pack routed to the
// reference-conditioned provider builds each provider request with the identity's
// anchor assets presigned into ReferenceURLs.
// readyAnchor builds a ready visual asset with a canonical high-res object,
// the shape referenceURLsForIdentity validates and presigns.
func readyAnchor(id, tenantID string) assets.VisualAsset {
	high := "s3://bucket/" + storage.ObjectKey(id, storage.VariantHigh, "png")
	return assets.VisualAsset{ID: id, TenantID: tenantID, Status: "ready", HighResUrl: &high}
}

func TestPackWithReferenceAssetsBuildsReferenceURLs(t *testing.T) {
	repo := newFakeJobsRepo()
	provider := &referenceProvider{}
	variants := []string{"neutral_front_portrait", "side_angle_portrait"}
	seedFalPackJob(repo, "job_fal1", "pack_fal1", variants)

	assetsRepo := &fakeAssetsRepo{}
	assetsRepo.seedAsset(readyAnchor("va_anchor_1", "tenant_a"))
	assetsRepo.seedAsset(readyAnchor("va_anchor_2", "tenant_a"))

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

	if err := w.ProcessPack(context.Background(), "job_fal1"); err != nil {
		t.Fatalf("ProcessPack: %v", err)
	}

	calls := provider.calls()
	if len(calls) != len(variants) {
		t.Fatalf("expected %d provider calls, got %d", len(variants), len(calls))
	}
	for i, refs := range calls {
		if len(refs) != 2 {
			t.Fatalf("call %d: expected 2 reference urls, got %v", i, refs)
		}
		// fakeStorage.Presign returns https://example.test/<key>?sig=test; the key
		// is the high-res object for each anchor asset.
		for _, u := range refs {
			if !strings.Contains(u, "https://example.test/") || !strings.Contains(u, "high") {
				t.Fatalf("call %d: reference url not a presigned high-res object: %q", i, u)
			}
		}
	}

	job := repo.jobs["job_fal1"]
	if job.Status != "completed" {
		t.Fatalf("expected completed pack, got %q", job.Status)
	}
}

// TestPackWithoutReferenceAssetsFailsClosed proves a pack routed to the
// reference-conditioned provider fails clearly (no provider call, terminal
// missing_reference_assets) when the identity has no anchor assets — never
// silently generating a different character.
func TestPackWithoutReferenceAssetsFailsClosed(t *testing.T) {
	repo := newFakeJobsRepo()
	provider := &referenceProvider{}
	seedFalPackJob(repo, "job_fal2", "pack_fal2", []string{"neutral_front_portrait"})

	w := &Worker{
		Jobs:      repo,
		Assets:    &fakeAssetsRepo{},
		Storage:   &fakeStorage{},
		Providers: newFalRegistry(provider),
		Identities: &fakeIdentityReader{identity: identities.VisualIdentity{
			ID:             "vi_test",
			TenantID:       "tenant_a",
			AnchorAssetIds: nil, // no reference assets
		}},
	}

	if err := w.ProcessPack(context.Background(), "job_fal2"); err != nil {
		t.Fatalf("ProcessPack returned infra error: %v", err)
	}

	if n := len(provider.calls()); n != 0 {
		t.Fatalf("provider must not be called without references; got %d calls", n)
	}
	job := repo.jobs["job_fal2"]
	if job.Status != "failed" {
		t.Fatalf("expected failed pack, got %q", job.Status)
	}
	if job.ErrorCode == nil || *job.ErrorCode != errorCodeMissingReference {
		t.Fatalf("expected error code %q, got %v", errorCodeMissingReference, job.ErrorCode)
	}
	if got := repo.packStatuses["pack_fal2"]; len(got) == 0 || got[len(got)-1] != packStatusFailed {
		t.Fatalf("expected pack status failed, got %v", got)
	}
}

// TestPackWithMockProviderIgnoresReferences proves the reference path is a no-op
// for a prompt-only provider: mock (RequiresReferenceImage=false) runs unchanged
// and is never blocked on missing references.
func TestPackWithMockProviderIgnoresReferences(t *testing.T) {
	repo := newFakeJobsRepo()
	provider := &selectiveProvider{} // RequiresReferenceImage == false
	variants := []string{"neutral_front_portrait", "side_angle_portrait"}
	seedPackJob(repo, "job_mock_ref", "pack_mock_ref", JobTypeCharacterPack, variants)

	w := &Worker{
		Jobs:      repo,
		Assets:    &fakeAssetsRepo{},
		Storage:   &fakeStorage{},
		Providers: testRegistry(provider),
		// No Identities wired: a prompt-only provider must never reach the reference
		// gathering path.
	}
	if err := w.ProcessPack(context.Background(), "job_mock_ref"); err != nil {
		t.Fatalf("ProcessPack: %v", err)
	}
	if provider.callCount() != len(variants) {
		t.Fatalf("expected %d provider calls, got %d", len(variants), provider.callCount())
	}
	if job := repo.jobs["job_mock_ref"]; job.Status != "completed" {
		t.Fatalf("expected completed pack, got %q", job.Status)
	}
}

// runFalPackWithAnchor seeds a fal pack job whose identity references anchorID,
// seeds the given anchor asset (or none if zero value), runs ProcessPack, and
// returns the terminal job + whether the provider was called.
func runFalPackWithAnchor(t *testing.T, anchorID string, seeded *assets.VisualAsset) (Job, int) {
	t.Helper()
	repo := newFakeJobsRepo()
	provider := &referenceProvider{}
	seedFalPackJob(repo, "job_inv", "pack_inv", []string{"neutral_front_portrait"})

	assetsRepo := &fakeAssetsRepo{}
	if seeded != nil {
		assetsRepo.seedAsset(*seeded)
	}
	w := &Worker{
		Jobs:      repo,
		Assets:    assetsRepo,
		Storage:   &fakeStorage{},
		Providers: newFalRegistry(provider),
		Identities: &fakeIdentityReader{identity: identities.VisualIdentity{
			ID:             "vi_test",
			TenantID:       "tenant_a",
			AnchorAssetIds: []string{anchorID},
		}},
	}
	if err := w.ProcessPack(context.Background(), "job_inv"); err != nil {
		t.Fatalf("ProcessPack infra error: %v", err)
	}
	return repo.jobs["job_inv"], len(provider.calls())
}

// TestPackInvalidAnchorsFailClosed proves each unusable-anchor case fails the
// pack closed with invalid_reference_asset and never calls the provider — the
// hardened referenceURLsForIdentity never presigns a guessed/foreign object.
func TestPackInvalidAnchorsFailClosed(t *testing.T) {
	high := "s3://bucket/" + storage.ObjectKey("va_x", storage.VariantHigh, "png")
	noURL := assets.VisualAsset{ID: "va_x", TenantID: "tenant_a", Status: "ready"}
	notReady := assets.VisualAsset{ID: "va_x", TenantID: "tenant_a", Status: "preview_ready", HighResUrl: &high}
	wrongTenant := assets.VisualAsset{ID: "va_x", TenantID: "tenant_b", Status: "ready", HighResUrl: &high}

	cases := []struct {
		name   string
		anchor string
		seeded *assets.VisualAsset
	}{
		{"missing/stale anchor (not in store)", "va_missing", nil},
		{"non-ready anchor", "va_x", &notReady},
		{"anchor without high-res object", "va_x", &noURL},
		{"wrong-tenant anchor", "va_x", &wrongTenant},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			job, calls := runFalPackWithAnchor(t, tc.anchor, tc.seeded)
			if calls != 0 {
				t.Fatalf("provider must not be called with an invalid anchor; got %d calls", calls)
			}
			if job.Status != "failed" {
				t.Fatalf("expected failed pack, got %q", job.Status)
			}
			if job.ErrorCode == nil || *job.ErrorCode != errorCodeInvalidReference {
				t.Fatalf("expected error code %q, got %v", errorCodeInvalidReference, job.ErrorCode)
			}
		})
	}
}
