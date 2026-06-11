// Package imaging produces the three delivery resolution tiers (PRD 06 §4)
// from a provider's PNG output: final (full size), preview, and thumbnail.
//
// Downscaling is deterministic and pure — the same input bytes always produce
// the same tier outputs — because it decodes once, resizes with a fixed
// Catmull-Rom kernel, and re-encodes PNG with fixed encoder settings. A tier
// target larger than the source short edge is never upscaled (tier =
// min(target, source)), so a small provider image yields identical-size tiers
// and the dimensions only diverge when the source is large enough.
package imaging

import (
	"bytes"
	"fmt"
	"image"
	_ "image/jpeg" // register the JPEG decoder so real-provider JPEG output decodes
	"image/png"
	"math"

	xdraw "golang.org/x/image/draw"
)

// Tier short-edge targets in pixels (PRD 06 §4): thumbnail 128–256px, preview
// 512–1024px, final = provider output. These are the upper bound a tier is
// downscaled toward; a smaller source is left untouched.
const (
	ThumbnailShortEdge = 256
	PreviewShortEdge   = 768
)

// Tiers carries the encoded PNG bytes for each delivery resolution tier.
type Tiers struct {
	// Final is the provider output re-encoded as PNG (full resolution).
	Final []byte
	// Preview is downscaled toward PreviewShortEdge (never upscaled).
	Preview []byte
	// Thumb is downscaled toward ThumbnailShortEdge (never upscaled).
	Thumb []byte
}

// EncodeTiers decodes a provider image (PNG or JPEG — image.Decode auto-detects
// the format) and returns the three resolution tiers, always re-encoded as PNG.
// final ≥ preview ≥ thumbnail by construction; the dimensions are only
// guaranteed distinct when the source short edge exceeds the tier targets.
func EncodeTiers(src []byte) (Tiers, error) {
	img, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return Tiers{}, fmt.Errorf("imaging: decode source image: %w", err)
	}

	final, err := encodePNG(img)
	if err != nil {
		return Tiers{}, err
	}
	preview, err := encodePNG(downscaleShortEdge(img, PreviewShortEdge))
	if err != nil {
		return Tiers{}, err
	}
	thumb, err := encodePNG(downscaleShortEdge(img, ThumbnailShortEdge))
	if err != nil {
		return Tiers{}, err
	}
	return Tiers{Final: final, Preview: preview, Thumb: thumb}, nil
}

// downscaleShortEdge returns a copy of src scaled so its short edge equals
// target, preserving aspect ratio. When the source short edge is already at or
// below target it returns src unchanged — tiers are never upscaled.
func downscaleShortEdge(src image.Image, target int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	shortEdge := w
	if h < w {
		shortEdge = h
	}
	if shortEdge <= target {
		return src
	}
	scale := float64(target) / float64(shortEdge)
	nw := int(math.Round(float64(w) * scale))
	nh := int(math.Round(float64(h) * scale))
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	// CatmullRom is a fixed, deterministic kernel: identical inputs always
	// produce identical pixels, so a regenerate/reupload is reproducible.
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, b, xdraw.Over, nil)
	return dst
}

func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.DefaultCompression}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("imaging: encode png: %w", err)
	}
	return buf.Bytes(), nil
}
