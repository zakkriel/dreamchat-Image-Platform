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
	// HueTolerance is the hue difference, in degrees, within which a
	// sufficiently saturated pixel is confidently backdrop.
	//
	// Hue rather than distance-from-the-key-colour, because that is what a real
	// render produces. Asked for #FF00FF, FLUX.1 Kontext paints the right HUE
	// at roughly half the saturation (measured chroma magnitude 61-77 against
	// the key's 136) with a vignette. Euclidean distance from the key is
	// dominated by that saturation gap and scored the backdrop as 68-82 away -
	// further than a subject colour - so it keyed nothing at all.
	HueTolerance float64
	// WeakHueTolerance is the looser hue difference accepted for pixels that
	// are CONNECTED to confident backdrop. This is hysteresis, and it is what
	// absorbs the vignette: the measured corner sits 42 degrees off-hue at a
	// third of the saturation, which no single threshold can accept without
	// also accepting real subject colours.
	WeakHueTolerance float64
	// MinChroma is the saturation floor for confident backdrop. Below it a
	// pixel is close to neutral grey and its hue is numerically unstable.
	MinChroma float64
	// WeakMinChroma is the saturation floor for connected backdrop.
	WeakMinChroma float64
	// WeakMaxChroma is the saturation CEILING for connected backdrop, and it is
	// what separates a vignette from vivid hair. Measured: the vignetted corner
	// sits 42 degrees off-hue at chroma magnitude 33, while pink hair sits 28
	// degrees off at 70. Hue cannot tell them apart - the vignette is further
	// off-hue than the hair - but saturation can.
	WeakMaxChroma float64
	// MaxTransparentFraction refuses a key that removed implausibly much of the
	// frame. A subject whose own colour matches the key (purple clothing lands
	// at the same hue offset as the backdrop) would otherwise be keyed away in
	// silence, leaving nothing to notice.
	MaxTransparentFraction float64
	// MinBorderCoverage is the fraction of the outer border ring that must key
	// before the image is accepted as having a backdrop at all.
	//
	// It is deliberately NOT near 1.0. A bust crop legitimately runs off the
	// frame - shoulders reach the bottom and side edges - and a measured render
	// of exactly that shape covers about 82% of the ring. The check exists to
	// catch a render with no backdrop at all, which measures 0, not to insist
	// the subject float clear of the edges.
	MinBorderCoverage float64
	// MaxInteriorKeyFraction is how much of the interior may match the key
	// before the subject is judged to collide with it.
	MaxInteriorKeyFraction float64
	// DangerHueTolerance is the hue difference under which an OPAQUE subject
	// pixel counts as uncomfortably close to the key. Such pixels stay opaque -
	// they are subject - but they signal the silhouette may be unreliable.
	DangerHueTolerance float64
	// MaxSubjectNearKeyFraction is how much of the subject may sit inside
	// DangerTolerance before the key is refused.
	MaxSubjectNearKeyFraction float64
	// DespillStrength scales how much key hue is pulled out of pixels touched by
	// the backdrop. 0 disables despill.
	DespillStrength float64
	// EdgeDespillRadius extends despill to OPAQUE pixels within this many
	// pixels of the cutout edge.
	//
	// Matte-bound despill is not enough. With a narrow ramp most silhouette
	// pixels land fully opaque while still carrying backdrop colour, which
	// shows up as a thin key-coloured rim - visible on a rendered bust even
	// when only ~0.2% of pixels are partial. Despill is a spatial operation:
	// it belongs on the band around the cutout, not only on the matte itself.
	// It is bounded to that band rather than applied globally because skin
	// tones lean far enough toward magenta to be desaturated by a global pass.
	EdgeDespillRadius int
}

