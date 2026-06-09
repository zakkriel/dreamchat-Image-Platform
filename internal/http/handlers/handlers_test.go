package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/zakkriel/drchat-image-platform/internal/assets"
	"github.com/zakkriel/drchat-image-platform/internal/auth"
	"github.com/zakkriel/drchat-image-platform/internal/identities"
	"github.com/zakkriel/drchat-image-platform/internal/styles"
	"github.com/zakkriel/drchat-image-platform/internal/telemetry"
)

const (
	tenantA = "tenant_a"
	tenantB = "tenant_b"
)

func init() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(io.Discard, nil)))
}

// ---------------------------------------------------------------------------
// Stub repositories
// ---------------------------------------------------------------------------

type stubStylesRepo struct {
	byTenant   map[string][]styles.StyleProfile
	created    []styles.CreateParams
	createErr  error
	listErr    error
	getErr     error
	tenantData map[string]map[string]styles.StyleProfile
}

func newStubStylesRepo() *stubStylesRepo {
	return &stubStylesRepo{
		byTenant:   map[string][]styles.StyleProfile{},
		tenantData: map[string]map[string]styles.StyleProfile{},
	}
}

func (s *stubStylesRepo) seed(profile styles.StyleProfile) {
	s.byTenant[profile.TenantID] = append(s.byTenant[profile.TenantID], profile)
	if _, ok := s.tenantData[profile.TenantID]; !ok {
		s.tenantData[profile.TenantID] = map[string]styles.StyleProfile{}
	}
	s.tenantData[profile.TenantID][profile.ID] = profile
}

func (s *stubStylesRepo) ListActiveByTenant(_ context.Context, tenantID string) ([]styles.StyleProfile, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return append([]styles.StyleProfile(nil), s.byTenant[tenantID]...), nil
}

func (s *stubStylesRepo) Create(_ context.Context, params styles.CreateParams) (styles.StyleProfile, error) {
	if s.createErr != nil {
		return styles.StyleProfile{}, s.createErr
	}
	s.created = append(s.created, params)
	out := styles.StyleProfile{
		ID:                 params.ID,
		TenantID:           params.TenantID,
		Name:               params.Name,
		StyleMode:          params.StyleMode,
		PositivePrompt:     params.PositivePrompt,
		NegativePrompt:     params.NegativePrompt,
		DefaultQualityTier: params.DefaultQualityTier,
		Status:             "active",
	}
	s.seed(out)
	return out, nil
}

func (s *stubStylesRepo) GetByIDForTenant(_ context.Context, id, tenantID string) (styles.StyleProfile, error) {
	if s.getErr != nil {
		return styles.StyleProfile{}, s.getErr
	}
	if data, ok := s.tenantData[tenantID]; ok {
		if profile, found := data[id]; found {
			return profile, nil
		}
	}
	return styles.StyleProfile{}, styles.ErrNotFound
}

type identityKey struct {
	tenantID, worldID, ownerType, ownerID string
}

type stubIdentitiesRepo struct {
	mu              sync.Mutex
	byOwner         map[identityKey]identities.VisualIdentity
	tenantStyleOK   map[string]map[string]bool // tenantID -> styleID -> ok
	versionsWritten []versionEntry
}

type versionEntry struct {
	IdentityID string
	Version    int32
	Reason     string
}

func newStubIdentitiesRepo() *stubIdentitiesRepo {
	return &stubIdentitiesRepo{
		byOwner:       map[identityKey]identities.VisualIdentity{},
		tenantStyleOK: map[string]map[string]bool{},
	}
}

func (s *stubIdentitiesRepo) registerStyle(tenantID, styleID string) {
	if _, ok := s.tenantStyleOK[tenantID]; !ok {
		s.tenantStyleOK[tenantID] = map[string]bool{}
	}
	s.tenantStyleOK[tenantID][styleID] = true
}

