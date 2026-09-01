package jobs

// Background removal — the worker-side post-step behind `background: "transparent"` on character
// packs (the visual-novel sprite contract). It runs AFTER the provider render and BEFORE
// imaging.EncodeTiers, so every stored tier carries the same real alpha; EncodeTiers already
// preserves alpha through its RGBA resize.
//
// The remover is an interface for the same reason providers are: the fal implementation makes a
// network call, and every test that exercises the transparent path stubs it. Nil is a configured
// absence — a transparent job with no remover fails closed with its own error code rather than
// shipping an opaque image under a transparent promise.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/zakkriel/drchat-image-platform/internal/imaging"
	"github.com/zakkriel/drchat-image-platform/internal/providers"
)

const errorCodeBackgroundRemoval = "background_removal_failed"

// errBackgroundRemoval wraps every failure of the removal step under the one code the pack item
// records, so a blank variant can explain itself.
var errBackgroundRemoval = fmt.Errorf("%s", errorCodeBackgroundRemoval)

// BackgroundRemover strips the background from one rendered image, returning a PNG with a real
// alpha channel.
type BackgroundRemover interface {
	Remove(ctx context.Context, image providers.ProviderImage) (providers.ProviderImage, error)
}

// removeBackgrounds applies the configured remover to every provider image, failing closed when
// none is wired: transparency was promised on the request, and silently keeping the background
// would be the exact class of quiet lie the pack's per-variant error codes exist to prevent.
func (w *Worker) removeBackgrounds(ctx context.Context, images []providers.ProviderImage) ([]providers.ProviderImage, error) {
	if w.Background == nil {
		return nil, fmt.Errorf("%w: no background remover configured (transparent packs need FAL_KEY)", errBackgroundRemoval)
	}
	out := make([]providers.ProviderImage, 0, len(images))
	for i, img := range images {
		cleaned, err := w.Background.Remove(ctx, img)
		if err != nil {
			return nil, fmt.Errorf("%w: image %d: %v", errBackgroundRemoval, i, err)
		}
		out = append(out, cleaned)
	}
	return out, nil
}

// falBirefnetPath is fal's BiRefNet endpoint. v2 is a superset of v1 and is the endpoint the
// BiRefNet author points at: it exposes the weight choice, the operating resolution, and
// mask_only, where v1 fixed all three.
//
// The alternatives are not upgrades. rembg, BRIA and Photoroom are all built on or around
// BiRefNet itself, and BRIA's RMBG-2.0 is gated with license "other" and self-tagged "legal
// liability" against BiRefNet's MIT. What was available here was a CONFIGURATION upgrade, not a
// better model.
const falBirefnetPath = "/fal-ai/birefnet/v2"

// BiRefNet weights exposed by the v2 endpoint. Portrait is trained on P3M-10k portraits; Matting
// is trained specifically for matting. Which one wins on a bust crop is an empirical question,
// which is exactly why it is configurable rather than hard-coded.
const (
	BirefnetModelPortrait  = "Portrait"
	BirefnetModelMatting   = "Matting"
	BirefnetModelDynamic   = "General Use (Dynamic)"
	BirefnetModelLight     = "General Use (Light)"
	BirefnetModelLight2K   = "General Use (Light 2K)"
	BirefnetModelHeavy     = "General Use (Heavy)"
	birefnetResolutionHigh = "2304x2304"
)

// FalBackgroundRemover calls fal.ai BiRefNet synchronously (fal.run host). The image travels as a
// data URI — fal accepts data URIs for file inputs platform-wide — so no intermediate upload
// exists, and sync_mode returns the result the same way, so nothing persists on their side to
// expire or leak.
type FalBackgroundRemover struct {
	// BaseURL is the sync host, e.g. "https://fal.run". Required.
	BaseURL string
	// APIKey authorizes as `Authorization: Key <APIKey>`. Required.
	APIKey string
	// Doer is the minimal HTTP surface; *http.Client satisfies it. Required.
	Doer interface {
		Do(*http.Request) (*http.Response, error)
	}
	// Timeout bounds one removal call. Zero falls back to a built-in default.
	Timeout time.Duration
	// Model selects the BiRefNet weight. Empty means Portrait, which is what
	// this platform has always sent - a silent change of weight would change
	// every cutout without any record of why.
	Model string
	// OperatingResolution is the resolution BiRefNet works at. Empty means
	// 1024x1024, fal's default and the previous behaviour. Higher resolves
	// finer edges - hair - on high-res input, at more compute.
	OperatingResolution string
}

