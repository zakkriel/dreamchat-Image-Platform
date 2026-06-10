package assets

import (
	"context"
	"testing"
)

// memSource is an in-memory CandidateSource that mirrors the SQL predicates so
// the retrieval decision layer can be unit-tested without a database.
type memSource struct {
	assets []VisualAsset
}

func (m *memSource) FindExact(_ context.Context, q RetrievalQuery) (VisualAsset, error) {
	var best *VisualAsset
	for i := range m.assets {
		a := m.assets[i]
		if a.TenantID != q.TenantID || a.WorldID != q.WorldID {
			continue
		}
		if ptrStr(a.VisualIdentityID) != q.VisualIdentityID {
			continue
		}
		if a.VariantKey != q.VariantKey || a.StateVersion != q.StateVersion {
			continue
		}
		if ptrStr(a.StyleProfileID) != q.StyleProfileID {
			continue
		}
		if a.Status != "ready" {
			continue
		}
		if q.StyleProfileVersion != nil {
			if a.StyleProfileVersion == nil || *a.StyleProfileVersion != *q.StyleProfileVersion {
				continue
			}
		}
		if q.QualityTier != "" && a.QualityTier != q.QualityTier {
			continue
		}
		if best == nil || a.ID < best.ID {
			cp := a
			best = &cp
		}
	}
	if best == nil {
		return VisualAsset{}, ErrNotFound
	}
	return *best, nil
}

func (m *memSource) ListRetrievalCandidates(_ context.Context, q RetrievalQuery) ([]VisualAsset, error) {
	var out []VisualAsset
	for i := range m.assets {
		a := m.assets[i]
		if a.TenantID != q.TenantID || a.WorldID != q.WorldID {
			continue
		}
		if ptrStr(a.VisualIdentityID) != q.VisualIdentityID {
			continue
		}
		if a.StateVersion != q.StateVersion || ptrStr(a.StyleProfileID) != q.StyleProfileID {
			continue
		}
		if a.Status != "ready" || a.IsIdentityAnchor {
			continue
		}
		out = append(out, a)
	}
	return out, nil
}

func ptrStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ---------------------------------------------------------------------------
// Builders
// ---------------------------------------------------------------------------

const (
	rtTenant   = "tenant_rt"
	rtWorld    = "world_rt"
	rtIdentity = "vi_rt"
	rtStyle    = "sty_rt"
)

func sp(s string) *string { return &s }
func ip(i int32) *int32   { return &i }

// asset builds a ready character/place asset with the Phase 5B classification
// stamped on, so retrieval sees the same fields the worker would have written.
func asset(id, entity, variantKey string) VisualAsset {
	cv := ClassifyVariant(entity, variantKey)
	fam := cv.Family
	rank := cv.FallbackRank
	return VisualAsset{
		ID:                id,
		TenantID:          rtTenant,
		WorldID:           rtWorld,
		VisualIdentityID:  sp(rtIdentity),
		VariantKey:        variantKey,
		VariantFamily:     &fam,
		StateVersion:      1,
		StyleProfileID:    sp(rtStyle),
		QualityTier:       "standard",
		Status:            "ready",
		CompatibilityTags: cv.CompatibilityTags,
		FallbackAllowed:   cv.FallbackAllowed,
		FallbackRank:      &rank,
	}
}

func baseQuery(entity, variantKey string) RetrievalQuery {
	return RetrievalQuery{
		TenantID:         rtTenant,
		WorldID:          rtWorld,
		VisualIdentityID: rtIdentity,
		EntityType:       entity,
		VariantKey:       variantKey,
		StyleProfileID:   rtStyle,
		StateVersion:     1,
		QualityTier:      "standard",
		FallbackPolicy:   FallbackPolicyCompatibleOnly,
	}
}

