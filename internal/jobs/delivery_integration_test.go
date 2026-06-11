//go:build integration

package jobs_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/zakkriel/drchat-image-platform/internal/assets"
	"github.com/zakkriel/drchat-image-platform/internal/auth"
	"github.com/zakkriel/drchat-image-platform/internal/config"
	"github.com/zakkriel/drchat-image-platform/internal/cost"
	"github.com/zakkriel/drchat-image-platform/internal/http/handlers"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
	"github.com/zakkriel/drchat-image-platform/internal/providers/mock"
	"github.com/zakkriel/drchat-image-platform/internal/styles"
	"github.com/zakkriel/drchat-image-platform/internal/telemetry"
)

// mountDeliveryRouter wires the Phase 6B read surface (asset read, job-assets
// read, style preview) plus the artifact generate path against the real DB +
// MinIO-backed storage.
func mountDeliveryRouter(svc jobs.Creator, stylesRepo styles.Repository, jobsRepo jobs.Repository, assetsRepo assets.Repository, store handlers.AssetURLSigner) *chi.Mux {
	artifacts := handlers.NewArtifactsHandler(svc, stylesRepo, config.ProviderMock, assetsRepo)
	assetsH := handlers.NewAssetsHandler(assetsRepo, assets.NewRetriever(assetsRepo)).
		WithDelivery(store, 15*time.Minute).
		WithJobs(jobsRepo)
	preview := handlers.NewStylePreviewHandler(svc, stylesRepo, config.ProviderMock)

	r := chi.NewRouter()
	r.Post("/v1/artifacts/{artifact_id}/generate", artifacts.Generate)
	r.Get("/v1/assets/{asset_id}", assetsH.Get)
	r.Get("/v1/jobs/{job_id}/assets", assetsH.JobAssets)
	r.Post("/v1/styles/{style_id}/preview", preview.GeneratePreview)
	return r
}

func deliveryReq(t *testing.T, r http.Handler, method, path string, scopes []string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = strings.NewReader(string(raw))
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	ctx := auth.ContextWithPrincipal(req.Context(), &auth.Principal{TokenID: itTokenID, TenantID: itTenant, Scopes: scopes})
	ctx = telemetry.ContextWithRequestID(ctx, "req_test")
	ctx = telemetry.ContextWithRequestLog(ctx, &telemetry.RequestLog{})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req.WithContext(ctx))
	return rec
}

// fetchPNGDims GETs a presigned URL from MinIO and returns the image
// dimensions, asserting a 200.
func fetchPNGDims(t *testing.T, presignedURL string) (int, int) {
	t.Helper()
	if !strings.HasPrefix(presignedURL, "http") {
		t.Fatalf("expected an http(s) presigned URL, got %q", presignedURL)
	}
	resp, err := http.Get(presignedURL) //nolint:gosec // presigned URL from our own storage
	if err != nil {
		t.Fatalf("GET presigned url: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("presigned GET expected 200, got %d body=%s", resp.StatusCode, string(body))
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read presigned body: %v", err)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decode fetched png: %v", err)
	}
	return cfg.Width, cfg.Height
}

