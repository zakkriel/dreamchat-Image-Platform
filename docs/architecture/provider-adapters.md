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

## Capability Reconciliation (PRD 03 §8)

Request-to-route matching is **exact** on `route.required_capability`: a
`scene_capable` request resolves only `scene_capable` routes and never collapses
onto an identity/pack route (that would route cheap scene work to expensive
identity providers).

Separately, the platform never trusts a route's claimed capability blindly. A
`provider_routes` row is mutable config and can claim a capability its provider
adapter does not actually support. So provider-satisfies-route is validated using
the §8.3 hierarchy (`production_capable` ⊇ `pack_capable` ⊇ `identity_capable`;
`scene_capable`/`draft_only` are parallel and satisfy only themselves):

- **At boot**, every route is reconciled against the registered adapters'
  `Capabilities()`; invalid routes are disabled with loud structured logs (route
  id, provider id, model id, required capability, provider capabilities,
  decision), and a readiness line reports whether a **real** (non-synthetic)
  identity-capable provider is configured.
- **At resolution**, the resolver re-applies the check as defense-in-depth and
  fails closed with `route_capability_mismatch` (HTTP 422) when the only matching
  route's provider cannot back its claimed capability.

The mock provider is synthetic: it may satisfy identity/pack routes only when
`ALLOW_SYNTHETIC_PROVIDERS=true`, and that flag defaults **false in every
environment** (safety does not key off `ENVIRONMENT`). Even when enabled, mock
never counts as a real identity-capable provider for readiness. BFL
`flux-pro-1.1` is `scene_capable` only — suitable for scenes/artifacts, not
recurring characters. Recurring character consistency requires a
reference/identity-capable provider; prompt-only retries do not solve recurring
identity. See `docs/adr/016-provider-capability-reconciliation.md`.

## Reference-conditioned providers (recurring characters)

The first **real** identity/pack-capable provider is **fal.ai** running
**FLUX.1 Kontext [pro] multi** (`fal-ai/flux-pro/kontext/multi`; `provider_id =
"fal"`, `model_name = "flux-pro-kontext-multi"`). It is reference-conditioned: it
takes a text prompt plus one or more reference image URLs (`image_urls`) and
renders the *same* subject in the prompted variation. It is registered only when
`FAL_KEY` is set, advertises `{scene_capable, identity_capable, pack_capable}`
(real, **not** `production_capable` until benchmarked), and is priced at
**$0.04/output image** (per-image, current schema). See
`docs/adr/017-reference-conditioned-provider.md` and the adapter doc comment in
`internal/providers/fal/fal.go`.

A provider that cannot hold a character from a prompt alone declares
`ProviderCapabilities.RequiresReferenceImage = true`. When the resolved provider
for a pack sets it, the worker gathers the visual identity's `anchor_asset_ids`,
mints a presigned high-res URL per anchor, and threads them into
`ProviderGenerateRequest.ReferenceURLs` for every role. **If the identity has no
reference assets, the pack fails closed** with `missing_reference_assets` — no
provider call, never a different character. Prompt-only providers (mock, BFL)
leave the flag false, so this path is a no-op for them.

Only the `pack_capable` fal route is seeded (migration `0011`): pack generation is
the recurring-character path wired with references in this slice. fal has no
`scene_capable` route (it requires references; scene/artifact requests carry
none), so BFL continues to serve all scene/artifact generation unchanged.

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