func retrieve(t *testing.T, src *memSource, q RetrievalQuery) RetrievalResult {
	t.Helper()
	res, err := NewRetriever(src).Retrieve(context.Background(), q)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	return res
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestRetrieveExactMatch(t *testing.T) {
	src := &memSource{assets: []VisualAsset{asset("a1", EntityCharacter, "neutral_front_portrait")}}
	res := retrieve(t, src, baseQuery(EntityCharacter, "neutral_front_portrait"))
	if res.MatchType != OutcomeExactMatch {
		t.Fatalf("want exact_match, got %s", res.MatchType)
	}
	if res.Asset == nil || res.Asset.ID != "a1" {
		t.Fatalf("want asset a1, got %+v", res.Asset)
	}
	if res.CompatibilityScore != 1.0 {
		t.Fatalf("want score 1.0, got %v", res.CompatibilityScore)
	}
	if res.GenerationRecommended {
		t.Fatal("exact match should not recommend generation")
	}
}

func TestRetrieveExactRespectsDimensions(t *testing.T) {
	a := asset("a1", EntityCharacter, "neutral_front_portrait")
	src := &memSource{assets: []VisualAsset{a}}

	// Wrong tenant.
	q := baseQuery(EntityCharacter, "neutral_front_portrait")
	q.TenantID = "tenant_other"
	if res := retrieve(t, src, q); res.MatchType != OutcomeGeneratedRequired {
		t.Fatalf("wrong tenant: want generated_required, got %s", res.MatchType)
	}
	// Wrong world.
	q = baseQuery(EntityCharacter, "neutral_front_portrait")
	q.WorldID = "world_other"
	if res := retrieve(t, src, q); res.MatchType != OutcomeGeneratedRequired {
		t.Fatalf("wrong world: want generated_required, got %s", res.MatchType)
	}
	// Wrong identity.
	q = baseQuery(EntityCharacter, "neutral_front_portrait")
	q.VisualIdentityID = "vi_other"
	if res := retrieve(t, src, q); res.MatchType != OutcomeGeneratedRequired {
		t.Fatalf("wrong identity: want generated_required, got %s", res.MatchType)
	}
	// Wrong style.
	q = baseQuery(EntityCharacter, "neutral_front_portrait")
	q.StyleProfileID = "sty_other"
	if res := retrieve(t, src, q); res.MatchType != OutcomeGeneratedRequired {
		t.Fatalf("wrong style: want generated_required, got %s", res.MatchType)
	}
	// Wrong state version.
	q = baseQuery(EntityCharacter, "neutral_front_portrait")
	q.StateVersion = 2
	if res := retrieve(t, src, q); res.MatchType != OutcomeGeneratedRequired {
		t.Fatalf("wrong state: want generated_required, got %s", res.MatchType)
	}
}

func TestRetrieveExactStyleProfileVersion(t *testing.T) {
	a := asset("a1", EntityCharacter, "neutral_front_portrait")
	a.StyleProfileVersion = ip(2)
	src := &memSource{assets: []VisualAsset{a}}

	q := baseQuery(EntityCharacter, "neutral_front_portrait")
	q.StyleProfileVersion = ip(2)
	if res := retrieve(t, src, q); res.MatchType != OutcomeExactMatch {
		t.Fatalf("matching style version: want exact_match, got %s", res.MatchType)
	}

	q.StyleProfileVersion = ip(3)
	if res := retrieve(t, src, q); res.MatchType != OutcomeGeneratedRequired {
		t.Fatalf("mismatched style version: want generated_required, got %s", res.MatchType)
	}
}

func TestRetrieveNonReadyNeverReturned(t *testing.T) {
	for _, status := range []string{"pending", "preview_ready", "failed", "archived"} {
		a := asset("a1", EntityCharacter, "neutral_front_portrait")
		a.Status = status
		// A warm candidate that would otherwise be compatible, also non-ready.
		warm := asset("a2", EntityCharacter, "expression_warm")
		warm.Status = status
		src := &memSource{assets: []VisualAsset{a, warm}}

		q := baseQuery(EntityCharacter, "neutral_front_portrait")
		q.FallbackPolicy = FallbackPolicyPreviewAllowed
		if res := retrieve(t, src, q); res.MatchType != OutcomeGeneratedRequired {
			t.Fatalf("status %s: want generated_required, got %s (asset=%+v)", status, res.MatchType, res.Asset)
		}
	}
}

func TestRetrievePolicyNoneExactOnly(t *testing.T) {
	// A warm candidate exists; requesting neutral with policy=none must not
	// return it (no compatible/preview), only generated_required.
	src := &memSource{assets: []VisualAsset{asset("a2", EntityCharacter, "expression_warm")}}
	q := baseQuery(EntityCharacter, "neutral_front_portrait")
	q.FallbackPolicy = FallbackPolicyNone
	if res := retrieve(t, src, q); res.MatchType != OutcomeGeneratedRequired {
		t.Fatalf("policy none: want generated_required, got %s", res.MatchType)
	}

	// But an exact asset is still returned under policy=none.
	src.assets = append(src.assets, asset("a1", EntityCharacter, "neutral_front_portrait"))
	if res := retrieve(t, src, q); res.MatchType != OutcomeExactMatch {
		t.Fatalf("policy none with exact: want exact_match, got %s", res.MatchType)
	}
}

func TestRetrievePolicyCompatibleOnly(t *testing.T) {
	// compatible_only allows a compatible_match but suppresses a preview-only
	// substitute. The genuine preview-only pair in the 5B matrix is a
	// side_profile candidate for a front request (compatibility.go characterAngle).

	// Case A: requesting warm, a smiling (same warm family) candidate exists →
	// compatible_match allowed.
	src := &memSource{assets: []VisualAsset{asset("a2", EntityCharacter, "expression_smiling")}}
	q := baseQuery(EntityCharacter, "expression_warm")
	q.FallbackPolicy = FallbackPolicyCompatibleOnly
	if res := retrieve(t, src, q); res.MatchType != OutcomeCompatibleMatch {
		t.Fatalf("compatible_only warm↔smiling: want compatible_match, got %s", res.MatchType)
	}

	// Case B: requesting neutral_front, only a side_profile candidate
	// (preview-only per the matrix) → suppressed under compatible_only.
	src = &memSource{assets: []VisualAsset{asset("a1", EntityCharacter, "side_angle_portrait")}}
	q = baseQuery(EntityCharacter, "neutral_front_portrait")
	q.FallbackPolicy = FallbackPolicyCompatibleOnly
	if res := retrieve(t, src, q); res.MatchType != OutcomeGeneratedRequired {
		t.Fatalf("compatible_only front←side preview: want generated_required, got %s", res.MatchType)
	}
}

func TestRetrievePolicyPreviewAllowed(t *testing.T) {
	// requesting neutral_front, only a side_profile candidate → preview_fallback
	// under preview_allowed (matrix characterAngle: side_profile is preview).
	src := &memSource{assets: []VisualAsset{asset("a1", EntityCharacter, "side_angle_portrait")}}
	q := baseQuery(EntityCharacter, "neutral_front_portrait")
	q.FallbackPolicy = FallbackPolicyPreviewAllowed
	res := retrieve(t, src, q)
	if res.MatchType != OutcomePreviewFallback {
		t.Fatalf("preview_allowed front←side: want preview_fallback, got %s", res.MatchType)
	}
	if res.Asset == nil || res.Asset.ID != "a1" {
		t.Fatalf("want asset a1, got %+v", res.Asset)
	}
	if !res.GenerationRecommended {
		t.Fatal("preview fallback should recommend generation")
	}
}

func TestRetrievePolicyAnyExisting(t *testing.T) {
	// requesting an angry (strong-emotion, strict) variant. A neutral_front
	// candidate exists but the matrix says invalid for strong emotion. Under
	// any_existing the best existing ready candidate is still returned
	// (debug), labelled preview_fallback, deterministically.
	neutral := asset("a1", EntityCharacter, "neutral_front_portrait")
	warm := asset("a2", EntityCharacter, "expression_warm")
	src := &memSource{assets: []VisualAsset{neutral, warm}}
	q := baseQuery(EntityCharacter, "angry_expression")
	q.FallbackPolicy = FallbackPolicyAnyExisting
	res := retrieve(t, src, q)
	if res.MatchType != OutcomePreviewFallback {
		t.Fatalf("any_existing: want preview_fallback, got %s", res.MatchType)
	}
	if res.Asset == nil {
		t.Fatal("any_existing: expected an asset")
	}

	// Never returns a failed/archived asset even under any_existing.
	for i := range src.assets {
		src.assets[i].Status = "archived"
	}
	if res := retrieve(t, src, q); res.MatchType != OutcomeGeneratedRequired {
		t.Fatalf("any_existing archived: want generated_required, got %s", res.MatchType)
	}
}

func TestRetrieveNeutralWarmCompatibleFromMatrix(t *testing.T) {
	// neutral_front candidate, requesting generic presence via a different
	// neutral key (neutral_three_quarter) → compatible_match (front↔3q).
	src := &memSource{assets: []VisualAsset{asset("a1", EntityCharacter, "neutral_front_portrait")}}
	q := baseQuery(EntityCharacter, "neutral_three_quarter_portrait")
	res := retrieve(t, src, q)
	if res.MatchType != OutcomeCompatibleMatch {
		t.Fatalf("neutral front↔3q: want compatible_match, got %s", res.MatchType)
	}
	if res.FallbackReason == "" {
		t.Fatal("compatible match should carry a fallback_reason")
	}
}

func TestRetrieveDayNightStrict(t *testing.T) {
	// place: requesting night_view with only a day_view candidate. day↔night is
	// invalid for compatible/preview-only-via-policy; under compatible_only it
	// must be generated_required.
	src := &memSource{assets: []VisualAsset{asset("p1", EntityPlace, "day_view")}}
	q := baseQuery(EntityPlace, "night_view")
	q.FallbackPolicy = FallbackPolicyCompatibleOnly
	if res := retrieve(t, src, q); res.MatchType != OutcomeGeneratedRequired {
		t.Fatalf("day→night compatible_only: want generated_required, got %s", res.MatchType)
	}
	// Even preview_allowed: night_view is strict (fallback_allowed=false) and
	// day's matrix outcome vs night is invalid → still generated_required.
	q.FallbackPolicy = FallbackPolicyPreviewAllowed
	if res := retrieve(t, src, q); res.MatchType != OutcomeGeneratedRequired {
		t.Fatalf("day→night preview_allowed: want generated_required, got %s", res.MatchType)
	}
}

func TestRetrieveUnknownVariantExactOnly(t *testing.T) {
	// An unknown requested variant only matches on an exact key, never
	// compatible. Seed a stored asset with the same unknown key → exact.
	unknown := asset("a1", EntityCharacter, "totally_made_up_variant")
	src := &memSource{assets: []VisualAsset{unknown}}
	q := baseQuery(EntityCharacter, "totally_made_up_variant")
	q.FallbackPolicy = FallbackPolicyPreviewAllowed
	if res := retrieve(t, src, q); res.MatchType != OutcomeExactMatch {
		t.Fatalf("unknown exact key: want exact_match, got %s", res.MatchType)
	}

	// A neutral candidate must not compatible-match an unknown request.
	src = &memSource{assets: []VisualAsset{asset("a2", EntityCharacter, "neutral_front_portrait")}}
	if res := retrieve(t, src, q); res.MatchType != OutcomeGeneratedRequired {
		t.Fatalf("unknown vs neutral: want generated_required, got %s", res.MatchType)
	}
}

func TestRetrieveAnchorsNotCompatibleSubstitute(t *testing.T) {
	// An identity-anchor neutral_front must not be returned as a compatible
	// substitute for a non-anchor neutral request.
	anchor := asset("a1", EntityCharacter, "neutral_front_portrait")
	anchor.IsIdentityAnchor = true
	src := &memSource{assets: []VisualAsset{anchor}}
	q := baseQuery(EntityCharacter, "neutral_three_quarter_portrait")
	q.FallbackPolicy = FallbackPolicyPreviewAllowed
	if res := retrieve(t, src, q); res.MatchType != OutcomeGeneratedRequired {
		t.Fatalf("anchor substitute: want generated_required, got %s", res.MatchType)
	}
}

func TestRetrieveDeterministicOrdering(t *testing.T) {
	// Three neutral candidates qualify as compatible for a 3q request. Winner
	// must be deterministic: same family/outcome/score → lowest fallback_rank,
	// then lowest id. Insert in shuffled order; expect the front portrait
	// (fallbackRankPrimary, id a1) to win over bust (tertiary) and a later id.
	front := asset("a1", EntityCharacter, "neutral_front_portrait")    // rank primary(10)
	bust := asset("a3", EntityCharacter, "neutral_bust")               // rank tertiary(30)
	frontDup := asset("a2", EntityCharacter, "neutral_front_portrait") // rank primary(10), higher id
	src := &memSource{assets: []VisualAsset{bust, frontDup, front}}
	q := baseQuery(EntityCharacter, "neutral_three_quarter_portrait")

	res := retrieve(t, src, q)
	if res.MatchType != OutcomeCompatibleMatch {
		t.Fatalf("want compatible_match, got %s", res.MatchType)
	}
	if res.Asset == nil || res.Asset.ID != "a1" {
		t.Fatalf("deterministic winner should be a1 (lowest rank, lowest id), got %+v", res.Asset)
	}

	// Stability: repeated runs return the same winner.
	for i := 0; i < 5; i++ {
		if got := retrieve(t, src, q); got.Asset == nil || got.Asset.ID != "a1" {
			t.Fatalf("run %d: unstable winner %+v", i, got.Asset)
		}
	}
}

func TestRetrieveInvalidFallbackPolicy(t *testing.T) {
	src := &memSource{}
	q := baseQuery(EntityCharacter, "neutral_front_portrait")
	q.FallbackPolicy = "bogus"
	_, err := NewRetriever(src).Retrieve(context.Background(), q)
	if err == nil {
		t.Fatal("want error for invalid fallback_policy")
	}
	var bad ErrInvalidFallbackPolicy
	if !asErr(err, &bad) {
		t.Fatalf("want ErrInvalidFallbackPolicy, got %T", err)
	}
}

// asErr is a tiny errors.As wrapper kept local to avoid importing errors in
// every assertion.
func asErr(err error, target *ErrInvalidFallbackPolicy) bool {
	e, ok := err.(ErrInvalidFallbackPolicy)
	if ok {
		*target = e
	}
	return ok
}
