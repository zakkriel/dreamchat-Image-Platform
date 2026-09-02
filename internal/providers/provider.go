package providers

import (
	"context"
	"errors"
)

var (
	ErrNotImplemented = errors.New("provider: not implemented")
	ErrNotApplicable  = errors.New("provider: not applicable")
	// ErrReferenceRequired is returned by a reference-conditioned provider's
	// Generate when the request carries no ReferenceURLs. It is the adapter-level
	// fail-closed guard that prevents a recurring-character provider from silently
	// producing a different character off a text prompt alone (PRD 03 §8).
	ErrReferenceRequired = errors.New("provider: reference image required")
	// ErrContentPolicyRejected marks a provider-side CONTENT-POLICY rejection
	// (e.g. BFL "Request Moderated" / "Content Moderated") as distinct from an
	// infrastructure/provider failure. The worker treats it as terminal for the
	// job and NEVER walks fallback routes past it: retrying the same content on
	// another route would silently circumvent the rejecting provider's policy
	// decision and bill additional attempts for a deterministic rejection. The
	// platform itself takes no content stance — the rejection is surfaced to the
	// caller verbatim as provider_content_rejected (docs/api/errors.md), never
	// sanitized or hidden.
	ErrContentPolicyRejected = errors.New("provider: content policy rejected")
	// ErrProviderUnpaid marks a BILLING refusal - the provider account has no
	// credit (BFL answers 402 on submit). Like a content rejection it is terminal
	// for the job: an unpaid account is still unpaid on the next sweep, so every
	// retry is a request spent to be told the same thing.
	//
	// WHY THIS IS ITS OWN CLASS. Measured 2026-09-01: 875 artifact jobs failed in
	// 24h, all `submit returned status 402`, each re-submitted by the world
	// backend's 2-minute reconciler. Those doomed submits consumed the whole
	// 1000-requests/hour token budget, and the asset READ path shares that budget -
	// so a billing problem on ONE provider blacked out every already-rendered
	// picture in the product, including those made by the other, fully-paid
	// provider. A refusal that cannot change must never be retried on a shared
	// budget.
	//
	// It stays distinct from ErrContentPolicyRejected because the remedy differs:
	// content needs a different prompt, this needs a payment. Collapsing them would
	// report "content rejected" for an unpaid invoice.
	ErrProviderUnpaid = errors.New("provider: account unpaid")
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
	// Synthetic marks a non-production provider (mock / fixture / test-only).
	// A synthetic provider may satisfy capability tests in dev/test, but it must
	// not make production readiness report that a real identity-capable provider
	// is configured (PRD 03 §8 readiness). Real provider adapters leave this
	// false.
	Synthetic bool
	// RequiresReferenceImage is true for a reference-conditioned provider that
	// CANNOT hold a recurring character from a text prompt alone — it must be given
	// one or more reference image URLs (Generate fails closed otherwise). The
	// worker honors this by gathering the identity's anchor/reference assets into
	// ProviderGenerateRequest.ReferenceURLs and failing the job clearly when none
	// exist, rather than silently generating a different character (PRD 03 §8 /
	// recurring-character consistency). Prompt-only providers (mock, BFL scene)
	// leave this false.
	RequiresReferenceImage bool
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
	// ProviderRequestID is the provider-side request identifier when the
	// adapter receives one. It is kept separate from ProviderJobID because
	// some providers expose a request id for billing and a different polling
	// job id.
	ProviderRequestID string
	Status            JobStatus
	Images            []ProviderImage
	PromptHash        string
	Seed              string
	Metadata          map[string]any
	// ActualCostUSD is optional provider-reported spend for this call. A nil
	// value means the provider did not report a billable amount; cost
	// finalization then falls back to the reserved estimate instead of
	// inventing an actual.
	ActualCostUSD *string
	CostCurrency  string
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