func (s *stubIdentitiesRepo) Upsert(_ context.Context, params identities.UpsertParams) (identities.VisualIdentity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.tenantStyleOK[params.TenantID][params.StyleProfileID] {
		return identities.VisualIdentity{}, identities.ErrInvalidStyle
	}
	key := identityKey{params.TenantID, params.WorldID, params.OwnerType, params.OwnerID}
	existing, found := s.byOwner[key]
	if !found {
		row := identities.VisualIdentity{
			ID:                    params.NewID,
			TenantID:              params.TenantID,
			WorldID:               params.WorldID,
			OwnerType:             params.OwnerType,
			OwnerID:               params.OwnerID,
			DisplayName:           params.DisplayName,
			CanonicalVisualTraits: params.CanonicalVisualTraits,
			StyleProfileID:        params.StyleProfileID,
			ConsistencyKey:        params.ConsistencyKey,
			CurrentVersion:        1,
			Status:                "active",
		}
		s.byOwner[key] = row
		s.versionsWritten = append(s.versionsWritten, versionEntry{row.ID, 1, "initial"})
		return row, nil
	}
	if reflect.DeepEqual(existing.CanonicalVisualTraits, params.CanonicalVisualTraits) &&
		existing.StyleProfileID == params.StyleProfileID &&
		ptrEqual(existing.ConsistencyKey, params.ConsistencyKey) {
		return existing, nil
	}
	existing.DisplayName = params.DisplayName
	existing.CanonicalVisualTraits = params.CanonicalVisualTraits
	existing.StyleProfileID = params.StyleProfileID
	existing.ConsistencyKey = params.ConsistencyKey
	existing.CurrentVersion++
	s.byOwner[key] = existing
	s.versionsWritten = append(s.versionsWritten, versionEntry{existing.ID, existing.CurrentVersion, "canonical_change"})
	return existing, nil
}

