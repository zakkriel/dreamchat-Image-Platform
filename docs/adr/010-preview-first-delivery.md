# ADR-010 — Preview-first delivery

## Status

Accepted for initial implementation.

## Context

High-quality image generation takes 15–60 seconds with most providers, sometimes longer. DreamChat is a chat-first product where the user's expectation is "the next thing happens fast." Waiting for a final image before the conversation can continue breaks the play-first UX described in PRD 01 and PRD 06.

The platform needs to deliver *something usable* fast, and improve it later, without making the client orchestrate two separate requests.

## Decision

Generation produces a **preview asset** (lower resolution, fast generation tier) before a **final asset** (high resolution, normal tier). Both belong to the same `generation_job`, exposed via `preview_asset_ids` and `final_asset_ids`. The job state machine has an intermediate `preview_ready` status. Clients render preview as soon as it lands and swap to final when it's ready.

## Alternatives considered

- **Final only.** Simplest pipeline, one asset per job. UI feels slow during multi-second waits. Acceptable only when the provider is fast enough that "preview" and "final" would be the same wait.
- **Single asset, progressive enhancement** (server-side upscale of one render). Works when the provider's draft pass produces a usable image; many providers do, but quality varies a lot. Also conflates "low res" and "draft quality" — sometimes you want the second but not the first.
- **Push final via SSE/WebSocket** instead of preview-then-poll. Faster perceived completion, doesn't change generation cost. Useful enhancement; orthogonal to whether we produce preview at all.
- **Skip preview, show a deterministic placeholder.** Cheapest, but the placeholder has no relationship to the actual asset; users learn to ignore it and the "feels responsive" win is theatre.

## Tradeoffs

- **+** UI feels responsive even on heavy backends.
- **+** Preview is independently cacheable and queryable (PRD 05 cache keys include `resolution_tier`).
- **+** Two-stage delivery is a natural seam for tier routing (cheap fast model for preview, premium model for final — ADR-007).
- **+** Failure isolation: preview can succeed while final fails, partial-success state is honest.
- **−** Only delivers real UX gain when the provider can produce a *genuinely faster* low-res path. With single-tier providers, "preview" ends up being a downscaled final and the perceived-latency win disappears.
- **−** Two assets per job means two storage writes, two DB rows, more telemetry. Cost may go up before it comes down.
- **−** Client must handle the swap (preview → final) without flicker — small UX detail with real implications.

## Consequences

- `GenerationJob.preview_asset_ids` and `.final_asset_ids` are separate arrays.
- Job state machine: `queued → running → preview_ready → completed` (and the failure transitions per `docs/architecture/job-lifecycle.md`).
- Storage layer (ADR-011) writes preview and final to distinct S3 keys with distinct quality/resolution metadata.
- Provider router (ADR-007) may route preview to a fast/cheap model and final to a quality model independently.

## Revisit when

- Provider latencies improve enough that single-asset response is consistently fast — preview may become an unused complication for some routes (consider per-route toggle).
- We observe that preview→final swap is the source of UX issues (flicker, layout shift) — reconsider whether server-side progressive enhancement (one upscaled asset) is better.
- A meaningful fraction of jobs end as "preview succeeded, final failed" — investigate whether final-only retry or a different provider for final is warranted.

---

## Confidence to Implement

**Score: 78/100 — High**

The state-machine work (`queued → running → preview_ready → completed`) and two-asset linkage (preview asset_id + final asset_id under same job_id) is straightforward. The risk is that "preview first, then upgrade" only delivers real UX value when the backend supports a genuinely faster low-res path or a dedicated draft model. With providers that do only one quality, the "preview" ends up being a downscaled crop of the final and the perceived latency win disappears. The decision is right; the *outcome quality* depends on provider routing (ADR-007).
