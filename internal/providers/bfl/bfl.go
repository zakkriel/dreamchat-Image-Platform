package bfl

import (
	"context"

	"github.com/zakkriel/drchat-image-platform/internal/providers"
)

const (
	ProviderID = "bfl"
	ModelName  = "flux"
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
			providers.CapabilitySceneCapable,
			providers.CapabilityIdentityCapable,
			providers.CapabilityProductionCapable,
		},
		PreviewCapability: providers.PreviewCapabilityDerived,
		SupportsHighRes:   true,
		MaxBatchSize:      1,
		SupportedAspects:  []string{"1:1", "16:9", "9:16", "4:3", "3:4"},
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
