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
