# ADR-010 — Use Preview-First Delivery

## Status

Accepted for initial implementation.

## Context

DreamChat Image Platform needs a clean, independently testable architecture for persistent visual assets.

## Decision

The platform produces and serves low-res previews before high-res finals when possible.

## Consequences

Positive:

- The web app should feel responsive even when high-quality generation takes longer.
- The decision supports standalone testing and future evolution.

Tradeoffs:

- Requires explicit contracts and discipline.
- May feel heavier than a quick direct integration, but it prevents coupling and drift.

## Notes

This ADR can be revisited after the first production benchmark.

## Confidence to Implement

**Score: 78/100 — High**

The state-machine work (`queued → running → preview_ready → completed`) and two-asset linkage (preview asset_id + final asset_id under same job_id) is straightforward. The risk is that "preview first, then upgrade" only delivers real UX value when the backend supports a genuinely faster low-res path or a dedicated draft model. With providers that do only one quality, the "preview" ends up being a downscaled crop of the final and the perceived latency win disappears. The decision is right; the *outcome quality* depends on provider routing (ADR-007).
