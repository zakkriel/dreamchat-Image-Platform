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
