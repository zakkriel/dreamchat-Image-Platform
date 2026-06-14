package http

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zakkriel/drchat-image-platform/internal/assets"
	"github.com/zakkriel/drchat-image-platform/internal/auth"
	"github.com/zakkriel/drchat-image-platform/internal/config"
	"github.com/zakkriel/drchat-image-platform/internal/identities"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
	"github.com/zakkriel/drchat-image-platform/internal/providers/routing"
	"github.com/zakkriel/drchat-image-platform/internal/styles"
)

type noopStylesRepo struct{}

func (noopStylesRepo) ListActiveByTenant(context.Context, string) ([]styles.StyleProfile, error) {
	return nil, nil
}
func (noopStylesRepo) Create(context.Context, styles.CreateParams) (styles.StyleProfile, error) {
	return styles.StyleProfile{ID: "sty_test", Status: "active", DefaultQualityTier: "standard"}, nil
}
func (noopStylesRepo) GetByIDForTenant(context.Context, string, string) (styles.StyleProfile, error) {
	return styles.StyleProfile{}, styles.ErrNotFound
}

type noopIdentitiesRepo struct{}

func (noopIdentitiesRepo) Upsert(context.Context, identities.UpsertParams) (identities.VisualIdentity, error) {
	return identities.VisualIdentity{ID: "vi_test", CurrentVersion: 1, Status: "active"}, nil
}
func (noopIdentitiesRepo) GetByOwner(context.Context, string, string, string, string) (identities.VisualIdentity, error) {
	return identities.VisualIdentity{}, identities.ErrNotFound
}
func (noopIdentitiesRepo) GetByIDForTenant(context.Context, string, string) (identities.VisualIdentity, error) {
	return identities.VisualIdentity{}, identities.ErrNotFound
}

type noopAssetsRepo struct{}

func (noopAssetsRepo) GetByIDForTenant(context.Context, string, string) (assets.VisualAsset, error) {
	return assets.VisualAsset{}, assets.ErrNotFound
}

func (noopAssetsRepo) Insert(context.Context, assets.InsertParams) (assets.VisualAsset, error) {
	return assets.VisualAsset{}, nil
}

func (noopAssetsRepo) InsertPreview(context.Context, assets.InsertParams) (assets.VisualAsset, error) {
	return assets.VisualAsset{}, nil
}

func (noopAssetsRepo) SupersedeAndInsertArtifact(context.Context, assets.InsertParams, assets.ArtifactSlot) (assets.VisualAsset, error) {
	return assets.VisualAsset{}, nil
}

func (noopAssetsRepo) FindExact(context.Context, assets.RetrievalQuery) (assets.VisualAsset, error) {
	return assets.VisualAsset{}, assets.ErrNotFound
}

func (noopAssetsRepo) ListRetrievalCandidates(context.Context, assets.RetrievalQuery) ([]assets.VisualAsset, error) {
	return nil, nil
}

func (noopAssetsRepo) ListRetrievalCandidatesByCompatTag(context.Context, assets.RetrievalQuery, []string) ([]assets.VisualAsset, error) {
	return nil, nil
}

func (noopAssetsRepo) FindReadyArtifactByPromptHash(context.Context, assets.ArtifactLookup) (assets.VisualAsset, error) {
	return assets.VisualAsset{}, assets.ErrNotFound
}

type noopJobsRepo struct{}

