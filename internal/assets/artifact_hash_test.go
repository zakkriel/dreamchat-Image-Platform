package assets

import "testing"

func baseArtifactInput() ArtifactHashInput {
	return ArtifactHashInput{
		TenantID:       "tenant_a",
		WorldID:        "w1",
		ArtifactID:     "art_bronze_key",
		Description:    "A bronze key",
		StyleProfileID: "sty_ok",
		QualityTier:    "standard",
	}
}

func TestArtifactRenderHashIsDeterministic(t *testing.T) {
	a := ArtifactRenderHash(baseArtifactInput())
	b := ArtifactRenderHash(baseArtifactInput())
	if a != b {
		t.Fatalf("same input produced different hashes: %s vs %s", a, b)
	}
	if len(a) != 64 {
		t.Fatalf("expected a 64-char sha256 hex hash, got %d chars: %q", len(a), a)
	}
}

func TestArtifactRenderHashVariantKeyDefaults(t *testing.T) {
	in := baseArtifactInput()
	withEmpty := ArtifactRenderHash(in)
	in.VariantKey = "default"
	withExplicit := ArtifactRenderHash(in)
	if withEmpty != withExplicit {
		t.Fatalf("empty variant_key must default to 'default': %s vs %s", withEmpty, withExplicit)
	}
}

func TestArtifactRenderHashDescriptionNormalizationIsDeterministic(t *testing.T) {
	in := baseArtifactInput()
	in.Description = "A bronze key"
	canonical := ArtifactRenderHash(in)

	for _, variant := range []string{
		"  A bronze key  ",
		"A   bronze   key",
		"A bronze\tkey",
		"A bronze\nkey",
		"\nA bronze key\n",
	} {
		in.Description = variant
		if got := ArtifactRenderHash(in); got != canonical {
			t.Fatalf("description %q should normalize to the canonical hash; got %s want %s", variant, got, canonical)
		}
	}

	// Case is preserved — a different case is a different prompt.
	in.Description = "a bronze key"
	if ArtifactRenderHash(in) == canonical {
		t.Fatalf("case differences must change the hash")
	}
}

func TestArtifactRenderHashDifferentArtifactIDDiffers(t *testing.T) {
	in := baseArtifactInput()
	a := ArtifactRenderHash(in)
	in.ArtifactID = "art_silver_key"
	if b := ArtifactRenderHash(in); a == b {
		t.Fatalf("different artifact_id must produce a different hash (cache-key collision risk)")
	}
}

func TestArtifactRenderHashDifferentStyleProfileDiffers(t *testing.T) {
	in := baseArtifactInput()
	a := ArtifactRenderHash(in)
	in.StyleProfileID = "sty_other"
	if b := ArtifactRenderHash(in); a == b {
		t.Fatalf("different style_profile_id must produce a different hash")
	}
}

func TestArtifactRenderHashDifferentQualityTierDiffers(t *testing.T) {
	in := baseArtifactInput()
	a := ArtifactRenderHash(in)
	in.QualityTier = "high"
	if b := ArtifactRenderHash(in); a == b {
		t.Fatalf("different quality_tier must produce a different hash")
	}
}

func TestArtifactRenderHashStyleProfileVersionParticipatesWhenPresent(t *testing.T) {
	in := baseArtifactInput()
	noVersion := ArtifactRenderHash(in)

	v1 := int32(1)
	in.StyleProfileVersion = &v1
	withV1 := ArtifactRenderHash(in)
	if withV1 == noVersion {
		t.Fatalf("a present style_profile_version must change the hash vs. absent")
	}

	v2 := int32(2)
	in.StyleProfileVersion = &v2
	withV2 := ArtifactRenderHash(in)
	if withV2 == withV1 {
		t.Fatalf("different style_profile_version values must produce different hashes")
	}

	// Re-supplying the same version reproduces the same hash (determinism).
	in.StyleProfileVersion = &v1
	if again := ArtifactRenderHash(in); again != withV1 {
		t.Fatalf("same style_profile_version must reproduce the same hash")
	}
}
