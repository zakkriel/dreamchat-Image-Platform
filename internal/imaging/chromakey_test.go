package imaging

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"testing"
)

// magentaBacked paints a flat key-coloured backdrop with an opaque subject
// rectangle in the middle.
func magentaBacked(w, h int, subject image.Rectangle, subjectColor color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			c := color.RGBA(DefaultChromaKey)
			if image.Pt(x, y).In(subject) {
				c = subjectColor
			}
			img.Set(x, y, c)
		}
	}
	return img
}

func TestChromaKeyRemovesFlatBackdropAndKeepsSubject(t *testing.T) {
	subject := image.Rect(10, 10, 30, 30)
	src := magentaBacked(40, 40, subject, color.RGBA{R: 20, G: 140, B: 90, A: 255})

	out, report, err := ChromaKey(src, DefaultChromaKeyOptions())
	if err != nil {
		t.Fatalf("key: %v (report %+v)", err, report)
	}
	if _, _, _, a := out.At(1, 1).RGBA(); a != 0 {
		t.Fatalf("backdrop corner must be fully transparent, got alpha %d", a>>8)
	}
	if _, _, _, a := out.At(20, 20).RGBA(); a>>8 != 255 {
		t.Fatalf("subject centre must stay opaque, got alpha %d", a>>8)
	}
	// The subject must survive intact, not just be opaque.
	r, g, b, _ := out.At(20, 20).RGBA()
	if r>>8 != 20 || g>>8 != 140 || b>>8 != 90 {
		t.Fatalf("subject colour altered: got %d,%d,%d", r>>8, g>>8, b>>8)
	}
	if report.BorderCoverage != 1 {
		t.Fatalf("expected a fully keyed border, got %.3f", report.BorderCoverage)
	}
	wantTransparent := 40*40 - subject.Dx()*subject.Dy()
	if report.TransparentPixels != wantTransparent {
		t.Fatalf("expected %d transparent pixels, got %d", wantTransparent, report.TransparentPixels)
	}
}

// The failure mode that matters most: a character with magenta hair. Keying
// must NOT punch a hole through the subject, and must tell the caller the key
// is unreliable so it can fall back.
func TestChromaKeyRefusesWhenSubjectContainsKeyColour(t *testing.T) {
	subject := image.Rect(10, 10, 30, 30)
	src := magentaBacked(40, 40, subject, color.RGBA{R: 20, G: 140, B: 90, A: 255})
	// A magenta patch well inside the subject — pink hair, a pink badge.
	for y := 16; y < 24; y++ {
		for x := 16; x < 24; x++ {
			src.Set(x, y, color.RGBA(DefaultChromaKey))
		}
	}

	_, report, err := ChromaKey(src, DefaultChromaKeyOptions())
	if !errors.Is(err, ErrSubjectContainsKey) {
		t.Fatalf("expected ErrSubjectContainsKey, got %v (report %+v)", err, report)
	}
	if report.InteriorKeyFraction <= 0 {
		t.Fatal("interior key collision must be reported, not silently zero")
	}
}

// An enclosed key-coloured region small enough to pass the threshold must still
// be filled in rather than punched through — the silhouette is what matters.
func TestChromaKeyNeverPunchesEnclosedRegions(t *testing.T) {
	subject := image.Rect(5, 5, 45, 45)
	src := magentaBacked(50, 50, subject, color.RGBA{R: 30, G: 120, B: 200, A: 255})
	// One tiny magenta speck inside a large subject: below the 2% threshold.
	src.Set(25, 25, color.RGBA(DefaultChromaKey))

	out, report, err := ChromaKey(src, DefaultChromaKeyOptions())
	if err != nil {
		t.Fatalf("small speck should not fail the key: %v (report %+v)", err, report)
	}
	if _, _, _, a := out.At(25, 25).RGBA(); a>>8 != 255 {
		t.Fatalf("an enclosed key pixel must be restored to opaque, got alpha %d", a>>8)
	}
}

// If the model ignored the backdrop instruction there is nothing to key, and
// keying anyway would erase whatever happened to be near the key hue.
func TestChromaKeyRefusesWhenBackdropIsMissing(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 30, 30))
	for y := range 30 {
		for x := range 30 {
			src.Set(x, y, color.RGBA{R: 90, G: 110, B: 130, A: 255})
		}
	}
	_, report, err := ChromaKey(src, DefaultChromaKeyOptions())
	if !errors.Is(err, ErrBackdropNotFound) {
		t.Fatalf("expected ErrBackdropNotFound, got %v (report %+v)", err, report)
	}
	if report.BorderCoverage != 0 {
		t.Fatalf("expected zero border coverage, got %.3f", report.BorderCoverage)
	}
}