func (noopJobsRepo) Insert(context.Context, jobs.InsertParams) (jobs.Job, error) {
	return jobs.Job{Status: "queued"}, nil
}
func (noopJobsRepo) GetByIDForTenant(context.Context, string, string) (jobs.Job, error) {
	return jobs.Job{}, jobs.ErrNotFound
}
func (noopJobsRepo) GetByID(context.Context, string) (jobs.Job, error) {
	return jobs.Job{}, jobs.ErrNotFound
}
func (noopJobsRepo) MarkRunning(context.Context, string, string) (jobs.Job, error) {
	return jobs.Job{}, nil
}
func (noopJobsRepo) MarkPreviewReady(context.Context, string, string, []string) (jobs.Job, error) {
	return jobs.Job{}, nil
}
func (noopJobsRepo) MarkCompleted(context.Context, string, string, []string) (jobs.Job, error) {
	return jobs.Job{}, nil
}
func (noopJobsRepo) MarkFailed(context.Context, string, string, string, string, bool) (jobs.Job, error) {
	return jobs.Job{}, nil
}
func (noopJobsRepo) InsertFinalAssetAndCompleteJobIfNotCancelled(context.Context, string, string, assets.InsertParams, bool, assets.ArtifactSlot) (assets.VisualAsset, jobs.PersistOutcome, error) {
	return assets.VisualAsset{}, jobs.PersistPersisted, nil
}
func (noopJobsRepo) InsertPreviewAssetAndMarkPreviewReadyIfNotCancelled(context.Context, string, string, assets.InsertParams) (assets.VisualAsset, jobs.PersistOutcome, error) {
	return assets.VisualAsset{}, jobs.PersistPersisted, nil
}
func (noopJobsRepo) InsertProviderAttempt(context.Context, jobs.ProviderAttemptInsertParams) (jobs.ProviderAttempt, error) {
	return jobs.ProviderAttempt{}, nil
}
func (noopJobsRepo) MarkProviderAttemptSucceeded(context.Context, string, int32) error { return nil }
func (noopJobsRepo) MarkProviderAttemptFailed(context.Context, string, string, string, int32) error {
	return nil
}
func (noopJobsRepo) CountProviderAttempts(context.Context, string) (int32, error) { return 1, nil }
func (noopJobsRepo) InsertCostEvent(context.Context, jobs.CostEventInsertParams) error {
	return nil
}
func (noopJobsRepo) UpdateAssetPackStatus(context.Context, string, string) error { return nil }
func (noopJobsRepo) UpdateAssetPackCompleteness(context.Context, string, []string, []string) error {
	return nil
}
func (noopJobsRepo) InsertAssetPackItem(context.Context, jobs.AssetPackItemInsertParams) error {
	return nil
}
func (noopJobsRepo) InsertPackItemWithAsset(context.Context, assets.InsertParams, jobs.AssetPackItemInsertParams) error {
	return nil
}
func (noopJobsRepo) InsertPackItemWithAssetSuperseding(context.Context, assets.InsertParams, jobs.AssetPackItemInsertParams, assets.VariantSlot) error {
	return nil
}
func (noopJobsRepo) ListAssetPackItems(context.Context, string) ([]jobs.AssetPackItem, error) {
	return nil, nil
}

type noopJobsService struct{}

func (noopJobsService) LookupReplay(context.Context, jobs.ReplayLookup) (jobs.CreateResult, bool, error) {
	return jobs.CreateResult{}, false, nil
}

func (noopJobsService) CreateAndEnqueue(context.Context, jobs.CreateAndEnqueueParams) (jobs.CreateResult, error) {
	return jobs.CreateResult{JobID: "job_routerstubaaaaaaa"}, nil
}

func (noopJobsService) CreateCompletedCacheHitJob(context.Context, jobs.CreateCacheHitParams) (jobs.CreateResult, error) {
	return jobs.CreateResult{JobID: "job_routerstubaaaaaaa", Status: "completed"}, nil
}

func (noopJobsService) CreateCompletedPackReuseJob(context.Context, jobs.CreatePackReuseParams) (jobs.CreateResult, error) {
	return jobs.CreateResult{JobID: "job_routerstubaaaaaaa", Status: "completed"}, nil
}

const (
	testPepper      = "test-pepper"
	testPrefix      = "dci_dev_abc123"
	testSecret      = "supersecret"
	testTokenID     = "tok_test"
	testTenantID    = "tenant_test"
	testEnvironment = "dev"
)

type stubRepo struct {
	token       auth.Token
	getErr      error
	touchCalled chan string
}

func newStubRepo() *stubRepo {
	hash := sha256.Sum256([]byte(testSecret + testPepper))
	return &stubRepo{
		token: auth.Token{
			ID:          testTokenID,
			TenantID:    testTenantID,
			TokenHash:   hex.EncodeToString(hash[:]),
			Scopes:      []string{"images:read", "images:write"},
			Environment: testEnvironment,
			Status:      "active",
		},
		touchCalled: make(chan string, 1),
	}
}

