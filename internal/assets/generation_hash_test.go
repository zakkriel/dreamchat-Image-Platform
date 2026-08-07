package assets

import "testing"

func baseGenerationInput() GenerationHashInput {
	return GenerationHashInput{
		TenantID:    "tenant_a",
		IdentityID:  "vi_hero",
		DisplayName: "Captain Mira",
		Intent:      "commit",
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
		"tenant":       func(in *GenerationHashInput) { in.TenantID = "tenant_b" },
		"identity":     func(in *GenerationHashInput) { in.IdentityID = "vi_other" },
		"display name": func(in *GenerationHashInput) { in.DisplayName = "Commander Mira" },
		"anchor":       func(in *GenerationHashInput) { in.AnchorAssetID = "va_anchor" },
		"derive from":  func(in *GenerationHashInput) { in.DeriveFrom = "va_source" },
		"intent":       func(in *GenerationHashInput) { in.Intent = "draft" },
		"transform":    func(in *GenerationHashInput) { in.TransformJSON = `{"schema_version":"1"}` },
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