// Anti-aliased edges are the whole reason for a soft matte: a blend of subject
// and backdrop must become partially transparent, not snap to one or the other.
func TestChromaKeyProducesSoftAlphaOnBlendedEdges(t *testing.T) {
	subject := image.Rect(10, 10, 30, 30)
	src := magentaBacked(40, 40, subject, color.RGBA{R: 20, G: 140, B: 90, A: 255})
	// A mostly-backdrop edge blend (~75% key), which is where the ramp lives.
	// A 50/50 blend sits ~93 chroma units out - deliberately outside the ramp,
	// because real subject colours live at 79-88 and would be eaten with it.
	blend := color.RGBA{R: 196, G: 35, B: 214, A: 255}
	for y := 10; y < 30; y++ {
		src.Set(9, y, blend)
	}

	out, report, err := ChromaKey(src, DefaultChromaKeyOptions())
	if err != nil {
		t.Fatalf("key: %v (report %+v)", err, report)
	}
	_, _, _, a := out.At(9, 20).RGBA()
	if a>>8 == 0 || a>>8 == 255 {
		t.Fatalf("a blended edge pixel must be partially transparent, got alpha %d", a>>8)
	}
	if report.PartialPixels == 0 {
		t.Fatal("expected the soft matte to be reported")
	}
}

// Despill is the difference between a clean cutout and a magenta-rimmed one.
func TestChromaKeyDespillsEdgeContamination(t *testing.T) {
	subject := image.Rect(10, 10, 30, 30)
	src := magentaBacked(40, 40, subject, color.RGBA{R: 20, G: 140, B: 90, A: 255})
	contaminated := color.RGBA{R: 196, G: 35, B: 214, A: 255}
	for y := 10; y < 30; y++ {
		src.Set(9, y, contaminated)
	}

	withDespill := DefaultChromaKeyOptions()
	out, _, err := ChromaKey(src, withDespill)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	noDespill := DefaultChromaKeyOptions()
	noDespill.DespillStrength = 0
	raw, _, err := ChromaKey(src, noDespill)
	if err != nil {
		t.Fatalf("key without despill: %v", err)
	}

	// Magenta is high red + high blue against low green. Despill must reduce
	// that excess.
	dr, dg, db, _ := out.At(9, 20).RGBA()
	rr, rg, rb, _ := raw.At(9, 20).RGBA()
	despilled := int(dr>>8) + int(db>>8) - 2*int(dg>>8)
	original := int(rr>>8) + int(rb>>8) - 2*int(rg>>8)
	if despilled >= original {
		t.Fatalf("despill did not reduce key contamination: %d vs %d", despilled, original)
	}
}

// Despill must not tint colours that oppose the key: a green pixel carries no
// magenta spill, and pulling it further from magenta would shift its hue.
func TestChromaKeyDespillLeavesOpposingHuesAlone(t *testing.T) {
	green := color.RGBA{R: 20, G: 200, B: 40, A: 255}
	r, g, b := despill(green.R, green.G, green.B, 1, 0, 1)
	if r != green.R || g != green.G || b != green.B {
		t.Fatalf("a hue opposing the key must be untouched, got %d,%d,%d", r, g, b)
	}
}

// A keyed sprite has to survive the tier pipeline with its alpha intact,
// otherwise the transparency never reaches storage.
func TestChromaKeyOutputSurvivesEncodeTiers(t *testing.T) {
	subject := image.Rect(100, 100, 700, 700)
	src := magentaBacked(800, 800, subject, color.RGBA{R: 20, G: 140, B: 90, A: 255})
	keyed, _, err := ChromaKey(src, DefaultChromaKeyOptions())
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	png, err := encodePNG(keyed)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	tiers, err := EncodeTiers(png)
	if err != nil {
		t.Fatalf("tiers: %v", err)
	}
	for name, blob := range map[string][]byte{"final": tiers.Final, "preview": tiers.Preview, "thumb": tiers.Thumb} {
		img, _, decErr := image.Decode(bytes.NewReader(blob))
		if decErr != nil {
			t.Fatalf("%s decode: %v", name, decErr)
		}
		if _, _, _, a := img.At(1, 1).RGBA(); a != 0 {
			t.Fatalf("%s tier lost its transparent corner: alpha %d", name, a>>8)
		}
	}
}

