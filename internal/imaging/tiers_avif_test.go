package imaging

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
	"testing"
)

// encodePNGForTest builds provider-shaped input bytes. Providers emit PNG or
// JPEG (fal and BFL expose no other output_format), so PNG is what the tier
// encoder is fed in production too.
func encodePNGForTest(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// checkerboard is deliberately high-frequency: flat colour hides encoder
// damage, alternating pixels do not.
func checkerboard(w, h, cell int, a, b color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			c := a
			if ((x/cell)+(y/cell))%2 == 0 {
				c = b
			}
			img.Set(x, y, c)
		}
	}
	return img
}

// spriteWithHole models what a matte actually produces: an opaque body, a SOFT
// alpha ramp at its border, and thin strands reaching into the transparent
// field — the hair case. Fine structure next to transparency is what makes a
// lossy alpha channel bleed; a plain disc on a clean field does not, which is
// why an earlier version of this fixture passed even with lossy alpha and
// therefore guarded nothing.
func spriteWithHole(size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	r := size / 3
	cx, cy := size/2, size/2
	body := color.NRGBA{R: 200, G: 40, B: 90, A: 255}
	for y := range size {
		for x := range size {
			dx, dy := x-cx, y-cy
			d2 := float64(dx*dx + dy*dy)
			rf := float64(r)
			switch {
			case d2 <= rf*rf:
				img.Set(x, y, body)
			case d2 <= (rf+6)*(rf+6):
				// Soft ramp, like a matted edge.
				t := 1 - (math.Sqrt(d2)-rf)/6
				img.Set(x, y, color.NRGBA{R: body.R, G: body.G, B: body.B, A: uint8(255 * t)})
			default:
				img.Set(x, y, color.NRGBA{})
			}
		}
	}
	// Thin strands: one-pixel spokes over transparency, the structure that makes
	// alpha compression bleed into pixels that must stay fully transparent.
	for i := range 24 {
		ang := float64(i) * math.Pi / 12
		for step := r; step < size/2-2; step++ {
			x := cx + int(float64(step)*math.Cos(ang))
			y := cy + int(float64(step)*math.Sin(ang))
			if x < 0 || y < 0 || x >= size || y >= size {
				break
			}
			img.Set(x, y, body)
		}
	}
	return img
}

// The tiers are AVIF now, not PNG. This pins the format at the bytes — a
// consumer reads Content-Type from TierContentType and the object key from
// TierFileExtension, so all three must agree or reads 404 / render as garbage.
func TestEncodeTiersProducesAVIF(t *testing.T) {
	src, err := encodePNGForTest(checkerboard(600, 600, 3,
		color.RGBA{R: 20, G: 30, B: 200, A: 255}, color.RGBA{R: 240, G: 240, B: 60, A: 255}))
	if err != nil {
		t.Fatalf("source: %v", err)
	}
	tiers, err := EncodeTiers(src)
	if err != nil {
		t.Fatalf("EncodeTiers: %v", err)
	}
	for name, blob := range map[string][]byte{"final": tiers.Final, "preview": tiers.Preview, "thumb": tiers.Thumb} {
		_, format, decErr := image.DecodeConfig(bytes.NewReader(blob))
		if decErr != nil {
			t.Fatalf("%s: decode config: %v", name, decErr)
		}
		if format != "avif" {
			t.Fatalf("%s: expected avif bytes, got %q", name, format)
		}
	}
	if TierContentType != "image/avif" || TierFileExtension != "avif" {
		t.Fatalf("format declaration drifted from the encoder: %s / %s", TierContentType, TierFileExtension)
	}
}

// A transparent sprite must come back with its fully-transparent pixels still
// fully transparent. Measured on a real cutout, a LOSSY alpha channel pushed
// 10,047 transparent pixels to non-transparent (worst deviation 52/255) — a
// ghost fringe around every sprite. This is the guard on avifQualityAlpha
// staying lossless.
//
// It counts EVERY transparent source pixel, not a few corners: corners are the
// easiest pixels in the image and an earlier version of this test passed with
// lossy alpha, which made it decoration rather than a guard.
func TestEncodeTiersKeepsTransparencyExact(t *testing.T) {
	// 512 == PreviewShortEdge, so the preview tier is the render untouched by
	// any resample and alpha can be compared pixel for pixel.
	sprite := spriteWithHole(PreviewShortEdge)
	src, err := encodePNGForTest(sprite)
	if err != nil {
		t.Fatalf("source: %v", err)
	}
	tiers, err := EncodeTiers(src)
	if err != nil {
		t.Fatalf("EncodeTiers: %v", err)
	}
	// Only the tiers that share the source's geometry can be compared pixel to
	// pixel; the reductions resample, which legitimately moves alpha.
	img, _, err := image.Decode(bytes.NewReader(tiers.Preview))
	if err != nil {
		t.Fatalf("preview decode: %v", err)
	}
	b := img.Bounds()
	if b.Dx() != sprite.Bounds().Dx() || b.Dy() != sprite.Bounds().Dy() {
		t.Fatalf("preview should be the render at its own size, got %v", b)
	}
	var leaked, worst int
	for y := range b.Dy() {
		for x := range b.Dx() {
			_, _, _, want := sprite.At(x, y).RGBA()
			_, _, _, got := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			if want == 0 && got != 0 {
				leaked++
				if d := int(got >> 8); d > worst {
					worst = d
				}
			}
		}
	}
	if leaked != 0 {
		t.Fatalf("%d fully-transparent pixels leaked (worst alpha %d/255) — alpha must be encoded losslessly",
			leaked, worst)
	}
}

