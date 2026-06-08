package bfl

import (
	"context"
	"errors"
	"testing"

	"github.com/zakkriel/drchat-image-platform/internal/providers"
)

func TestPhase0CapabilitiesFloor(t *testing.T) {
	caps := New("test-key").Capabilities()

	if caps.ProviderID != ProviderID {
		t.Fatalf("provider id mismatch: %q", caps.ProviderID)
	}
	if caps.PreviewCapability != providers.PreviewCapabilityNone {
		t.Fatalf("BFL stub must advertise no_preview, got %q", caps.PreviewCapability)
	}
	if caps.SupportsHighRes {
		t.Fatalf("BFL stub must not claim high-res support")
	}
	if caps.MaxBatchSize != 1 {
		t.Fatalf("BFL stub must report MaxBatchSize=1, got %d", caps.MaxBatchSize)
	}
	if len(caps.Capabilities) != 1 || caps.Capabilities[0] != providers.CapabilityDraftOnly {
		t.Fatalf("BFL stub capabilities must be exactly [draft_only], got %v", caps.Capabilities)
	}
	// Belt-and-braces: catch the specific upgrades that require benchmark evidence.
	forbidden := map[providers.Capability]bool{
		providers.CapabilityIdentityCapable:   true,
		providers.CapabilityPackCapable:       true,
		providers.CapabilityProductionCapable: true,
		providers.CapabilitySceneCapable:      true,
	}
	for _, c := range caps.Capabilities {
		if forbidden[c] {
			t.Fatalf("BFL stub must not advertise %q before integration + benchmark evidence", c)
		}
	}
}

func TestStubMethodsReturnNotImplemented(t *testing.T) {
	p := New("test-key")
	ctx := context.Background()

	if _, err := p.Generate(ctx, providers.ProviderGenerateRequest{}); !errors.Is(err, providers.ErrNotImplemented) {
		t.Fatalf("Generate: expected ErrNotImplemented, got %v", err)
	}
	if _, err := p.PollStatus(ctx, "x"); !errors.Is(err, providers.ErrNotImplemented) {
		t.Fatalf("PollStatus: expected ErrNotImplemented, got %v", err)
	}
	if _, err := p.Upscale(ctx, providers.ProviderUpscaleRequest{}); !errors.Is(err, providers.ErrNotImplemented) {
		t.Fatalf("Upscale: expected ErrNotImplemented, got %v", err)
	}
}
