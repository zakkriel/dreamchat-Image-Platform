# ADR-002 — Use Go for API and Workers

## Status

Accepted for initial implementation.

## Context

DreamChat Image Platform needs a clean, independently testable architecture for persistent visual assets.

## Decision

Use Go for the Image Platform API and worker processes.

## Consequences

Positive:

- The service is mostly HTTP, queues, storage, provider calls, concurrency, and metadata persistence. Go provides strong concurrency, low memory usage, simple deployment, and predictable service performance.
- The decision supports standalone testing and future evolution.

Tradeoffs:

- Requires explicit contracts and discipline.
- May feel heavier than a quick direct integration, but it prevents coupling and drift.

## Notes

This ADR can be revisited after the first production benchmark.

## Confidence to Implement

**Score: 95/100 — Very High**

Go is a well-suited choice for an HTTP+queue+storage+provider-call service: standard library is strong for this shape, concurrency is straightforward, deploy is a single binary. The author already chose chi/echo/gin-class routers and pgx/sqlc-class DB libs implicitly. Only mild risk: if the team later wants ML/inference in-process (PRD 03 reference embeddings, drift detection), they'll have to call out to Python via gRPC/HTTP rather than do it in-Go — this ADR doesn't address that future seam.
