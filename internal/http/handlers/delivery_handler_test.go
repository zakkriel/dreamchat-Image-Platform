package handlers

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/zakkriel/drchat-image-platform/internal/assets"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
	"github.com/zakkriel/drchat-image-platform/internal/storage"
)

// stubSigner records the keys it was asked to sign and returns a deterministic
// https URL embedding the key, so tests can assert the read surface only signs
// DERIVED object keys. Presigns within a request are sequential, so no locking
// is needed.
type stubSigner struct {
	calls []string
	err   error
}

func newStubSigner() *stubSigner { return &stubSigner{} }

func (s *stubSigner) Presign(_ context.Context, key string, _ time.Duration) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	s.calls = append(s.calls, key)
	return "https://signed.example/" + key + "?X-Amz-Signature=test", nil
}

// stubJobAssetsLookup implements handlers.JobAssetsLookup for the job-assets
// read tests: tenant-scoped job rows + ordered pack items.
type stubJobAssetsLookup struct {
	jobsByID map[string]jobs.Job
	items    map[string][]jobs.AssetPackItem
}

func newStubJobAssetsLookup() *stubJobAssetsLookup {
	return &stubJobAssetsLookup{jobsByID: map[string]jobs.Job{}, items: map[string][]jobs.AssetPackItem{}}
}

func (s *stubJobAssetsLookup) GetByIDForTenant(_ context.Context, id, tenantID string) (jobs.Job, error) {
	job, ok := s.jobsByID[id]
	if !ok || job.TenantID != tenantID {
		return jobs.Job{}, jobs.ErrNotFound
	}
	return job, nil
}

func (s *stubJobAssetsLookup) ListAssetPackItems(_ context.Context, packID string) ([]jobs.AssetPackItem, error) {
	return s.items[packID], nil
}

func newDeliveryRouter(repo assets.Repository, signer AssetURLSigner, lookup JobAssetsLookup) chi.Router {
	h := NewAssetsHandler(repo, assets.NewRetriever(repo)).WithDelivery(signer, time.Minute).WithJobs(lookup)
	r := chi.NewRouter()
	r.Get("/v1/assets/{asset_id}", h.Get)
	r.Get("/v1/jobs/{job_id}/assets", h.JobAssets)
	return r
}

func strPtrLocal(s string) *string { return &s }

// ---------------------------------------------------------------------------
// Asset read: presigned tier URLs
// ---------------------------------------------------------------------------

