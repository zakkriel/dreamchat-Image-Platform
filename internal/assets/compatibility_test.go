package assets

import "testing"

// Phase 5B compatibility matrix: representative (requested, candidate) pairs
// resolve to the documented outcome. The matrix is pure and deterministic; it
// is not wired to any DB path in 5B.

func TestCompareVariantsCharacter(t *testing.T) {
	cases := []struct {
		name      string
		requested string
		candidate string
		outcome   string
	}{
		{"same key is exact", "neutral_front_portrait", "neutral_front_portrait", OutcomeExactMatch},
		{"front vs three_quarter compatible", "neutral_front_portrait", "neutral_three_quarter_portrait", OutcomeCompatibleMatch},
		{"front vs side_profile preview", "neutral_front_portrait", "side_angle_portrait", OutcomePreviewFallback},
		{"neutral vs warm smile compatible", "neutral_front_portrait", "expression_smiling", OutcomeCompatibleMatch},
		{"warm smile vs neutral compatible", "expression_smiling", "neutral_front_portrait", OutcomeCompatibleMatch},
		{"warm vs smiling same family compatible", "expression_warm", "expression_smiling", OutcomeCompatibleMatch},
		{"serious vs tense same family compatible", "expression_serious", "expression_tense", OutcomeCompatibleMatch},
		{"warm vs tense cross is invalid", "expression_warm", "expression_serious", OutcomeInvalidMatch},
		{"angry vs neutral invalid", "expression_angry", "neutral_front_portrait", OutcomeInvalidMatch},
		{"neutral vs angry invalid", "neutral_front_portrait", "expression_angry", OutcomeInvalidMatch},
		{"terrified vs warm invalid", "expression_terrified", "expression_warm", OutcomeInvalidMatch},
		{"injured vs neutral invalid", "state_injured", "neutral_front_portrait", OutcomeInvalidMatch},
		{"outfit vs neutral invalid", "outfit_formal", "neutral_front_portrait", OutcomeInvalidMatch},
		{"full_body vs neutral invalid", "full_body_reference", "neutral_front_portrait", OutcomeInvalidMatch},
		{"angry exact still matches", "expression_angry", "expression_angry", OutcomeExactMatch},
		{"unknown vs neutral invalid", "made_up_key", "neutral_front_portrait", OutcomeInvalidMatch},
		{"unknown exact key matches", "made_up_key", "made_up_key", OutcomeExactMatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := ClassifyVariant(EntityCharacter, tc.requested)
			cand := ClassifyVariant(EntityCharacter, tc.candidate)
			got := CompareVariants(EntityCharacter, req, cand)
			assertOutcome(t, got, tc.outcome)
		})
	}
}

func TestCompareVariantsPlace(t *testing.T) {
	cases := []struct {
		name      string
		requested string
		candidate string
		outcome   string
	}{
		{"same key exact", "day_view", "day_view", OutcomeExactMatch},
		{"day vs night invalid", "day_view", "night_view", OutcomeInvalidMatch},
		{"night vs day invalid", "night_view", "day_view", OutcomeInvalidMatch},
		{"dawn vs dusk compatible", "dawn_view", "dusk_view", OutcomeCompatibleMatch},
		{"dawn vs day invalid", "dawn_view", "day_view", OutcomeInvalidMatch},
		{"rainy vs clear_day invalid", "rainy_view", "clear_day_view", OutcomeInvalidMatch},
		{"clear vs rainy invalid", "clear_day_view", "rainy_view", OutcomeInvalidMatch},
		{"clear vs overcast compatible (mild)", "clear_day_view", "overcast_view", OutcomeCompatibleMatch},
		{"establishing wide vs close compatible", "establishing_wide_view", "closer_atmospheric_view", OutcomeCompatibleMatch},
		{"time_of_day vs establishing preview", "day_view", "establishing_wide_view", OutcomePreviewFallback},
		{"empty vs busy invalid", "calm_empty_view", "busy_active_view", OutcomeInvalidMatch},
		{"state damaged vs establishing invalid", "state_damaged", "establishing_wide_view", OutcomeInvalidMatch},
		{"interior vs establishing invalid", "interior_view", "establishing_wide_view", OutcomeInvalidMatch},
		{"state rebuilt exact", "state_rebuilt", "state_rebuilt", OutcomeExactMatch},
		{"unknown vs day invalid", "made_up", "day_view", OutcomeInvalidMatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := ClassifyVariant(EntityPlace, tc.requested)
			cand := ClassifyVariant(EntityPlace, tc.candidate)
			got := CompareVariants(EntityPlace, req, cand)
			assertOutcome(t, got, tc.outcome)
		})
	}
}

func TestCompareVariantsUnknownEntityIsInvalid(t *testing.T) {
	req := ClassifyVariant(EntityCharacter, "neutral_front_portrait")
	cand := ClassifyVariant(EntityCharacter, "neutral_three_quarter_portrait")
	got := CompareVariants("artifact", req, cand)
	if got.Outcome != OutcomeInvalidMatch {
		t.Fatalf("unknown entity must be invalid, got %s", got.Outcome)
	}
}

func TestCompareVariantsIsDeterministic(t *testing.T) {
	req := ClassifyVariant(EntityCharacter, "neutral_front_portrait")
	cand := ClassifyVariant(EntityCharacter, "expression_smiling")
	first := CompareVariants(EntityCharacter, req, cand)
	for i := 0; i < 5; i++ {
		again := CompareVariants(EntityCharacter, req, cand)
		if again != first {
			t.Fatalf("CompareVariants not deterministic: %+v vs %+v", first, again)
		}
	}
}

func TestCompareVariantsScoreBounds(t *testing.T) {
	exactReq := ClassifyVariant(EntityCharacter, "neutral_front_portrait")
	if got := CompareVariants(EntityCharacter, exactReq, exactReq); got.Score != 1.0 {
		t.Fatalf("exact match score must be 1.0, got %v", got.Score)
	}
	invReq := ClassifyVariant(EntityCharacter, "expression_angry")
	invCand := ClassifyVariant(EntityCharacter, "neutral_front_portrait")
	if got := CompareVariants(EntityCharacter, invReq, invCand); got.Score != 0.0 {
		t.Fatalf("invalid match score must be 0.0, got %v", got.Score)
	}
	// compatible / preview land strictly between.
	compReq := ClassifyVariant(EntityCharacter, "neutral_front_portrait")
	compCand := ClassifyVariant(EntityCharacter, "neutral_three_quarter_portrait")
	got := CompareVariants(EntityCharacter, compReq, compCand)
	if got.Score <= 0.0 || got.Score >= 1.0 {
		t.Fatalf("compatible score must be in (0,1), got %v", got.Score)
	}
}

func assertOutcome(t *testing.T, got CompatibilityResult, want string) {
	t.Helper()
	if got.Outcome != want {
		t.Fatalf("expected outcome %s, got %s (score %v)", want, got.Outcome, got.Score)
	}
	switch want {
	case OutcomeExactMatch:
		if got.Score != 1.0 {
			t.Fatalf("exact match must score 1.0, got %v", got.Score)
		}
	case OutcomeInvalidMatch:
		if got.Score != 0.0 {
			t.Fatalf("invalid match must score 0.0, got %v", got.Score)
		}
	default:
		if got.Score <= 0.0 || got.Score >= 1.0 {
			t.Fatalf("%s must score in (0,1), got %v", want, got.Score)
		}
	}
}