// The whole point of rendering small is that the delivery tier is enlarged
// here. A 512 render must produce a 1024 final; a render already larger than
// UpscaleBelowShortEdge must NOT be inflated past what the provider drew.
func TestEncodeTiersUpscalesOnlySmallRenders(t *testing.T) {
	for _, tc := range []struct {
		name      string
		size      int
		wantFinal int
	}{
		{"512 render is enlarged for delivery", 512, 1024},
		{"1024 render is delivered as rendered", 1024, 1024},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src, err := encodePNGForTest(checkerboard(tc.size, tc.size, 4,
				color.RGBA{R: 10, G: 10, B: 10, A: 255}, color.RGBA{R: 245, G: 245, B: 245, A: 255}))
			if err != nil {
				t.Fatalf("source: %v", err)
			}
			tiers, err := EncodeTiers(src)
			if err != nil {
				t.Fatalf("EncodeTiers: %v", err)
			}
			cfg, _, err := image.DecodeConfig(bytes.NewReader(tiers.Final))
			if err != nil {
				t.Fatalf("final config: %v", err)
			}
			if cfg.Width != tc.wantFinal || cfg.Height != tc.wantFinal {
				t.Fatalf("final tier: expected %dx%d, got %dx%d",
					tc.wantFinal, tc.wantFinal, cfg.Width, cfg.Height)
			}
		})
	}
}

// Tier ladder: final >= preview >= thumb, and the ladder is genuinely distinct
// for a 512 render. Two identical tiers means a consumer picking "preview" to
// save bytes gets no saving.
func TestEncodeTiersLadderIsDistinct(t *testing.T) {
	src, err := encodePNGForTest(checkerboard(512, 512, 4,
		color.RGBA{R: 30, G: 90, B: 160, A: 255}, color.RGBA{R: 250, G: 200, B: 40, A: 255}))
	if err != nil {
		t.Fatalf("source: %v", err)
	}
	tiers, err := EncodeTiers(src)
	if err != nil {
		t.Fatalf("EncodeTiers: %v", err)
	}
	dim := func(blob []byte) int {
		cfg, _, err := image.DecodeConfig(bytes.NewReader(blob))
		if err != nil {
			t.Fatalf("config: %v", err)
		}
		return cfg.Width
	}
	final, preview, thumb := dim(tiers.Final), dim(tiers.Preview), dim(tiers.Thumb)
	if !(final > preview && preview > thumb) {
		t.Fatalf("expected final > preview > thumb, got %d / %d / %d", final, preview, thumb)
	}
	if preview != PreviewShortEdge || thumb != ThumbnailShortEdge {
		t.Fatalf("ladder drifted: preview %d (want %d), thumb %d (want %d)",
			preview, PreviewShortEdge, thumb, ThumbnailShortEdge)
	}
}

// A regenerate must reproduce the same objects, which the storage layer and the
// forced-regeneration path both assume. Fixed kernels, fixed iteration counts
// and fixed encoder settings are what make that true.
func TestEncodeTiersIsDeterministic(t *testing.T) {
	src, err := encodePNGForTest(spriteWithHole(512))
	if err != nil {
		t.Fatalf("source: %v", err)
	}
	a, err := EncodeTiers(src)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	b, err := EncodeTiers(src)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	for name, pair := range map[string][2][]byte{
		"final": {a.Final, b.Final}, "preview": {a.Preview, b.Preview}, "thumb": {a.Thumb, b.Thumb},
	} {
		if !bytes.Equal(pair[0], pair[1]) {
			t.Fatalf("%s tier is not byte-identical across two encodes of the same input", name)
		}
	}
}