func TestAssetGetReturnsPresignedTierURLs(t *testing.T) {
	repo := newStubAssetsRepo()
	repo.byID["asset_1"] = assets.VisualAsset{
		ID: "asset_1", TenantID: tenantA, WorldID: "w1",
		AssetType: "artifact", VariantKey: "default", Version: 1, Status: "ready",
		LowResUrl: strPtrLocal("s3://b/assets/asset_1/low.png"),
	}
	signer := newStubSigner()
	rec := sendJSON(t, newDeliveryRouter(repo, signer, newStubJobAssetsLookup()), http.MethodGet, "/v1/assets/asset_1", tenantA, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	resp := decode[map[string]any](t, rec)

	for _, field := range []string{"thumbnail_download_url", "preview_download_url", "final_download_url"} {
		u, _ := resp[field].(string)
		if !strings.HasPrefix(u, "https://") {
			t.Fatalf("%s must be an https URL, got %v", field, resp[field])
		}
	}
	if resp["url_expires_at"] == nil || resp["url_expires_at"] == "" {
		t.Fatalf("expected url_expires_at, got %v", resp["url_expires_at"])
	}
	// The durable s3:// provenance must be untouched (additive).
	if resp["low_res_url"] != "s3://b/assets/asset_1/low.png" {
		t.Fatalf("provenance low_res_url must be unchanged, got %v", resp["low_res_url"])
	}
	// Keys signed must be the DERIVED object keys, never a client path.
	wantKeys := map[string]bool{
		storage.ObjectKey("asset_1", storage.VariantThumb, "png"): true,
		storage.ObjectKey("asset_1", storage.VariantLow, "png"):   true,
		storage.ObjectKey("asset_1", storage.VariantHigh, "png"):  true,
	}
	if len(signer.calls) != 3 {
		t.Fatalf("expected exactly 3 presign calls (one per tier), got %d: %v", len(signer.calls), signer.calls)
	}
	for _, k := range signer.calls {
		if !wantKeys[k] {
			t.Fatalf("signer asked to sign a non-derived key: %q", k)
		}
	}
}

func TestAssetGetCrossTenantNeverPresigns(t *testing.T) {
	repo := newStubAssetsRepo()
	repo.byID["asset_1"] = assets.VisualAsset{ID: "asset_1", TenantID: tenantA, Status: "ready"}
	signer := newStubSigner()
	rec := sendJSON(t, newDeliveryRouter(repo, signer, newStubJobAssetsLookup()), http.MethodGet, "/v1/assets/asset_1", tenantB, nil)
	assertError(t, rec, http.StatusNotFound, "not_found")
	if len(signer.calls) != 0 {
		t.Fatalf("a presigned URL must never be minted on a tenant miss, got %d calls", len(signer.calls))
	}
}

func TestAssetGetWithoutSignerOmitsURLs(t *testing.T) {
	repo := newStubAssetsRepo()
	repo.byID["asset_1"] = assets.VisualAsset{ID: "asset_1", TenantID: tenantA, AssetType: "artifact", VariantKey: "default", Version: 1, Status: "ready"}
	// No signer wired (pre-6B behavior) → additive fields omitted.
	h := NewAssetsHandler(repo, assets.NewRetriever(repo))
	r := chi.NewRouter()
	r.Get("/v1/assets/{asset_id}", h.Get)
	rec := sendJSON(t, r, http.MethodGet, "/v1/assets/asset_1", tenantA, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	resp := decode[map[string]any](t, rec)
	if _, ok := resp["final_download_url"]; ok {
		t.Fatalf("download URLs must be omitted when no signer is wired, got %v", resp["final_download_url"])
	}
}

// ---------------------------------------------------------------------------
// Job-assets read
// ---------------------------------------------------------------------------

func TestJobAssetsArtifactDeliveryOrder(t *testing.T) {
	repo := newStubAssetsRepo()
	repo.byID["asset_a"] = assets.VisualAsset{ID: "asset_a", TenantID: tenantA, AssetType: "artifact", VariantKey: "default", Version: 1, Status: "ready"}
	repo.byID["asset_b"] = assets.VisualAsset{ID: "asset_b", TenantID: tenantA, AssetType: "artifact", VariantKey: "default", Version: 1, Status: "archived"}

	lookup := newStubJobAssetsLookup()
	// final_asset_ids order is [asset_b, asset_a] — must be preserved.
	lookup.jobsByID["job_1"] = jobs.Job{ID: "job_1", TenantID: tenantA, JobType: "artifact", Status: "completed", FinalAssetIds: []string{"asset_b", "asset_a"}}

	signer := newStubSigner()
	rec := sendJSON(t, newDeliveryRouter(repo, signer, lookup), http.MethodGet, "/v1/jobs/job_1/assets", tenantA, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	resp := decode[map[string]any](t, rec)
	list, _ := resp["assets"].([]any)
	if len(list) != 2 {
		t.Fatalf("expected 2 delivered assets, got %d", len(list))
	}
	first := list[0].(map[string]any)
	second := list[1].(map[string]any)
	if first["id"] != "asset_b" || second["id"] != "asset_a" {
		t.Fatalf("delivery must follow final_asset_ids order, got %v then %v", first["id"], second["id"])
	}
	// Archived asset is still delivered (not restricted to ready) with URLs.
	if u, _ := first["final_download_url"].(string); !strings.HasPrefix(u, "https://") {
		t.Fatalf("archived asset must still carry a fetchable URL, got %v", first["final_download_url"])
	}
}

func TestJobAssetsPackDeliveryOrder(t *testing.T) {
	repo := newStubAssetsRepo()
	repo.byID["asset_x"] = assets.VisualAsset{ID: "asset_x", TenantID: tenantA, AssetType: "character_portrait", VariantKey: "front", Version: 1, Status: "ready"}
	repo.byID["asset_y"] = assets.VisualAsset{ID: "asset_y", TenantID: tenantA, AssetType: "character_portrait", VariantKey: "side", Version: 1, Status: "ready"}

	lookup := newStubJobAssetsLookup()
	packID := "pack_1"
	lookup.jobsByID["job_pack"] = jobs.Job{ID: "job_pack", TenantID: tenantA, JobType: "character_pack", Status: "completed", AssetPackID: &packID, FinalAssetIds: []string{"asset_y", "asset_x"}}
	// Pack items are returned in sort_order; that order (x then y) must win over
	// final_asset_ids order (y then x).
	lookup.items[packID] = []jobs.AssetPackItem{
		{ID: "i1", AssetPackID: packID, VisualAssetID: "asset_x", VariantKey: "front", SortOrder: 0},
		{ID: "i2", AssetPackID: packID, VisualAssetID: "asset_y", VariantKey: "side", SortOrder: 1},
	}

	rec := sendJSON(t, newDeliveryRouter(repo, newStubSigner(), lookup), http.MethodGet, "/v1/jobs/job_pack/assets", tenantA, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	resp := decode[map[string]any](t, rec)
	list, _ := resp["assets"].([]any)
	if len(list) != 2 {
		t.Fatalf("expected 2 pack assets, got %d", len(list))
	}
	if list[0].(map[string]any)["id"] != "asset_x" || list[1].(map[string]any)["id"] != "asset_y" {
		t.Fatalf("pack delivery must follow sort_order, got %v", []any{list[0].(map[string]any)["id"], list[1].(map[string]any)["id"]})
	}
}

// Phase 7B: a preview_ready job (no final assets yet) delivers its preview asset
// through the job-assets read, so the preview is observable before final.
func TestJobAssetsPreviewReadyReturnsPreviewAsset(t *testing.T) {
	repo := newStubAssetsRepo()
	repo.byID["asset_preview"] = assets.VisualAsset{ID: "asset_preview", TenantID: tenantA, AssetType: "artifact", VariantKey: "default", Version: 1, Status: "preview_ready"}

	lookup := newStubJobAssetsLookup()
	lookup.jobsByID["job_pf"] = jobs.Job{ID: "job_pf", TenantID: tenantA, JobType: "artifact", Status: "preview_ready", PreviewAssetIds: []string{"asset_preview"}}

	rec := sendJSON(t, newDeliveryRouter(repo, newStubSigner(), lookup), http.MethodGet, "/v1/jobs/job_pf/assets", tenantA, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	resp := decode[map[string]any](t, rec)
	list, _ := resp["assets"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["id"] != "asset_preview" {
		t.Fatalf("preview_ready job must deliver the preview asset, got %v", resp["assets"])
	}
	if list[0].(map[string]any)["status"] != "preview_ready" {
		t.Fatalf("delivered preview must be status=preview_ready, got %v", list[0].(map[string]any)["status"])
	}
}

// Phase 7B: a completed two-phase job (both preview and final present) delivers
// the FINAL asset — final_asset_ids takes precedence over preview_asset_ids.
func TestJobAssetsCompletedPrefersFinalOverPreview(t *testing.T) {
	repo := newStubAssetsRepo()
	repo.byID["asset_preview"] = assets.VisualAsset{ID: "asset_preview", TenantID: tenantA, AssetType: "artifact", VariantKey: "default", Version: 1, Status: "preview_ready"}
	repo.byID["asset_final"] = assets.VisualAsset{ID: "asset_final", TenantID: tenantA, AssetType: "artifact", VariantKey: "default", Version: 1, Status: "ready"}

	lookup := newStubJobAssetsLookup()
	lookup.jobsByID["job_done"] = jobs.Job{ID: "job_done", TenantID: tenantA, JobType: "artifact", Status: "completed", PreviewAssetIds: []string{"asset_preview"}, FinalAssetIds: []string{"asset_final"}}

	rec := sendJSON(t, newDeliveryRouter(repo, newStubSigner(), lookup), http.MethodGet, "/v1/jobs/job_done/assets", tenantA, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	resp := decode[map[string]any](t, rec)
	list, _ := resp["assets"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["id"] != "asset_final" {
		t.Fatalf("completed job must deliver the final asset, got %v", resp["assets"])
	}
}

// Phase 7B: a failed-after-preview job (preview present, final empty) still
// delivers the preview asset — it is the last useful output, not superseded.
func TestJobAssetsFailedAfterPreviewReturnsPreview(t *testing.T) {
	repo := newStubAssetsRepo()
	repo.byID["asset_preview"] = assets.VisualAsset{ID: "asset_preview", TenantID: tenantA, AssetType: "artifact", VariantKey: "default", Version: 1, Status: "preview_ready"}

	lookup := newStubJobAssetsLookup()
	lookup.jobsByID["job_failed"] = jobs.Job{ID: "job_failed", TenantID: tenantA, JobType: "artifact", Status: "failed", PreviewAssetIds: []string{"asset_preview"}}

	rec := sendJSON(t, newDeliveryRouter(repo, newStubSigner(), lookup), http.MethodGet, "/v1/jobs/job_failed/assets", tenantA, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	resp := decode[map[string]any](t, rec)
	list, _ := resp["assets"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["id"] != "asset_preview" {
		t.Fatalf("failed two-phase job must still deliver the preview, got %v", resp["assets"])
	}
}

func TestJobAssetsEmptyForJobWithNoAssets(t *testing.T) {
	lookup := newStubJobAssetsLookup()
	lookup.jobsByID["job_empty"] = jobs.Job{ID: "job_empty", TenantID: tenantA, JobType: "artifact", Status: "queued"}
	rec := sendJSON(t, newDeliveryRouter(newStubAssetsRepo(), newStubSigner(), lookup), http.MethodGet, "/v1/jobs/job_empty/assets", tenantA, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	resp := decode[map[string]any](t, rec)
	list, ok := resp["assets"].([]any)
	if !ok || len(list) != 0 {
		t.Fatalf("expected empty assets array, got %v", resp["assets"])
	}
}

func TestJobAssetsCrossTenantReturns404(t *testing.T) {
	lookup := newStubJobAssetsLookup()
	lookup.jobsByID["job_1"] = jobs.Job{ID: "job_1", TenantID: tenantA, JobType: "artifact", Status: "completed", FinalAssetIds: []string{"asset_a"}}
	signer := newStubSigner()
	rec := sendJSON(t, newDeliveryRouter(newStubAssetsRepo(), signer, lookup), http.MethodGet, "/v1/jobs/job_1/assets", tenantB, nil)
	assertError(t, rec, http.StatusNotFound, "not_found")
	if len(signer.calls) != 0 {
		t.Fatalf("cross-tenant job-assets read must never presign, got %d", len(signer.calls))
	}
}