func ptrEqual(a, b *string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func (s *stubIdentitiesRepo) GetByOwner(_ context.Context, tenantID, worldID, ownerType, ownerID string) (identities.VisualIdentity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := identityKey{tenantID, worldID, ownerType, ownerID}
	if v, ok := s.byOwner[key]; ok {
		return v, nil
	}
	return identities.VisualIdentity{}, identities.ErrNotFound
}

func (s *stubIdentitiesRepo) GetByIDForTenant(_ context.Context, id, tenantID string) (identities.VisualIdentity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.byOwner {
		if v.ID == id && v.TenantID == tenantID {
			return v, nil
		}
	}
	return identities.VisualIdentity{}, identities.ErrNotFound
}

type stubAssetsRepo struct {
	byID map[string]assets.VisualAsset
}

func newStubAssetsRepo() *stubAssetsRepo {
	return &stubAssetsRepo{byID: map[string]assets.VisualAsset{}}
}

func (s *stubAssetsRepo) GetByIDForTenant(_ context.Context, id, tenantID string) (assets.VisualAsset, error) {
	row, ok := s.byID[id]
	if !ok || row.TenantID != tenantID {
		return assets.VisualAsset{}, assets.ErrNotFound
	}
	return row, nil
}

func (s *stubAssetsRepo) Insert(_ context.Context, params assets.InsertParams) (assets.VisualAsset, error) {
	asset := assets.VisualAsset{
		ID:           params.ID,
		TenantID:     params.TenantID,
		WorldID:      params.WorldID,
		AssetType:    params.AssetType,
		VariantKey:   params.VariantKey,
		Status:       "ready",
		LowResUrl:    params.LowResUrl,
		HighResUrl:   params.HighResUrl,
		ThumbnailUrl: params.ThumbnailUrl,
		ProviderID:   params.ProviderID,
		ModelID:      params.ModelID,
		PromptHash:   params.PromptHash,
		Seed:         params.Seed,
	}
	s.byID[params.ID] = asset
	return asset, nil
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func authedContext(tenantID string, scopes ...string) context.Context {
	ctx := telemetry.ContextWithRequestID(context.Background(), "req_test")
	ctx = telemetry.ContextWithRequestLog(ctx, &telemetry.RequestLog{})
	return auth.ContextWithPrincipal(ctx, &auth.Principal{
		TokenID:     "tok_test",
		TenantID:    tenantID,
		Scopes:      scopes,
		Environment: "dev",
	})
}

// newCharacterRouter mounts the identity routes the way the production
// router does, so chi.URLParam works inside the handler.
func newCharacterRouter(repo identities.Repository, idFn func() string) chi.Router {
	h := NewIdentitiesHandler(repo)
	if idFn != nil {
		h.NewID = idFn
	}
	r := chi.NewRouter()
	r.Post("/v1/characters/{character_id}/visual-identity", h.UpsertCharacter)
	r.Get("/v1/characters/{character_id}/visual-identity", h.GetCharacter)
	r.Post("/v1/places/{place_id}/visual-identity", h.UpsertPlace)
	r.Get("/v1/places/{place_id}/visual-identity", h.GetPlace)
	return r
}

func newStylesRouter(repo styles.Repository, idFn func() string) chi.Router {
	h := NewStylesHandler(repo)
	if idFn != nil {
		h.NewID = idFn
	}
	r := chi.NewRouter()
	r.Get("/v1/styles", h.List)
	r.Post("/v1/styles", h.Create)
	return r
}

func newAssetsRouter(repo assets.Repository) chi.Router {
	h := NewAssetsHandler(repo)
	r := chi.NewRouter()
	r.Get("/v1/assets/{asset_id}", h.Get)
	return r
}

func sendJSON(t *testing.T, h http.Handler, method, path, tenant string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if raw, ok := body.(json.RawMessage); ok {
			buf.Write(raw)
		} else {
			if err := json.NewEncoder(&buf).Encode(body); err != nil {
				t.Fatalf("encode body: %v", err)
			}
		}
	}
	req := httptest.NewRequest(method, path, &buf).WithContext(authedContext(tenant, "images:read", "images:write", "styles:read", "styles:write"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rec.Body.String())
	}
	return out
}

// ---------------------------------------------------------------------------
// Styles tests
// ---------------------------------------------------------------------------

func TestStylesListReturnsOnlyCallingTenant(t *testing.T) {
	repo := newStubStylesRepo()
	repo.seed(styles.StyleProfile{ID: "sty_aaa", TenantID: tenantA, Name: "a", StyleMode: "open_prompt", PositivePrompt: "x", DefaultQualityTier: "standard", Status: "active"})
	repo.seed(styles.StyleProfile{ID: "sty_bbb", TenantID: tenantB, Name: "b", StyleMode: "open_prompt", PositivePrompt: "x", DefaultQualityTier: "standard", Status: "active"})

	rec := sendJSON(t, newStylesRouter(repo, nil), http.MethodGet, "/v1/styles", tenantA, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := decode[map[string]any](t, rec)
	stylesList, _ := body["styles"].([]any)
	if len(stylesList) != 1 {
		t.Fatalf("expected exactly 1 style, got %d", len(stylesList))
	}
	first := stylesList[0].(map[string]any)
	if first["id"] != "sty_aaa" {
		t.Fatalf("expected sty_aaa, got %v", first["id"])
	}
}

func TestStylesCreateHappyPath(t *testing.T) {
	repo := newStubStylesRepo()
	rec := sendJSON(t, newStylesRouter(repo, func() string { return "sty_1234567890abcdef" }),
		http.MethodPost, "/v1/styles", tenantA,
		map[string]any{
			"name":            "test",
			"style_mode":      "open_prompt",
			"positive_prompt": "watercolor",
		},
	)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := decode[map[string]any](t, rec)
	if body["id"] != "sty_1234567890abcdef" {
		t.Fatalf("expected generated id, got %v", body["id"])
	}
	if body["status"] != "active" {
		t.Fatalf("expected status=active, got %v", body["status"])
	}
	if body["default_quality_tier"] != "standard" {
		t.Fatalf("expected default_quality_tier=standard, got %v", body["default_quality_tier"])
	}
}

func TestStylesCreateMissingNameReturns400(t *testing.T) {
	repo := newStubStylesRepo()
	rec := sendJSON(t, newStylesRouter(repo, nil), http.MethodPost, "/v1/styles", tenantA,
		map[string]any{"style_mode": "open_prompt", "positive_prompt": "watercolor"},
	)
	assertError(t, rec, http.StatusBadRequest, "invalid_request")
}

func TestStylesCreateMissingPositivePromptReturns400(t *testing.T) {
	repo := newStubStylesRepo()
	rec := sendJSON(t, newStylesRouter(repo, nil), http.MethodPost, "/v1/styles", tenantA,
		map[string]any{"name": "n", "style_mode": "open_prompt"},
	)
	assertError(t, rec, http.StatusBadRequest, "invalid_request")
}

func TestStylesCreateInvalidStyleModeReturns400(t *testing.T) {
	repo := newStubStylesRepo()
	rec := sendJSON(t, newStylesRouter(repo, nil), http.MethodPost, "/v1/styles", tenantA,
		map[string]any{"name": "n", "style_mode": "bogus", "positive_prompt": "watercolor"},
	)
	assertError(t, rec, http.StatusBadRequest, "invalid_request")
}

func TestStylesCreateRejectsBodyTenantID(t *testing.T) {
	repo := newStubStylesRepo()
	rec := sendJSON(t, newStylesRouter(repo, nil), http.MethodPost, "/v1/styles", tenantA,
		map[string]any{"tenant_id": "tenant_other", "name": "n", "style_mode": "open_prompt", "positive_prompt": "watercolor"},
	)
	assertError(t, rec, http.StatusBadRequest, "invalid_request")
}

// ---------------------------------------------------------------------------
// Identities tests
// ---------------------------------------------------------------------------

func TestCharacterVisualIdentityCreate(t *testing.T) {
	idents := newStubIdentitiesRepo()
	idents.registerStyle(tenantA, "sty_ok")

	gen := func() string { return "vi_aaaaaaaaaaaaaaaa" }
	router := newCharacterRouter(idents, gen)

	body := map[string]any{
		"world_id":                "w1",
		"owner_type":              "character",
		"owner_id":                "char_alice",
		"display_name":            "Alice",
		"canonical_visual_traits": map[string]any{},
		"style_profile_id":        "sty_ok",
	}
	rec := sendJSON(t, router, http.MethodPost, "/v1/characters/char_alice/visual-identity", tenantA, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	resp := decode[map[string]any](t, rec)
	if resp["id"] != "vi_aaaaaaaaaaaaaaaa" {
		t.Fatalf("expected generated id, got %v", resp["id"])
	}
	if int(resp["current_version"].(float64)) != 1 {
		t.Fatalf("expected current_version=1, got %v", resp["current_version"])
	}
	if len(idents.versionsWritten) != 1 || idents.versionsWritten[0].Reason != "initial" {
		t.Fatalf("expected one initial version, got %+v", idents.versionsWritten)
	}
}

func TestCharacterVisualIdentitySecondUpsertSameFieldsNoVersionBump(t *testing.T) {
	idents := newStubIdentitiesRepo()
	idents.registerStyle(tenantA, "sty_ok")

	router := newCharacterRouter(idents, func() string { return "vi_aaaaaaaaaaaaaaaa" })

	body := map[string]any{
		"world_id":                "w1",
		"owner_type":              "character",
		"owner_id":                "char_alice",
		"display_name":            "Alice",
		"canonical_visual_traits": map[string]any{"hair": "black"},
		"style_profile_id":        "sty_ok",
	}
	if rec := sendJSON(t, router, http.MethodPost, "/v1/characters/char_alice/visual-identity", tenantA, body); rec.Code != http.StatusOK {
		t.Fatalf("first upsert failed: %d %s", rec.Code, rec.Body.String())
	}
	rec := sendJSON(t, router, http.MethodPost, "/v1/characters/char_alice/visual-identity", tenantA, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("second upsert failed: %d %s", rec.Code, rec.Body.String())
	}
	resp := decode[map[string]any](t, rec)
	if int(resp["current_version"].(float64)) != 1 {
		t.Fatalf("expected current_version to stay at 1, got %v", resp["current_version"])
	}
	if len(idents.versionsWritten) != 1 {
		t.Fatalf("expected exactly one version row, got %d", len(idents.versionsWritten))
	}
}

func TestCharacterVisualIdentitySecondUpsertChangedTraitsBumpsVersion(t *testing.T) {
	idents := newStubIdentitiesRepo()
	idents.registerStyle(tenantA, "sty_ok")

	router := newCharacterRouter(idents, func() string { return "vi_aaaaaaaaaaaaaaaa" })

	first := map[string]any{
		"world_id":                "w1",
		"owner_type":              "character",
		"owner_id":                "char_alice",
		"display_name":            "Alice",
		"canonical_visual_traits": map[string]any{},
		"style_profile_id":        "sty_ok",
	}
	second := map[string]any{
		"world_id":                "w1",
		"owner_type":              "character",
		"owner_id":                "char_alice",
		"display_name":            "Alice",
		"canonical_visual_traits": map[string]any{"hair": "black"},
		"style_profile_id":        "sty_ok",
	}
	sendJSON(t, router, http.MethodPost, "/v1/characters/char_alice/visual-identity", tenantA, first)
	rec := sendJSON(t, router, http.MethodPost, "/v1/characters/char_alice/visual-identity", tenantA, second)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	resp := decode[map[string]any](t, rec)
	if int(resp["current_version"].(float64)) != 2 {
		t.Fatalf("expected current_version=2, got %v", resp["current_version"])
	}
	if len(idents.versionsWritten) != 2 || idents.versionsWritten[1].Reason != "canonical_change" {
		t.Fatalf("expected second version with reason=canonical_change, got %+v", idents.versionsWritten)
	}
}

func TestCharacterVisualIdentityOwnerTypeMismatch(t *testing.T) {
	idents := newStubIdentitiesRepo()
	idents.registerStyle(tenantA, "sty_ok")
	router := newCharacterRouter(idents, func() string { return "vi_aaaaaaaaaaaaaaaa" })

	body := map[string]any{
		"world_id":                "w1",
		"owner_type":              "place",
		"owner_id":                "char_alice",
		"display_name":            "Alice",
		"canonical_visual_traits": map[string]any{},
		"style_profile_id":        "sty_ok",
	}
	rec := sendJSON(t, router, http.MethodPost, "/v1/characters/char_alice/visual-identity", tenantA, body)
	assertError(t, rec, http.StatusBadRequest, "invalid_request")
}

func TestCharacterVisualIdentityOwnerIDMismatch(t *testing.T) {
	idents := newStubIdentitiesRepo()
	idents.registerStyle(tenantA, "sty_ok")
	router := newCharacterRouter(idents, func() string { return "vi_aaaaaaaaaaaaaaaa" })

	body := map[string]any{
		"world_id":                "w1",
		"owner_type":              "character",
		"owner_id":                "char_bob",
		"display_name":            "Alice",
		"canonical_visual_traits": map[string]any{},
		"style_profile_id":        "sty_ok",
	}
	rec := sendJSON(t, router, http.MethodPost, "/v1/characters/char_alice/visual-identity", tenantA, body)
	assertError(t, rec, http.StatusBadRequest, "invalid_request")
}

func TestVisualIdentityRejectsBodyTenantID(t *testing.T) {
	idents := newStubIdentitiesRepo()
	idents.registerStyle(tenantA, "sty_ok")
	router := newCharacterRouter(idents, func() string { return "vi_aaaaaaaaaaaaaaaa" })

	body := map[string]any{
		"tenant_id":               "tenant_other",
		"world_id":                "w1",
		"owner_type":              "character",
		"owner_id":                "char_alice",
		"display_name":            "Alice",
		"canonical_visual_traits": map[string]any{},
		"style_profile_id":        "sty_ok",
	}
	rec := sendJSON(t, router, http.MethodPost, "/v1/characters/char_alice/visual-identity", tenantA, body)
	assertError(t, rec, http.StatusBadRequest, "invalid_request")
}

func TestVisualIdentityInvalidStyleProfileReturns422(t *testing.T) {
	idents := newStubIdentitiesRepo()
	// No style registered for tenant.
	router := newCharacterRouter(idents, func() string { return "vi_aaaaaaaaaaaaaaaa" })

	body := map[string]any{
		"world_id":                "w1",
		"owner_type":              "character",
		"owner_id":                "char_alice",
		"display_name":            "Alice",
		"canonical_visual_traits": map[string]any{},
		"style_profile_id":        "sty_ghost",
	}
	rec := sendJSON(t, router, http.MethodPost, "/v1/characters/char_alice/visual-identity", tenantA, body)
	assertError(t, rec, http.StatusUnprocessableEntity, "invalid_style_profile")
}

func TestGetCharacterVisualIdentityWithoutWorldIDReturns400(t *testing.T) {
	idents := newStubIdentitiesRepo()
	router := newCharacterRouter(idents, nil)

	rec := sendJSON(t, router, http.MethodGet, "/v1/characters/char_alice/visual-identity", tenantA, nil)
	assertError(t, rec, http.StatusBadRequest, "invalid_request")
}

func TestGetCharacterVisualIdentityWithEmptyWorldIDReturns400(t *testing.T) {
	idents := newStubIdentitiesRepo()
	router := newCharacterRouter(idents, nil)

	rec := sendJSON(t, router, http.MethodGet, "/v1/characters/char_alice/visual-identity?world_id=", tenantA, nil)
	assertError(t, rec, http.StatusBadRequest, "invalid_request")
}

func TestGetPlaceVisualIdentityWithoutWorldIDReturns400(t *testing.T) {
	idents := newStubIdentitiesRepo()
	router := newCharacterRouter(idents, nil)

	rec := sendJSON(t, router, http.MethodGet, "/v1/places/place_castle/visual-identity", tenantA, nil)
	assertError(t, rec, http.StatusBadRequest, "invalid_request")
}

func TestGetVisualIdentityNotFoundReturns404(t *testing.T) {
	idents := newStubIdentitiesRepo()
	router := newCharacterRouter(idents, nil)

	rec := sendJSON(t, router, http.MethodGet, "/v1/characters/char_ghost/visual-identity?world_id=w1", tenantA, nil)
	assertError(t, rec, http.StatusNotFound, "not_found")
}

func TestGetCharacterVisualIdentityIsWorldScoped(t *testing.T) {
	idents := newStubIdentitiesRepo()
	idents.registerStyle(tenantA, "sty_ok")

	idCounter := 0
	gen := func() string {
		idCounter++
		return []string{"vi_world1aaaaaaaa", "vi_world2bbbbbbbb"}[idCounter-1]
	}
	router := newCharacterRouter(idents, gen)

	for _, world := range []string{"w1", "w2"} {
		body := map[string]any{
			"world_id":                world,
			"owner_type":              "character",
			"owner_id":                "char_alice",
			"display_name":            "Alice " + world,
			"canonical_visual_traits": map[string]any{},
			"style_profile_id":        "sty_ok",
		}
		if rec := sendJSON(t, router, http.MethodPost, "/v1/characters/char_alice/visual-identity", tenantA, body); rec.Code != http.StatusOK {
			t.Fatalf("upsert %s failed: %d %s", world, rec.Code, rec.Body.String())
		}
	}

	rec := sendJSON(t, router, http.MethodGet, "/v1/characters/char_alice/visual-identity?world_id=w1", tenantA, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for w1, got %d", rec.Code)
	}
	resp := decode[map[string]any](t, rec)
	if resp["id"] != "vi_world1aaaaaaaa" || resp["world_id"] != "w1" {
		t.Fatalf("expected w1 identity, got %v", resp)
	}

	rec = sendJSON(t, router, http.MethodGet, "/v1/characters/char_alice/visual-identity?world_id=w2", tenantA, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for w2, got %d", rec.Code)
	}
	resp = decode[map[string]any](t, rec)
	if resp["id"] != "vi_world2bbbbbbbb" || resp["world_id"] != "w2" {
		t.Fatalf("expected w2 identity, got %v", resp)
	}
}

func TestGetCharacterVisualIdentityCrossTenantReturns404(t *testing.T) {
	idents := newStubIdentitiesRepo()
	idents.registerStyle(tenantA, "sty_ok")
	router := newCharacterRouter(idents, func() string { return "vi_aaaaaaaaaaaaaaaa" })

	body := map[string]any{
		"world_id":                "w1",
		"owner_type":              "character",
		"owner_id":                "char_alice",
		"display_name":            "Alice",
		"canonical_visual_traits": map[string]any{},
		"style_profile_id":        "sty_ok",
	}
	if rec := sendJSON(t, router, http.MethodPost, "/v1/characters/char_alice/visual-identity", tenantA, body); rec.Code != http.StatusOK {
		t.Fatalf("upsert failed: %d %s", rec.Code, rec.Body.String())
	}

	rec := sendJSON(t, router, http.MethodGet, "/v1/characters/char_alice/visual-identity?world_id=w1", tenantB, nil)
	assertError(t, rec, http.StatusNotFound, "not_found")
}

func TestPlaceVisualIdentityUpsertAndGet(t *testing.T) {
	idents := newStubIdentitiesRepo()
	idents.registerStyle(tenantA, "sty_ok")
	router := newCharacterRouter(idents, func() string { return "vi_pppppppppppppppp" })

	body := map[string]any{
		"world_id":                "w1",
		"owner_type":              "place",
		"owner_id":                "place_castle",
		"display_name":            "Castle",
		"canonical_visual_traits": map[string]any{},
		"style_profile_id":        "sty_ok",
	}
	if rec := sendJSON(t, router, http.MethodPost, "/v1/places/place_castle/visual-identity", tenantA, body); rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	rec := sendJSON(t, router, http.MethodGet, "/v1/places/place_castle/visual-identity?world_id=w1", tenantA, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected GET 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	resp := decode[map[string]any](t, rec)
	if resp["owner_type"] != "place" {
		t.Fatalf("expected owner_type=place, got %v", resp["owner_type"])
	}
	if resp["world_id"] != "w1" {
		t.Fatalf("expected world_id=w1, got %v", resp["world_id"])
	}
}

func TestGetPlaceVisualIdentityIsWorldScoped(t *testing.T) {
	idents := newStubIdentitiesRepo()
	idents.registerStyle(tenantA, "sty_ok")

	idCounter := 0
	gen := func() string {
		idCounter++
		return []string{"vi_w1placeeeeeeeee", "vi_w2placeeeeeeeee"}[idCounter-1]
	}
	router := newCharacterRouter(idents, gen)

	for _, world := range []string{"w1", "w2"} {
		body := map[string]any{
			"world_id":                world,
			"owner_type":              "place",
			"owner_id":                "place_castle",
			"display_name":            "Castle " + world,
			"canonical_visual_traits": map[string]any{},
			"style_profile_id":        "sty_ok",
		}
		if rec := sendJSON(t, router, http.MethodPost, "/v1/places/place_castle/visual-identity", tenantA, body); rec.Code != http.StatusOK {
			t.Fatalf("upsert %s failed: %d %s", world, rec.Code, rec.Body.String())
		}
	}

	rec := sendJSON(t, router, http.MethodGet, "/v1/places/place_castle/visual-identity?world_id=w2", tenantA, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for w2, got %d body=%s", rec.Code, rec.Body.String())
	}
	resp := decode[map[string]any](t, rec)
	if resp["id"] != "vi_w2placeeeeeeeee" || resp["world_id"] != "w2" {
		t.Fatalf("expected w2 place identity, got %v", resp)
	}
}

// ---------------------------------------------------------------------------
// Assets tests
// ---------------------------------------------------------------------------

func TestAssetGetSameTenant(t *testing.T) {
	repo := newStubAssetsRepo()
	repo.byID["asset_1"] = assets.VisualAsset{
		ID:         "asset_1",
		TenantID:   tenantA,
		WorldID:    "w1",
		AssetType:  "character_portrait",
		VariantKey: "neutral",
		Version:    1,
		Status:     "ready",
	}
	rec := sendJSON(t, newAssetsRouter(repo), http.MethodGet, "/v1/assets/asset_1", tenantA, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	resp := decode[map[string]any](t, rec)
	if resp["id"] != "asset_1" {
		t.Fatalf("expected id=asset_1, got %v", resp["id"])
	}
}

func TestAssetGetCrossTenantReturns404(t *testing.T) {
	repo := newStubAssetsRepo()
	repo.byID["asset_1"] = assets.VisualAsset{
		ID:        "asset_1",
		TenantID:  tenantA,
		WorldID:   "w1",
		AssetType: "character_portrait",
		Status:    "ready",
	}
	rec := sendJSON(t, newAssetsRouter(repo), http.MethodGet, "/v1/assets/asset_1", tenantB, nil)
	assertError(t, rec, http.StatusNotFound, "not_found")
}

// ---------------------------------------------------------------------------
// Auth wiring smoke test (missing principal returns 500)
// ---------------------------------------------------------------------------

func TestMissingPrincipalReturnsInternalError(t *testing.T) {
	repo := newStubStylesRepo()
	h := NewStylesHandler(repo)
	req := httptest.NewRequest(http.MethodGet, "/v1/styles", nil)
	req = req.WithContext(telemetry.ContextWithRequestID(req.Context(), "req"))
	rec := httptest.NewRecorder()
	h.List(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func assertError(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int, wantCode string) {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("expected status %d, got %d body=%s", wantStatus, rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Fatalf("expected application/problem+json, got %q", ct)
	}
	body := decode[map[string]any](t, rec)
	if body["code"] != wantCode {
		t.Fatalf("expected code=%q, got %v", wantCode, body["code"])
	}
	if body["message"] == "" {
		t.Fatalf("expected non-empty message, got %v", body)
	}
	if _, ok := body["request_id"]; !ok {
		t.Fatalf("expected request_id in body, got %v", body)
	}
}
