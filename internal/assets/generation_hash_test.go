package assets

import "testing"

func baseGenerationInput() GenerationHashInput {
	return GenerationHashInput{
		TenantID:       "tenant_a",
		IdentityID:     "vi_hero",
		DisplayName:    "Captain Mira",
		StyleProfileID: "sty_captain",
		Intent:         "commit",
	}
}

func TestGenerationRenderHashIsDeterministic(t *testing.T) {
	a := GenerationRenderHash(baseGenerationInput())
	b := GenerationRenderHash(baseGenerationInput())
	if a != b {
		t.Fatalf("same input produced different hashes: %s vs %s", a, b)
	}
}

// Every render-determining field must change the key: a collision across any
// of these would silently serve the wrong cached image.
func TestGenerationRenderHashChangesPerField(t *testing.T) {
	base := GenerationRenderHash(baseGenerationInput())
	mutations := map[string]func(*GenerationHashInput){
		"tenant":        func(in *GenerationHashInput) { in.TenantID = "tenant_b" },
		"identity":      func(in *GenerationHashInput) { in.IdentityID = "vi_other" },
		"display name":  func(in *GenerationHashInput) { in.DisplayName = "Commander Mira" },
		"style profile": func(in *GenerationHashInput) { in.StyleProfileID = "sty_commander" },
		"anchor":        func(in *GenerationHashInput) { in.AnchorAssetID = "va_anchor" },
		"identity anchors": func(in *GenerationHashInput) {
			in.IdentityAnchorAssetIDs = []string{"va_anchor_1"}
		},
		"derive from":    func(in *GenerationHashInput) { in.DeriveFrom = "va_source" },
		"intent":         func(in *GenerationHashInput) { in.Intent = "draft" },
		"max_megapixels": func(in *GenerationHashInput) { in.MaxMegapixels = 3.5 },
		"transform":      func(in *GenerationHashInput) { in.TransformJSON = `{"schema_version":"1"}` },
	}
	for name, mutate := range mutations {
		in := baseGenerationInput()
		mutate(&in)
		if got := GenerationRenderHash(in); got == base {
			t.Fatalf("%s change did not change the hash", name)
		}
	}
}

// Field-boundary ambiguity: shifting content between adjacent fields must not
// collide (labels + delimiter prevent "ab"+"c" == "a"+"bc").
func TestGenerationRenderHashFieldBoundaries(t *testing.T) {
	a := baseGenerationInput()
	a.IdentityID, a.DisplayName = "vi_ab", "c"
	b := baseGenerationInput()
	b.IdentityID, b.DisplayName = "vi_a", "bc"
	if GenerationRenderHash(a) == GenerationRenderHash(b) {
		t.Fatal("field boundary shift collided")
	}
}

// The key quantizes max_megapixels to what the column stores, NUMERIC(6, 2)
// (migrations/0013_cost_routing.sql). Two requests the store cannot tell apart
// must share one key — otherwise the second one pays for an identical render —
// while a difference the store DOES keep must still split the key.
func TestGenerationRenderHashQuantizesMegapixelsToStoredPrecision(t *testing.T) {
	hashFor := func(mp float64) string {
		in := baseGenerationInput()
		in.MaxMegapixels = mp
		return GenerationRenderHash(in)
	}

	// A float32-widened 2.1 and a literal 2.1 both persist as 2.10.
	if got, want := hashFor(float64(float32(2.1))), hashFor(2.1); got != want {
		t.Fatalf("2.0999999046325684 and 2.1 both store as 2.10 and must share one key, got %s vs %s", got, want)
	}
	if got, want := hashFor(1.0000001), hashFor(1.0000002); got != want {
		t.Fatalf("budgets differing past the stored precision must share one key, got %s vs %s", got, want)
	}
	// Differences the column keeps still split the key.
	if hashFor(2.1) == hashFor(2.15) {
		t.Fatal("2.10 and 2.15 are distinct stored budgets and must not collide")
	}
	if hashFor(2.1) == hashFor(4.0) {
		t.Fatal("distinct megapixel budgets collided in render hash")
	}
	// The <= 0 default is unchanged: an unset budget hashes as the handler's 4.0.
	if hashFor(0) != hashFor(4.0) {
		t.Fatal("an unset budget must hash as the effective 4.0 default")
	}
}

// Replacing a character's reference images changes what it looks like, because
// the worker conditions the render on the identity's current anchor set. If the
// key ignored them, the reuse path would keep serving the previous appearance.
func TestGenerationRenderHashTracksIdentityAnchorSet(t *testing.T) {
	one := baseGenerationInput()
	one.IdentityAnchorAssetIDs = []string{"va_a"}
	two := baseGenerationInput()
	two.IdentityAnchorAssetIDs = []string{"va_b"}
	if GenerationRenderHash(one) == GenerationRenderHash(two) {
		t.Fatal("swapping the identity's anchor asset must change the render hash")
	}

	added := baseGenerationInput()
	added.IdentityAnchorAssetIDs = []string{"va_a", "va_b"}
	if GenerationRenderHash(one) == GenerationRenderHash(added) {
		t.Fatal("adding an anchor must change the render hash")
	}

	// Order is passed through to the provider as an ordered reference list, so a
	// reordering may render differently and must not silently reuse.
	reordered := baseGenerationInput()
	reordered.IdentityAnchorAssetIDs = []string{"va_b", "va_a"}
	if GenerationRenderHash(added) == GenerationRenderHash(reordered) {
		t.Fatal("reordering anchors must change the render hash")
	}

	// An empty set and a nil set are the same absence of anchors.
	empty := baseGenerationInput()
	empty.IdentityAnchorAssetIDs = []string{}
	if GenerationRenderHash(empty) != GenerationRenderHash(baseGenerationInput()) {
		t.Fatal("nil and empty anchor sets must hash identically")
	}
}

// Anchor ids are joined into one field, so the join must not let a boundary
// shift collide (["a","b"] vs ["a,b"]).
func TestGenerationRenderHashAnchorJoinIsUnambiguous(t *testing.T) {
	split := baseGenerationInput()
	split.IdentityAnchorAssetIDs = []string{"va_a", "va_b"}
	joined := baseGenerationInput()
	joined.IdentityAnchorAssetIDs = []string{"va_a,va_b"}
	if GenerationRenderHash(split) == GenerationRenderHash(joined) {
		t.Fatal("anchor list boundaries must not collide with a comma in an id")
	}
}
