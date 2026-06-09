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
