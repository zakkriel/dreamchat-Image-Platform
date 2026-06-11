package handlers

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/zakkriel/drchat-image-platform/internal/assets"
	"github.com/zakkriel/drchat-image-platform/internal/config"
	"github.com/zakkriel/drchat-image-platform/internal/identities"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
	"github.com/zakkriel/drchat-image-platform/internal/providers/routing"
)

// ---------------------------------------------------------------------------
// Pack planning (no HTTP)
// ---------------------------------------------------------------------------

// The no-template default is the PRD 04 §4.2 / §5.2 minimum/starter pack —
// 7 character roles, 6 place roles — and must match the named minimal
// template exactly (the handler derives one from the other).
func TestPlanPackVariantsCharacterDefaults(t *testing.T) {
	got, err := planPackVariants(characterPackKind, nil)
	if err != nil {
		t.Fatalf("planPackVariants: %v", err)
	}
	want := []string{
		"neutral_front_portrait", "neutral_three_quarter_portrait", "side_angle_portrait",
		"warm_or_smiling_expression", "serious_or_tense_expression",
		"angry_or_defensive_expression", "surprised_or_shocked_expression",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("character defaults: expected %v, got %v", want, got)
	}
}

func TestPlanPackVariantsPlaceDefaults(t *testing.T) {
	got, err := planPackVariants(placePackKind, nil)
	if err != nil {
		t.Fatalf("planPackVariants: %v", err)
	}
	want := []string{
		"establishing_wide_view", "closer_atmospheric_view",
		"day_view", "night_view", "calm_or_empty_view", "busy_or_active_view",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("place defaults: expected %v, got %v", want, got)
	}
}

// TestMinimalDefaultMatchesNamedTemplate locks the no-template default and the
// named minimal template together so "minimal/starter" can never diverge.
func TestMinimalDefaultMatchesNamedTemplate(t *testing.T) {
	cKeys, cType, err := resolvePackPlan(characterPackKind, nil, "")
	if err != nil {
		t.Fatalf("character default: %v", err)
	}
	cTmpl, _, err := resolvePackPlan(characterPackKind, nil, "character_minimal_portrait_pack")
	if err != nil {
		t.Fatalf("character template: %v", err)
	}
	if !reflect.DeepEqual(cKeys, cTmpl) || cType != "character_minimal_portrait_pack" {
		t.Fatalf("character default %v (%s) must equal minimal template %v", cKeys, cType, cTmpl)
	}
	pKeys, _, err := resolvePackPlan(placePackKind, nil, "")
	if err != nil {
		t.Fatalf("place default: %v", err)
	}
	pTmpl, _, err := resolvePackPlan(placePackKind, nil, "place_minimal_scene_pack")
	if err != nil {
		t.Fatalf("place template: %v", err)
	}
	if !reflect.DeepEqual(pKeys, pTmpl) {
		t.Fatalf("place default %v must equal minimal template %v", pKeys, pTmpl)
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
// Pack template resolution (no HTTP)
// ---------------------------------------------------------------------------

func TestResolvePackPlanMinimalDefault(t *testing.T) {
	keys, packType, err := resolvePackPlan(characterPackKind, nil, "")
	if err != nil {
		t.Fatalf("resolvePackPlan: %v", err)
	}
	want := []string{
		"neutral_front_portrait", "neutral_three_quarter_portrait", "side_angle_portrait",
		"warm_or_smiling_expression", "serious_or_tense_expression",
		"angry_or_defensive_expression", "surprised_or_shocked_expression",
	}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("default keys: expected %v, got %v", want, keys)
	}
	if packType != "character_minimal_portrait_pack" {
		t.Fatalf("default pack_type: expected character_minimal_portrait_pack, got %q", packType)
	}
}

func TestResolvePackPlanTemplateResolvesRoleSet(t *testing.T) {
	keys, packType, err := resolvePackPlan(characterPackKind, nil, "character_expression_pack")
	if err != nil {
		t.Fatalf("resolvePackPlan: %v", err)
	}
	want := []string{
		"neutral_front_portrait", "expression_warm", "expression_serious",
		"expression_angry", "expression_surprised",
	}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("template keys: expected %v, got %v", want, keys)
	}
	if packType != "character_expression_pack" {
		t.Fatalf("template pack_type: expected character_expression_pack, got %q", packType)
	}
}

