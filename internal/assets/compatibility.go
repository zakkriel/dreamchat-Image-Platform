package assets

// Variant compatibility matrix (Phase 5B). This is the pure, deterministic
// library that docs/architecture/variant-compatibility-matrix.md describes:
// given a requested variant and a candidate stored variant, it returns one of
// four outcomes plus a confidence score. Phase 6A will wire DB retrieval to
// it; 5B only builds and tests it (it is *not* consulted by any DB path yet).
//
// The matrix never compares families alone — that would be too coarse (a
// neutral_front and a neutral side_profile share a family but are not freely
// interchangeable). It also consults the structured tags (angle, expression,
// time_of_day, weather, …) that ClassifyVariant attaches.
//
// Product-safety override (matrix §2): when in doubt, return invalid_match.
// Strong-emotion, state-change, strong-weather, and unknown variants are
// strict — they match only on an exact key. It is always safer to generate
// than to show a misleading substitute.

// Compatibility outcomes.
const (
	OutcomeExactMatch      = "exact_match"
	OutcomeCompatibleMatch = "compatible_match"
	OutcomePreviewFallback = "preview_fallback"
	OutcomeInvalidMatch    = "invalid_match"
)

// CompatibilityResult is the matrix verdict for one (requested, candidate)
// pair. Score is 0.0–1.0: 1.0 for exact, lower for compatible/preview, 0.0
// for invalid.
type CompatibilityResult struct {
	Outcome string
	Score   float64
}

func exact() CompatibilityResult { return CompatibilityResult{OutcomeExactMatch, 1.0} }
func invalid() CompatibilityResult {
	return CompatibilityResult{OutcomeInvalidMatch, 0.0}
}
func compatible(score float64) CompatibilityResult {
	return CompatibilityResult{OutcomeCompatibleMatch, score}
}
func preview(score float64) CompatibilityResult {
	return CompatibilityResult{OutcomePreviewFallback, score}
}

// strictCharacterFamilies never substitute except on an exact key match.
var strictCharacterFamilies = map[string]struct{}{
	FamilyStrongEmotion: {},
	FamilyOutfit:        {},
	FamilyState:         {},
	FamilyPortraitCrop:  {},
	FamilyFullBody:      {},
}

// strictPlaceFamilies never substitute except on an exact key match.
var strictPlaceFamilies = map[string]struct{}{
	FamilyPlaceFraming: {},
	FamilyPlaceState:   {},
}

// strongWeatherValues are weather tags that contradict a calm/default scene
// (and each other). Mild weather (clear/overcast/light_rain) is not here.
var strongWeatherValues = map[string]struct{}{
	"rain":  {},
	"snow":  {},
	"storm": {},
	"fog":   {},
	"fire":  {},
	"flood": {},
}

// CompareVariants decides whether candidate may stand in for requested. It is
// deterministic and order-sensitive in intent: `requested` is what the caller
// wants, `candidate` is the stored asset being considered as a substitute.
func CompareVariants(entityType string, requested, candidate ClassifiedVariant) CompatibilityResult {
	// 1. Exact key match always wins — even for unknown/strict variants.
	if requested.Key == candidate.Key {
		return exact()
	}
	// 2. Unknown variants are invalid unless the keys matched above.
	if requested.Family == FamilyUnknown || candidate.Family == FamilyUnknown {
		return invalid()
	}
	switch entityType {
	case EntityCharacter:
		return compareCharacter(requested, candidate)
	case EntityPlace:
		return comparePlace(requested, candidate)
	default:
		// Unknown entity type: product-safety override.
		return invalid()
	}
}

