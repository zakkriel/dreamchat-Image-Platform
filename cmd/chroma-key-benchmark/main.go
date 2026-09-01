// Command chroma-key-benchmark answers the one question the chroma-key path
// cannot answer offline: when we ask a real model for a flat magenta backdrop,
// does it paint one, and does the key hold?
//
// It drives the REAL production path — the fal Kontext adapter, the same
// ChromaBackdropInstruction the worker appends, and the same
// imaging.ChromaKey — then reports the acceptance rate and writes every
// artifact for eye inspection. Nothing here is synthetic; without credentials
// it refuses to run rather than pretending.
//
// It costs real money: one Kontext render per sample.
//
//	FAL_KEY=... go run ./cmd/chroma-key-benchmark -samples 4 -out ./out
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "image/jpeg"

	"github.com/zakkriel/drchat-image-platform/internal/imaging"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
	"github.com/zakkriel/drchat-image-platform/internal/providers"
	"github.com/zakkriel/drchat-image-platform/internal/providers/fal"
)

type sample struct {
	N              int     `json:"sample"`
	Prompt         string  `json:"prompt"`
	RequestID      string  `json:"provider_request_id"`
	RenderMs       int64   `json:"render_ms"`
	Width          int     `json:"width"`
	Height         int     `json:"height"`
	ContentType    string  `json:"content_type"`
	Keyed          bool    `json:"keyed"`
	Refusal        string  `json:"refusal,omitempty"`
	BorderCoverage float64 `json:"border_coverage"`
	InteriorKey    float64 `json:"interior_key_fraction"`
	SubjectNearKey float64 `json:"subject_near_key_fraction"`
	TransparentPct float64 `json:"transparent_pct"`
	PartialPixels  int     `json:"partial_pixels"`
	RenderFile     string  `json:"render_file"`
	KeyedFile      string  `json:"keyed_file,omitempty"`
	MattedFile     string  `json:"matted_file,omitempty"`
	MatteMs        int64   `json:"matte_ms,omitempty"`
	MatteErr       string  `json:"matte_error,omitempty"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "chroma-key-benchmark:", err)
		os.Exit(1)
	}
}

func run() error {
	anchor := flag.String("anchor", "", "reference image URL fal can fetch (required: Kontext is reference-conditioned)")
	samples := flag.Int("samples", 4, "number of renders")
	out := flag.String("out", "./chroma-bench", "artifact directory")
	compare := flag.Bool("compare-matting", true, "also run BiRefNet on the same render for a side-by-side")
	timeout := flag.Duration("timeout", 5*time.Minute, "per-render timeout")
	subjects := flag.String("subjects", "", "comma-separated subject prompts; defaults to a built-in bust set")
	flag.Parse()

	key := os.Getenv("FAL_KEY")
	if key == "" {
		return errors.New("FAL_KEY is not set: this benchmark makes real provider calls and will not fabricate a result")
	}
	if *anchor == "" {
		return errors.New("-anchor is required: FLUX.1 Kontext is reference-conditioned and fails closed without one")
	}
	prompts := defaultSubjects
	if strings.TrimSpace(*subjects) != "" {
		prompts = nil
		for _, p := range strings.Split(*subjects, ",") {
			if s := strings.TrimSpace(p); s != "" {
				prompts = append(prompts, s)
			}
		}
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}

	adapter := fal.New(key)
	matter := &jobs.FalBackgroundRemover{
		BaseURL: "https://fal.run", APIKey: key,
		Doer: &http.Client{Timeout: 120 * time.Second},
	}

	results := make([]sample, 0, *samples)
	accepted := 0
	for i := range *samples {
		subject := prompts[i%len(prompts)]
		prompt := subject + "\n\n" + jobs.ChromaBackdropInstruction
		rec := sample{N: i + 1, Prompt: subject}

		ctx, cancel := context.WithTimeout(context.Background(), *timeout)
		started := time.Now()
		res, err := adapter.Generate(ctx, providers.ProviderGenerateRequest{
			JobID:         fmt.Sprintf("chromabench_%d", i+1),
			Operation:     providers.OperationTextToImage,
			Prompt:        prompt,
			ReferenceURLs: []string{*anchor},
			AspectRatio:   "1:1",
		})
		cancel()
		rec.RenderMs = time.Since(started).Milliseconds()
		if err != nil {
			fmt.Printf("sample %d: render FAILED: %v\n", i+1, err)
			rec.Refusal = "render failed: " + err.Error()
			results = append(results, rec)
			continue
		}
		if len(res.Images) == 0 || len(res.Images[0].Bytes) == 0 {
			rec.Refusal = "provider returned no bytes"
			results = append(results, rec)
			continue
		}
		img := res.Images[0]
		rec.RequestID, rec.ContentType = res.ProviderRequestID, img.ContentType
		rec.RenderFile = filepath.Join(*out, fmt.Sprintf("s%02d_render.png", i+1))
		if err := os.WriteFile(rec.RenderFile, img.Bytes, 0o644); err != nil {
			return err
		}

		decoded, _, decErr := image.Decode(strings.NewReader(string(img.Bytes)))
		if decErr != nil {
			rec.Refusal = "undecodable: " + decErr.Error()
			results = append(results, rec)
			continue
		}
		rec.Width, rec.Height = decoded.Bounds().Dx(), decoded.Bounds().Dy()

		keyed, report, keyErr := imaging.ChromaKey(decoded, imaging.DefaultChromaKeyOptions())
		rec.BorderCoverage = report.BorderCoverage
		rec.InteriorKey = report.InteriorKeyFraction
		rec.SubjectNearKey = report.SubjectNearKeyFraction
		rec.PartialPixels = report.PartialPixels
		if report.TotalPixels > 0 {
			rec.TransparentPct = 100 * float64(report.TransparentPixels) / float64(report.TotalPixels)
		}
		if keyErr != nil {
			rec.Refusal = keyErr.Error()
			fmt.Printf("sample %d: KEY REFUSED (%v) border=%.3f nearKey=%.4f\n",
				i+1, keyErr, report.BorderCoverage, report.SubjectNearKeyFraction)
		} else {
			rec.Keyed = true
			accepted++
			rec.KeyedFile = filepath.Join(*out, fmt.Sprintf("s%02d_keyed.png", i+1))
			f, _ := os.Create(rec.KeyedFile)
			_ = png.Encode(f, keyed)
			_ = f.Close()
			chk := filepath.Join(*out, fmt.Sprintf("s%02d_keyed_on_checker.png", i+1))
			cf, _ := os.Create(chk)
			_ = png.Encode(cf, checker(keyed))
			_ = cf.Close()
			fmt.Printf("sample %d: KEYED border=%.3f transparent=%.1f%% partial=%d nearKey=%.4f (%dms)\n",
				i+1, report.BorderCoverage, rec.TransparentPct, report.PartialPixels, report.SubjectNearKeyFraction, rec.RenderMs)
		}

		if *compare {
			mctx, mcancel := context.WithTimeout(context.Background(), *timeout)
			mstart := time.Now()
			matted, mErr := matter.Remove(mctx, img)
			mcancel()
			rec.MatteMs = time.Since(mstart).Milliseconds()
			if mErr != nil {
				rec.MatteErr = mErr.Error()
			} else {
				rec.MattedFile = filepath.Join(*out, fmt.Sprintf("s%02d_matted.png", i+1))
				_ = os.WriteFile(rec.MattedFile, matted.Bytes, 0o644)
				if dec, _, e := image.Decode(strings.NewReader(string(matted.Bytes))); e == nil {
					mc := filepath.Join(*out, fmt.Sprintf("s%02d_matted_on_checker.png", i+1))
					mf, _ := os.Create(mc)
					_ = png.Encode(mf, checker(dec))
					_ = mf.Close()
				}
			}
		}
		results = append(results, rec)
	}

	blob, _ := json.MarshalIndent(map[string]any{
		"samples":           results,
		"accepted":          accepted,
		"total":             len(results),
		"acceptance_rate":   float64(accepted) / float64(max(len(results), 1)),
		"kontext_usd_spent": 0.04 * float64(len(results)),
	}, "", "  ")
	_ = os.WriteFile(filepath.Join(*out, "summary.json"), append(blob, '\n'), 0o644)
	fmt.Printf("\naccepted %d/%d (%.0f%%)  approx spend $%.2f on renders\n",
		accepted, len(results), 100*float64(accepted)/float64(max(len(results), 1)), 0.04*float64(len(results)))
	fmt.Printf("artifacts: %s\n", *out)
	return nil
}

var defaultSubjects = []string{
	"a stern dwarf blacksmith, bust portrait, neutral expression, facing forward",
	"a young elven scout, bust portrait, warm smile, facing forward",
	"an old human innkeeper, bust portrait, surprised expression, facing forward",
	"a hooded rogue, bust portrait, suspicious expression, facing forward",
}

func checker(src image.Image) image.Image {
	b := src.Bounds()
	dst := image.NewRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			v := uint8(210)
			if (x/24+y/24)%2 == 0 {
				v = 245
			}
			dst.Set(x, y, colorGray(v))
		}
	}
	drawOver(dst, src)
	return dst
}

func colorGray(v uint8) color.RGBA { return color.RGBA{R: v, G: v, B: v, A: 255} }

func drawOver(dst *image.RGBA, src image.Image) {
	draw.Draw(dst, dst.Bounds(), src, src.Bounds().Min, draw.Over)
}
