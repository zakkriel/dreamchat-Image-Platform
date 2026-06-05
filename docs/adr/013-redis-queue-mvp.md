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

## Confidence to Implement

**Score: 85/100 — High**

Asynq, river, or a small custom Redis-Streams worker pool all fit MVP. Idempotency locks via `SETNX`, rate-limiting via token-bucket Lua scripts — all standard. Subtracting points because "Redis is enough for MVP" works until visibility/at-least-once semantics matter (provider call accepted but worker crash), at which point either Postgres-based queue (`river`) or NATS JetStream becomes preferable. The ADR acknowledges this as a known follow-up.
