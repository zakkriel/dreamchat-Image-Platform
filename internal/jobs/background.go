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
	"io"
	"net/http"
	"strings"
	"time"

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

// falBirefnetPath is fal's BiRefNet segmentation endpoint; the "Portrait" model is tuned for
// people, which is what every emotion-pack variant is.
const falBirefnetPath = "/fal-ai/birefnet"

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
	body, err := json.Marshal(map[string]any{
		"image_url":     "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(image.Bytes),
		"model":         "Portrait",
		"output_format": "png",
		"sync_mode":     true,
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
	pngBytes, err := f.fetchResult(ctx, parsed.Image.URL)
	if err != nil {
		return providers.ProviderImage{}, err
	}
	return providers.ProviderImage{Bytes: pngBytes, ContentType: "image/png"}, nil
}

// fetchResult resolves the result image: sync_mode answers with a data URI, but the contract also
// permits a hosted URL, so both are handled rather than assumed.
func (f *FalBackgroundRemover) fetchResult(ctx context.Context, url string) ([]byte, error) {
	if strings.HasPrefix(url, "data:") {
		_, b64, ok := strings.Cut(url, ",")
		if !ok {
			return nil, fmt.Errorf("malformed data URI in birefnet response")
		}
		decoded, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("decode birefnet data URI: %w", err)
		}
		return decoded, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build birefnet download: %w", err)
	}
	res, err := f.Doer.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download birefnet result: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("birefnet download status %d", res.StatusCode)
	}
	return io.ReadAll(io.LimitReader(res.Body, 64<<20))
}
