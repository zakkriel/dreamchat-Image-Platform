package imaging

// Chroma keying — extracting alpha from a render made against a known flat
// backdrop, with no second provider call.
//
// The idea is the reverse green screen: ask the model for a strong flat
// backdrop colour, then remove that colour locally. It is free, deterministic,
// and reproducible, which a hosted matting call is none of.
//
// It is also the technique most likely to fail SILENTLY, in three ways, and
// each one is checked rather than hoped about:
//
//  1. The model ignores the instruction and renders a textured or gradient
//     backdrop. Keying then removes almost nothing. Caught by BorderCoverage.
//  2. The subject legitimately CONTAINS the key colour — magenta hair, a pink
//     shirt — and keying punches holes through it. Caught by flood-filling the
//     key mask from the border: any keyed region not connected to the border is
//     interior, and interior regions are never made transparent, only reported.
//  3. Edge pixels blend subject with backdrop, so a clean key still leaves a
//     coloured fringe. Handled by despill, not by tightening the threshold.
//
// Keying is done on chroma, not RGB distance: a backdrop is rarely uniformly
// lit even when the model cooperates, and shadow or falloff on the backdrop
// changes luma while leaving hue intact.

import (
	"errors"
	"image"
	"image/color"
	"math"
)

var (
	// ErrBackdropNotFound means the border is not predominantly the key colour,
	// so the render does not have the flat backdrop keying assumes. Treating a
	// normal image as a keyed one would erase whatever happened to be near the
	// key hue, so the caller must fall back instead.
	ErrBackdropNotFound = errors.New("imaging: chroma key backdrop not found")
	// ErrSubjectContainsKey means too much of the SUBJECT matches the key
	// colour. The subject is never punched through, so the output is intact -
	// but it is also not reliably separable, and the caller should fall back to
	// a matting model rather than ship a cutout with the wrong silhouette.
	ErrSubjectContainsKey = errors.New("imaging: subject contains the key colour")
	// ErrSubjectNearKey means the subject carries colours close enough to the
	// key that its silhouette cannot be trusted.
	//
	// This is the check that makes chroma keying honest, and it exists because
	// of a measured limit rather than a hunch. Sweeping every key hue against a
	// realistic character palette, the nearest risky subject colour sits 79-88
	// chroma units away (magenta/purple cloth 78.9, green/green cloth 88.1),
	// while a pixel that is HALF backdrop sits about 93 away. The two bands
	// overlap for every possible key colour, so no threshold both softens
	// anti-aliased edges and protects saturated subject colours. The ramp is
	// therefore kept narrow - edges come out harder than a matting model would
	// give - and anything chromatically close to the key is refused outright.
	ErrSubjectNearKey = errors.New("imaging: subject colours are too close to the key")
)

// DefaultChromaKey is the backdrop to ask the model for: full-intensity
// magenta. It is chosen because it is far from skin, hair, foliage and sky in
// chroma terms, which green - the video-production default - is not.
var DefaultChromaKey = color.RGBA{R: 255, G: 0, B: 255, A: 255}

// ChromaKeyOptions tunes the key. The zero value is not useful; use
// DefaultChromaKeyOptions.
type ChromaKeyOptions struct {
	Key color.RGBA
	// InnerTolerance is the chroma distance within which a pixel is pure
	// backdrop and becomes fully transparent.
	InnerTolerance float64
	// OuterTolerance is the chroma distance beyond which a pixel is pure
	// subject and stays fully opaque. Between the two the alpha ramps, which is
	// what gives anti-aliased edges and fine hair a soft matte instead of a
	// staircase.
	OuterTolerance float64
	// MinBorderCoverage is the fraction of the outer border ring that must key
	// before the image is accepted as having a backdrop at all.
	MinBorderCoverage float64
	// MaxInteriorKeyFraction is how much of the interior may match the key
	// before the subject is judged to collide with it.
	MaxInteriorKeyFraction float64
	// DangerTolerance is the chroma distance under which an OPAQUE subject
	// pixel counts as uncomfortably close to the key. Pixels between
	// OuterTolerance and this stay fully opaque - they are subject - but they
	// signal that the silhouette may be unreliable.
	DangerTolerance float64
	// MaxSubjectNearKeyFraction is how much of the subject may sit inside
	// DangerTolerance before the key is refused.
	MaxSubjectNearKeyFraction float64
	// DespillStrength scales how much key hue is pulled out of partially
	// transparent pixels. 0 disables despill.
	DespillStrength float64
}

