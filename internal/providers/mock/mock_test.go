package mock

import (
	"bytes"
	"context"
	"testing"

	"github.com/zakkriel/drchat-image-platform/internal/providers"
)

func TestGenerateIsDeterministic(t *testing.T) {
	p := New()
	req := providers.ProviderGenerateRequest{
		JobID:  "job_123",
		Prompt: "a tiny robot",
		Seed:   "42",
	}
	a, err := p.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("first generate: %v", err)
	}
	b, err := p.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("second generate: %v", err)
	}

	if a.ProviderJobID != b.ProviderJobID {
		t.Fatalf("provider job id should be deterministic: %s vs %s", a.ProviderJobID, b.ProviderJobID)
	}
	if a.PromptHash != b.PromptHash {
		t.Fatalf("prompt hash should be deterministic")
	}
	if len(a.Images) != 1 || len(b.Images) != 1 {
		t.Fatalf("expected one image per result")
	}
	if !bytes.Equal(a.Images[0].Bytes, b.Images[0].Bytes) {
		t.Fatalf("placeholder bytes should be deterministic for identical input")
	}
}

func TestGenerateRespondsToPromptChange(t *testing.T) {
	p := New()
	ctx := context.Background()
	a, err := p.Generate(ctx, providers.ProviderGenerateRequest{Prompt: "alpha", Seed: "s"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := p.Generate(ctx, providers.ProviderGenerateRequest{Prompt: "beta", Seed: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a.Images[0].Bytes, b.Images[0].Bytes) {
		t.Fatalf("different prompts should produce different bytes")
	}
}

func TestPollStatus(t *testing.T) {
	p := New()
	ctx := context.Background()
	res, err := p.Generate(ctx, providers.ProviderGenerateRequest{Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	status, err := p.PollStatus(ctx, res.ProviderJobID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != providers.JobStatusCompleted {
		t.Fatalf("expected completed, got %s", status.Status)
	}
}

func TestCapabilities(t *testing.T) {
	caps := New().Capabilities()
	if caps.ProviderID != ProviderID {
		t.Fatalf("provider id mismatch")
	}
	if caps.PreviewCapability != providers.PreviewCapabilityTrue {
		t.Fatalf("mock should advertise true_preview")
	}
}