func (s *stubRepo) GetActiveAPITokenByPrefix(_ context.Context, prefix string) (auth.Token, error) {
	if s.getErr != nil {
		return auth.Token{}, s.getErr
	}
	if prefix != testPrefix {
		return auth.Token{}, auth.ErrTokenNotFound
	}
	return s.token, nil
}

func (s *stubRepo) TouchAPITokenLastUsed(_ context.Context, id string) error {
	select {
	case s.touchCalled <- id:
	default:
	}
	return nil
}

func newTestDeps(t *testing.T, repo auth.Repository, env config.Environment, docsEnabled bool) Deps {
	t.Helper()
	return Deps{
		Logger:   slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Config:   &config.Config{Environment: env, APITokenPepper: testPepper, OpenAPIDocsEnabled: docsEnabled},
		AuthRepo: repo,
	}
}

func newPhase2Deps(t *testing.T, repo *stubRepo) Deps {
	t.Helper()
	deps := newTestDeps(t, repo, config.EnvDev, true)
	deps.StylesRepo = &noopStylesRepo{}
	deps.IdentitiesRepo = &noopIdentitiesRepo{}
	deps.AssetsRepo = &noopAssetsRepo{}
	return deps
}

func withPhase3Stubs(d Deps) Deps {
	d.JobsRepo = &noopJobsRepo{}
	d.JobsService = noopJobsService{}
	d.Resolver = noopResolver{}
	if d.Config != nil {
		d.Config.ImageProvider = config.ProviderMock
	}
	return d
}

// noopResolver satisfies handlers.RouteResolver for the router mounting tests:
// it always resolves the seeded mock route.
type noopResolver struct{}

func (noopResolver) Resolve(context.Context, routing.ResolveRequest) (routing.ResolvedRoute, error) {
	return routing.ResolvedRoute{
		ProviderID:      "mock",
		ProviderRouteID: "route_mock_text_to_image_standard",
		ProviderModelID: "pm_mock_v1",
		OperationType:   "text_to_image",
	}, nil
}

// ResolveChain returns a single-element chain (the seeded mock route) so the
// Phase 7C-4 fallback wiring is exercised without adding alternates.
func (n noopResolver) ResolveChain(ctx context.Context, req routing.ResolveRequest) ([]routing.ResolvedRoute, error) {
	route, err := n.Resolve(ctx, req)
	if err != nil {
		return nil, err
	}
	return []routing.ResolvedRoute{route}, nil
}

func TestHealthEndpoint(t *testing.T) {
	r := NewRouter(newTestDeps(t, newStubRepo(), config.EnvDev, true))

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := rec.Header().Get(HeaderRequestID); got == "" {
		t.Fatalf("expected %s header", HeaderRequestID)
	}
	var body HealthResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Status != "ok" {
		t.Fatalf("expected status ok, got %s", body.Status)
	}
}

func TestRequestIDPassthrough(t *testing.T) {
	r := NewRouter(newTestDeps(t, newStubRepo(), config.EnvDev, true))

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set(HeaderRequestID, "fixed-id")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if got := rec.Header().Get(HeaderRequestID); got != "fixed-id" {
		t.Fatalf("expected request id passthrough, got %q", got)
	}
}

func TestOpenAPIJSONIsValid(t *testing.T) {
	r := NewRouter(newTestDeps(t, newStubRepo(), config.EnvDev, true))

	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("expected application/json, got %q", ct)
	}

	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode openapi.json: %v", err)
	}
	if got, _ := doc["openapi"].(string); !strings.HasPrefix(got, "3.") {
		t.Fatalf("expected openapi 3.x, got %v", doc["openapi"])
	}
	if _, ok := doc["paths"].(map[string]any); !ok {
		t.Fatalf("expected paths object")
	}
}

func TestDocsHTMLReferencesOpenAPIJSON(t *testing.T) {
	r := NewRouter(newTestDeps(t, newStubRepo(), config.EnvDev, true))

	req := httptest.NewRequest(http.MethodGet, "/docs", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("expected text/html, got %q", ct)
	}
	if !strings.Contains(rec.Body.String(), "/openapi.json") {
		t.Fatalf("expected docs page to reference /openapi.json")
	}
	if !strings.Contains(rec.Body.String(), "swagger-ui") {
		t.Fatalf("expected docs page to load swagger-ui assets")
	}
}

