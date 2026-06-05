# Provider Adapter Architecture

## Goal

No provider-specific logic should leak into the business layer.

The service must be able to add or replace image generation providers without changing API contracts.

## Interface

```go
type ImageProvider interface {
    Generate(ctx context.Context, req ProviderGenerateRequest) (ProviderGenerateResult, error)
    Upscale(ctx context.Context, req ProviderUpscaleRequest) (ProviderGenerateResult, error)
    GetStatus(ctx context.Context, providerJobID string) (ProviderJobStatus, error)
    Capabilities() ProviderCapabilities
}
```

## Provider Adapters

Initial adapters:

```txt
mock_provider
bfl_provider
replicate_provider or fal_provider if selected later
```

The mock provider is mandatory for local development and CI.

## Provider Router

The router decides which adapter to use.

Inputs:

- asset type
- quality tier
- latency tier
- style profile
- provider availability
- cost policy
- token/client limits
- `ProviderCapability` level required by the request (per PRD 03 §8 —
  e.g. `identity_capable` for recurring NPC packs)
- `PreviewCapability` of the provider model (per ADR-010 and PRD 06
  §3.0 — `true_preview` / `derived_preview` / `no_preview`); routes
  that promise preview-first UX require `true_preview`.

Example routing:

```txt
character portrait, standard, fast -> provider A
place scene, high_quality -> provider B
artifact icon, cheap -> provider C
```

## Error Normalization

Provider-specific errors should be converted into platform errors:

```txt
provider_timeout
provider_rate_limited
provider_content_rejected
provider_auth_failed
provider_capacity_error
provider_invalid_request
provider_unknown_error
```

## Circuit Breaker

The platform should track provider failures.

If a provider has too many recent failures, route away from it temporarily.

Not mandatory for MVP, but the adapter/router design should allow it.

## Provider Payload Isolation

Do not store raw provider payloads as primary data.

Allowed:

- store sanitized provider request metadata
- store provider request ID
- store provider response summary
- store full raw payload only in protected debug logs if needed

## Provider Secrets

Provider API keys must be loaded from environment or secret manager.

Never store provider secrets in DB.
Never log provider secrets.

---

## Confidence to Implement

**Score: 82/100 — High**

The Go interface is small and right. Mock adapter is trivial. Error normalization vocabulary is complete. Risks are real-adapter-specific: each provider has its own async/polling shape, its own way of passing reference images (URL vs. base64 vs. asset upload), its own seed semantics. The interface's `Generate(ProviderGenerateRequest)` will need to grow when reference-image conditioning is added. Circuit breaker is correctly deferred. The router-decision policy is the underspecified piece.
