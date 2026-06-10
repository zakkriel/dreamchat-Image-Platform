package assets

import (
	"reflect"
	"sort"
	"testing"
)

// Phase 5B variant classification: every documented character and place role
// maps to the expected family / tags / compatibility tags / fallback flags,
// unknown keys classify unsafely, and the classifier is deterministic.

func TestClassifyCharacterRoles(t *testing.T) {
	cases := []struct {
		key             string
		family          string
		tags            map[string]string
		compatTags      []string
		fallbackAllowed bool
	}{
		{
			key:             "neutral_front_portrait",
			family:          FamilyNeutral,
			tags:            map[string]string{"angle": "front", "expression": "neutral"},
			compatTags:      []string{TagGenericPresence, TagPreviewSafe, TagIdentityAnchorEligible},
			fallbackAllowed: true,
		},
		{
			key:             "neutral_three_quarter_portrait",
			family:          FamilyNeutral,
			tags:            map[string]string{"angle": "three_quarter", "expression": "neutral"},
			compatTags:      []string{TagGenericPresence, TagPreviewSafe, TagIdentityAnchorEligible},
			fallbackAllowed: true,
		},
		{
			key:             "side_angle_portrait",
			family:          FamilyNeutral,
			tags:            map[string]string{"angle": "side_profile", "expression": "neutral"},
			compatTags:      []string{TagPreviewSafe},
			fallbackAllowed: true,
		},
		{
			key:             "expression_warm",
			family:          FamilyWarm,
			tags:            map[string]string{"expression": "warm"},
			compatTags:      []string{TagPreviewSafe},
			fallbackAllowed: true,
		},
		{
			key:             "expression_smiling",
			family:          FamilyWarm,
			tags:            map[string]string{"expression": "smiling"},
			compatTags:      []string{TagPreviewSafe},
			fallbackAllowed: true,
		},
		{
			key:             "expression_serious",
			family:          FamilyTense,
			tags:            map[string]string{"expression": "serious"},
			compatTags:      []string{TagPreviewSafe},
			fallbackAllowed: true,
		},
		{
			key:             "expression_angry",
			family:          FamilyStrongEmotion,
			tags:            map[string]string{"expression": "angry"},
			compatTags:      []string{},
			fallbackAllowed: false,
		},
		{
			key:             "expression_surprised",
			family:          FamilyStrongEmotion,
			tags:            map[string]string{"expression": "surprised"},
			compatTags:      []string{},
			fallbackAllowed: false,
		},
		{
			key:             "state_injured",
			family:          FamilyState,
			tags:            map[string]string{"state": "injured"},
			compatTags:      []string{},
			fallbackAllowed: false,
		},
		{
			key:             "outfit_formal",
			family:          FamilyOutfit,
			tags:            map[string]string{"outfit": "formal"},
			compatTags:      []string{},
			fallbackAllowed: false,
		},
		{
			key:             "full_body_reference",
			family:          FamilyFullBody,
			tags:            map[string]string{"framing": "full_body"},
			compatTags:      []string{},
			fallbackAllowed: false,
		},
		{
			key:             "profile_icon",
			family:          FamilyPortraitCrop,
			tags:            map[string]string{"framing": "icon"},
			compatTags:      []string{},
			fallbackAllowed: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			got := ClassifyVariant(EntityCharacter, tc.key)
			assertClassification(t, got, tc.key, EntityCharacter, tc.family, tc.tags, tc.compatTags, tc.fallbackAllowed)
		})
	}
}

func TestClassifyPlaceRoles(t *testing.T) {
	cases := []struct {
		key             string
		family          string
		tags            map[string]string
		compatTags      []string
		fallbackAllowed bool
	}{
		{
			key:             "establishing_wide_view",
			family:          FamilyEstablishing,
			tags:            map[string]string{"framing": "wide"},
			compatTags:      []string{TagGenericPresence, TagPreviewSafe},
			fallbackAllowed: true,
		},
		{
			key:             "closer_atmospheric_view",
			family:          FamilyEstablishing,
			tags:            map[string]string{"framing": "close"},
			compatTags:      []string{TagPreviewSafe},
			fallbackAllowed: true,
		},
		{
			key:             "day_view",
			family:          FamilyTimeOfDay,
			tags:            map[string]string{"time_of_day": "day"},
			compatTags:      []string{TagPreviewSafe},
			fallbackAllowed: true,
		},
		{
			key:             "night_view",
			family:          FamilyTimeOfDay,
			tags:            map[string]string{"time_of_day": "night"},
			compatTags:      []string{},
			fallbackAllowed: false,
		},
		{
			key:             "dawn_view",
			family:          FamilyTimeOfDay,
			tags:            map[string]string{"time_of_day": "dawn"},
			compatTags:      []string{},
			fallbackAllowed: false,
		},
		{
			key:             "calm_empty_view",
			family:          FamilyCrowd,
			tags:            map[string]string{"crowd": "empty", "mood": "calm"},
			compatTags:      []string{TagPreviewSafe},
			fallbackAllowed: true,
		},
		{
			key:             "busy_active_view",
			family:          FamilyCrowd,
			tags:            map[string]string{"crowd": "busy", "mood": "active"},
			compatTags:      []string{},
			fallbackAllowed: false,
		},
		{
			key:             "rainy_view",
			family:          FamilyWeather,
			tags:            map[string]string{"weather": "rain"},
			compatTags:      []string{},
			fallbackAllowed: false,
		},
		{
			key:             "weather_rain",
			family:          FamilyWeather,
			tags:            map[string]string{"weather": "rain"},
			compatTags:      []string{},
			fallbackAllowed: false,
		},
		{
			key:             "state_damaged",
			family:          FamilyPlaceState,
			tags:            map[string]string{"state": "damaged"},
			compatTags:      []string{},
			fallbackAllowed: false,
		},
		{
			key:             "interior_view",
			family:          FamilyPlaceFraming,
			tags:            map[string]string{"framing": "interior"},
			compatTags:      []string{},
			fallbackAllowed: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			got := ClassifyVariant(EntityPlace, tc.key)
			assertClassification(t, got, tc.key, EntityPlace, tc.family, tc.tags, tc.compatTags, tc.fallbackAllowed)
		})
	}
}

