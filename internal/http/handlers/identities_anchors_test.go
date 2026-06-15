package handlers

import (
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/zakkriel/drchat-image-platform/internal/assets"
	"github.com/zakkriel/drchat-image-platform/internal/http/apigen"
	"github.com/zakkriel/drchat-image-platform/internal/identities"
)

// newAnchorRouter wires the create/get/attach-anchors identity routes with a
// real asset validator, so the tests exercise the same orchestration the
// production router mounts.
func newAnchorRouter(idents *stubIdentitiesRepo, assetsRepo *stubAssetsRepo) chi.Router {
	h := NewIdentitiesHandler(idents).WithAssets(assetsRepo)
	r := chi.NewRouter()
	r.Post("/v1/characters/{character_id}/visual-identity", h.UpsertCharacter)
	r.Get("/v1/characters/{character_id}/visual-identity", h.GetCharacter)
	r.Post("/v1/characters/{character_id}/visual-identity/anchors", h.AttachCharacterAnchors)
	return r
}

func readyAnchorAsset(id, tenantID, worldID string) assets.VisualAsset {
	high := "s3://bucket/assets/" + id + "/high.png"
	return assets.VisualAsset{ID: id, TenantID: tenantID, WorldID: worldID, Status: "ready", HighResUrl: &high}
}

// TestAttachCharacterAnchorsNormalFlow proves a client can create a visual
// identity and attach anchor assets entirely over the API — no manual SQL — and
// that the identity then returns those anchor_asset_ids (so pack generation can
// use them).
func TestAttachCharacterAnchorsNormalFlow(t *testing.T) {
	idents := newStubIdentitiesRepo()
	idents.registerStyle("tenant_a", "sty_1")
	assetsRepo := newStubAssetsRepo()
	r := newAnchorRouter(idents, assetsRepo)

	// 1. Create the visual identity over the API.
	create := apigen.CreateVisualIdentityRequest{
		WorldId:               "w1",
		OwnerType:             apigen.OwnerTypeCharacter,
		OwnerId:               "char_1",
		DisplayName:           "Captain Mira",
		StyleProfileId:        "sty_1",
		CanonicalVisualTraits: map[string]interface{}{"hair": "red"},
	}
	rec := sendJSON(t, r, "POST", "/v1/characters/char_1/visual-identity", "tenant_a", create)
	if rec.Code != http.StatusOK {
		t.Fatalf("create identity: status %d, body %s", rec.Code, rec.Body.String())
	}

	// 2. Seed a ready, tenant-owned asset with a high-res object.
	assetsRepo.seed(readyAnchorAsset("va_1", "tenant_a", "w1"))

	// 3. Attach it as an anchor over the API.
	rec = sendJSON(t, r, "POST", "/v1/characters/char_1/visual-identity/anchors", "tenant_a",
		apigen.AttachAnchorAssetsRequest{WorldId: "w1", AnchorAssetIds: []string{"va_1"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("attach anchors: status %d, body %s", rec.Code, rec.Body.String())
	}
	vi := decode[apigen.VisualIdentity](t, rec)
	if vi.AnchorAssetIds == nil || len(*vi.AnchorAssetIds) != 1 || (*vi.AnchorAssetIds)[0] != "va_1" {
		t.Fatalf("attach response anchor_asset_ids = %v, want [va_1]", vi.AnchorAssetIds)
	}

	// 4. The identity read returns the anchors (what pack generation consumes).
	rec = sendJSON(t, r, "GET", "/v1/characters/char_1/visual-identity?world_id=w1", "tenant_a", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get identity: status %d, body %s", rec.Code, rec.Body.String())
	}
	got := decode[apigen.VisualIdentity](t, rec)
	if got.AnchorAssetIds == nil || len(*got.AnchorAssetIds) != 1 || (*got.AnchorAssetIds)[0] != "va_1" {
		t.Fatalf("get identity anchor_asset_ids = %v, want [va_1]", got.AnchorAssetIds)
	}
}

// TestAttachCharacterAnchorsValidation proves each rejection path returns a clear
// status/code and never persists the bad set.
func TestAttachCharacterAnchorsValidation(t *testing.T) {
	seedIdentity := func(idents *stubIdentitiesRepo) {
		idents.byOwner[identityKey{"tenant_a", "w1", "character", "char_1"}] = identities.VisualIdentity{
			ID: "vi_1", TenantID: "tenant_a", WorldID: "w1", OwnerType: "character", OwnerID: "char_1",
			DisplayName: "Captain Mira", StyleProfileID: "sty_1", CurrentVersion: 1, Status: "active",
		}
	}

	otherIdentity := "vi_other"
	high := "s3://bucket/assets/va_1/high.png"

	cases := []struct {
		name       string
		seedAssets func(*stubAssetsRepo)
		body       apigen.AttachAnchorAssetsRequest
		wantStatus int
		wantCode   string
	}{
		{
			name:       "empty anchor list",
			seedAssets: func(*stubAssetsRepo) {},
			body:       apigen.AttachAnchorAssetsRequest{WorldId: "w1", AnchorAssetIds: []string{}},
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_request",
		},
		{
			name:       "missing asset",
			seedAssets: func(*stubAssetsRepo) {},
			body:       apigen.AttachAnchorAssetsRequest{WorldId: "w1", AnchorAssetIds: []string{"va_missing"}},
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "invalid_anchor_asset",
		},
		{
			name: "non-ready asset",
			seedAssets: func(a *stubAssetsRepo) {
				a.seed(assets.VisualAsset{ID: "va_1", TenantID: "tenant_a", WorldID: "w1", Status: "preview_ready", HighResUrl: &high})
			},
			body:       apigen.AttachAnchorAssetsRequest{WorldId: "w1", AnchorAssetIds: []string{"va_1"}},
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "invalid_anchor_asset",
		},
		{
			name: "asset without high-res object",
			seedAssets: func(a *stubAssetsRepo) {
				a.seed(assets.VisualAsset{ID: "va_1", TenantID: "tenant_a", WorldID: "w1", Status: "ready"})
			},
			body:       apigen.AttachAnchorAssetsRequest{WorldId: "w1", AnchorAssetIds: []string{"va_1"}},
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "invalid_anchor_asset",
		},
		{
			name: "asset bound to a different identity",
			seedAssets: func(a *stubAssetsRepo) {
				a.seed(assets.VisualAsset{ID: "va_1", TenantID: "tenant_a", WorldID: "w1", Status: "ready", HighResUrl: &high, VisualIdentityID: &otherIdentity})
			},
			body:       apigen.AttachAnchorAssetsRequest{WorldId: "w1", AnchorAssetIds: []string{"va_1"}},
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "invalid_anchor_asset",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idents := newStubIdentitiesRepo()
			seedIdentity(idents)
			assetsRepo := newStubAssetsRepo()
			tc.seedAssets(assetsRepo)
			r := newAnchorRouter(idents, assetsRepo)

			rec := sendJSON(t, r, "POST", "/v1/characters/char_1/visual-identity/anchors", "tenant_a", tc.body)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			body := decode[map[string]any](t, rec)
			if code, _ := body["code"].(string); code != tc.wantCode {
				t.Fatalf("error code = %q, want %q", code, tc.wantCode)
			}
			// The bad set was never persisted.
			vi := idents.byOwner[identityKey{"tenant_a", "w1", "character", "char_1"}]
			if len(vi.AnchorAssetIds) != 0 {
				t.Fatalf("anchors must not be persisted on rejection, got %v", vi.AnchorAssetIds)
			}
		})
	}
}
