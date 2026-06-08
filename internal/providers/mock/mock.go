package mock

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"sync"

	"github.com/zakkriel/drchat-image-platform/internal/providers"
)

const (
	ProviderID = "mock"
	ModelName  = "mock-v1"
)

type Provider struct {
	mu   sync.Mutex
	jobs map[string]providers.ProviderJobStatus
}

func New() *Provider {
	return &Provider{
		jobs: make(map[string]providers.ProviderJobStatus),
	}
}

func (p *Provider) Capabilities() providers.ProviderCapabilities {
	return providers.ProviderCapabilities{
		ProviderID: ProviderID,
		ModelName:  ModelName,
		Capabilities: []providers.Capability{
			providers.CapabilitySceneCapable,
			providers.CapabilityIdentityCapable,
			providers.CapabilityPackCapable,
			providers.CapabilityProductionCapable,
		},
		PreviewCapability: providers.PreviewCapabilityTrue,
		SupportsHighRes:   true,
		MaxBatchSize:      4,
		SupportedAspects:  []string{"1:1", "16:9", "9:16", "4:3", "3:4"},
	}
}

func (p *Provider) Generate(ctx context.Context, req providers.ProviderGenerateRequest) (providers.ProviderGenerateResult, error) {
	if err := ctx.Err(); err != nil {
		return providers.ProviderGenerateResult{}, err
	}

	width, height := dimensionsFor(req)
	img, err := deterministicPNG(width, height, req.Prompt, req.Seed)
	if err != nil {
		return providers.ProviderGenerateResult{}, fmt.Errorf("mock: render placeholder: %w", err)
	}

	providerJobID := "mock_" + hashKey(req.JobID, req.Prompt, req.Seed)
	result := providers.ProviderGenerateResult{
		ProviderJobID: providerJobID,
		Status:        providers.JobStatusCompleted,
		Images: []providers.ProviderImage{{
			Bytes:       img,
			ContentType: "image/png",
			Width:       width,
			Height:      height,
		}},
		PromptHash: hashPrompt(req.Prompt, req.NegativePrompt),
		Seed:       req.Seed,
		Metadata:   map[string]any{"provider": ProviderID, "model": ModelName},
	}

	p.mu.Lock()
	p.jobs[providerJobID] = providers.ProviderJobStatus{
		ProviderJobID: providerJobID,
		Status:        providers.JobStatusCompleted,
		Images:        result.Images,
	}
	p.mu.Unlock()

	return result, nil
}

func (p *Provider) PollStatus(ctx context.Context, providerJobID string) (providers.ProviderJobStatus, error) {
	if err := ctx.Err(); err != nil {
		return providers.ProviderJobStatus{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	status, ok := p.jobs[providerJobID]
	if !ok {
		return providers.ProviderJobStatus{}, fmt.Errorf("mock: unknown provider job id %q", providerJobID)
	}
	return status, nil
}

func (p *Provider) Upscale(ctx context.Context, req providers.ProviderUpscaleRequest) (providers.ProviderGenerateResult, error) {
	if err := ctx.Err(); err != nil {
		return providers.ProviderGenerateResult{}, err
	}
	scale := req.ScaleFactor
	if scale < 1 {
		scale = 2
	}
	width := 512 * scale
	height := 512 * scale
	img, err := deterministicPNG(width, height, req.SourceURL, fmt.Sprintf("upscale-%d", scale))
	if err != nil {
		return providers.ProviderGenerateResult{}, fmt.Errorf("mock: render upscale: %w", err)
	}
	providerJobID := "mock_upscale_" + hashKey(req.JobID, req.SourceURL, fmt.Sprintf("%d", scale))
	return providers.ProviderGenerateResult{
		ProviderJobID: providerJobID,
		Status:        providers.JobStatusCompleted,
		Images: []providers.ProviderImage{{
			Bytes:       img,
			ContentType: "image/png",
			Width:       width,
			Height:      height,
		}},
		Metadata: map[string]any{"provider": ProviderID, "model": ModelName, "operation": "upscale"},
	}, nil
}

func dimensionsFor(req providers.ProviderGenerateRequest) (int, int) {
	if req.Width > 0 && req.Height > 0 {
		return req.Width, req.Height
	}
	switch req.AspectRatio {
	case "16:9":
		return 1024, 576
	case "9:16":
		return 576, 1024
	case "4:3":
		return 1024, 768
	case "3:4":
		return 768, 1024
	default:
		return 512, 512
	}
}

// deterministicPNG produces a small PNG whose pixel data is deterministic for
// the same inputs. These are placeholder bytes so callers can exercise the
// upload + storage path end to end.
func deterministicPNG(width, height int, seedParts ...string) ([]byte, error) {
	h := sha256.New()
	for _, s := range seedParts {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	digest := h.Sum(nil)

	const cells = 8
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	cellW := width / cells
	cellH := height / cells
	if cellW == 0 {
		cellW = 1
	}
	if cellH == 0 {
		cellH = 1
	}
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			cx := x / cellW
			cy := y / cellH
			i := (cy*cells + cx) % len(digest)
			img.SetRGBA(x, y, color.RGBA{
				R: digest[i],
				G: digest[(i+11)%len(digest)],
				B: digest[(i+23)%len(digest)],
				A: 255,
			})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func hashKey(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func hashPrompt(prompt, negative string) string {
	h := sha256.New()
	h.Write([]byte(prompt))
	h.Write([]byte{0})
	h.Write([]byte(negative))
	return hex.EncodeToString(h.Sum(nil))
}