func TestClassifyUnknownVariantIsUnsafe(t *testing.T) {
	for _, entity := range []string{EntityCharacter, EntityPlace, "artifact", "nonsense"} {
		got := ClassifyVariant(entity, "totally_made_up_key")
		if got.Family != FamilyUnknown {
			t.Fatalf("%s: expected family unknown, got %q", entity, got.Family)
		}
		if len(got.CompatibilityTags) != 0 {
			t.Fatalf("%s: unknown must carry no compatibility tags, got %v", entity, got.CompatibilityTags)
		}
		if got.FallbackAllowed {
			t.Fatalf("%s: unknown must not be fallback-allowed", entity)
		}
		if got.FallbackRank != fallbackRankNever {
			t.Fatalf("%s: unknown must carry the low-priority fallback rank %d, got %d", entity, fallbackRankNever, got.FallbackRank)
		}
	}
}

func TestClassifyDefaultArtifactKeyIsUnknown(t *testing.T) {
	// The single-artifact path uses the "default" key and is not variant-aware;
	// it must classify as unknown (no compatibility tags, no fallback).
	got := ClassifyVariant("artifact", DefaultVariantKey)
	if got.Family != FamilyUnknown || got.FallbackAllowed {
		t.Fatalf("default artifact key must be unknown/unsafe, got %+v", got)
	}
}

func TestClassifyIsDeterministic(t *testing.T) {
	keys := []string{"neutral_front_portrait", "expression_angry", "day_view", "unknown_key"}
	for _, k := range keys {
		first := ClassifyVariant(EntityCharacter, k)
		for i := 0; i < 5; i++ {
			again := ClassifyVariant(EntityCharacter, k)
			if !reflect.DeepEqual(first, again) {
				t.Fatalf("classification of %q not deterministic: %+v vs %+v", k, first, again)
			}
		}
	}
}

func TestClassifyReturnsCopiesNotSharedState(t *testing.T) {
	// Mutating a returned classification must not poison the shared table.
	a := ClassifyVariant(EntityCharacter, "neutral_front_portrait")
	a.Tags["angle"] = "mutated"
	a.CompatibilityTags[0] = "mutated"
	b := ClassifyVariant(EntityCharacter, "neutral_front_portrait")
	if b.Tags["angle"] != "front" {
		t.Fatalf("shared tags map leaked mutation: %v", b.Tags)
	}
	if b.CompatibilityTags[0] != TagGenericPresence {
		t.Fatalf("shared compat tags leaked mutation: %v", b.CompatibilityTags)
	}
}

func assertClassification(t *testing.T, got ClassifiedVariant, key, entity, family string, tags map[string]string, compatTags []string, fallbackAllowed bool) {
	t.Helper()
	if got.Key != key {
		t.Fatalf("key: expected %q, got %q", key, got.Key)
	}
	if got.EntityType != entity {
		t.Fatalf("entity: expected %q, got %q", entity, got.EntityType)
	}
	if got.Family != family {
		t.Fatalf("family: expected %q, got %q", family, got.Family)
	}
	if !reflect.DeepEqual(got.Tags, tags) {
		t.Fatalf("tags: expected %v, got %v", tags, got.Tags)
	}
	if !sameStringSet(got.CompatibilityTags, compatTags) {
		t.Fatalf("compatibility tags: expected %v, got %v", compatTags, got.CompatibilityTags)
	}
	if got.FallbackAllowed != fallbackAllowed {
		t.Fatalf("fallback_allowed: expected %v, got %v", fallbackAllowed, got.FallbackAllowed)
	}
	// Strict / unknown variants must carry the low-priority rank; fallback-able
	// variants must carry a usable (lower) rank.
	if !fallbackAllowed && got.FallbackRank != fallbackRankNever {
		t.Fatalf("non-fallback %q must carry rank %d, got %d", key, fallbackRankNever, got.FallbackRank)
	}
	if fallbackAllowed && got.FallbackRank >= fallbackRankNever {
		t.Fatalf("fallback-able %q must carry a usable rank, got %d", key, got.FallbackRank)
	}
}

func sameStringSet(a, b []string) bool {
	ac := append([]string(nil), a...)
	bc := append([]string(nil), b...)
	sort.Strings(ac)
	sort.Strings(bc)
	return reflect.DeepEqual(ac, bc)
}
