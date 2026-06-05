# ADR-007 — Use Provider Adapters

## Status

Accepted for initial implementation.

## Context

DreamChat Image Platform needs a clean, independently testable architecture for persistent visual assets.

## Decision

All image providers implement a common adapter interface.

## Consequences

Positive:

- This avoids vendor lock-in, enables fallback routing, supports benchmarking, and allows self-hosting later.
- The decision supports standalone testing and future evolution.

Tradeoffs:

- Requires explicit contracts and discipline.
- May feel heavier than a quick direct integration, but it prevents coupling and drift.

## Notes

This ADR can be revisited after the first production benchmark.

## Confidence to Implement

**Score: 85/100 — High**

The Go interface in `docs/architecture/provider-adapters.md` (`Generate`, `Upscale`, `GetStatus`, `Capabilities`) is small and reasonable. The mock adapter is trivial (deterministic placeholder bytes). Risk shows up only when adding *real* adapters: each provider (BFL, Replicate, Fal, etc.) has its own quirks for image-to-image references, seeds, async polling cadence, and content-policy errors — the interface may need to widen. The router decision logic ("character portrait + standard + fast → provider A") is policy-shaped and not pinned down here.