// DefaultChromaKeyOptions are deliberately conservative: they would rather
// refuse a render and fall back than ship a subject with holes in it.
func DefaultChromaKeyOptions() ChromaKeyOptions {
	return ChromaKeyOptions{
		Key:               DefaultChromaKey,
		InnerTolerance:    28,
		OuterTolerance:    72,
		MinBorderCoverage: 0.90,
		// A hole punched through the subject is the worst outcome, so the
		// enclosed-region budget is tiny.
		MaxInteriorKeyFraction: 0.02,
		// 92 sits just under the ~93 of a half-covered edge pixel and just
		// above the 79-88 where real subject colours live, so the danger band
		// flags a magenta-haired character without flagging its own edges.
		DangerTolerance:           92,
		MaxSubjectNearKeyFraction: 0.03,
		DespillStrength:           1,
	}
}

// ChromaKeyReport is the evidence for whether the key can be trusted. It is
// returned even on failure so a caller can log why a cell fell back.
type ChromaKeyReport struct {
	// BorderCoverage is the fraction of the outer border ring that keyed.
	BorderCoverage float64
	// InteriorKeyFraction is the fraction of non-border-connected pixels that
	// matched the key - the subject-collision signal.
	InteriorKeyFraction float64
	// TransparentPixels counts fully transparent output pixels.
	TransparentPixels int
	// PartialPixels counts soft-matte edge pixels, the ones despill acts on.
	PartialPixels int
	// SubjectNearKeyFraction is the fraction of opaque subject pixels sitting
	// inside DangerTolerance - the silhouette-reliability signal.
	SubjectNearKeyFraction float64
	TotalPixels            int
}

// ChromaKey removes a flat backdrop, returning a non-premultiplied RGBA image.
// NRGBA is deliberate: PNG stores straight alpha, so this avoids a lossy
// round trip through premultiplied values on the way to disk.
//
// On ErrBackdropNotFound or ErrSubjectContainsKey the returned image is nil and
// the report explains which check failed.
func ChromaKey(src image.Image, opts ChromaKeyOptions) (*image.NRGBA, ChromaKeyReport, error) {
	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	report := ChromaKeyReport{TotalPixels: w * h}
	if w <= 0 || h <= 0 {
		return nil, report, errors.New("imaging: chroma key on an empty image")
	}
	if opts.OuterTolerance <= opts.InnerTolerance {
		return nil, report, errors.New("imaging: chroma key OuterTolerance must exceed InnerTolerance")
	}

	keyCb, keyCr := chromaOf(opts.Key.R, opts.Key.G, opts.Key.B)
	keyDirCb, keyDirCr := normalizeChroma(keyCb, keyCr)

	// Pass 1: alpha from chroma distance. Alpha is stored per pixel so pass 2
	// can reason about connectivity before anything is made transparent.
	alpha := make([]uint8, w*h)
	dist := make([]float64, w*h)
	for y := range h {
		for x := range w {
			r, g, b, _ := src.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			cb, cr := chromaOf(uint8(r>>8), uint8(g>>8), uint8(b>>8))
			d := math.Hypot(cb-keyCb, cr-keyCr)
			dist[y*w+x] = d
			switch {
			case d <= opts.InnerTolerance:
				alpha[y*w+x] = 0
			case d >= opts.OuterTolerance:
				alpha[y*w+x] = 255
			default:
				ramp := (d - opts.InnerTolerance) / (opts.OuterTolerance - opts.InnerTolerance)
				alpha[y*w+x] = uint8(math.Round(ramp * 255))
			}
		}
	}

	// Pass 2: only backdrop CONNECTED TO THE BORDER is really backdrop. A keyed
	// region enclosed by the subject is the subject's own colour, and punching
	// it out would silently produce a holed sprite.
	connected := floodFillFromBorder(alpha, w, h)

	borderKeyed, borderTotal := 0, 0
	interiorKeyed, interiorTotal := 0, 0
	for y := range h {
		for x := range w {
			i := y*w + x
			onBorder := x == 0 || y == 0 || x == w-1 || y == h-1
			if onBorder {
				borderTotal++
				if alpha[i] == 0 {
					borderKeyed++
				}
				continue
			}
			interiorTotal++
			if alpha[i] < 255 && !connected[i] {
				interiorKeyed++
			}
		}
	}

	// Subject proximity: how much of what stays opaque is chromatically close
	// to the key. A magenta-haired character trips this even though no pixel
	// crossed the key threshold, which is the point - the silhouette would be
	// unreliable at the edges where hair meets backdrop.
	subjectNear, subjectTotal := 0, 0
	if opts.DangerTolerance > opts.OuterTolerance {
		for i := range alpha {
			if alpha[i] != 255 {
				continue
			}
			subjectTotal++
			if dist[i] < opts.DangerTolerance {
				subjectNear++
			}
		}
		if subjectTotal > 0 {
			report.SubjectNearKeyFraction = float64(subjectNear) / float64(subjectTotal)
		}
	}
	if borderTotal > 0 {
		report.BorderCoverage = float64(borderKeyed) / float64(borderTotal)
	}
	if interiorTotal > 0 {
		report.InteriorKeyFraction = float64(interiorKeyed) / float64(interiorTotal)
	}

	if report.BorderCoverage < opts.MinBorderCoverage {
		return nil, report, ErrBackdropNotFound
	}
	if report.InteriorKeyFraction > opts.MaxInteriorKeyFraction {
		return nil, report, ErrSubjectContainsKey
	}
	if opts.MaxSubjectNearKeyFraction > 0 && report.SubjectNearKeyFraction > opts.MaxSubjectNearKeyFraction {
		return nil, report, ErrSubjectNearKey
	}

	// Pass 3: compose. Unconnected keyed pixels are restored to opaque so the
	// subject keeps its silhouette; connected ones carry their ramped alpha and
	// get despilled.
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			i := y*w + x
			r16, g16, b16, _ := src.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			r, g, b := uint8(r16>>8), uint8(g16>>8), uint8(b16>>8)

			a := alpha[i]
			if a < 255 && !connected[i] {
				a = 255
			}
			if a < 255 && opts.DespillStrength > 0 {
				// Spill scales with how much backdrop was removed: a pixel that
				// is mostly backdrop carries mostly backdrop colour.
				r, g, b = despill(r, g, b, keyDirCb, keyDirCr, opts.DespillStrength*(1-float64(a)/255))
			}
			switch {
			case a == 0:
				report.TransparentPixels++
			case a < 255:
				report.PartialPixels++
			}
			o := dst.PixOffset(x, y)
			dst.Pix[o], dst.Pix[o+1], dst.Pix[o+2], dst.Pix[o+3] = r, g, b, a
		}
	}
	return dst, report, nil
}