func TestResolvePackPlanPlaceTemplate(t *testing.T) {
	keys, packType, err := resolvePackPlan(placePackKind, nil, "place_time_of_day_pack")
	if err != nil {
		t.Fatalf("resolvePackPlan: %v", err)
	}
	want := []string{"day_view", "night_view", "dawn_view", "dusk_view"}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("place template keys: expected %v, got %v", want, keys)
	}
	if packType != "place_time_of_day_pack" {
		t.Fatalf("place template pack_type: expected place_time_of_day_pack, got %q", packType)
	}
}

func TestResolvePackPlanVariantKeysOverrideTemplate(t *testing.T) {
	// Explicit variant_keys win verbatim over a template, and the pack is a
	// custom pack — not the named template.
	keys, packType, err := resolvePackPlan(characterPackKind, []string{"a", "b", "a", "c"}, "character_expression_pack")
	if err != nil {
		t.Fatalf("resolvePackPlan: %v", err)
	}
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("override keys: expected de-duped %v, got %v", want, keys)
	}
	if packType != "character_custom_pack" {
		t.Fatalf("override pack_type: expected character_custom_pack, got %q", packType)
	}
}

func TestResolvePackPlanUnknownTemplateErrors(t *testing.T) {
	if _, _, err := resolvePackPlan(characterPackKind, nil, "no_such_template"); err == nil {
		t.Fatalf("expected error for unknown template")
	}
	// A place template under the character entity is unknown too.
	if _, _, err := resolvePackPlan(characterPackKind, nil, "place_time_of_day_pack"); err == nil {
		t.Fatalf("expected error for cross-entity template")
	}
}

