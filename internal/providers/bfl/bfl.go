// Package bfl is the Phase 0 skeleton for the Black Forest Labs provider
// adapter. BFL is the first real provider target, but this adapter is Phase 0
// skeleton only: Generate, PollStatus, and Upscale return
// providers.ErrNotImplemented. Capabilities() advertises the
// provider-capability floor — draft_only with no preview and no high-res —
// and MUST only be upgraded after the real integration lands AND the
// benchmark pass produces evidence for the higher tier. Do not claim
// identity_capable, pack_capable, or production_capable here without that
// evidence.
//
// Planned aspect ratios once the integration ships (not advertised today):
//
//	1:1, 16:9, 9:16, 4:3, 3:4
package bfl

import (
	"context"

	"github.com/zakkriel/drchat-image-platform/internal/providers"
)

const (
	ProviderID = "bfl"
	// ModelName is intentionally generic until the integration picks a
	// concrete FLUX model variant.
	ModelName = "bfl-integration-pending"
)

type Provider struct {
	apiKey string
}

func New(apiKey string) *Provider {
	return &Provider{apiKey: apiKey}
}

func (p *Provider) Capabilities() providers.ProviderCapabilities {
	return providers.ProviderCapabilities{
		ProviderID: ProviderID,
		ModelName:  ModelName,
		Capabilities: []providers.Capability{
			providers.CapabilityDraftOnly,
		},
		PreviewCapability: providers.PreviewCapabilityNone,
		SupportsHighRes:   false,
		MaxBatchSize:      1,
		SupportedAspects:  nil,
	}
}

func (p *Provider) Generate(ctx context.Context, req providers.ProviderGenerateRequest) (providers.ProviderGenerateResult, error) {
	return providers.ProviderGenerateResult{}, providers.ErrNotImplemented
}

func (p *Provider) PollStatus(ctx context.Context, providerJobID string) (providers.ProviderJobStatus, error) {
	return providers.ProviderJobStatus{}, providers.ErrNotImplemented
}

func (p *Provider) Upscale(ctx context.Context, req providers.ProviderUpscaleRequest) (providers.ProviderGenerateResult, error) {
	return providers.ProviderGenerateResult{}, providers.ErrNotImplemented
}
