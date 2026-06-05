# ADR-001 — Image Platform is a standalone API

## Status

Accepted for initial implementation.

## Context

DreamChat Image Platform needs a clean, independently testable architecture for persistent visual assets.

## Decision

The DreamChat image system is implemented as a separate API service, not embedded directly into the web app.

## Consequences

Positive:

- This enables independent testing, clean contracts, separate scaling, provider switching, and future API reuse.
- The decision supports standalone testing and future evolution.

Tradeoffs:

- Requires explicit contracts and discipline.
- May feel heavier than a quick direct integration, but it prevents coupling and drift.

## Notes

This ADR can be revisited after the first production benchmark.

## Confidence to Implement

**Score: 95/100 — Very High**

"Build it as a separate service" is operationally clear: one Go module, its own deploy unit, its own DB. Nothing here requires invention. The decision *enables* implementability — it isolates the platform from the web app's churn. Mild caveats only: the boundary must be enforced by code review (otherwise web devs reach in), and inter-service auth/networking has to be configured (S2S token, internal DNS) — not in this ADR.