func TestDocsGatedInLiveWhenDisabled(t *testing.T) {
	r := NewRouter(newTestDeps(t, newStubRepo(), config.EnvLive, false))

	for _, path := range []string{"/openapi.json", "/docs"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected 404 for %s in live with docs disabled, got %d", path, rec.Code)
		}
	}
}

func TestDocsRequireAdminReadScopeInLive(t *testing.T) {
	repo := newStubRepo()
	repo.token.Environment = "live"
	r := NewRouter(newTestDeps(t, repo, config.EnvLive, true))

	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	req.Header.Set("Authorization", "Bearer "+testPrefix+"_"+testSecret)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing admin:read scope, got %d", rec.Code)
	}

	repo.token.Scopes = append(repo.token.Scopes, "admin:read")
	req2 := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	req2.Header.Set("Authorization", "Bearer "+testPrefix+"_"+testSecret)
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 with admin:read scope, got %d", rec2.Code)
	}
}

func TestV1StylesWithoutAuthReturns401(t *testing.T) {
	r := NewRouter(newTestDeps(t, newStubRepo(), config.EnvDev, true))

	req := httptest.NewRequest(http.MethodGet, "/v1/styles", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Fatalf("expected application/problem+json, got %q", ct)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["code"] != "unauthorized" {
		t.Fatalf("expected code=unauthorized, got %v", body["code"])
	}
}

func TestV1StylesWithStylesReadScopeReturns200(t *testing.T) {
	repo := newStubRepo()
	repo.token.Scopes = []string{"styles:read", "styles:write"}
	r := NewRouter(newPhase2Deps(t, repo))

	req := httptest.NewRequest(http.MethodGet, "/v1/styles", nil)
	req.Header.Set("Authorization", "Bearer "+testPrefix+"_"+testSecret)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case id := <-repo.touchCalled:
		if id != testTokenID {
			t.Fatalf("expected touch for token id %s, got %s", testTokenID, id)
		}
	case <-time.After(time.Second):
		t.Fatalf("expected touch goroutine to fire within 1s")
	}
}

func TestV1StylesRequiresStylesReadScope(t *testing.T) {
	repo := newStubRepo()
	repo.token.Scopes = []string{"images:read"}
	r := NewRouter(newPhase2Deps(t, repo))

	req := httptest.NewRequest(http.MethodGet, "/v1/styles", nil)
	req.Header.Set("Authorization", "Bearer "+testPrefix+"_"+testSecret)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["code"] != "forbidden" {
		t.Fatalf("expected code=forbidden, got %v", body["code"])
	}
}

func TestV1VisualIdentityPostRequiresImagesWrite(t *testing.T) {
	repo := newStubRepo()
	repo.token.Scopes = []string{"images:read"}
	r := NewRouter(newPhase2Deps(t, repo))

	req := httptest.NewRequest(http.MethodPost, "/v1/characters/char_alice/visual-identity", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+testPrefix+"_"+testSecret)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestV1ArtifactGenerateRequiresImagesWrite(t *testing.T) {
	repo := newStubRepo()
	repo.token.Scopes = []string{"images:read"}
	deps := newPhase2Deps(t, repo)
	deps = withPhase3Stubs(deps)
	r := NewRouter(deps)

	req := httptest.NewRequest(http.MethodPost, "/v1/artifacts/art_1/generate", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+testPrefix+"_"+testSecret)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestV1JobsGetRequiresJobsRead(t *testing.T) {
	repo := newStubRepo()
	repo.token.Scopes = []string{"images:read"}
	deps := withPhase3Stubs(newPhase2Deps(t, repo))
	r := NewRouter(deps)

	req := httptest.NewRequest(http.MethodGet, "/v1/jobs/job_abc", nil)
	req.Header.Set("Authorization", "Bearer "+testPrefix+"_"+testSecret)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestV1ArtifactGenerateWithoutAuthReturns401(t *testing.T) {
	deps := withPhase3Stubs(newPhase2Deps(t, newStubRepo()))
	r := NewRouter(deps)
	req := httptest.NewRequest(http.MethodPost, "/v1/artifacts/art_1/generate", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}
