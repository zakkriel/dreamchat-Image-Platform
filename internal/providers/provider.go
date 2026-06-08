package providers

import (
	"context"
	"errors"
)

var (
	ErrNotImplemented = errors.New("provider: not implemented")
	ErrNotApplicable  = errors.New("provider: not applicable")
)

type PreviewCapability string

const (
	PreviewCapabilityTrue    PreviewCapability = "true_preview"
	PreviewCapabilityDerived PreviewCapability = "derived_preview"
	PreviewCapabilityNone    PreviewCapability = "no_preview"
)

type Capability string

const (
	CapabilityDraftOnly         Capability = "draft_only"
	CapabilitySceneCapable      Capability = "scene_capable"
	CapabilityIdentityCapable   Capability = "identity_capable"
	CapabilityPackCapable       Capability = "pack_capable"
	CapabilityProductionCapable Capability = "production_capable"
)

type OperationType string

const (
	OperationTextToImage  OperationType = "text_to_image"
	OperationImageToImage OperationType = "image_to_image"
	OperationUpscale      OperationType = "upscale"
	OperationVariantPack  OperationType = "variant_pack"
	OperationEdit         OperationType = "edit"
)

type JobStatus string

const (
	JobStatusQueued       JobStatus = "queued"
	JobStatusRunning      JobStatus = "running"
	JobStatusPreviewReady JobStatus = "preview_ready"
	JobStatusCompleted    JobStatus = "completed"
	JobStatusFailed       JobStatus = "failed"
	JobStatusCancelled    JobStatus = "cancelled"
)

type ProviderCapabilities struct {
	ProviderID        string
	ModelName         string
	Capabilities      []Capability
	PreviewCapability PreviewCapability
	SupportsHighRes   bool
	MaxBatchSize      int
	SupportedAspects  []string
}

type ProviderGenerateRequest struct {
	JobID          string
	Operation      OperationType
	Prompt         string
	NegativePrompt string
	Seed           string
	AspectRatio    string
	Width          int
	Height         int
	ReferenceURLs  []string
	Metadata       map[string]any
}

type ProviderUpscaleRequest struct {
	JobID       string
	SourceURL   string
	ScaleFactor int
	Metadata    map[string]any
}

type ProviderImage struct {
	URL         string
	Bytes       []byte
	ContentType string
	Width       int
	Height      int
}

type ProviderGenerateResult struct {
	ProviderJobID string
	Status        JobStatus
	Images        []ProviderImage
	PromptHash    string
	Seed          string
	Metadata      map[string]any
}

type ProviderJobStatus struct {
	ProviderJobID string
	Status        JobStatus
	Images        []ProviderImage
	ErrorCode     string
	ErrorMessage  string
}

type ImageProvider interface {
	Generate(ctx context.Context, req ProviderGenerateRequest) (ProviderGenerateResult, error)
	PollStatus(ctx context.Context, providerJobID string) (ProviderJobStatus, error)
	Upscale(ctx context.Context, req ProviderUpscaleRequest) (ProviderGenerateResult, error)
	Capabilities() ProviderCapabilities
}
