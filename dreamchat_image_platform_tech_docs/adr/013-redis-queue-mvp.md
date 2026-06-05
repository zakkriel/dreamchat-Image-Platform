# ADR-013 — Use Redis for MVP Queue and Cache

## Status

Accepted for initial implementation.

## Context

DreamChat Image Platform needs a clean, independently testable architecture for persistent visual assets.

## Decision

Use Redis for MVP job queue, short-lived cache, idempotency locks, and rate limiting.

## Consequences

Positive:

- Redis is simple, fast, widely supported, and enough for MVP. NATS JetStream can be considered later.
- The decision supports standalone testing and future evolution.

Tradeoffs:

- Requires explicit contracts and discipline.
- May feel heavier than a quick direct integration, but it prevents coupling and drift.

## Notes

This ADR can be revisited after the first production benchmark.
