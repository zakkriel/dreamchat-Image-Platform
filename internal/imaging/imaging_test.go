package imaging

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// pngOf encodes a solid-ish w×h PNG for tier tests.
func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 0x7f, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode source: %v", err)
	}
	return buf.Bytes()
}

func dimsOf(t *testing.T, b []byte) (int, int) {
	t.Helper()
	cfg, err := png.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("decode tier png: %v", err)
	}
	return cfg.Width, cfg.Height
}

// A large source must yield three genuinely distinct sizes, ordered
// thumb < preview < final (PRD 06 §4).
func TestEncodeTiersDistinctSizesForLargeSource(t *testing.T) {
	src := pngOf(t, 1024, 1024)
	tiers, err := EncodeTiers(src)
	if err != nil {
		t.Fatalf("EncodeTiers: %v", err)
	}
	fw, fh := dimsOf(t, tiers.Final)
	pw, ph := dimsOf(t, tiers.Preview)
	tw, th := dimsOf(t, tiers.Thumb)

	if fw != 1024 || fh != 1024 {
		t.Fatalf("final must be full size 1024x1024, got %dx%d", fw, fh)
	}
	if pw != PreviewShortEdge {
		t.Fatalf("preview short edge must be %d, got %d", PreviewShortEdge, pw)
	}
	if tw != ThumbnailShortEdge {
		t.Fatalf("thumbnail short edge must be %d, got %d", ThumbnailShortEdge, tw)
	}
	// thumb < preview <= final
	if tw >= pw || pw > fw {
		t.Fatalf("expected thumb(%d) < preview(%d) <= final(%d)", tw, pw, fw)
	}
	if th >= ph || ph > fh {
		t.Fatalf("expected thumb(%d) < preview(%d) <= final(%d) on height", th, ph, fh)
	}
}

// Aspect ratio is preserved: a non-square source keeps its ratio after the
// short edge is pinned to the tier target.
func TestEncodeTiersPreservesAspect(t *testing.T) {
	src := pngOf(t, 2000, 1000) // short edge = 1000 (height)
	tiers, err := EncodeTiers(src)
	if err != nil {
		t.Fatalf("EncodeTiers: %v", err)
	}
	pw, ph := dimsOf(t, tiers.Preview)
	if ph != PreviewShortEdge {
		t.Fatalf("preview short edge (height) must be %d, got %d", PreviewShortEdge, ph)
	}
	if pw != 2*ph {
		t.Fatalf("preview must preserve 2:1 aspect, got %dx%d", pw, ph)
	}
}

// A source smaller than every tier target is never upscaled: all three tiers
// equal the source dimensions.
func TestEncodeTiersNeverUpscales(t *testing.T) {
	src := pngOf(t, 200, 200)
	tiers, err := EncodeTiers(src)
	if err != nil {
		t.Fatalf("EncodeTiers: %v", err)
	}
	for name, b := range map[string][]byte{"final": tiers.Final, "preview": tiers.Preview, "thumb": tiers.Thumb} {
		w, h := dimsOf(t, b)
		if w != 200 || h != 200 {
			t.Fatalf("%s must not upscale a 200x200 source, got %dx%d", name, w, h)
		}
	}
}

// Determinism: identical input bytes produce byte-for-byte identical tiers.
func TestEncodeTiersDeterministic(t *testing.T) {
	src := pngOf(t, 1024, 768)
	a, err := EncodeTiers(src)
	if err != nil {
		t.Fatalf("EncodeTiers a: %v", err)
	}
	b, err := EncodeTiers(src)
	if err != nil {
		t.Fatalf("EncodeTiers b: %v", err)
	}
	if !bytes.Equal(a.Final, b.Final) || !bytes.Equal(a.Preview, b.Preview) || !bytes.Equal(a.Thumb, b.Thumb) {
		t.Fatal("tier encodings must be deterministic for identical input")
	}
}

func TestEncodeTiersRejectsNonPNG(t *testing.T) {
	if _, err := EncodeTiers([]byte{0x1, 0x2, 0x3}); err == nil {
		t.Fatal("expected an error decoding non-PNG bytes")
	}
}
