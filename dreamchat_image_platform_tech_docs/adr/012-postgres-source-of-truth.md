# ADR-012 — Use Postgres as Metadata Source of Truth

## Status

Accepted for initial implementation.

## Context

DreamChat Image Platform needs a clean, independently testable architecture for persistent visual assets.

## Decision

Postgres stores visual identities, assets, jobs, styles, tokens, and cost events.

## Consequences

Positive:

- The service needs transactional metadata, queryability, and consistency.
- The decision supports standalone testing and future evolution.

Tradeoffs:

- Requires explicit contracts and discipline.
- May feel heavier than a quick direct integration, but it prevents coupling and drift.

## Notes

This ADR can be revisited after the first production benchmark.