func TestResolvePackPlanOverrideCapAndEmpty(t *testing.T) {
	var over []string
	for i := 0; i < maxPackVariants+1; i++ {
		over = append(over, fmt.Sprintf("v%02d", i))
	}
	if _, _, err := resolvePackPlan(characterPackKind, over, ""); err == nil {
		t.Fatalf("expected over-cap error")
	}
	if _, _, err := resolvePackPlan(characterPackKind, []string{"ok", ""}, ""); err == nil {
		t.Fatalf("expected empty-key error")
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
	h := NewPacksHandler(creator, seededStyles(), identitiesRepo, okResolver(), string(provider))
	r := chi.NewRouter()
	r.Post("/v1/characters/{character_id}/generate-pack", h.GenerateCharacterPack)
	r.Post("/v1/places/{place_id}/generate-pack", h.GeneratePlacePack)
	return r
}

func newPacksRouterWithResolver(creator jobs.Creator, identitiesRepo *stubIdentitiesRepo, resolver RouteResolver) chi.Router {
	h := NewPacksHandler(creator, seededStyles(), identitiesRepo, resolver, "mock")
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
	// The default character pack is the PRD 04 §4.2 starter pack = 7 variants.
	if got.Units != 7 {
		t.Fatalf("expected Units=7 (PRD starter character variants), got %d", got.Units)
	}
	keys, _ := got.InputPayload["variant_keys"].([]string)
	if len(keys) != 7 {
		t.Fatalf("expected 7 variant_keys in payload, got %v", got.InputPayload["variant_keys"])
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
	// PRD 04 §5.2 starter place pack = 6 variants.
	if got.Units != 6 {
		t.Fatalf("expected Units=6 (PRD starter place variants), got %d", got.Units)
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
	h := NewPacksHandler(creator, newStubStylesRepo(), seededPackIdentities(), okResolver(), "mock")
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

// Pack generation requests the pack_capable route capability (Phase 7A,
// Option A: a seeded pack_capable mock route serves it).
func TestPackPassesPackCapability(t *testing.T) {
	resolver := okResolver()
	router := newPacksRouterWithResolver(&estimatingPackCreator{}, seededPackIdentities(), resolver)
	body := map[string]any{"world_id": packWorldID, "style_profile_id": "sty_ok"}
	if rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/characters/char_hero/generate-pack",
		tenantA, []string{"images:write"}, body, nil); rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s", rec.Code, "")
	}
	if resolver.lastReq.RequiredCapability != "pack_capable" {
		t.Fatalf("expected pack_capable, got %q", resolver.lastReq.RequiredCapability)
	}
}

// Pack generation with an unsupported capability fails 422 before any cost
// reservation / job create / enqueue.
func TestPackUnsupportedCapabilityReturns422BeforeWrites(t *testing.T) {
	creator := newStubCreator()
	router := newPacksRouterWithResolver(creator, seededPackIdentities(), &fakeResolver{err: routing.ErrUnsupportedCapability})
	body := map[string]any{"world_id": packWorldID, "style_profile_id": "sty_ok"}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/characters/char_hero/generate-pack",
		tenantA, []string{"images:write"}, body, nil)
	assertError(t, rec, http.StatusUnprocessableEntity, "unsupported_capability")
	if len(creator.calls) != 0 {
		t.Fatalf("expected zero service calls on unsupported capability, got %d", len(creator.calls))
	}
}

// Phase 7A: a pack request that resolves no provider route fails 422 no_route
// before any cost reservation / job create.
func TestPackNoRouteReturns422BeforeAnyWrites(t *testing.T) {
	creator := newStubCreator()
	router := newPacksRouterWithResolver(creator, seededPackIdentities(), &fakeResolver{err: routing.ErrNoRoute})
	body := map[string]any{"world_id": packWorldID, "style_profile_id": "sty_ok"}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/places/place_dock/generate-pack",
		tenantA, []string{"images:write"}, body, nil)
	assertError(t, rec, http.StatusUnprocessableEntity, "no_route")
	if len(creator.calls) != 0 {
		t.Fatalf("expected zero service calls when no route resolves, got %d", len(creator.calls))
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

func TestPackTemplateSelectsRoleSetAndPackType(t *testing.T) {
	creator := &estimatingPackCreator{}
	router := newPacksRouter(creator, seededPackIdentities(), config.ProviderMock)
	body := map[string]any{
		"world_id":         packWorldID,
		"style_profile_id": "sty_ok",
		"pack_template":    "character_expression_pack",
	}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/characters/char_hero/generate-pack",
		tenantA, []string{"images:write"}, body, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	got := creator.got
	// character_expression_pack has 5 roles → 5 priced units.
	if got.Units != 5 {
		t.Fatalf("expected Units=5 for expression pack, got %d", got.Units)
	}
	if got.AssetPack == nil || got.AssetPack.PackType != "character_expression_pack" {
		t.Fatalf("expected pack_type character_expression_pack, got %+v", got.AssetPack)
	}
	keys, _ := got.InputPayload["variant_keys"].([]string)
	want := []string{"neutral_front_portrait", "expression_warm", "expression_serious", "expression_angry", "expression_surprised"}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("expected template roles %v, got %v", want, keys)
	}
}

func TestPackUnknownTemplateReturns400(t *testing.T) {
	creator := newStubCreator()
	router := newPacksRouter(creator, seededPackIdentities(), config.ProviderMock)
	body := map[string]any{
		"world_id":         packWorldID,
		"style_profile_id": "sty_ok",
		"pack_template":    "character_galaxy_brain_pack",
	}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/characters/char_hero/generate-pack",
		tenantA, []string{"images:write"}, body, nil)
	assertError(t, rec, http.StatusBadRequest, "invalid_request")
	if len(creator.calls) != 0 {
		t.Fatalf("expected zero service calls on unknown template, got %d", len(creator.calls))
	}
}

