# ADR-008 — Use Asset-State-First Persistence

## Status

Accepted for initial implementation.

## Context

DreamChat Image Platform needs a clean, independently testable architecture for persistent visual assets.

## Decision

Assets are generated from visual identities, variants, and versions, not one-off prompts.

## Consequences

Positive:

- DreamChat needs persistent character and place consistency.
- The decision supports standalone testing and future evolution.

Tradeoffs:

- Requires explicit contracts and discipline.
- May feel heavier than a quick direct integration, but it prevents coupling and drift.

## Notes

This ADR can be revisited after the first production benchmark.

## Confidence to Implement

**Score: 85/100 — High**

The shift from "prompt → image" to "visual identity → generation intent → prompt package → job → asset → variant" is well captured. It makes downstream choices (caching, reuse, versioning, drift) much cleaner. The implementable parts (identity records, version transitions, variant tags, anchor refs) are all defined in the data model. The non-implementable part — same as PRD 03 — is whether the underlying model actually *honors* identity inputs across calls; that's a provider-quality issue, not an architecture decision.
