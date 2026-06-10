package assets

// Variant classification (Phase 5B). This file gives the previously-opaque
// `variant_key` (Phase 5A) deterministic, table-driven meaning: a family, a
// set of structured tags, compatibility tags, and fallback eligibility. The
// classification feeds two consumers:
//
//   - the pack worker, which stamps variant_family / compatibility_tags /
//     fallback_allowed / fallback_rank / metadata onto every generated asset
//     (so 6A retrieval can query them), and
//   - the compatibility matrix (compatibility.go), which decides whether one
//     classified variant may substitute for another.
//
// The vocabularies come from PRD 04 §4.4 / §5.4 (asset roles),
// docs/architecture/asset-versioning.md (starter-pack names) and
// docs/architecture/variant-compatibility-matrix.md (families + dimensions).
// Where two specs name the same concept differently (e.g. `neutral_front`
// vs `neutral_front_portrait`) both spellings are aliased to one spec.
//
// Classification is deterministic: the same key always yields the same
// ClassifiedVariant, and an unknown key classifies *unsafely* (family
// "unknown", no compatibility tags, fallback disabled, lowest fallback rank)
// rather than masquerading as a generic-safe asset — the product-safety rule
// from the matrix (§2: "when in doubt, generate") starts here.

// Entity types a variant can be classified under.
const (
	EntityCharacter = "character"
	EntityPlace     = "place"
)

// Variant families. These group keys for matrix lookups; the matrix never
// compares families alone (it also consults the structured tags below).
const (
	// Shared / fallback.
	FamilyUnknown = "unknown"

	// Character families.
	FamilyNeutral       = "neutral"
	FamilyWarm          = "warm"
	FamilyTense         = "tense"
	FamilyStrongEmotion = "strong_emotion"
	FamilyOutfit        = "outfit"
	FamilyState         = "state"         // character state change (injury, disguise, …)
	FamilyPortraitCrop  = "portrait_crop" // profile_icon and similar crops
	FamilyFullBody      = "full_body"

	// Place families.
	FamilyEstablishing = "establishing"
	FamilyTimeOfDay    = "time_of_day"
	FamilyCrowd        = "crowd"
	FamilyWeather      = "weather"
	FamilyPlaceFraming = "place_framing" // interior / exterior / landmark detail
	FamilyPlaceState   = "place_state"   // damaged / rebuilt / occupied / …
)

// Compatibility tags consulted by the matrix and 6A retrieval.
const (
	TagGenericPresence        = "generic_presence"
	TagPreviewSafe            = "preview_safe"
	TagIdentityAnchorEligible = "identity_anchor_eligible"
)

// Fallback rank tiers. Lower = preferred when several candidates qualify.
// fallbackRankNever is the deliberately-low priority handed to strict and
// unknown variants so they sort last if a buggy caller ever considers them.
const (
	fallbackRankPrimary   int32 = 10
	fallbackRankSecondary int32 = 20
	fallbackRankTertiary  int32 = 30
	fallbackRankMild      int32 = 40
	fallbackRankWeak      int32 = 60
	fallbackRankNever     int32 = 1000
)

// ClassifiedVariant is the deterministic classification of a variant_key.
type ClassifiedVariant struct {
	Key               string
	EntityType        string // character | place
	Family            string
	Tags              map[string]string
	CompatibilityTags []string
	FallbackAllowed   bool
	FallbackRank      int32
}

// variantSpec is the table-driven definition of a known variant. The map key
// is the (entity_type, variant_key) pair; aliases share one spec.
type variantSpec struct {
	family            string
	tags              map[string]string
	compatibilityTags []string
	fallbackAllowed   bool
	fallbackRank      int32
}