func TestPackVariantKeysOverrideTemplateProducesCustomPack(t *testing.T) {
	creator := &estimatingPackCreator{}
	router := newPacksRouter(creator, seededPackIdentities(), config.ProviderMock)
	body := map[string]any{
		"world_id":         packWorldID,
		"style_profile_id": "sty_ok",
		"pack_template":    "character_expression_pack",
		"variant_keys":     []string{"custom_a", "custom_b"},
	}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/characters/char_hero/generate-pack",
		tenantA, []string{"images:write"}, body, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	got := creator.got
	if got.Units != 2 {
		t.Fatalf("expected Units=2 from override, got %d", got.Units)
	}
	if got.AssetPack == nil || got.AssetPack.PackType != "character_custom_pack" {
		t.Fatalf("expected pack_type character_custom_pack when override wins, got %+v", got.AssetPack)
	}
}

func TestPlacePackTemplateSelectsRoleSet(t *testing.T) {
	creator := &estimatingPackCreator{}
	router := newPacksRouter(creator, seededPackIdentities(), config.ProviderMock)
	body := map[string]any{
		"world_id":         packWorldID,
		"style_profile_id": "sty_ok",
		"pack_template":    "place_time_of_day_pack",
	}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/places/place_dock/generate-pack",
		tenantA, []string{"images:write"}, body, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	got := creator.got
	if got.Units != 4 {
		t.Fatalf("expected Units=4 for time-of-day pack, got %d", got.Units)
	}
	if got.AssetPack == nil || got.AssetPack.PackType != "place_time_of_day_pack" {
		t.Fatalf("expected pack_type place_time_of_day_pack, got %+v", got.AssetPack)
	}
}

// ---------------------------------------------------------------------------
// Phase 6A3: pack reuse-first (retrieval before generation)
// ---------------------------------------------------------------------------

// fakeCandidateSource is an in-memory assets.CandidateSource so the handler
// tests can drive the REAL retrieval decision layer (assets.NewRetriever)
// without a database. exact maps a variant_key to an exact-match asset;
// candidates is the (variant-key-independent) compatible/preview candidate pool
// the matrix evaluates per requested role.
type fakeCandidateSource struct {
	exact      map[string]assets.VisualAsset
	candidates []assets.VisualAsset
}

func (f *fakeCandidateSource) FindExact(_ context.Context, q assets.RetrievalQuery) (assets.VisualAsset, error) {
	if a, ok := f.exact[q.VariantKey]; ok {
		return a, nil
	}
	return assets.VisualAsset{}, assets.ErrNotFound
}

func (f *fakeCandidateSource) ListRetrievalCandidates(_ context.Context, _ assets.RetrievalQuery) ([]assets.VisualAsset, error) {
	return f.candidates, nil
}

func readyReuseAsset(id, variantKey string) assets.VisualAsset {
	return assets.VisualAsset{ID: id, VariantKey: variantKey, Status: "ready"}
}

func newPacksRouterWithRetriever(creator jobs.Creator, identitiesRepo *stubIdentitiesRepo, src assets.CandidateSource) chi.Router {
	h := NewPacksHandler(creator, seededStyles(), identitiesRepo, okResolver(), "mock").
		WithRetriever(assets.NewRetriever(src))
	r := chi.NewRouter()
	r.Post("/v1/characters/{character_id}/generate-pack", h.GenerateCharacterPack)
	r.Post("/v1/places/{place_id}/generate-pack", h.GeneratePlacePack)
	return r
}

