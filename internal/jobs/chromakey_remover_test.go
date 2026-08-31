package jobs

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/zakkriel/drchat-image-platform/internal/imaging"
	"github.com/zakkriel/drchat-image-platform/internal/providers"
)

type recordingRemover struct {
	calls int
	out   providers.ProviderImage
	err   error
}

func (r *recordingRemover) Remove(context.Context, providers.ProviderImage) (providers.ProviderImage, error) {
	r.calls++
	return r.out, r.err
}

func pngImage(t *testing.T, w, h int, paint func(x, y int) color.RGBA) providers.ProviderImage {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, paint(x, y))
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode fixture: %v", err)
	}
	return providers.ProviderImage{Bytes: buf.Bytes(), ContentType: "image/png", Width: w, Height: h}
}

// The whole point of the cheap path: a cooperative render costs no provider
// call at all.
func TestChromaKeyRemoverKeysLocallyWithoutCallingTheProvider(t *testing.T) {
	subject := image.Rect(10, 10, 30, 30)
	src := pngImage(t, 40, 40, func(x, y int) color.RGBA {
		if image.Pt(x, y).In(subject) {
			return color.RGBA{R: 20, G: 140, B: 90, A: 255}
		}
		return color.RGBA(imaging.DefaultChromaKey)
	})

	fallback := &recordingRemover{}
	remover := &ChromaKeyRemover{Fallback: fallback}

	out, err := remover.Remove(context.Background(), src)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if fallback.calls != 0 {
		t.Fatalf("a keyable render must not reach the provider, got %d calls", fallback.calls)
	}
	if out.ContentType != "image/png" {
		t.Fatalf("expected png output, got %q", out.ContentType)
	}
	decoded, err := png.Decode(bytes.NewReader(out.Bytes))
	if err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if _, _, _, a := decoded.At(1, 1).RGBA(); a != 0 {
		t.Fatalf("backdrop must be transparent, got alpha %d", a>>8)
	}
	if _, _, _, a := decoded.At(20, 20).RGBA(); a>>8 != 255 {
		t.Fatalf("subject must be opaque, got alpha %d", a>>8)
	}
}

// When the model ignored the backdrop instruction, the matting model must get
// the ORIGINAL render — not a half-keyed one.
func TestChromaKeyRemoverFallsBackOnRefusalWithOriginalBytes(t *testing.T) {
	src := pngImage(t, 30, 30, func(int, int) color.RGBA {
		return color.RGBA{R: 90, G: 110, B: 130, A: 255}
	})
	expected := providers.ProviderImage{Bytes: []byte("matted"), ContentType: "image/png"}
	fallback := &recordingRemover{out: expected}
	remover := &ChromaKeyRemover{Fallback: fallback}

	out, err := remover.Remove(context.Background(), src)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if fallback.calls != 1 {
		t.Fatalf("expected exactly one fallback call, got %d", fallback.calls)
	}
	if string(out.Bytes) != "matted" {
		t.Fatalf("expected the fallback's output, got %q", out.Bytes)
	}
}

// A subject whose colours crowd the key must go to the matting model rather
// than ship a silhouette the key cannot be trusted to find.
func TestChromaKeyRemoverFallsBackWhenSubjectCrowdsTheKey(t *testing.T) {
	subject := image.Rect(5, 5, 35, 35)
	src := pngImage(t, 40, 40, func(x, y int) color.RGBA {
		if image.Pt(x, y).In(subject) {
			return color.RGBA{R: 150, G: 60, B: 200, A: 255} // purple: ~79 from magenta
		}
		return color.RGBA(imaging.DefaultChromaKey)
	})
	fallback := &recordingRemover{out: providers.ProviderImage{Bytes: []byte("matted")}}
	remover := &ChromaKeyRemover{Fallback: fallback}

	if _, err := remover.Remove(context.Background(), src); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if fallback.calls != 1 {
		t.Fatalf("a key-crowding subject must fall back, got %d calls", fallback.calls)
	}
}

// Without a fallback a refusal has to fail the cell. Shipping the opaque render
// would break the transparent promise, which is the one thing this path exists
// to guarantee.
func TestChromaKeyRemoverFailsClosedWithoutFallback(t *testing.T) {
	src := pngImage(t, 20, 20, func(int, int) color.RGBA {
		return color.RGBA{R: 90, G: 110, B: 130, A: 255}
	})
	remover := &ChromaKeyRemover{}
	if _, err := remover.Remove(context.Background(), src); err == nil {
		t.Fatal("expected a refused key with no fallback to fail")
	} else if !errors.Is(err, errBackgroundRemoval) {
		t.Fatalf("failure must carry the background-removal code, got %v", err)
	}
}