// The measured limit, pinned as a test: saturated character colours must stay
// FULLY opaque under the default key. Pink hair (~82 chroma units from magenta)
// and purple cloth (~79) are the closest realistic cases, and eating either
// would silently deform a silhouette.
func TestChromaKeyLeavesSaturatedSubjectColoursOpaque(t *testing.T) {
	risky := map[string]color.RGBA{
		"pink hair":    {R: 255, G: 105, B: 180, A: 255},
		"purple cloth": {R: 150, G: 60, B: 200, A: 255},
		"red lips":     {R: 200, G: 40, B: 60, A: 255},
		"light skin":   {R: 255, G: 200, B: 200, A: 255},
		"sky blue":     {R: 110, G: 170, B: 230, A: 255},
		"white":        {R: 250, G: 250, B: 250, A: 255},
	}
	for name, c := range risky {
		subject := image.Rect(10, 10, 30, 30)
		src := magentaBacked(40, 40, subject, c)
		out, report, err := ChromaKey(src, DefaultChromaKeyOptions())
		if err != nil {
			// Colours inside the danger band must be REFUSED, never silently
			// keyed - a fallback is correct, a deformed sprite is not.
			if errors.Is(err, ErrSubjectNearKey) {
				continue
			}
			t.Fatalf("%s: unexpected error %v (report %+v)", name, err, report)
		}
		if _, _, _, a := out.At(20, 20).RGBA(); a>>8 != 255 {
			t.Fatalf("%s: subject must stay fully opaque, got alpha %d", name, a>>8)
		}
	}
}

// A character whose colours crowd the key must be refused rather than keyed on
// a silhouette that cannot be trusted.
func TestChromaKeyRefusesSubjectCrowdingTheKey(t *testing.T) {
	subject := image.Rect(5, 5, 35, 35)
	// Purple: ~79 chroma units from magenta, inside the danger band.
	src := magentaBacked(40, 40, subject, color.RGBA{R: 150, G: 60, B: 200, A: 255})
	_, report, err := ChromaKey(src, DefaultChromaKeyOptions())
	if !errors.Is(err, ErrSubjectNearKey) {
		t.Fatalf("expected ErrSubjectNearKey, got %v (report %+v)", err, report)
	}
	if report.SubjectNearKeyFraction <= 0 {
		t.Fatal("subject proximity must be reported so a fallback can explain itself")
	}
}

// The rim case. With a narrow ramp most silhouette pixels land FULLY OPAQUE
// while still carrying backdrop colour, which shows as a thin key-coloured
// outline. Matte-bound despill never touches them, so despill has to extend to
// the band around the cutout.
func TestChromaKeyDespillsOpaqueRimPixels(t *testing.T) {
	subject := image.Rect(10, 10, 30, 30)
	src := magentaBacked(40, 40, subject, color.RGBA{R: 20, G: 140, B: 90, A: 255})
	// A rim pixel contaminated toward magenta but far enough from the key to
	// stay fully opaque - exactly the pixel that produced the visible rim.
	rim := color.RGBA{R: 150, G: 120, B: 160, A: 255}
	for y := 10; y < 30; y++ {
		src.Set(10, y, rim)
	}

	banded := DefaultChromaKeyOptions()
	out, _, err := ChromaKey(src, banded)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	noBand := DefaultChromaKeyOptions()
	noBand.EdgeDespillRadius = 0
	raw, _, err := ChromaKey(src, noBand)
	if err != nil {
		t.Fatalf("key without the band: %v", err)
	}

	if _, _, _, a := out.At(10, 20).RGBA(); a>>8 != 255 {
		t.Fatalf("the rim pixel must stay opaque, got alpha %d", a>>8)
	}
	dr, dg, db, _ := out.At(10, 20).RGBA()
	rr, rg, rb, _ := raw.At(10, 20).RGBA()
	despilled := int(dr>>8) + int(db>>8) - 2*int(dg>>8)
	original := int(rr>>8) + int(rb>>8) - 2*int(rg>>8)
	if despilled >= original {
		t.Fatalf("edge despill did not reduce rim contamination: %d vs %d", despilled, original)
	}

	// The band must stay a band: a pixel deep inside the subject is untouched.
	di := out.At(20, 20)
	ri := raw.At(20, 20)
	if di != ri {
		t.Fatalf("despill leaked into the subject interior: %v vs %v", di, ri)
	}
}
