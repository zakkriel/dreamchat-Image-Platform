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
