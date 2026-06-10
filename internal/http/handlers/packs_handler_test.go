package handlers

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/zakkriel/drchat-image-platform/internal/config"
	"github.com/zakkriel/drchat-image-platform/internal/identities"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
)

// ---------------------------------------------------------------------------
// Pack planning (no HTTP)
// ---------------------------------------------------------------------------

func TestPlanPackVariantsCharacterDefaults(t *testing.T) {
	got, err := planPackVariants(characterPackKind, nil)
	if err != nil {
		t.Fatalf("planPackVariants: %v", err)
	}
	want := []string{"neutral_front_portrait", "neutral_three_quarter_portrait", "side_angle_portrait"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("character defaults: expected %v, got %v", want, got)
	}
}

func TestPlanPackVariantsPlaceDefaults(t *testing.T) {
	got, err := planPackVariants(placePackKind, nil)
	if err != nil {
		t.Fatalf("planPackVariants: %v", err)
	}
	want := []string{"establishing_wide_view", "closer_atmospheric_view"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("place defaults: expected %v, got %v", want, got)
	}
}

func TestPlanPackVariantsOverrideIsVerbatimOpaque(t *testing.T) {
	// Variant keys are opaque strings in 5A — no interpretation, no
	// normalization beyond de-dup.
	override := []string{"sunset_over_harbour", "weird key with spaces", "sunset_over_harbour"}
	got, err := planPackVariants(characterPackKind, override)
	if err != nil {
		t.Fatalf("planPackVariants: %v", err)
	}
	want := []string{"sunset_over_harbour", "weird key with spaces"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("override: expected de-duplicated %v, got %v", want, got)
	}
}

func TestPlanPackVariantsOverCapFails(t *testing.T) {
	var override []string
	for i := 0; i < maxPackVariants+1; i++ {
		override = append(override, fmt.Sprintf("variant_%02d", i))
	}
	if _, err := planPackVariants(placePackKind, override); err == nil {
		t.Fatalf("expected over-cap error for %d variants", len(override))
	}
	// De-dup happens before the cap: 13 raw keys collapsing to <= 12 is fine.
	override[len(override)-1] = "variant_00"
	if _, err := planPackVariants(placePackKind, override); err != nil {
		t.Fatalf("expected de-dup below cap to pass, got %v", err)
	}
}