// characterVariants is the character role vocabulary. Several PRD/spec
// spellings alias onto one spec (see asset-versioning.md vs PRD 04 §4.4).
var characterVariants = map[string]variantSpec{
	// --- Neutral portraits (generic presence) --------------------------------
	"neutral_front_portrait": {
		family:            FamilyNeutral,
		tags:              map[string]string{"angle": "front", "expression": "neutral"},
		compatibilityTags: []string{TagGenericPresence, TagPreviewSafe, TagIdentityAnchorEligible},
		fallbackAllowed:   true,
		fallbackRank:      fallbackRankPrimary,
	},
	"neutral_front": { // asset-versioning.md spelling
		family:            FamilyNeutral,
		tags:              map[string]string{"angle": "front", "expression": "neutral"},
		compatibilityTags: []string{TagGenericPresence, TagPreviewSafe, TagIdentityAnchorEligible},
		fallbackAllowed:   true,
		fallbackRank:      fallbackRankPrimary,
	},
	"neutral_three_quarter_portrait": {
		family:            FamilyNeutral,
		tags:              map[string]string{"angle": "three_quarter", "expression": "neutral"},
		compatibilityTags: []string{TagGenericPresence, TagPreviewSafe, TagIdentityAnchorEligible},
		fallbackAllowed:   true,
		fallbackRank:      fallbackRankSecondary,
	},
	"neutral_three_quarter": {
		family:            FamilyNeutral,
		tags:              map[string]string{"angle": "three_quarter", "expression": "neutral"},
		compatibilityTags: []string{TagGenericPresence, TagPreviewSafe, TagIdentityAnchorEligible},
		fallbackAllowed:   true,
		fallbackRank:      fallbackRankSecondary,
	},
	"neutral_bust": {
		family:            FamilyNeutral,
		tags:              map[string]string{"angle": "front", "framing": "bust", "expression": "neutral"},
		compatibilityTags: []string{TagGenericPresence, TagPreviewSafe},
		fallbackAllowed:   true,
		fallbackRank:      fallbackRankTertiary,
	},
	"side_angle_portrait": {
		family:            FamilyNeutral,
		tags:              map[string]string{"angle": "side_profile", "expression": "neutral"},
		compatibilityTags: []string{TagPreviewSafe},
		fallbackAllowed:   true,
		fallbackRank:      fallbackRankWeak,
	},
	"side_profile": {
		family:            FamilyNeutral,
		tags:              map[string]string{"angle": "side_profile", "expression": "neutral"},
		compatibilityTags: []string{TagPreviewSafe},
		fallbackAllowed:   true,
		fallbackRank:      fallbackRankWeak,
	},

	// --- Warm expressions ----------------------------------------------------
	"expression_warm": {
		family:            FamilyWarm,
		tags:              map[string]string{"expression": "warm"},
		compatibilityTags: []string{TagPreviewSafe},
		fallbackAllowed:   true,
		fallbackRank:      fallbackRankMild,
	},
	"warm_expression": {
		family:            FamilyWarm,
		tags:              map[string]string{"expression": "warm"},
		compatibilityTags: []string{TagPreviewSafe},
		fallbackAllowed:   true,
		fallbackRank:      fallbackRankMild,
	},
	"expression_smiling": {
		family:            FamilyWarm,
		tags:              map[string]string{"expression": "smiling"},
		compatibilityTags: []string{TagPreviewSafe},
		fallbackAllowed:   true,
		fallbackRank:      fallbackRankMild,
	},
	"smiling": {
		family:            FamilyWarm,
		tags:              map[string]string{"expression": "smiling"},
		compatibilityTags: []string{TagPreviewSafe},
		fallbackAllowed:   true,
		fallbackRank:      fallbackRankMild,
	},
	// PRD 04 §4.2 minimum-pack spelling ("warm_or_smiling").
	"warm_or_smiling_expression": {
		family:            FamilyWarm,
		tags:              map[string]string{"expression": "warm"},
		compatibilityTags: []string{TagPreviewSafe},
		fallbackAllowed:   true,
		fallbackRank:      fallbackRankMild,
	},

	// --- Serious / tense expressions -----------------------------------------
	"expression_serious": {
		family:            FamilyTense,
		tags:              map[string]string{"expression": "serious"},
		compatibilityTags: []string{TagPreviewSafe},
		fallbackAllowed:   true,
		fallbackRank:      fallbackRankMild,
	},
	"serious": {
		family:            FamilyTense,
		tags:              map[string]string{"expression": "serious"},
		compatibilityTags: []string{TagPreviewSafe},
		fallbackAllowed:   true,
		fallbackRank:      fallbackRankMild,
	},
	"expression_tense": {
		family:            FamilyTense,
		tags:              map[string]string{"expression": "tense"},
		compatibilityTags: []string{TagPreviewSafe},
		fallbackAllowed:   true,
		fallbackRank:      fallbackRankMild,
	},
	"tense_expression": {
		family:            FamilyTense,
		tags:              map[string]string{"expression": "tense"},
		compatibilityTags: []string{TagPreviewSafe},
		fallbackAllowed:   true,
		fallbackRank:      fallbackRankMild,
	},
	// PRD 04 §4.2 minimum-pack spelling ("serious_or_tense").
	"serious_or_tense_expression": {
		family:            FamilyTense,
		tags:              map[string]string{"expression": "serious"},
		compatibilityTags: []string{TagPreviewSafe},
		fallbackAllowed:   true,
		fallbackRank:      fallbackRankMild,
	},

	// --- Strong-emotion expressions (strict: never substitute) ---------------
	"expression_angry":     strongEmotion("angry"),
	"angry_expression":     strongEmotion("angry"),
	"expression_surprised": strongEmotion("surprised"),
	"surprised_expression": strongEmotion("surprised"),
	// PRD 04 §4.2 minimum-pack spellings ("angry_or_defensive",
	// "surprised_or_shocked"). Strong emotion is strict: never substitutes.
	"angry_or_defensive_expression":   strongEmotion("angry"),
	"surprised_or_shocked_expression": strongEmotion("surprised"),
	"expression_sad":                  strongEmotion("sad"),
	"expression_afraid":               strongEmotion("afraid"),
	"expression_terrified":            strongEmotion("terrified"),
	"expression_crying":               strongEmotion("crying"),
	"expression_romantic":             strongEmotion("romantic"),

	// --- Outfits (strict) ----------------------------------------------------
	"outfit_formal": outfit("formal"),
	"outfit_travel": outfit("travel"),
	"outfit_work":   outfit("work"),

	// --- Character state changes (strict) ------------------------------------
	"state_injured":   characterState("injured"),
	"state_disguised": characterState("disguised"),
	"battle_damaged":  characterState("battle_damaged"),

	// --- Crops / framing (strict) --------------------------------------------
	"profile_icon": {
		family:          FamilyPortraitCrop,
		tags:            map[string]string{"framing": "icon"},
		fallbackAllowed: false,
		fallbackRank:    fallbackRankNever,
	},
	"full_body_reference": {
		family:          FamilyFullBody,
		tags:            map[string]string{"framing": "full_body"},
		fallbackAllowed: false,
		fallbackRank:    fallbackRankNever,
	},
}

