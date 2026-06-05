# ADR-009 — Use Retrieval Before Generation

## Status

Accepted for initial implementation.

## Context

DreamChat Image Platform needs a clean, independently testable architecture for persistent visual assets.

## Decision

Every generation request checks existing assets before creating a new job.

## Consequences

Positive:

- This controls cost, improves speed, and preserves visual continuity.
- The decision supports standalone testing and future evolution.

Tradeoffs:

- Requires explicit contracts and discipline.
- May feel heavier than a quick direct integration, but it prevents coupling and drift.

## Notes

This ADR can be revisited after the first production benchmark.
