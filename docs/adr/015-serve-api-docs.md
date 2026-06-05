# ADR-015 — Serve API Docs From the Service

## Status

Accepted for initial implementation.

## Context

DreamChat Image Platform needs a clean, independently testable architecture for persistent visual assets.

## Decision

The Go service serves `/docs` and `/openapi.json` in local/dev. Production docs are protected or intentionally exposed.

## Consequences

Positive:

- Every environment should be self-documenting and aligned with the current contract.
- The decision supports standalone testing and future evolution.

Tradeoffs:

- Requires explicit contracts and discipline.
- May feel heavier than a quick direct integration, but it prevents coupling and drift.

## Notes

This ADR can be revisited after the first production benchmark.

## Confidence to Implement

**Score: 95/100 — Very High**

Embedding `swagger-ui` or `redoc` as a static asset and serving `/openapi.json` from the same Go binary takes ~50 LoC. The only production concern (gating docs behind admin auth or a flag) is a single conditional in the router. Done.