// placeVariants is the place role vocabulary (PRD 04 §5.4 + asset-versioning).
var placeVariants = map[string]variantSpec{
	// --- Establishing --------------------------------------------------------
	"establishing_wide_view": {
		family:            FamilyEstablishing,
		tags:              map[string]string{"framing": "wide"},
		compatibilityTags: []string{TagGenericPresence, TagPreviewSafe},
		fallbackAllowed:   true,
		fallbackRank:      fallbackRankPrimary,
	},
	"establishing_wide": {
		family:            FamilyEstablishing,
		tags:              map[string]string{"framing": "wide"},
		compatibilityTags: []string{TagGenericPresence, TagPreviewSafe},
		fallbackAllowed:   true,
		fallbackRank:      fallbackRankPrimary,
	},
	"establishing_wide_day": {
		family:            FamilyEstablishing,
		tags:              map[string]string{"framing": "wide", "time_of_day": "day"},
		compatibilityTags: []string{TagGenericPresence, TagPreviewSafe},
		fallbackAllowed:   true,
		fallbackRank:      fallbackRankPrimary,
	},
	"closer_atmospheric_view": {
		family:            FamilyEstablishing,
		tags:              map[string]string{"framing": "close"},
		compatibilityTags: []string{TagPreviewSafe},
		fallbackAllowed:   true,
		fallbackRank:      fallbackRankMild,
	},
	"closer_atmospheric": {
		family:            FamilyEstablishing,
		tags:              map[string]string{"framing": "close"},
		compatibilityTags: []string{TagPreviewSafe},
		fallbackAllowed:   true,
		fallbackRank:      fallbackRankMild,
	},

	// --- Time of day ---------------------------------------------------------
	// day is the most neutral establishing light, so it may preview-fallback;
	// night/dawn/dusk are specific and never serve as a fallback for others.
	"day_view": {
		family:            FamilyTimeOfDay,
		tags:              map[string]string{"time_of_day": "day"},
		compatibilityTags: []string{TagPreviewSafe},
		fallbackAllowed:   true,
		fallbackRank:      fallbackRankMild,
	},
	"night_view": {
		family:          FamilyTimeOfDay,
		tags:            map[string]string{"time_of_day": "night"},
		fallbackAllowed: false,
		fallbackRank:    fallbackRankNever,
	},
	"dawn_view": {
		family:          FamilyTimeOfDay,
		tags:            map[string]string{"time_of_day": "dawn"},
		fallbackAllowed: false,
		fallbackRank:    fallbackRankNever,
	},
	"dusk_view": {
		family:          FamilyTimeOfDay,
		tags:            map[string]string{"time_of_day": "dusk"},
		fallbackAllowed: false,
		fallbackRank:    fallbackRankNever,
	},

	// --- Crowd / mood --------------------------------------------------------
	"calm_empty_view": {
		family:            FamilyCrowd,
		tags:              map[string]string{"crowd": "empty", "mood": "calm"},
		compatibilityTags: []string{TagPreviewSafe},
		fallbackAllowed:   true,
		fallbackRank:      fallbackRankMild,
	},
	"calm_or_empty_view": {
		family:            FamilyCrowd,
		tags:              map[string]string{"crowd": "empty", "mood": "calm"},
		compatibilityTags: []string{TagPreviewSafe},
		fallbackAllowed:   true,
		fallbackRank:      fallbackRankMild,
	},
	"empty_view": {
		family:            FamilyCrowd,
		tags:              map[string]string{"crowd": "empty", "mood": "calm"},
		compatibilityTags: []string{TagPreviewSafe},
		fallbackAllowed:   true,
		fallbackRank:      fallbackRankMild,
	},
	"busy_active_view": {
		family:          FamilyCrowd,
		tags:            map[string]string{"crowd": "busy", "mood": "active"},
		fallbackAllowed: false,
		fallbackRank:    fallbackRankNever,
	},
	"busy_or_active_view": {
		family:          FamilyCrowd,
		tags:            map[string]string{"crowd": "busy", "mood": "active"},
		fallbackAllowed: false,
		fallbackRank:    fallbackRankNever,
	},
	"active_view": {
		family:          FamilyCrowd,
		tags:            map[string]string{"crowd": "busy", "mood": "active"},
		fallbackAllowed: false,
		fallbackRank:    fallbackRankNever,
	},

	// --- Weather -------------------------------------------------------------
	// Mild weather (clear/overcast/light_rain) is mutually compatible; strong
	// weather (rain/snow/storm/fog) is strict.
	"clear_day_view": {
		family:            FamilyWeather,
		tags:              map[string]string{"weather": "clear", "time_of_day": "day"},
		compatibilityTags: []string{TagPreviewSafe},
		fallbackAllowed:   true,
		fallbackRank:      fallbackRankMild,
	},
	"overcast_view": {
		family:            FamilyWeather,
		tags:              map[string]string{"weather": "overcast"},
		compatibilityTags: []string{TagPreviewSafe},
		fallbackAllowed:   true,
		fallbackRank:      fallbackRankMild,
	},
	"weather_rain":  strongWeather("rain"),
	"rainy_view":    strongWeather("rain"),
	"weather_snow":  strongWeather("snow"),
	"weather_storm": strongWeather("storm"),
	"storm_view":    strongWeather("storm"),

	// --- Framing (interior / exterior / landmark) — strict -------------------
	"interior_view":   placeFraming("interior"),
	"exterior_view":   placeFraming("exterior"),
	"landmark_detail": placeFraming("landmark_detail"),

	// --- Place state changes (strict) ----------------------------------------
	"state_damaged":  placeState("damaged"),
	"state_rebuilt":  placeState("rebuilt"),
	"state_occupied": placeState("occupied"),
}