// DefaultChromaKeyOptions are deliberately conservative: they would rather
// refuse a render and fall back than ship a subject with holes in it.
func DefaultChromaKeyOptions() ChromaKeyOptions {
	return ChromaKeyOptions{
		Key: DefaultChromaKey,
		// Measured against real renders: the backdrop lands 19-21 degrees off
		// pure magenta, every subject colour except purple clothing is 50+
		// degrees away, and pink hair is 28.
		HueTolerance:      25,
		WeakHueTolerance:  55,
		MinChroma:         25,
		WeakMinChroma:     12,
		WeakMaxChroma:     45,
		MinBorderCoverage: 0.50,
		// A hole punched through the subject is the worst outcome, so the
		// enclosed-region budget is tiny.
		MaxInteriorKeyFraction: 0.02,
		// Purple clothing sits at the SAME 20-degree offset as the backdrop and
		// pink hair at 28, so a subject wearing either is genuinely ambiguous
		// and must be refused rather than keyed.
		DangerHueTolerance:        35,
		MaxSubjectNearKeyFraction: 0.03,
		MaxTransparentFraction:    0.85,
		DespillStrength:           1,
		EdgeDespillRadius:         2,
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
	if opts.WeakHueTolerance < opts.HueTolerance {
		return nil, report, errors.New("imaging: chroma key WeakHueTolerance must be >= HueTolerance")
	}

	keyCb, keyCr := chromaOf(opts.Key.R, opts.Key.G, opts.Key.B)
	keyDirCb, keyDirCr := normalizeChroma(keyCb, keyCr)
	keyHue := math.Atan2(keyCr, keyCb) * 180 / math.Pi

	// Pass 1: classify by hue offset and saturation. strong = confident
	// backdrop; weak = plausible backdrop, accepted only where it connects to
	// strong (hysteresis, which is what absorbs a vignette).
	const (
		clsSubject = 0
		clsWeak    = 1
		clsStrong  = 2
	)
	class := make([]uint8, w*h)
	hueDiff := make([]float64, w*h)
	for y := range h {
		for x := range w {
			r, g, b, _ := src.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			cb, cr := chromaOf(uint8(r>>8), uint8(g>>8), uint8(b>>8))
			mag := math.Hypot(cb, cr)
			hd := math.Abs(math.Mod(math.Atan2(cr, cb)*180/math.Pi-keyHue+540, 360) - 180)
			i := y*w + x
			hueDiff[i] = hd
			switch {
			case hd <= opts.HueTolerance && mag >= opts.MinChroma:
				class[i] = clsStrong
			case hd <= opts.WeakHueTolerance && mag >= opts.WeakMinChroma &&
				(opts.WeakMaxChroma <= 0 || mag <= opts.WeakMaxChroma):
				class[i] = clsWeak
			}
		}
	}

	// Pass 2: the backdrop is what is REACHABLE FROM THE BORDER through
	// backdrop-ish pixels, seeded only from confident ones. A purple shirt in
	// the middle of the subject is never reached; a vignetted corner is.
	backdrop := floodBackdrop(class, w, h)

	borderKeyed, borderTotal := 0, 0
	interiorKeyed, interiorTotal := 0, 0
	for y := range h {
		for x := range w {
			i := y*w + x
			if x == 0 || y == 0 || x == w-1 || y == h-1 {
				borderTotal++
				if backdrop[i] {
					borderKeyed++
				}
				continue
			}
			interiorTotal++
			if class[i] == clsStrong && !backdrop[i] {
				interiorKeyed++
			}
		}
	}
	if borderTotal > 0 {
		report.BorderCoverage = float64(borderKeyed) / float64(borderTotal)
	}
	if interiorTotal > 0 {
		report.InteriorKeyFraction = float64(interiorKeyed) / float64(interiorTotal)
	}

	// Alpha: backdrop is transparent. A pixel that is NOT backdrop but touches
	// it is the anti-aliased boundary, and gets a soft matte from how far its
	// hue sits from the key - that is where a blend of subject and backdrop
	// lands.
	alpha := make([]uint8, w*h)
	for i := range alpha {
		alpha[i] = 255
	}
	for y := range h {
		for x := range w {
			i := y*w + x
			if backdrop[i] {
				alpha[i] = 0
				continue
			}
			if !touchesBackdrop(backdrop, w, h, x, y) {
				continue
			}
			hd := hueDiff[i]
			if hd >= opts.WeakHueTolerance {
				continue
			}
			ramp := (hd - opts.HueTolerance) / (opts.WeakHueTolerance - opts.HueTolerance)
			if ramp < 0 {
				ramp = 0
			}
			alpha[i] = uint8(math.Round(ramp * 255))
		}
	}

	// Subject proximity: opaque pixels whose hue crowds the key. Purple
	// clothing lands at the same offset as the backdrop itself, so this is the
	// check that keeps an ambiguous character out of the cheap path.
	subjectNear, subjectTotal := 0, 0
	for i := range alpha {
		if alpha[i] != 255 {
			continue
		}
		subjectTotal++
		if hueDiff[i] <= opts.DangerHueTolerance && class[i] != clsSubject {
			subjectNear++
		}
	}
	if subjectTotal > 0 {
		report.SubjectNearKeyFraction = float64(subjectNear) / float64(subjectTotal)
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
	transparent := 0
	for _, a := range alpha {
		if a == 0 {
			transparent++
		}
	}
	if opts.MaxTransparentFraction > 0 && report.TotalPixels > 0 &&
		float64(transparent)/float64(report.TotalPixels) > opts.MaxTransparentFraction {
		report.TransparentPixels = transparent
		return nil, report, ErrSubjectNearKey
	}

	connected := backdrop
	edgeDist := edgeDistance(alpha, connected, w, h, opts.EdgeDespillRadius)

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
			if opts.DespillStrength > 0 {
				// Spill scales with how much backdrop was removed, and for
				// opaque rim pixels with how close they sit to the cutout edge.
				strength := 0.0
				if a < 255 {
					strength = 1 - float64(a)/255
				} else if d := edgeDist[i]; d > 0 {
					strength = 1 - float64(d-1)/float64(max(opts.EdgeDespillRadius, 1))
				}
				if strength > 0 {
					r, g, b = despill(r, g, b, keyDirCb, keyDirCr, opts.DespillStrength*strength)
				}
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

// edgeDistance returns, for every pixel, its distance in pixels to the nearest
// fully transparent pixel, capped at radius. Zero means "further away than the
// radius" so the despill band has a hard, cheap boundary. A multi-source BFS
// keeps it linear in the pixel count.
func edgeDistance(alpha []uint8, connected []bool, w, h, radius int) []int {
	dist := make([]int, w*h)
	if radius <= 0 {
		return dist
	}
	queue := make([]int, 0, w*h/8)
	for i := range alpha {
		if alpha[i] == 0 && connected[i] {
			dist[i] = 1
			queue = append(queue, i)
		}
	}
	for head := 0; head < len(queue); head++ {
		i := queue[head]
		if dist[i] > radius {
			continue
		}
		x, y := i%w, i/w
		visit := func(nx, ny int) {
			if nx < 0 || ny < 0 || nx >= w || ny >= h {
				return
			}
			n := ny*w + nx
			if dist[n] != 0 || (alpha[n] == 0 && connected[n]) {
				return
			}
			dist[n] = dist[i] + 1
			queue = append(queue, n)
		}
		visit(x-1, y)
		visit(x+1, y)
		visit(x, y-1)
		visit(x, y+1)
	}
	// Drop the seeds and anything beyond the band.
	for i := range dist {
		if dist[i] <= 1 || dist[i] > radius+1 {
			dist[i] = 0
		} else {
			dist[i]--
		}
	}
	return dist
}

// floodBackdrop returns the backdrop mask: pixels reachable from the image
// border through backdrop-ish pixels, seeded ONLY from confident ones. Seeding
// from confident pixels is what stops a weak, ambiguous region on the frame
// edge from dragging the whole subject out.
func floodBackdrop(class []uint8, w, h int) []bool {
	const clsWeak, clsStrong = 1, 2
	out := make([]bool, w*h)
	queue := make([]int, 0, w*2+h*2)
	seed := func(x, y int) {
		i := y*w + x
		if class[i] == clsStrong && !out[i] {
			out[i] = true
			queue = append(queue, i)
		}
	}
	for x := range w {
		seed(x, 0)
		seed(x, h-1)
	}
	for y := range h {
		seed(0, y)
		seed(w-1, y)
	}
	for head := 0; head < len(queue); head++ {
		i := queue[head]
		x, y := i%w, i/w
		grow := func(nx, ny int) {
			if nx < 0 || ny < 0 || nx >= w || ny >= h {
				return
			}
			n := ny*w + nx
			if out[n] || (class[n] != clsStrong && class[n] != clsWeak) {
				return
			}
			out[n] = true
			queue = append(queue, n)
		}
		grow(x-1, y)
		grow(x+1, y)
		grow(x, y-1)
		grow(x, y+1)
	}
	return out
}

func touchesBackdrop(backdrop []bool, w, h, x, y int) bool {
	i := y*w + x
	return (x > 0 && backdrop[i-1]) || (x < w-1 && backdrop[i+1]) ||
		(y > 0 && backdrop[i-w]) || (y < h-1 && backdrop[i+w])
}
