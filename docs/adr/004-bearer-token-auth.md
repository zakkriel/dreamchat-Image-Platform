# ADR-004 — Use Bearer Tokens with Scoped API Keys

## Status

Accepted for initial implementation.

## Context

DreamChat Image Platform needs a clean, independently testable architecture for persistent visual assets.

## Decision

All non-public endpoints require Authorization: Bearer <token>. Tokens have explicit scopes.

## Consequences

Positive:

- Internal and future external clients need controlled access, revocation, auditability, and rate limiting.
- The decision supports standalone testing and future evolution.

Tradeoffs:

- Requires explicit contracts and discipline.
- May feel heavier than a quick direct integration, but it prevents coupling and drift.

## Notes

This ADR can be revisited after the first production benchmark.