// TestEndToEndDeliveryPresignedTiers is the Phase 6B acceptance test: generate
// an artifact, run the worker, then read the asset and confirm the presigned
// per-tier URLs actually GET 200 from MinIO and the three tiers are genuinely
// distinct sizes. Also exercises GET /v1/jobs/{job_id}/assets.
func TestEndToEndDeliveryPresignedTiers(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	store := openTestStorage(t)

	jobsRepo := jobs.NewRepository(pool)
	assetsRepo := assets.NewRepository(pool)
	stylesRepo := styles.NewRepository(pool)
	svc := jobs.NewService(pool, newRecordingEnqueuer(), cost.NewService(nil))
	r := mountDeliveryRouter(svc, stylesRepo, jobsRepo, assetsRepo, store)

	rec := sendArtifactRequest(t, r, map[string]any{
		"world_id":         "w1",
		"style_profile_id": itStyleID,
		"description":      "A bronze key",
	}, "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST generate expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	var acc map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &acc)
	jobID := acc["job_id"].(string)

	worker := &jobs.Worker{Jobs: jobsRepo, Assets: assetsRepo, Storage: store, Provider: mock.New()}
	if err := worker.Process(context.Background(), jobID, 0); err != nil {
		t.Fatalf("worker process: %v", err)
	}

	// Read the job's delivered assets.
	jaRec := deliveryReq(t, r, http.MethodGet, "/v1/jobs/"+jobID+"/assets", []string{"images:read"}, nil)
	if jaRec.Code != http.StatusOK {
		t.Fatalf("GET job-assets expected 200, got %d body=%s", jaRec.Code, jaRec.Body.String())
	}
	var ja struct {
		Assets []map[string]any `json:"assets"`
	}
	_ = json.Unmarshal(jaRec.Body.Bytes(), &ja)
	if len(ja.Assets) != 1 {
		t.Fatalf("expected 1 delivered asset, got %d", len(ja.Assets))
	}
	assetID := ja.Assets[0]["id"].(string)

	// Read the asset directly and confirm the presigned URLs.
	aRec := deliveryReq(t, r, http.MethodGet, "/v1/assets/"+assetID, []string{"images:read"}, nil)
	if aRec.Code != http.StatusOK {
		t.Fatalf("GET asset expected 200, got %d body=%s", aRec.Code, aRec.Body.String())
	}
	var asset map[string]any
	_ = json.Unmarshal(aRec.Body.Bytes(), &asset)

	finalURL, _ := asset["final_download_url"].(string)
	previewURL, _ := asset["preview_download_url"].(string)
	thumbURL, _ := asset["thumbnail_download_url"].(string)
	if finalURL == "" || previewURL == "" || thumbURL == "" {
		t.Fatalf("expected three presigned URLs, got final=%q preview=%q thumb=%q", finalURL, previewURL, thumbURL)
	}
	if asset["url_expires_at"] == nil {
		t.Fatal("expected url_expires_at")
	}
	// The durable s3:// provenance must survive untouched.
	if lr, _ := asset["low_res_url"].(string); !strings.HasPrefix(lr, "s3://") {
		t.Fatalf("s3:// provenance must be preserved, got %q", lr)
	}

	// The presigned URLs must actually fetch from MinIO, and the three tiers
	// must be genuinely distinct sizes (PRD 06 §4).
	fw, fh := fetchPNGDims(t, finalURL)
	pw, ph := fetchPNGDims(t, previewURL)
	tw, th := fetchPNGDims(t, thumbURL)

	if tw >= pw || pw >= fw {
		t.Fatalf("expected distinct tier widths thumb(%d) < preview(%d) < final(%d)", tw, pw, fw)
	}
	if th >= ph || ph >= fh {
		t.Fatalf("expected distinct tier heights thumb(%d) < preview(%d) < final(%d)", th, ph, fh)
	}
}

// TestEndToEndStylePreviewDelivery is the Phase 6B style-preview acceptance:
// previewing a style renders a sample asset that is retrievable through the
// same presigned read machinery.
func TestEndToEndStylePreviewDelivery(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	store := openTestStorage(t)

	jobsRepo := jobs.NewRepository(pool)
	assetsRepo := assets.NewRepository(pool)
	stylesRepo := styles.NewRepository(pool)
	svc := jobs.NewService(pool, newRecordingEnqueuer(), cost.NewService(nil))
	r := mountDeliveryRouter(svc, stylesRepo, jobsRepo, assetsRepo, store)

	rec := deliveryReq(t, r, http.MethodPost, "/v1/styles/"+itStyleID+"/preview", []string{"images:write"}, map[string]any{"world_id": "w1"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("style preview expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	var acc map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &acc)
	jobID := acc["job_id"].(string)

	worker := &jobs.Worker{Jobs: jobsRepo, Assets: assetsRepo, Storage: store, Provider: mock.New()}
	if err := worker.Process(context.Background(), jobID, 0); err != nil {
		t.Fatalf("worker process preview: %v", err)
	}

	jaRec := deliveryReq(t, r, http.MethodGet, "/v1/jobs/"+jobID+"/assets", []string{"images:read"}, nil)
	if jaRec.Code != http.StatusOK {
		t.Fatalf("GET preview job-assets expected 200, got %d body=%s", jaRec.Code, jaRec.Body.String())
	}
	var ja struct {
		Assets []map[string]any `json:"assets"`
	}
	_ = json.Unmarshal(jaRec.Body.Bytes(), &ja)
	if len(ja.Assets) != 1 {
		t.Fatalf("expected 1 preview asset, got %d", len(ja.Assets))
	}
	finalURL, _ := ja.Assets[0]["final_download_url"].(string)
	if finalURL == "" {
		t.Fatal("preview asset must carry a presigned final URL")
	}
	// The preview sample is genuinely retrievable through the presigned read.
	if w, h := fetchPNGDims(t, finalURL); w == 0 || h == 0 {
		t.Fatalf("preview image must be fetchable, got %dx%d", w, h)
	}
}