// TestPackAllHitsCompletesSynchronously: every required role exact-matches an
// existing asset → the pack completes synchronously (CreateCompletedPackReuseJob),
// no reservation/enqueue, completeness = all delivered / none missing.
func TestPackAllHitsCompletesSynchronously(t *testing.T) {
	creator := &estimatingPackCreator{}
	roles := characterPackKind.defaultVariants
	src := &fakeCandidateSource{exact: map[string]assets.VisualAsset{}}
	for i, role := range roles {
		src.exact[role] = readyReuseAsset(fmt.Sprintf("asset_%02d", i), role)
	}
	router := newPacksRouterWithRetriever(creator, seededPackIdentities(), src)

	body := map[string]any{"world_id": packWorldID, "style_profile_id": "sty_ok"}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/characters/char_hero/generate-pack",
		tenantA, []string{"images:write"}, body, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	// The all-hits path was taken: no generate/reserve/enqueue call.
	if creator.got.JobType != "" {
		t.Fatalf("all-hits must not call CreateAndEnqueue, got %+v", creator.got)
	}
	reuse := creator.gotReuse
	if reuse.JobType != "character_pack" {
		t.Fatalf("expected CreateCompletedPackReuseJob with job_type character_pack, got %q", reuse.JobType)
	}
	if len(reuse.ReusedItems) != len(roles) {
		t.Fatalf("expected %d reused items, got %d", len(roles), len(reuse.ReusedItems))
	}
	if !reflect.DeepEqual(reuse.RequiredRoles, roles) {
		t.Fatalf("required roles: expected %v, got %v", roles, reuse.RequiredRoles)
	}
	if reuse.CacheResult != "exact_match" {
		t.Fatalf("all-exact pack cache_result: expected exact_match, got %q", reuse.CacheResult)
	}
	// Reused items reference the seeded existing asset ids, one per role in order.
	for i, item := range reuse.ReusedItems {
		if item.VariantKey != roles[i] {
			t.Fatalf("item %d: expected variant %q, got %q", i, roles[i], item.VariantKey)
		}
		if item.AssetID != fmt.Sprintf("asset_%02d", i) {
			t.Fatalf("item %d: expected asset asset_%02d, got %q", i, i, item.AssetID)
		}
		if item.MatchType != "exact_match" {
			t.Fatalf("item %d: expected exact_match, got %q", i, item.MatchType)
		}
	}
	resp := decode[map[string]any](t, rec)
	if resp["status"] != "queued" {
		t.Fatalf("accepted envelope status must be queued, got %v", resp["status"])
	}
	if resp["estimated_cost_usd"] != "0.0000" {
		t.Fatalf("all-hits estimated_cost_usd must be 0.0000, got %v", resp["estimated_cost_usd"])
	}
	if resp["asset_pack_id"] != "pack_reuse" {
		t.Fatalf("expected asset_pack_id pack_reuse, got %v", resp["asset_pack_id"])
	}
	if _, found := resp["cost_reservation_id"]; found {
		t.Fatalf("all-hits response must carry no cost_reservation_id, got %v", resp)
	}
}

