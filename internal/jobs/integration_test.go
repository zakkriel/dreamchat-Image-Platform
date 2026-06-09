//go:build integration

package jobs_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zakkriel/drchat-image-platform/internal/assets"
	"github.com/zakkriel/drchat-image-platform/internal/auth"
	"github.com/zakkriel/drchat-image-platform/internal/config"
	"github.com/zakkriel/drchat-image-platform/internal/http/handlers"
	"github.com/zakkriel/drchat-image-platform/internal/idempotency"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
	"github.com/zakkriel/drchat-image-platform/internal/providers/mock"
	"github.com/zakkriel/drchat-image-platform/internal/storage"
	"github.com/zakkriel/drchat-image-platform/internal/styles"
	"github.com/zakkriel/drchat-image-platform/internal/telemetry"
)

// To run end-to-end (requires Postgres + MinIO running):
//   POSTGRES_DSN=postgres://image_platform:image_platform@localhost:5432/image_platform?sslmode=disable \
//   S3_BUCKET=image-platform S3_REGION=us-east-1 \
//   S3_ENDPOINT=http://localhost:9000 \
//   S3_ACCESS_KEY_ID=minioadmin S3_SECRET_ACCESS_KEY=minioadmin \
//   S3_USE_PATH_STYLE=true \
//   go test -tags=integration ./internal/jobs/...

const (
	itTenant  = "tenant_it_jobs"
	itStyleID = "sty_it_jobs"
	itTokenID = "tok_it_jobs"
)

func openTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_DSN not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("pool.Ping: %v", err)
	}
	return pool
}

func cleanup(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Logf("cleanup %q: %v", sql, err)
		}
	}
	exec(`DELETE FROM generation_cost_events WHERE tenant_id = $1`, itTenant)
	exec(`DELETE FROM provider_attempts WHERE generation_job_id IN (SELECT id FROM generation_jobs WHERE tenant_id = $1)`, itTenant)
	exec(`DELETE FROM idempotency_keys WHERE token_id = $1`, itTokenID)
	exec(`DELETE FROM visual_assets WHERE tenant_id = $1`, itTenant)
	exec(`DELETE FROM generation_jobs WHERE tenant_id = $1`, itTenant)
	exec(`DELETE FROM api_tokens WHERE id = $1`, itTokenID)
	exec(`DELETE FROM style_profiles WHERE tenant_id = $1`, itTenant)
}

func seedFixtures(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`INSERT INTO api_tokens (id, tenant_id, token_prefix, token_hash, name, owner_type, scopes, environment, status)
		 VALUES ($1, $2, $3, 'h', 't', 'tenant', ARRAY['images:write','jobs:read','images:read'], 'dev', 'active')`,
		itTokenID, itTenant, "dci_it_jobs",
	); err != nil {
		t.Fatalf("seed token: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO style_profiles (id, tenant_id, name, style_mode, positive_prompt, default_quality_tier, status)
		 VALUES ($1, $2, 'it', 'open_prompt', 'p', 'standard', 'active')`,
		itStyleID, itTenant,
	); err != nil {
		t.Fatalf("seed style: %v", err)
	}
}

func TestEndToEndArtifactGeneration(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)

	bucket := os.Getenv("S3_BUCKET")
	if bucket == "" {
		t.Skip("S3 env vars not set; skipping end-to-end test")
	}
	store, err := storage.NewS3Storage(context.Background(), storage.S3Config{
		Bucket:          bucket,
		Region:          os.Getenv("S3_REGION"),
		Endpoint:        os.Getenv("S3_ENDPOINT"),
		AccessKeyID:     os.Getenv("S3_ACCESS_KEY_ID"),
		SecretAccessKey: os.Getenv("S3_SECRET_ACCESS_KEY"),
		UsePathStyle:    true,
	})
	if err != nil {
		t.Fatalf("NewS3Storage: %v", err)
	}

	jobsRepo := jobs.NewRepository(pool)
	assetsRepo := assets.NewRepository(pool)
	stylesRepo := styles.NewRepository(pool)
	idemRepo := idempotency.NewRepository(pool)

	enq := &inProcessEnqueuer{
		worker: &jobs.Worker{
			Jobs:     jobsRepo,
			Assets:   assetsRepo,
			Storage:  store,
			Provider: mock.New(),
		},
	}

	h := handlers.NewArtifactsHandler(jobsRepo, stylesRepo, enq, config.ProviderMock)
	jobsH := handlers.NewJobsHandler(jobsRepo)
	r := chi.NewRouter()
	idemMW := idempotency.Middleware(idempotency.Deps{Repo: idemRepo})
	r.With(idemMW).Post("/v1/artifacts/{artifact_id}/generate", h.Generate)
	r.Get("/v1/jobs/{job_id}", jobsH.Get)

	body, _ := json.Marshal(map[string]any{
		"world_id":         "w1",
		"style_profile_id": itStyleID,
		"description":      "A bronze key",
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/artifacts/art_int/generate", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	ctx := auth.ContextWithPrincipal(req.Context(), &auth.Principal{
		TokenID:  itTokenID,
		TenantID: itTenant,
		Scopes:   []string{"images:write"},
	})
	ctx = telemetry.ContextWithRequestID(ctx, "req_test")
	ctx = telemetry.ContextWithRequestLog(ctx, &telemetry.RequestLog{})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	jobID, _ := resp["job_id"].(string)

	if err := enq.worker.Process(context.Background(), jobID, 0); err != nil {
		t.Fatalf("worker process: %v", err)
	}

	// Poll the job.
	getReq := httptest.NewRequest(http.MethodGet, "/v1/jobs/"+jobID, nil)
	getCtx := auth.ContextWithPrincipal(getReq.Context(), &auth.Principal{
		TokenID:  itTokenID,
		TenantID: itTenant,
		Scopes:   []string{"jobs:read"},
	})
	getCtx = telemetry.ContextWithRequestID(getCtx, "req_test")
	getCtx = telemetry.ContextWithRequestLog(getCtx, &telemetry.RequestLog{})
	getReq = getReq.WithContext(getCtx)
	getRec := httptest.NewRecorder()
	r.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET job expected 200, got %d body=%s", getRec.Code, getRec.Body.String())
	}
	var jobBody map[string]any
	_ = json.Unmarshal(getRec.Body.Bytes(), &jobBody)
	if jobBody["status"] != "completed" {
		t.Fatalf("expected job status=completed, got %v", jobBody["status"])
	}
	finalIDs, _ := jobBody["final_asset_ids"].([]any)
	if len(finalIDs) != 1 {
		t.Fatalf("expected 1 final_asset_id, got %v", finalIDs)
	}

	// Verify visual_assets row has three URLs.
	var lowURL, highURL, thumbURL *string
	if err := pool.QueryRow(context.Background(),
		`SELECT low_res_url, high_res_url, thumbnail_url FROM visual_assets WHERE id = $1`,
		finalIDs[0],
	).Scan(&lowURL, &highURL, &thumbURL); err != nil {
		t.Fatalf("read asset row: %v", err)
	}
	if lowURL == nil || highURL == nil || thumbURL == nil {
		t.Fatalf("expected three URLs populated, got low=%v high=%v thumb=%v", lowURL, highURL, thumbURL)
	}
}

type inProcessEnqueuer struct {
	worker *jobs.Worker
}

func (e *inProcessEnqueuer) EnqueueGenerateArtifact(context.Context, string) error {
	// Tests drive the worker explicitly to keep the in-process flow
	// deterministic; this stub just records the intent.
	return nil
}
