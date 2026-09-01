package jobs

import (
	"context"
	"strings"
	"testing"

	"github.com/zakkriel/drchat-image-platform/internal/assets"
	"github.com/zakkriel/drchat-image-platform/internal/identities"
	"github.com/zakkriel/drchat-image-platform/internal/storage"
)

// anchorWithTiers is a ready anchor carrying BOTH a preview and a high-res
// object, which is what every asset written by the tier encoder looks like.
func anchorWithTiers(id, tenantID string) assets.VisualAsset {
	high := "s3://bucket/" + storage.ObjectKey(id, storage.VariantHigh, "avif")
	low := "s3://bucket/" + storage.ObjectKey(id, storage.VariantLow, "avif")
	return assets.VisualAsset{ID: id, TenantID: tenantID, Status: "ready", HighResUrl: &high, LowResUrl: &low}
}

func identityWithAnchors(id, tenantID string, anchors ...string) identities.VisualIdentity {
	return identities.VisualIdentity{ID: id, TenantID: tenantID, AnchorAssetIds: anchors}
}

// The reference handed to a reference-conditioned provider is the PREVIEW tier.
//
// This is the cost lever, not a detail: FLUX.1 Kontext [dev] renders at the
// reference's resolution and bills per compute-second, so a 512 reference costs
// $0.0033 against $0.0106 for a 1024 one. Delivery resolution is rebuilt by
// imaging.Upscale, which is free.
func TestReferenceURLsUsePreviewTier(t *testing.T) {
	assetsRepo := &fakeAssetsRepo{}
	assetsRepo.seedAsset(anchorWithTiers("va_anchor_1", "tenant_a"))
	ids := &fakeIdentityReader{identity: identityWithAnchors("vi_test", "tenant_a", "va_anchor_1")}

	w := &Worker{Jobs: newFakeJobsRepo(), Assets: assetsRepo, Storage: &fakeStorage{}, Identities: ids}
	urls, err := w.referenceURLsForIdentity(context.Background(), "vi_test", "tenant_a")
	if err != nil {
		t.Fatalf("referenceURLsForIdentity: %v", err)
	}
	if len(urls) != 1 {
		t.Fatalf("expected one reference url, got %v", urls)
	}
	if !strings.Contains(urls[0], "low") {
		t.Fatalf("the reference must be the preview tier (the cheap render size), got %q", urls[0])
	}
	if strings.Contains(urls[0], "high") {
		t.Fatalf("presigning the high-res tier renders at full size and costs ~3x, got %q", urls[0])
	}
}

// An anchor written before tiers existed, or one whose preview never landed,
// must still condition the render rather than failing the job: high-res is the
// fallback, not an error.
func TestReferenceURLsFallBackToHighRes(t *testing.T) {
	high := "s3://bucket/" + storage.ObjectKey("va_legacy", storage.VariantHigh, "png")
	assetsRepo := &fakeAssetsRepo{}
	assetsRepo.seedAsset(assets.VisualAsset{
		ID: "va_legacy", TenantID: "tenant_a", Status: "ready", HighResUrl: &high,
	})
	ids := &fakeIdentityReader{identity: identityWithAnchors("vi_test", "tenant_a", "va_legacy")}

	w := &Worker{Jobs: newFakeJobsRepo(), Assets: assetsRepo, Storage: &fakeStorage{}, Identities: ids}
	urls, err := w.referenceURLsForIdentity(context.Background(), "vi_test", "tenant_a")
	if err != nil {
		t.Fatalf("an anchor with only a high-res object must still condition: %v", err)
	}
	if len(urls) != 1 || !strings.Contains(urls[0], "high") {
		t.Fatalf("expected the high-res fallback, got %v", urls)
	}
}

// An anchor with no usable object at all still fails closed — generating a
// different character is worse than failing the job.
func TestReferenceURLsFailClosedWithoutAnyObject(t *testing.T) {
	assetsRepo := &fakeAssetsRepo{}
	assetsRepo.seedAsset(assets.VisualAsset{ID: "va_empty", TenantID: "tenant_a", Status: "ready"})
	ids := &fakeIdentityReader{identity: identityWithAnchors("vi_test", "tenant_a", "va_empty")}

	w := &Worker{Jobs: newFakeJobsRepo(), Assets: assetsRepo, Storage: &fakeStorage{}, Identities: ids}
	if _, err := w.referenceURLsForIdentity(context.Background(), "vi_test", "tenant_a"); err == nil {
		t.Fatal("an anchor with no reference object must fail closed, not render a different character")
	}
}