// TestPackMixedHitsPricesMissesOnly: 5 of 7 roles exact-match, 2 miss → the
// reservation prices only the 2 misses, the 5 hits are persisted as reused items
// and carried as delivered, and the 2 misses are carried to the worker.
func TestPackMixedHitsPricesMissesOnly(t *testing.T) {
	creator := &estimatingPackCreator{}
	roles := characterPackKind.defaultVariants // 7 roles
	missWant := map[string]bool{roles[2]: true, roles[5]: true}
	src := &fakeCandidateSource{exact: map[string]assets.VisualAsset{}}
	for i, role := range roles {
		if missWant[role] {
			continue // no exact, no candidate → generated_required
		}
		src.exact[role] = readyReuseAsset(fmt.Sprintf("asset_%02d", i), role)
	}
	router := newPacksRouterWithRetriever(creator, seededPackIdentities(), src)

	body := map[string]any{"world_id": packWorldID, "style_profile_id": "sty_ok"}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/characters/char_hero/generate-pack",
		tenantA, []string{"images:write"}, body, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	if creator.gotReuse.JobType != "" {
		t.Fatalf("a partial pack must not complete synchronously")
	}
	got := creator.got
	if got.Units != 2 {
		t.Fatalf("misses-only pricing: expected Units=2, got %d", got.Units)
	}
	if got.AssetPack == nil {
		t.Fatalf("expected an AssetPack spec")
	}
	if len(got.AssetPack.ReusedItems) != 5 {
		t.Fatalf("expected 5 reused items, got %d", len(got.AssetPack.ReusedItems))
	}
	gotMissing := append([]string(nil), got.AssetPack.MissingRoles...)
	sort.Strings(gotMissing)
	wantMissing := []string{roles[2], roles[5]}
	sort.Strings(wantMissing)
	if !reflect.DeepEqual(gotMissing, wantMissing) {
		t.Fatalf("missing roles: expected %v, got %v", wantMissing, gotMissing)
	}
	if !reflect.DeepEqual(got.AssetPack.RequiredRoles, roles) {
		t.Fatalf("required roles: expected %v, got %v", roles, got.AssetPack.RequiredRoles)
	}
	// variant_keys carried to the worker stays the FULL role set (the worker skips
	// the reused items and generates only the missing roles).
	keys, _ := got.InputPayload["variant_keys"].([]string)
	if !reflect.DeepEqual(keys, roles) {
		t.Fatalf("payload variant_keys must be the full role set, got %v", keys)
	}
	// Reused items keep the role's position as sort order.
	for _, item := range got.AssetPack.ReusedItems {
		if missWant[item.VariantKey] {
			t.Fatalf("missing role %q must not appear as a reused item", item.VariantKey)
		}
		if roles[item.SortOrder] != item.VariantKey {
			t.Fatalf("reused item %q has sort_order %d (role at that index is %q)", item.VariantKey, item.SortOrder, roles[item.SortOrder])
		}
	}
}

// TestPackZeroHitsPricesWholePack: no role has a reusable asset → behaves like
// the pre-6A3 full pack generate (every role missing, priced fully, no reused
// items).
func TestPackZeroHitsPricesWholePack(t *testing.T) {
	creator := &estimatingPackCreator{}
	src := &fakeCandidateSource{exact: map[string]assets.VisualAsset{}} // no exact, no candidates
	router := newPacksRouterWithRetriever(creator, seededPackIdentities(), src)

	body := map[string]any{"world_id": packWorldID, "style_profile_id": "sty_ok"}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/characters/char_hero/generate-pack",
		tenantA, []string{"images:write"}, body, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	got := creator.got
	if got.Units != 7 {
		t.Fatalf("zero hits: expected Units=7 (whole pack), got %d", got.Units)
	}
	if got.AssetPack == nil || len(got.AssetPack.ReusedItems) != 0 {
		t.Fatalf("zero hits must persist no reused items, got %+v", got.AssetPack)
	}
	if len(got.AssetPack.MissingRoles) != 7 {
		t.Fatalf("zero hits: every role is missing, got %v", got.AssetPack.MissingRoles)
	}
}