func strongEmotion(expr string) variantSpec {
	return variantSpec{
		family:          FamilyStrongEmotion,
		tags:            map[string]string{"expression": expr},
		fallbackAllowed: false,
		fallbackRank:    fallbackRankNever,
	}
}

func outfit(kind string) variantSpec {
	return variantSpec{
		family:          FamilyOutfit,
		tags:            map[string]string{"outfit": kind},
		fallbackAllowed: false,
		fallbackRank:    fallbackRankNever,
	}
}

func characterState(state string) variantSpec {
	return variantSpec{
		family:          FamilyState,
		tags:            map[string]string{"state": state},
		fallbackAllowed: false,
		fallbackRank:    fallbackRankNever,
	}
}

func strongWeather(weather string) variantSpec {
	return variantSpec{
		family:          FamilyWeather,
		tags:            map[string]string{"weather": weather},
		fallbackAllowed: false,
		fallbackRank:    fallbackRankNever,
	}
}

func placeFraming(framing string) variantSpec {
	return variantSpec{
		family:          FamilyPlaceFraming,
		tags:            map[string]string{"framing": framing},
		fallbackAllowed: false,
		fallbackRank:    fallbackRankNever,
	}
}

func placeState(state string) variantSpec {
	return variantSpec{
		family:          FamilyPlaceState,
		tags:            map[string]string{"state": state},
		fallbackAllowed: false,
		fallbackRank:    fallbackRankNever,
	}
}