func TestPlanPackVariantsRejectsEmptyKey(t *testing.T) {
	if _, err := planPackVariants(characterPackKind, []string{"ok", ""}); err == nil {
		t.Fatalf("expected error for empty variant key")
	}
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

const packWorldID = "w1"

func seededPackIdentities() *stubIdentitiesRepo {
	repo := newStubIdentitiesRepo()
	repo.byOwner[identityKey{tenantA, packWorldID, "character", "char_hero"}] = identities.VisualIdentity{
		ID:          "vi_hero",
		TenantID:    tenantA,
		WorldID:     packWorldID,
		OwnerType:   "character",
		OwnerID:     "char_hero",
		DisplayName: "Captain Mira",
	}
	repo.byOwner[identityKey{tenantA, packWorldID, "place", "place_dock"}] = identities.VisualIdentity{
		ID:          "vi_dock",
		TenantID:    tenantA,
		WorldID:     packWorldID,
		OwnerType:   "place",
		OwnerID:     "place_dock",
		DisplayName: "The Old Dock",
	}
	return repo
}

func newPacksRouter(creator jobs.Creator, identitiesRepo *stubIdentitiesRepo, provider config.Provider) chi.Router {
	h := NewPacksHandler(creator, seededStyles(), identitiesRepo, provider)
	r := chi.NewRouter()
	r.Post("/v1/characters/{character_id}/generate-pack", h.GenerateCharacterPack)
	r.Post("/v1/places/{place_id}/generate-pack", h.GeneratePlacePack)
	return r
}

func TestCharacterPackHappyPathReturns202WithPackAndReservation(t *testing.T) {
	creator := &estimatingPackCreator{}
	router := newPacksRouter(creator, seededPackIdentities(), config.ProviderMock)
	body := map[string]any{"world_id": packWorldID, "style_profile_id": "sty_ok"}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/characters/char_hero/generate-pack",
		tenantA, []string{"images:write"}, body, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	resp := decode[map[string]any](t, rec)
	if resp["asset_pack_id"] != "pack_test" {
		t.Fatalf("expected asset_pack_id=pack_test, got %v", resp["asset_pack_id"])
	}
	if resp["cost_reservation_id"] != "resv_test" {
		t.Fatalf("expected cost_reservation_id=resv_test, got %v", resp["cost_reservation_id"])
	}
	if resp["status"] != "queued" {
		t.Fatalf("expected status=queued, got %v", resp["status"])
	}

	got := creator.got
	if got.JobType != "character_pack" {
		t.Fatalf("expected job_type=character_pack, got %q", got.JobType)
	}
	if got.AssetPack == nil || got.AssetPack.PackType != "character_minimal_portrait_pack" {
		t.Fatalf("expected character_minimal_portrait_pack spec, got %+v", got.AssetPack)
	}
	if got.AssetPack.VisualIdentityID != "vi_hero" {
		t.Fatalf("expected resolved identity vi_hero, got %q", got.AssetPack.VisualIdentityID)
	}
	// The default character pack is 3 variants → 3 priced units.
	if got.Units != 3 {
		t.Fatalf("expected Units=3 (default character variants), got %d", got.Units)
	}
	keys, _ := got.InputPayload["variant_keys"].([]string)
	if len(keys) != 3 {
		t.Fatalf("expected 3 variant_keys in payload, got %v", got.InputPayload["variant_keys"])
	}
	if got.InputPayload["visual_identity_id"] != "vi_hero" || got.InputPayload["display_name"] != "Captain Mira" {
		t.Fatalf("payload missing identity context: %v", got.InputPayload)
	}
}

func TestPlacePackHappyPathUsesPlaceDefaults(t *testing.T) {
	creator := &estimatingPackCreator{}
	router := newPacksRouter(creator, seededPackIdentities(), config.ProviderMock)
	body := map[string]any{"world_id": packWorldID, "style_profile_id": "sty_ok"}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/places/place_dock/generate-pack",
		tenantA, []string{"images:write"}, body, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	got := creator.got
	if got.JobType != "place_pack" || got.AssetPack == nil || got.AssetPack.PackType != "place_minimal_scene_pack" {
		t.Fatalf("expected place pack spec, got job_type=%q spec=%+v", got.JobType, got.AssetPack)
	}
	if got.Units != 2 {
		t.Fatalf("expected Units=2 (default place variants), got %d", got.Units)
	}
}

func TestPackVariantKeysOverrideSetsUnits(t *testing.T) {
	creator := &estimatingPackCreator{}
	router := newPacksRouter(creator, seededPackIdentities(), config.ProviderMock)
	body := map[string]any{
		"world_id":         packWorldID,
		"style_profile_id": "sty_ok",
		"variant_keys":     []string{"a", "b", "a", "c", "d"},
	}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/characters/char_hero/generate-pack",
		tenantA, []string{"images:write"}, body, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	if creator.got.Units != 4 {
		t.Fatalf("expected Units=4 after de-dup, got %d", creator.got.Units)
	}
}

func TestPackOverCapVariantKeysReturns400(t *testing.T) {
	creator := newStubCreator()
	router := newPacksRouter(creator, seededPackIdentities(), config.ProviderMock)
	var keys []string
	for i := 0; i < maxPackVariants+1; i++ {
		keys = append(keys, fmt.Sprintf("v%02d", i))
	}
	body := map[string]any{"world_id": packWorldID, "style_profile_id": "sty_ok", "variant_keys": keys}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/characters/char_hero/generate-pack",
		tenantA, []string{"images:write"}, body, nil)
	assertError(t, rec, http.StatusBadRequest, "invalid_request")
	if len(creator.calls) != 0 {
		t.Fatalf("expected zero service calls on over-cap, got %d", len(creator.calls))
	}
}

func TestPackMissingWorldIDReturns400(t *testing.T) {
	router := newPacksRouter(newStubCreator(), seededPackIdentities(), config.ProviderMock)
	body := map[string]any{"style_profile_id": "sty_ok"}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/characters/char_hero/generate-pack",
		tenantA, []string{"images:write"}, body, nil)
	assertError(t, rec, http.StatusBadRequest, "invalid_request")
}

func TestPackMissingStyleProfileReturns400(t *testing.T) {
	router := newPacksRouter(newStubCreator(), seededPackIdentities(), config.ProviderMock)
	body := map[string]any{"world_id": packWorldID}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/places/place_dock/generate-pack",
		tenantA, []string{"images:write"}, body, nil)
	assertError(t, rec, http.StatusBadRequest, "invalid_request")
}