func compareCharacter(requested, candidate ClassifiedVariant) CompatibilityResult {
	// Strict families never substitute across different keys.
	if isStrict(strictCharacterFamilies, requested.Family) || isStrict(strictCharacterFamilies, candidate.Family) {
		return invalid()
	}

	// Both families are now in the mild set {neutral, warm, tense}.
	switch {
	case requested.Family == candidate.Family:
		if requested.Family == FamilyNeutral {
			return characterAngle(requested, candidate)
		}
		// warm↔warm or tense↔tense: same emotional family, different key
		// (e.g. warm vs smiling, serious vs tense) — safe to substitute.
		return compatible(0.85)

	case isMildExpression(requested.Family) && candidate.Family == FamilyNeutral:
		// Requesting a mild expression, candidate is a neutral portrait.
		// Warm/neutral are compatible when the neutral asset is fallback-safe.
		if candidate.FallbackAllowed {
			return compatible(0.65)
		}
		return invalid()

	case requested.Family == FamilyNeutral && isMildExpression(candidate.Family):
		// Requesting generic presence, candidate is a mild expression — a warm
		// smile reads fine as generic presence when fallback-safe.
		if candidate.FallbackAllowed {
			return compatible(0.65)
		}
		return invalid()

	default:
		// warm ↔ tense cross: matrix §7.2 is explicit that warm is invalid for
		// a tense request and vice versa.
		return invalid()
	}
}

// characterAngle resolves two neutral portraits that differ only in angle.
func characterAngle(requested, candidate ClassifiedVariant) CompatibilityResult {
	ra := requested.Tags["angle"]
	ca := candidate.Tags["angle"]
	if ra == ca {
		// Same angle, different key spelling (e.g. neutral_front vs
		// neutral_front_portrait) — effectively the same view.
		return compatible(0.9)
	}
	if isFrontish(ra) && isFrontish(ca) {
		// front / three_quarter / bust are interchangeable for portrait display.
		return compatible(0.85)
	}
	if ra == "side_profile" || ca == "side_profile" {
		// A side profile may be shown temporarily while the real angle renders.
		return preview(0.5)
	}
	if ra == "back" || ca == "back" {
		return invalid()
	}
	return invalid()
}

func comparePlace(requested, candidate ClassifiedVariant) CompatibilityResult {
	if isStrict(strictPlaceFamilies, requested.Family) || isStrict(strictPlaceFamilies, candidate.Family) {
		return invalid()
	}

	switch {
	case requested.Family == candidate.Family:
		switch requested.Family {
		case FamilyTimeOfDay:
			return placeTimeOfDay(requested, candidate)
		case FamilyWeather:
			return placeWeather(requested, candidate)
		case FamilyCrowd:
			return placeCrowd(requested, candidate)
		case FamilyEstablishing:
			// Wide vs close establishing views read the same location.
			return compatible(0.8)
		default:
			return invalid()
		}

	case requested.Family == FamilyTimeOfDay && candidate.Family == FamilyEstablishing:
		// A time-agnostic establishing shot may preview a missing time-of-day
		// variant (matrix §8.1) when it is fallback-safe.
		if candidate.FallbackAllowed && candidate.Tags["time_of_day"] == "" {
			return preview(0.4)
		}
		return invalid()

	default:
		// Any other cross-family place comparison contradicts the scene intent.
		return invalid()
	}
}

func placeTimeOfDay(requested, candidate ClassifiedVariant) CompatibilityResult {
	rt := requested.Tags["time_of_day"]
	ct := candidate.Tags["time_of_day"]
	if rt == ct {
		return compatible(0.9)
	}
	// dawn/dusk share lighting and may substitute for each other only.
	if isTwilight(rt) && isTwilight(ct) {
		return compatible(0.7)
	}
	// day↔night and any day/night↔twilight crossing contradicts the scene.
	return invalid()
}

func placeWeather(requested, candidate ClassifiedVariant) CompatibilityResult {
	rw := requested.Tags["weather"]
	cw := candidate.Tags["weather"]
	if _, strong := strongWeatherValues[rw]; strong {
		return invalid()
	}
	if _, strong := strongWeatherValues[cw]; strong {
		return invalid()
	}
	// Both mild (clear/overcast/light_rain): substitutable for a calm scene.
	return compatible(0.7)
}

func placeCrowd(requested, candidate ClassifiedVariant) CompatibilityResult {
	if requested.Tags["crowd"] == candidate.Tags["crowd"] {
		return compatible(0.8)
	}
	// empty ↔ busy contradicts whether the scene is active.
	return invalid()
}

func isStrict(set map[string]struct{}, family string) bool {
	_, ok := set[family]
	return ok
}

func isMildExpression(family string) bool {
	return family == FamilyWarm || family == FamilyTense
}

func isFrontish(angle string) bool {
	return angle == "front" || angle == "three_quarter" || angle == "bust"
}

func isTwilight(tod string) bool {
	return tod == "dawn" || tod == "dusk"
}