// TestPackForceRegenerateBypassesReuse (Phase 6A4): even when EVERY required
// role has an exact ready asset (the all-hits case), force_regenerate:true skips
// per-role retrieval entirely → the whole pack is priced (Units == all roles),
// no reused items are persisted, there is no all-hits synchronous completion, and
// the job payload carries force_regenerate so the worker supersedes each slot.
func TestPackForceRegenerateBypassesReuse(t *testing.T) {
	creator := &estimatingPackCreator{}
	roles := characterPackKind.defaultVariants
	src := &fakeCandidateSource{exact: map[string]assets.VisualAsset{}}
	for i, role := range roles {
		src.exact[role] = readyReuseAsset(fmt.Sprintf("asset_%02d", i), role)
	}
	router := newPacksRouterWithRetriever(creator, seededPackIdentities(), src)

	body := map[string]any{
		"world_id":         packWorldID,
		"style_profile_id": "sty_ok",
		"force_regenerate": true,
	}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/characters/char_hero/generate-pack",
		tenantA, []string{"images:write"}, body, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	// No all-hits synchronous completion despite every role having a ready asset.
	if creator.gotReuse.JobType != "" {
		t.Fatalf("force_regenerate must not complete synchronously (no all-hits), got %+v", creator.gotReuse)
	}
	got := creator.got
	if got.JobType != "character_pack" {
		t.Fatalf("force_regenerate must go through CreateAndEnqueue, got job_type %q", got.JobType)
	}
	if int(got.Units) != len(roles) {
		t.Fatalf("force_regenerate prices the whole pack: expected Units=%d, got %d", len(roles), got.Units)
	}
	if got.AssetPack == nil || len(got.AssetPack.ReusedItems) != 0 {
		t.Fatalf("force_regenerate must persist no reused items, got %+v", got.AssetPack)
	}
	if len(got.AssetPack.MissingRoles) != len(roles) {
		t.Fatalf("force_regenerate: every role is missing, got %v", got.AssetPack.MissingRoles)
	}
	if !reflect.DeepEqual(got.AssetPack.RequiredRoles, roles) {
		t.Fatalf("required roles: expected %v, got %v", roles, got.AssetPack.RequiredRoles)
	}
	if fr, _ := got.InputPayload["force_regenerate"].(bool); !fr {
		t.Fatalf("forced pack payload must carry force_regenerate=true, got %v", got.InputPayload["force_regenerate"])
	}
}

// TestPackForceRegenerateFalseStillAllHits (Phase 6A4): force_regenerate:false
// with every role hitting is unchanged 6A3 — still all-hits synchronous
// completion.
func TestPackForceRegenerateFalseStillAllHits(t *testing.T) {
	creator := &estimatingPackCreator{}
	roles := characterPackKind.defaultVariants
	src := &fakeCandidateSource{exact: map[string]assets.VisualAsset{}}
	for i, role := range roles {
		src.exact[role] = readyReuseAsset(fmt.Sprintf("asset_%02d", i), role)
	}
	router := newPacksRouterWithRetriever(creator, seededPackIdentities(), src)

	body := map[string]any{
		"world_id":         packWorldID,
		"style_profile_id": "sty_ok",
		"force_regenerate": false,
	}
	rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/characters/char_hero/generate-pack",
		tenantA, []string{"images:write"}, body, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	if creator.got.JobType != "" {
		t.Fatalf("force_regenerate:false all-hits must not call CreateAndEnqueue, got %+v", creator.got)
	}
	if creator.gotReuse.JobType != "character_pack" || len(creator.gotReuse.ReusedItems) != len(roles) {
		t.Fatalf("force_regenerate:false must still complete synchronously with all roles reused, got %+v", creator.gotReuse)
	}
}