// floodFillFromBorder marks every fully-keyed pixel reachable from the image
// border through other fully-keyed pixels. 4-connectivity: diagonal leakage
// through an anti-aliased edge would let the fill escape into an enclosed
// region and defeat the whole check.
func floodFillFromBorder(alpha []uint8, w, h int) []bool {
	connected := make([]bool, w*h)
	queue := make([]int, 0, w*2+h*2)

	push := func(x, y int) {
		i := y*w + x
		if alpha[i] == 0 && !connected[i] {
			connected[i] = true
			queue = append(queue, i)
		}
	}
	for x := range w {
		push(x, 0)
		push(x, h-1)
	}
	for y := range h {
		push(0, y)
		push(w-1, y)
	}
	for len(queue) > 0 {
		i := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		x, y := i%w, i/w
		if x > 0 {
			push(x-1, y)
		}
		if x < w-1 {
			push(x+1, y)
		}
		if y > 0 {
			push(x, y-1)
		}
		if y < h-1 {
			push(x, y+1)
		}
	}
	// Partially-keyed edge pixels neighbouring connected backdrop are part of
	// the silhouette, not interior holes.
	for y := range h {
		for x := range w {
			i := y*w + x
			if alpha[i] == 0 || alpha[i] == 255 || connected[i] {
				continue
			}
			if (x > 0 && connected[i-1]) || (x < w-1 && connected[i+1]) ||
				(y > 0 && connected[i-w]) || (y < h-1 && connected[i+w]) {
				connected[i] = true
			}
		}
	}
	return connected
}

// chromaOf returns the Cb/Cr chroma of an RGB triple. Luma is discarded on
// purpose: an unevenly lit backdrop varies in luma, not hue.
func chromaOf(r, g, b uint8) (float64, float64) {
	_, cb, cr := color.RGBToYCbCr(r, g, b)
	return float64(cb) - 128, float64(cr) - 128
}

func normalizeChroma(cb, cr float64) (float64, float64) {
	if mag := math.Hypot(cb, cr); mag > 0 {
		return cb / mag, cr / mag
	}
	return 0, 0
}

// despill removes the key hue from a pixel by subtracting its projection onto
// the key's chroma direction, preserving luma. Clamping the projection at zero
// matters: a pixel whose hue OPPOSES the key must be left alone, or despill
// would tint the subject with the key's complement.
func despill(r, g, b uint8, keyDirCb, keyDirCr, strength float64) (uint8, uint8, uint8) {
	y, cb8, cr8 := color.RGBToYCbCr(r, g, b)
	cb, cr := float64(cb8)-128, float64(cr8)-128
	proj := cb*keyDirCb + cr*keyDirCr
	if proj <= 0 || strength <= 0 {
		return r, g, b
	}
	remove := proj * math.Min(strength, 1)
	cb -= remove * keyDirCb
	cr -= remove * keyDirCr
	return color.YCbCrToRGB(y, clampChroma(cb), clampChroma(cr))
}

func clampChroma(v float64) uint8 {
	shifted := math.Round(v + 128)
	if shifted < 0 {
		return 0
	}
	if shifted > 255 {
		return 255
	}
	return uint8(shifted)
}