// DefaultVariantKey is the benign key used by the single-artifact path, which
// is not variant-aware. It classifies as a default generated asset: no
// compatibility tags and no fallback eligibility, so it is never substituted.
const DefaultVariantKey = "default"

// ClassifyVariant returns the deterministic classification of a variant_key
// under the given entity type. Unknown keys (including the single-artifact
// "default") classify safely as family "unknown" with no compatibility tags
// and fallback disabled — they are never treated as generic-safe.
func ClassifyVariant(entityType, key string) ClassifiedVariant {
	table := tableFor(entityType)
	if spec, ok := table[key]; ok {
		return ClassifiedVariant{
			Key:               key,
			EntityType:        entityType,
			Family:            spec.family,
			Tags:              copyTags(spec.tags),
			CompatibilityTags: copyTagList(spec.compatibilityTags),
			FallbackAllowed:   spec.fallbackAllowed,
			FallbackRank:      spec.fallbackRank,
		}
	}
	return ClassifiedVariant{
		Key:               key,
		EntityType:        entityType,
		Family:            FamilyUnknown,
		Tags:              map[string]string{},
		CompatibilityTags: []string{},
		FallbackAllowed:   false,
		FallbackRank:      fallbackRankNever,
	}
}

// MetadataMap renders the structured classification as the JSONB metadata
// blob stored on visual_assets.metadata: the variant tags plus the derived
// family / fallback annotations, so 6A retrieval and debugging can read the
// classification back off the asset row.
func (c ClassifiedVariant) MetadataMap() map[string]any {
	tags := make(map[string]any, len(c.Tags))
	for k, v := range c.Tags {
		tags[k] = v
	}
	return map[string]any{
		"variant_family":     c.Family,
		"entity_type":        c.EntityType,
		"variant_tags":       tags,
		"compatibility_tags": copyTagList(c.CompatibilityTags),
		"fallback_allowed":   c.FallbackAllowed,
		"fallback_rank":      c.FallbackRank,
	}
}

// ApplyTo stamps the classification's compatibility fields and structured
// metadata onto an InsertParams. It leaves identity/provider/URL fields alone,
// so the worker fills those separately. variant_family is always set (to
// "unknown" for unrecognized keys) so the column is explicit and queryable.
func (c ClassifiedVariant) ApplyTo(p *InsertParams) {
	family := c.Family
	p.VariantFamily = &family
	p.CompatibilityTags = copyTagList(c.CompatibilityTags)
	p.FallbackAllowed = c.FallbackAllowed
	rank := c.FallbackRank
	p.FallbackRank = &rank
	p.Metadata = c.MetadataMap()
}

func tableFor(entityType string) map[string]variantSpec {
	switch entityType {
	case EntityCharacter:
		return characterVariants
	case EntityPlace:
		return placeVariants
	default:
		return nil
	}
}

func copyTags(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func copyTagList(in []string) []string {
	out := make([]string, len(in))
	copy(out, in)
	return out
}
