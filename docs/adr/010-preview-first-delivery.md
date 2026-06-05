# ADR-010 — Preview-first delivery

## Status

Accepted for initial implementation.

## Context

High-quality image generation takes 15–60 seconds with most providers, sometimes longer. DreamChat is a chat-first product where the user's expectation is "the next thing happens fast." Waiting for a final image before the conversation can continue breaks the play-first UX described in PRD 01 and PRD 06.

The platform needs to deliver *something usable* fast, and improve it later, without making the client orchestrate two separate requests.

## Decision

Use **provider-dependent preview capability**. Generation may produce a
**preview asset** (lower resolution, fast generation tier) before a
**final asset** (high resolution, normal tier) — but only when the
chosen provider supports a true preview path. The platform does not
claim preview-first UX unless the route's `preview_capability` is
`true_preview` (per PRD 06 §3.0 and the
`ProviderModel.preview_capability` field in
`docs/api/openapi.yaml`).

Three preview modes are recognized:

- `true_preview` — provider returns a fast preview before the final
  asset. Both belong to the same `generation_job`, exposed via
  `preview_asset_ids` and `final_asset_ids`. The job state machine
  passes through `preview_ready`. Clients render preview as soon as it
  lands and swap to final when it's ready. **This is the only mode
  where preview-first UX delivers a real latency win.**
- `derived_preview` — provider returns one final image and the platform
  downscales it for thumbnail / preview tiers. Browser rendering and
  bandwidth improve but generation wait time does not. The UI must not
  promise "preview coming soon."
- `no_preview` — no preview behavior; API returns progress state and a
  placeholder until the final asset lands.

The router (ADR-007) enforces route rules: interactive scene generation
prefers `true_preview` routes; pack generation tolerates
`derived_preview`; in either case, the platform never silently promises
a UX the provider cannot deliver.

## Alternatives considered

- **Always block until final image.** Simplest pipeline, one asset per
  job, no state machine complexity. UI feels slow during multi-second
  waits. Acceptable only when every supported provider returns finals
  in under ~5 seconds, which is not the case.
- **Always generate preview and final separately, regardless of
  provider.** Forces every provider to produce two assets. For
  `derived_preview` providers, the "preview" is just a downscaled final
  — useful for bandwidth but not for perceived latency, which makes the
  state machine more complex without the payoff. For `no_preview`
  providers, this is impossible.
- **Use provider-dependent preview capability** (chosen). Models the
  reality that providers vary, exposes a typed `preview_capability`
  enum so the router can enforce the contract, and lets the UI know
  which preview semantics to expect. Cost: one extra field on
  `ProviderModel` and route rules in the router.

Earlier alternatives, retained for context:

- **Single asset, progressive enhancement** (server-side upscale of one
  render). Works when the provider's draft pass produces a usable image;
  many providers do, but quality varies. This is essentially the
  `derived_preview` mode above.
- **Push final via SSE/WebSocket** instead of preview-then-poll. Faster
  perceived completion, doesn't change generation cost. Useful future
  enhancement; orthogonal to which preview mode applies.
- **Skip preview, show a deterministic placeholder.** Cheapest, but the
  placeholder has no relationship to the actual asset; users learn to
  ignore it. Essentially the `no_preview` mode above.

## Tradeoffs

- **+** UI feels responsive even on heavy backends — **for
  `true_preview` routes**.
- **+** Preview is independently cacheable and queryable (PRD 05 cache
  keys include `resolution_tier`).
- **+** Two-stage delivery is a natural seam for tier routing (cheap
  fast model for preview, premium model for final — ADR-007).
- **+** Failure isolation: preview can succeed while final fails;
  partial-success state is honest.
- **+** The router enforces honesty — `derived_preview` and `no_preview`
  routes don't pretend to deliver preview-first UX.
- **−** Three modes mean the client UI has three render paths
  (preview-then-final, single-asset, placeholder-then-final).
- **−** `true_preview` providers writing two assets per job mean two
  storage writes, two DB rows, more telemetry. Cost may go up before
  it comes down.
- **−** Client must handle the swap (preview → final) without flicker
  on `true_preview` routes.

## Consequences

- `ProviderModel.preview_capability` is added to the schema (per
  `docs/api/openapi.yaml`) and required on every provider model
  registered with the platform.
- `GenerationJob.preview_asset_ids` and `.final_asset_ids` are separate
  arrays. For `derived_preview` routes, `preview_asset_ids` points at
  the downscaled derivative of the final image (same generation, two
  rendered resolutions). For `no_preview`, `preview_asset_ids` stays
  empty.
- Job state machine: `queued → running → preview_ready → completed`
  for `true_preview`; `queued → running → completed` for
  `derived_preview` and `no_preview` (no intermediate `preview_ready`
  transition).
- Storage layer (ADR-011) writes preview and final to distinct S3 keys
  with distinct quality / resolution metadata across all three modes.
- Router (ADR-007) consults `preview_capability` when picking a route
  for interactive scene generation; rejects with `503
  preview_unavailable` if the request requires `true_preview` and none
  is available.

## Revisit when

- Provider latencies improve enough that single-asset response is consistently fast — preview may become an unused complication for some routes (consider per-route toggle).
- We observe that preview→final swap is the source of UX issues (flicker, layout shift) — reconsider whether server-side progressive enhancement (one upscaled asset) is better.
- A meaningful fraction of jobs end as "preview succeeded, final failed" — investigate whether final-only retry or a different provider for final is warranted.

---

## Confidence to Implement

**Score: 88/100 — High** *(was 78; +10 after provider-dependent preview capability + ProviderModel.preview_capability landed)*

The state-machine work is straightforward across all three modes. The previously-open risk — "preview-first only delivers value with a true fast-preview path" — is now structurally addressed: the typed `preview_capability` field forces every provider to declare its mode, and the router enforces the route rules. The UI can branch on the mode without guessing. Remaining 12 points reflect that the client-side three-path render logic is non-trivial UX work and the `503 preview_unavailable` rejection behavior needs careful product framing (when to reject vs. downgrade vs. delay).