// TestPackFallbackPolicyGatesReuse pins the fallback_policy gating: with a single
// candidate (a neutral front portrait), the three-quarter role is a COMPATIBLE
// match and the side-angle role is only a PREVIEW. Under compatible_only the
// compatible role is reused and the preview-only role is a miss; under
// preview_allowed both are reused.
func TestPackFallbackPolicyGatesReuse(t *testing.T) {
	roles := []string{"neutral_three_quarter_portrait", "side_angle_portrait"}
	src := &fakeCandidateSource{
		exact:      map[string]assets.VisualAsset{},
		candidates: []assets.VisualAsset{readyReuseAsset("asset_front", "neutral_front_portrait")},
	}

	// compatible_only: three_quarter→front is compatible (hit), side_angle→front
	// is preview (miss).
	t.Run("compatible_only", func(t *testing.T) {
		creator := &estimatingPackCreator{}
		router := newPacksRouterWithRetriever(creator, seededPackIdentities(), src)
		body := map[string]any{
			"world_id":         packWorldID,
			"style_profile_id": "sty_ok",
			"variant_keys":     roles,
			"fallback_policy":  "compatible_only",
		}
		rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/characters/char_hero/generate-pack",
			tenantA, []string{"images:write"}, body, nil)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
		}
		got := creator.got
		if got.Units != 1 {
			t.Fatalf("compatible_only: expected Units=1 (preview role is a miss), got %d", got.Units)
		}
		if len(got.AssetPack.ReusedItems) != 1 || got.AssetPack.ReusedItems[0].VariantKey != "neutral_three_quarter_portrait" {
			t.Fatalf("compatible_only: expected the three-quarter role reused, got %+v", got.AssetPack.ReusedItems)
		}
		if got.AssetPack.ReusedItems[0].MatchType != "compatible_match" {
			t.Fatalf("compatible_only: expected compatible_match, got %q", got.AssetPack.ReusedItems[0].MatchType)
		}
		if len(got.AssetPack.MissingRoles) != 1 || got.AssetPack.MissingRoles[0] != "side_angle_portrait" {
			t.Fatalf("compatible_only: expected side_angle missing, got %v", got.AssetPack.MissingRoles)
		}
	})

	// preview_allowed: the preview-only role (side_angle → front) now reuses.
	// A single-role pack isolates it from the compatible role (both would
	// otherwise claim the one candidate asset, and an asset backs only one role).
	t.Run("preview_allowed", func(t *testing.T) {
		creator := &estimatingPackCreator{}
		router := newPacksRouterWithRetriever(creator, seededPackIdentities(), src)
		body := map[string]any{
			"world_id":         packWorldID,
			"style_profile_id": "sty_ok",
			"variant_keys":     []string{"side_angle_portrait"},
			"fallback_policy":  "preview_allowed",
		}
		rec := sendJSONWithHeaders(t, router, http.MethodPost, "/v1/characters/char_hero/generate-pack",
			tenantA, []string{"images:write"}, body, nil)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("expected 202, got %d body=%s", rec.Code, rec.Body.String())
		}
		if creator.got.JobType != "" {
			t.Fatalf("preview_allowed all-hits must not call CreateAndEnqueue")
		}
		reuse := creator.gotReuse
		if len(reuse.ReusedItems) != 1 || reuse.ReusedItems[0].MatchType != "preview_fallback" {
			t.Fatalf("preview_allowed: expected the side-angle role reused as preview_fallback, got %+v", reuse.ReusedItems)
		}
		// The weakest reuse tier (preview_fallback) is the aggregate cache result.
		if reuse.CacheResult != "preview_fallback" {
			t.Fatalf("preview_allowed aggregate cache_result: expected preview_fallback, got %q", reuse.CacheResult)
		}
	})
}

// estimatingPackCreator captures the params and returns a populated pack
// result, mirroring estimatingCreator for the artifact path.
type estimatingPackCreator struct {
	got      jobs.CreateAndEnqueueParams
	gotReuse jobs.CreatePackReuseParams
}

func (c *estimatingPackCreator) LookupReplay(_ context.Context, _ jobs.ReplayLookup) (jobs.CreateResult, bool, error) {
	return jobs.CreateResult{}, false, nil
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

// CreateCompletedCacheHitJob is unused by the pack path (it is the artifact
// reuse primitive); it exists only to satisfy the jobs.Creator interface.
func (c *estimatingPackCreator) CreateCompletedCacheHitJob(_ context.Context, _ jobs.CreateCacheHitParams) (jobs.CreateResult, error) {
	return jobs.CreateResult{}, nil
}

// CreateCompletedPackReuseJob captures the all-hits pack reuse call and returns
// a completed pack result.
func (c *estimatingPackCreator) CreateCompletedPackReuseJob(_ context.Context, params jobs.CreatePackReuseParams) (jobs.CreateResult, error) {
	c.gotReuse = params
	final := make([]string, 0, len(params.ReusedItems))
	for _, item := range params.ReusedItems {
		final = append(final, item.AssetID)
	}
	return jobs.CreateResult{
		JobID:            "job_packreuse12345aa",
		Status:           "completed",
		EstimatedCostUSD: "0.0000",
		CacheResult:      params.CacheResult,
		FinalAssetIDs:    final,
		AssetPackID:      "pack_reuse",
	}, nil
}
