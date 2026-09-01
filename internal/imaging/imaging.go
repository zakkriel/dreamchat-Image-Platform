// Package imaging produces the three delivery resolution tiers (PRD 06 §4)
// from a provider's output: final (upscaled), preview (the render as produced),
// and thumbnail.
//
// Two decisions are load-bearing here.
//
// TIERS ARE AVIF, NOT PNG. Measured across the six art styles, an AVIF q80
// tier is 13-20x smaller than the PNG it replaces with no visible loss: on a
// real transparent cutout the mean error over 13,544 semi-transparent hair
// pixels is 1.86/255, and at 4x zoom over black and over white the two are
// indistinguishable. Per asset the three tiers fall from ~1,700 KB to ~45 KB.
//
// THE FINAL TIER IS UPSCALED, NOT RENDERED LARGE. Providers charge for pixels
// (or a flat per-image fee that no size parameter can reduce), so the delivery
// resolution is produced here from a cheaper small render. See upscale.go for
// the measured justification.
//
// Everything stays deterministic — fixed kernels, fixed iteration counts, fixed
// encoder settings — so a regenerate of the same bytes yields the same objects.
package imaging

import (
	"bytes"
	"fmt"
	"image"
	_ "image/jpeg" // register the JPEG decoder so real-provider JPEG output decodes
	_ "image/png"  // register the PNG decoder: providers emit jpeg or png only
	"math"

	"github.com/gen2brain/avif"
	xdraw "golang.org/x/image/draw"
)

// Tier short-edge targets in pixels (PRD 06 §4). The ladder assumes a 512
// render: final is that render enlarged by FinalUpscaleFactor, preview is the
// render itself, thumbnail is a reduction. A larger render simply skips the
// upscale and downscales as before.
const (
	ThumbnailShortEdge = 256
	PreviewShortEdge   = 512

	// FinalUpscaleFactor enlarges the delivery tier. A 512 render costs a
	// fraction of a 1024 one and the difference is largely recoverable
	// (upscale.go), so the large image is made here rather than bought.
	FinalUpscaleFactor = 2

	// UpscaleBelowShortEdge bounds that: only a render at or under this short
	// edge is enlarged. A provider that already returned a large image is
	// delivered as-is rather than inflated past what it actually rendered.
	UpscaleBelowShortEdge = 512

	// avifQuality and avifSpeed are the encoder settings for every tier.
	// Quality 80 is where the measured error stops being visible; speed 8 is
	// the knee of the cost curve — speed 6 costs 2.9s per 1024 tier against
	// 0.5s for 4 KB less, and this runs on every generated image.
	avifQuality = 80
	avifSpeed   = 8

	// avifQualityAlpha is 100 — LOSSLESS — and that is not a preference.
	// Measured on a real cutout, a lossy alpha channel at quality 80 pushed
	// 10,047 fully-transparent pixels to non-transparent (worst deviation
	// 52/255), which is a visible ghost fringe around every sprite. Lossless
	// alpha costs ~37% more bytes on a transparent asset and still lands ~7x
	// under the PNG it replaces. A silhouette is structural, not perceptual.
	avifQualityAlpha = 100

	// TierContentType and TierFileExtension describe what the tiers now are.
	// Storage keys and the Content-Type header both derive from these, so the
	// format lives in ONE place.
	TierContentType   = "image/avif"
	TierFileExtension = "avif"
)

// Tiers carries the encoded bytes for each delivery resolution tier.
type Tiers struct {
	// Final is the delivery tier: the render enlarged when it is small enough
	// to warrant it, otherwise the render itself.
	Final []byte
	// Preview is downscaled toward PreviewShortEdge (never upscaled).
	Preview []byte
	// Thumb is downscaled toward ThumbnailShortEdge (never upscaled).
	Thumb []byte
}

// EncodeTiers decodes a provider image (PNG or JPEG — image.Decode auto-detects
// the format) and returns the three resolution tiers as AVIF.
// final >= preview >= thumbnail by construction.
func EncodeTiers(src []byte) (Tiers, error) {
	img, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return Tiers{}, fmt.Errorf("imaging: decode source image: %w", err)
	}

	final, err := encodeAVIF(upscaleForDelivery(img))
	if err != nil {
		return Tiers{}, err
	}
	preview, err := encodeAVIF(downscaleShortEdge(img, PreviewShortEdge))
	if err != nil {
		return Tiers{}, err
	}
	thumb, err := encodeAVIF(downscaleShortEdge(img, ThumbnailShortEdge))
	if err != nil {
		return Tiers{}, err
	}
	return Tiers{Final: final, Preview: preview, Thumb: thumb}, nil
}

// upscaleForDelivery enlarges a small render and passes a large one through.
func upscaleForDelivery(src image.Image) image.Image {
	b := src.Bounds()
	shortEdge := b.Dx()
	if b.Dy() < shortEdge {
		shortEdge = b.Dy()
	}
	if shortEdge > UpscaleBelowShortEdge {
		return src
	}
	return Upscale(src, FinalUpscaleFactor)
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

// encodeAVIF writes one tier. Settings are fixed constants rather than
// parameters so two encodes of the same pixels are byte-identical.
//
// 4:4:4 chroma is deliberate: the default 4:2:0 halves colour resolution, which
// lands hardest on exactly the content this platform renders — saturated ink
// outlines and flat colour blocking. It costs +2.7% bytes and measured better
// (SSIMULACRA2 83.72 vs 83.53 on the comic style).
func encodeAVIF(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	opts := avif.Options{
		Quality:           avifQuality,
		QualityAlpha:      avifQualityAlpha,
		Speed:             avifSpeed,
		ChromaSubsampling: image.YCbCrSubsampleRatio444,
	}
	if err := avif.Encode(&buf, img, opts); err != nil {
		return nil, fmt.Errorf("imaging: encode avif: %w", err)
	}
	return buf.Bytes(), nil
}