func TestPackBodyTenantIDReturns400(t *testing.T) {
	router := newPacksRouter(newStubCreator(), seededPackIdentities(), config.ProviderMock)
	body := map[string]any{"tenant_id": "tenant_other", "world_id": packWorldID, "style_profile_id": "sty_ok"}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/characters/char_hero/generate-pack",
		tenantA, []string{"images:write"}, body, nil)
	assertError(t, rec, http.StatusBadRequest, "invalid_request")
}

func TestPackUnknownStyleReturns422(t *testing.T) {
	creator := newStubCreator()
	h := NewPacksHandler(creator, newStubStylesRepo(), seededPackIdentities(), config.ProviderMock)
	r := chi.NewRouter()
	r.Post("/v1/characters/{character_id}/generate-pack", h.GenerateCharacterPack)
	body := map[string]any{"world_id": packWorldID, "style_profile_id": "sty_ghost"}
	rec := sendJSONWithHeaders(t, r, http.MethodPost, "/v1/characters/char_hero/generate-pack",
		tenantA, []string{"images:write"}, body, nil)
	assertError(t, rec, http.StatusUnprocessableEntity, "invalid_style_profile")
	if len(creator.calls) != 0 {
		t.Fatalf("expected zero service calls on unknown style, got %d", len(creator.calls))
	}
}

func TestPackMissingVisualIdentityReturns422(t *testing.T) {
	creator := newStubCreator()
	router := newPacksRouter(creator, seededPackIdentities(), config.ProviderMock)
	// char_ghost has no visual identity; packs never create one (Phase 2 does).
	body := map[string]any{"world_id": packWorldID, "style_profile_id": "sty_ok"}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/characters/char_ghost/generate-pack",
		tenantA, []string{"images:write"}, body, nil)
	assertError(t, rec, http.StatusUnprocessableEntity, "invalid_request")
	if len(creator.calls) != 0 {
		t.Fatalf("expected zero service calls without identity, got %d", len(creator.calls))
	}
}

func TestPackWrongWorldIdentityReturns422(t *testing.T) {
	router := newPacksRouter(newStubCreator(), seededPackIdentities(), config.ProviderMock)
	// Identity exists in w1, not w2 — resolution is tenant+world scoped.
	body := map[string]any{"world_id": "w2", "style_profile_id": "sty_ok"}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/characters/char_hero/generate-pack",
		tenantA, []string{"images:write"}, body, nil)
	assertError(t, rec, http.StatusUnprocessableEntity, "invalid_request")
}

func TestPackBFLProviderReturns503BeforeAnyWrites(t *testing.T) {
	creator := newStubCreator()
	router := newPacksRouter(creator, seededPackIdentities(), config.ProviderBFL)
	body := map[string]any{"world_id": packWorldID, "style_profile_id": "sty_ok"}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/places/place_dock/generate-pack",
		tenantA, []string{"images:write"}, body, nil)
	assertError(t, rec, http.StatusServiceUnavailable, "provider_unavailable")
	if len(creator.calls) != 0 {
		t.Fatalf("expected zero service calls when provider unavailable, got %d", len(creator.calls))
	}
}

func TestPackBudgetExceededReturns422(t *testing.T) {
	creator := newStubCreator()
	creator.failErr = jobs.ErrBudgetExceeded
	router := newPacksRouter(creator, seededPackIdentities(), config.ProviderMock)
	body := map[string]any{"world_id": packWorldID, "style_profile_id": "sty_ok"}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/characters/char_hero/generate-pack",
		tenantA, []string{"images:write"}, body, nil)
	assertError(t, rec, http.StatusUnprocessableEntity, "budget_exceeded")
}

// estimatingPackCreator captures the params and returns a populated pack
// result, mirroring estimatingCreator for the artifact path.
type estimatingPackCreator struct {
	got jobs.CreateAndEnqueueParams
}

func (c *estimatingPackCreator) CreateAndEnqueue(_ context.Context, params jobs.CreateAndEnqueueParams) (jobs.CreateResult, error) {
	c.got = params
	return jobs.CreateResult{
		JobID:             "job_packtest1234567a",
		Status:            "queued",
		EstimatedCostUSD:  "0.0300",
		Currency:          "USD",
		CostReservationID: "resv_test",
		AssetPackID:       "pack_test",
	}, nil
}