const falRemovalDefaultTimeout = 60 * time.Second

func (f *FalBackgroundRemover) Remove(ctx context.Context, image providers.ProviderImage) (providers.ProviderImage, error) {
	timeout := f.Timeout
	if timeout <= 0 {
		timeout = falRemovalDefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	contentType := image.ContentType
	if contentType == "" {
		contentType = "image/png"
	}
	model := f.Model
	if model == "" {
		model = BirefnetModelPortrait
	}
	resolution := f.OperatingResolution
	if resolution == "" {
		resolution = "1024x1024"
	}
	// fal restricts 2304x2304 to the dynamic-resolution weight. Catching it here
	// turns a remote 4xx mid-pack into a configuration error at the call site.
	if resolution == birefnetResolutionHigh && model != BirefnetModelDynamic {
		return providers.ProviderImage{}, fmt.Errorf("%w: operating resolution %s requires model %q, got %q",
			errBackgroundRemoval, birefnetResolutionHigh, BirefnetModelDynamic, model)
	}
	body, err := json.Marshal(map[string]any{
		"image_url":            "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(image.Bytes),
		"model":                model,
		"operating_resolution": resolution,
		// The foreground-colour estimator is what removes the halo of background
		// colour left around hair by the raw mask. It defaults true remotely;
		// sent explicitly so the cutout does not silently change if that default
		// ever moves.
		"refine_foreground": true,
		"output_format":     "png",
		"sync_mode":         true,
	})
	if err != nil {
		return providers.ProviderImage{}, fmt.Errorf("marshal birefnet request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(f.BaseURL, "/")+falBirefnetPath, bytes.NewReader(body))
	if err != nil {
		return providers.ProviderImage{}, fmt.Errorf("build birefnet request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Key "+f.APIKey)

	res, err := f.Doer.Do(req)
	if err != nil {
		return providers.ProviderImage{}, fmt.Errorf("birefnet request: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if err != nil {
		return providers.ProviderImage{}, fmt.Errorf("read birefnet response: %w", err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		snippet := string(raw)
		if len(snippet) > 300 {
			snippet = snippet[:300]
		}
		return providers.ProviderImage{}, fmt.Errorf("birefnet status %d: %s", res.StatusCode, snippet)
	}
	var parsed struct {
		Image struct {
			URL         string `json:"url"`
			ContentType string `json:"content_type"`
		} `json:"image"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return providers.ProviderImage{}, fmt.Errorf("parse birefnet response: %w", err)
	}
	if parsed.Image.URL == "" {
		return providers.ProviderImage{}, fmt.Errorf("birefnet response carried no image")
	}
	pngBytes, err := fetchRemovalResult(ctx, f.Doer, parsed.Image.URL)
	if err != nil {
		return providers.ProviderImage{}, err
	}
	return providers.ProviderImage{Bytes: pngBytes, ContentType: "image/png"}, nil
}

// fetchResult resolves the result image: sync_mode answers with a data URI, but the contract also
// permits a hosted URL, so both are handled rather than assumed.
// fetchRemovalResult reads a removal result, which fal returns either inline as
// a data URI (sync_mode) or as a URL to download. Shared by every fal-hosted
// remover because the answer shape is the same regardless of which model
// produced it.
func fetchRemovalResult(ctx context.Context, doer interface {
	Do(*http.Request) (*http.Response, error)
}, url string) ([]byte, error) {
	if strings.HasPrefix(url, "data:") {
		_, b64, ok := strings.Cut(url, ",")
		if !ok {
			return nil, fmt.Errorf("malformed data URI in removal response")
		}
		decoded, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("decode removal data URI: %w", err)
		}
		return decoded, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build removal download: %w", err)
	}
	res, err := doer.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download removal result: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("removal download status %d", res.StatusCode)
	}
	return io.ReadAll(io.LimitReader(res.Body, 64<<20))
}

// ChromaKeyRemover extracts alpha from a render made against a known flat
// backdrop, with NO provider call. It is the cheap path: the render already
// happened, and the backdrop is removed locally in microseconds.
//
// It is deliberately built as "cheap path, verified, with a fallback" rather
// than as a replacement. Keying a diffusion render is ambiguous by measurement,
// not by opinion: across every possible key hue, the nearest realistic subject
// colour sits 79-88 chroma units away while a half-covered edge pixel sits
// about 93, so the safe threshold is always narrower than a soft matte wants
// (see internal/imaging/chromakey.go). When imaging refuses - no flat backdrop,
// the subject contains the key, or the subject crowds it - this delegates to
// Fallback, which is the hosted matting model.
//
// Fallback is therefore not optional in production: without it a refused key
// fails the cell rather than costing one provider call.
type ChromaKeyRemover struct {
	// Options tunes the key; the zero value uses imaging defaults.
	Options *imaging.ChromaKeyOptions
	// Fallback handles renders the key refuses. Nil means a refusal is fatal
	// for the cell.
	Fallback BackgroundRemover
	Logger   *slog.Logger
}

func (c *ChromaKeyRemover) log() *slog.Logger {
	if c.Logger == nil {
		return slog.Default()
	}
	return c.Logger
}

func (c *ChromaKeyRemover) Remove(ctx context.Context, img providers.ProviderImage) (providers.ProviderImage, error) {
	opts := imaging.DefaultChromaKeyOptions()
	if c.Options != nil {
		opts = *c.Options
	}
	decoded, _, err := image.Decode(bytes.NewReader(img.Bytes))
	if err != nil {
		return c.fallback(ctx, img, "undecodable render", err)
	}
	keyed, report, keyErr := imaging.ChromaKey(decoded, opts)
	if keyErr != nil {
		return c.fallback(ctx, img, "key refused", keyErr,
			"border_coverage", report.BorderCoverage,
			"interior_key_fraction", report.InteriorKeyFraction,
			"subject_near_key_fraction", report.SubjectNearKeyFraction)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, keyed); err != nil {
		return c.fallback(ctx, img, "keyed encode failed", err)
	}
	c.log().Info("worker: background removed by chroma key (no provider call)",
		"transparent_pixels", report.TransparentPixels,
		"partial_pixels", report.PartialPixels,
		"border_coverage", report.BorderCoverage)
	return providers.ProviderImage{
		Bytes:       buf.Bytes(),
		ContentType: "image/png",
		Width:       keyed.Bounds().Dx(),
		Height:      keyed.Bounds().Dy(),
	}, nil
}

// fallback hands the ORIGINAL render to the matting model. The partially keyed
// result is deliberately discarded: mixing a refused key into the fallback's
// input would feed it an image the key already damaged.
func (c *ChromaKeyRemover) fallback(ctx context.Context, img providers.ProviderImage, reason string, cause error, fields ...any) (providers.ProviderImage, error) {
	if c.Fallback == nil {
		return providers.ProviderImage{}, fmt.Errorf("%w: chroma key %s and no fallback remover is configured: %v", errBackgroundRemoval, reason, cause)
	}
	c.log().Info("worker: chroma key fell back to the matting provider",
		append([]any{"reason", reason, "cause", cause.Error()}, fields...)...)
	matted, err := c.Fallback.Remove(ctx, img)
	if err != nil {
		return providers.ProviderImage{}, err
	}
	return c.despillMatte(matted), nil
}

// despillMatte cleans key-colour spill out of the matting model's cutout.
//
// A matting model solves the silhouette and stops there - it decides which
// pixels are subject, not what colour they should be - so a subject rendered
// against a magenta backdrop comes back correctly cut out and still tinted
// magenta through the hair. Measured on real renders, this removes about half
// the contamination in the translucent edge region.
//
// It only runs on this path because this path is the only one that renders
// against a known backdrop colour in the first place. Failure is not fatal: the
// matte is already correct, so a despill that cannot be applied leaves it
// exactly as the provider returned it.
func (c *ChromaKeyRemover) despillMatte(matted providers.ProviderImage) providers.ProviderImage {
	opts := imaging.DefaultChromaKeyOptions()
	if c.Options != nil {
		opts = *c.Options
	}
	decoded, _, err := image.Decode(bytes.NewReader(matted.Bytes))
	if err != nil {
		return matted
	}
	cleaned, err := imaging.DespillMatte(decoded, opts)
	if err != nil {
		return matted
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, cleaned); err != nil {
		return matted
	}
	return providers.ProviderImage{
		Bytes:       buf.Bytes(),
		ContentType: "image/png",
		Width:       cleaned.Bounds().Dx(),
		Height:      cleaned.Bounds().Dy(),
	}
}

// ChromaBackdropInstruction is appended to a transparent pack cell's prompt
// when chroma keying is enabled. It is a RENDERING instruction, composed the
// same way the style profile's prose already is - the platform still never
// inspects or rewrites what the caller wrote about the subject.
//
// It is explicit about flatness because the failure mode is a backdrop the
// model shades or textures: keying needs one hue, evenly applied, with no
// gradient and no cast shadow. The caller already asked for a transparent
// result, so the platform owns the backdrop entirely.
const ChromaBackdropInstruction = "The background must be a completely flat, uniform, solid magenta " +
	"(hex #FF00FF) chroma-key backdrop: a single even colour with no gradient, " +
	"no shading, no texture, no vignette, and no shadows cast onto it. The " +
	"subject must not be magenta and must not touch the edges of the frame."

// composeChromaBackdrop appends the backdrop instruction to an already composed
// prompt. It goes last so it reads as a trailing directive rather than
// competing with the subject prose the caller authored.
func composeChromaBackdrop(prompt string) string {
	if strings.TrimSpace(prompt) == "" {
		return ChromaBackdropInstruction
	}
	return prompt + "\n\n" + ChromaBackdropInstruction
}

// chromaKeyEnabled reports whether the configured remover keys locally, which
// is what makes the backdrop instruction correct to add. Asking the model for a
// magenta backdrop when the remover is a matting model would actively hurt: it
// would hand the matter a saturated backdrop it never asked for.
func (w *Worker) chromaKeyEnabled() bool {
	_, ok := w.Background.(*ChromaKeyRemover)
	return ok
}

// falIdeogramRemoveBGPath is Ideogram's background removal on fal.
//
// Chosen over BiRefNet for one reason: it has a PRICE. fal publishes $0.01 per
// request for this endpoint and publishes nothing at all for BiRefNet, which
// renders "$0 per compute seconds" on its model card and is absent from the
// pricing page. An unpriced call cannot be reserved against a budget, cannot be
// reconciled, and cannot appear honestly in cost-per-usable-image - so the
// cheaper-looking option was the one we could not actually account for.
const falIdeogramRemoveBGPath = "/fal-ai/ideogram/remove-background"

// IdeogramRemoveBackgroundUSD is the published per-request price.
const IdeogramRemoveBackgroundUSD = "0.0100"

// FalIdeogramBackgroundRemover strips the background through Ideogram on fal at
// a known $0.01 per request. It speaks the same data-URI-in, image.url-out shape
// as the BiRefNet remover, so it is a drop-in swap.
type FalIdeogramBackgroundRemover struct {
	// BaseURL is the sync host, e.g. "https://fal.run". Required.
	BaseURL string
	// APIKey authorizes as `Authorization: Key <APIKey>`. Required.
	APIKey string
	// Doer is the minimal HTTP surface; *http.Client satisfies it. Required.
	Doer interface {
		Do(*http.Request) (*http.Response, error)
	}
	// Timeout bounds one removal call. Zero falls back to a built-in default.
	Timeout time.Duration
}

func (f *FalIdeogramBackgroundRemover) Remove(ctx context.Context, image providers.ProviderImage) (providers.ProviderImage, error) {
	timeout := f.Timeout
	if timeout <= 0 {
		timeout = falRemovalDefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	contentType := image.ContentType
	if contentType == "" {
		contentType = "image/png"
	}
	body, err := json.Marshal(map[string]any{
		"image_url": "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(image.Bytes),
		"sync_mode": true,
	})
	if err != nil {
		return providers.ProviderImage{}, fmt.Errorf("marshal ideogram request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(f.BaseURL, "/")+falIdeogramRemoveBGPath, bytes.NewReader(body))
	if err != nil {
		return providers.ProviderImage{}, fmt.Errorf("build ideogram request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Key "+f.APIKey)

	res, err := f.Doer.Do(req)
	if err != nil {
		return providers.ProviderImage{}, fmt.Errorf("ideogram request: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if err != nil {
		return providers.ProviderImage{}, fmt.Errorf("read ideogram response: %w", err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		snippet := string(raw)
		if len(snippet) > 300 {
			snippet = snippet[:300]
		}
		return providers.ProviderImage{}, fmt.Errorf("ideogram status %d: %s", res.StatusCode, snippet)
	}
	var parsed struct {
		Image struct {
			URL         string `json:"url"`
			ContentType string `json:"content_type"`
		} `json:"image"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return providers.ProviderImage{}, fmt.Errorf("parse ideogram response: %w", err)
	}
	if parsed.Image.URL == "" {
		return providers.ProviderImage{}, fmt.Errorf("ideogram response carried no image")
	}
	pngBytes, err := fetchRemovalResult(ctx, f.Doer, parsed.Image.URL)
	if err != nil {
		return providers.ProviderImage{}, err
	}
	return providers.ProviderImage{Bytes: pngBytes, ContentType: "image/png"}, nil
}
