# ADR-005 — Store Only Hashed API Tokens

## Status

Accepted for initial implementation.

## Context

DreamChat Image Platform needs a clean, independently testable architecture for persistent visual assets.

## Decision

Raw API tokens are shown only once. The database stores token hashes and lookup prefixes only.

## Consequences

Positive:

- A token database leak should not expose usable credentials.
- The decision supports standalone testing and future evolution.

Tradeoffs:

- Requires explicit contracts and discipline.
- May feel heavier than a quick direct integration, but it prevents coupling and drift.

## Notes

This ADR can be revisited after the first production benchmark.