// A corrupt render must not be keyed into nonsense; it goes to the fallback.
func TestChromaKeyRemoverFallsBackOnUndecodableInput(t *testing.T) {
	fallback := &recordingRemover{out: providers.ProviderImage{Bytes: []byte("matted")}}
	remover := &ChromaKeyRemover{Fallback: fallback}
	_, err := remover.Remove(context.Background(), providers.ProviderImage{Bytes: []byte("not an image")})
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if fallback.calls != 1 {
		t.Fatalf("undecodable input must fall back, got %d calls", fallback.calls)
	}
}

// The backdrop instruction is only correct when the key runs locally. With the
// hosted matting model configured, adding it would hand the matter a saturated
// magenta backdrop nobody asked for.
func TestChromaBackdropInstructionOnlyWhenKeyingLocally(t *testing.T) {
	keying := &Worker{Background: &ChromaKeyRemover{}}
	if !keying.chromaKeyEnabled() {
		t.Fatal("a ChromaKeyRemover must enable the backdrop instruction")
	}

	matting := &Worker{Background: &recordingRemover{}}
	if matting.chromaKeyEnabled() {
		t.Fatal("a matting remover must NOT get the backdrop instruction")
	}

	none := &Worker{}
	if none.chromaKeyEnabled() {
		t.Fatal("no remover must not enable the backdrop instruction")
	}
}

// The instruction is appended, never substituted: the caller's subject prose
// has to survive intact.
func TestComposeChromaBackdropPreservesCallerPrompt(t *testing.T) {
	got := composeChromaBackdrop("a stern dwarf, arms crossed")
	if !strings.Contains(got, "a stern dwarf, arms crossed") {
		t.Fatalf("caller prompt was lost: %q", got)
	}
	if !strings.Contains(got, "#FF00FF") {
		t.Fatalf("backdrop instruction missing: %q", got)
	}
	if !strings.HasPrefix(got, "a stern dwarf") {
		t.Fatalf("the caller's prose must lead the prompt: %q", got)
	}
	if got := composeChromaBackdrop("   "); got != ChromaBackdropInstruction {
		t.Fatalf("an empty prompt must yield just the instruction, got %q", got)
	}
}

// The fallback must despill what the matting model returns: the render was made
// against a magenta backdrop, and a matting model corrects the silhouette but
// not the colour bleeding through the hair.
func TestChromaKeyRemoverDespillsTheFallbackResult(t *testing.T) {
	// A render with no keyable backdrop, so the key refuses and falls back.
	src := pngImage(t, 30, 30, func(int, int) color.RGBA {
		return color.RGBA{R: 90, G: 110, B: 130, A: 255}
	})

	// The matting model returns a cutout whose translucent edge is magenta-
	// contaminated - exactly what BiRefNet does on a magenta-backdrop render.
	matted := image.NewNRGBA(image.Rect(0, 0, 20, 20))
	for y := range 20 {
		for x := range 20 {
			switch {
			case x >= 6 && x < 14 && y >= 6 && y < 14:
				matted.SetNRGBA(x, y, color.NRGBA{R: 40, G: 120, B: 80, A: 255})
			case x >= 5 && x < 15 && y >= 5 && y < 15:
				matted.SetNRGBA(x, y, color.NRGBA{R: 200, G: 90, B: 210, A: 120})
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, matted); err != nil {
		t.Fatal(err)
	}
	fallback := &recordingRemover{out: providers.ProviderImage{Bytes: buf.Bytes(), ContentType: "image/png"}}
	remover := &ChromaKeyRemover{Fallback: fallback}

	out, err := remover.Remove(context.Background(), src)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	decoded, err := png.Decode(bytes.NewReader(out.Bytes))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := color.NRGBAModel.Convert(decoded.At(5, 10)).(color.NRGBA)
	if got.A != 120 {
		t.Fatalf("the matting model's alpha must survive despill, got %d", got.A)
	}
	before := 200 + 210 - 2*90
	after := int(got.R) + int(got.B) - 2*int(got.G)
	if after >= before {
		t.Fatalf("fallback result was not despilled: %d vs %d", after, before)
	}
}
