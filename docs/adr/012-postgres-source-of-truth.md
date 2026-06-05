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

## Confidence to Implement

**Score: 95/100 — Very High**

Postgres for transactional metadata is the default for this shape of service. `docs/db/initial_schema.sql` is already a reasonable starting point, and `sqlc`/`pgx` patterns are mature. Adding the `asset_packs` and `provider_attempts` tables that the PRD data model has but the SQL is missing is a small cleanup.
