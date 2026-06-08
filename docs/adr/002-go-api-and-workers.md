# ADR-002 — Use Go for API and workers

## Status

Accepted for initial implementation.

## Context

The DreamChat Image Platform is dominated by HTTP handling, queueing, S3 I/O, outbound provider calls, structured metadata writes, and concurrent worker scheduling. It has no in-process ML inference yet (provider calls are outbound HTTP). It needs predictable memory, easy deploys, and a fast feedback loop in CI.

The chosen language affects: developer hiring, web-app code sharing, MLOps integration, container size, and worker concurrency model.

## Decision

Use Go for both the API server (`cmd/api`) and the worker (`cmd/worker`). Both binaries are built from the same module and share the `internal/...` packages.

Python is reserved for *later, optional* concerns: self-hosted inference, LoRA training, offline image evaluation, ML experiments.

## Alternatives considered

- **Node / NestJS.** Aligns with the TypeScript web app (PRD 07 even hints at it). Same language for full stack is appealing. But Node's worker story (worker_threads, BullMQ) is weaker for many small CPU-bound or I/O-fanout jobs, the single-event-loop model is awkward for provider-call concurrency, and per-binary memory is higher.
- **Python / FastAPI.** Strongest ML ecosystem. Useful if we wanted in-process inference, embedding-based drift detection, or LoRA training in the same service. But we don't, yet, and we'd pay for it everywhere (GIL, ASGI worker count, container size, dependency churn). Python becomes appropriate when we add the ML/inference path, as a second service behind the same API.
- **Rust.** Best raw performance and safest concurrency. But the team velocity tax (compile times, ecosystem maturity for OpenAPI/S3/Redis/Postgres glue, hiring) isn't justified for a service that's mostly HTTP and DB I/O.

## Tradeoffs

- **+** Strong concurrency primitives (goroutines, channels) match the worker + provider-fanout shape.
- **+** Low memory footprint and single-binary deploys.
- **+** Mature libraries for everything in the stack: chi/echo (HTTP), pgx/sqlc (Postgres), aws-sdk-go-v2 (S3), asynq/river (queue), oapi-codegen/ogen (OpenAPI).
- **+** Predictable runtime (no GC surprises at our scale, no GIL).
- **−** No code sharing with the TypeScript web app — type duplication via OpenAPI codegen on both sides.
- **−** No first-class ML in-process; the ML/inference path will be a second service in Python later (ADR mentions this explicitly).
- **−** Smaller standard set of "batteries included" web-framework conveniences than Django/Rails — we choose libraries deliberately.

## Consequences

- Project layout: `/cmd/api`, `/cmd/worker`, `/internal/{auth,assets,identities,jobs,providers,styles,storage,telemetry,db,http}` per `docs/guidelines/go-service-guidelines.md`.
- OpenAPI codegen produces Go types and HTTP handler interfaces; client SDKs for the web app are generated separately.
- ML / inference / training work, when it lands, runs as a separate Python service called via gRPC or HTTP from this platform.

## Revisit when

- We need in-process embedding or classifier work (drift detection, asset similarity) at a rate where outbound HTTP to a Python service becomes a latency/cost problem.
- Provider-side work moves substantially in-house (self-hosted models), at which point a Python or hybrid layout may make sense.

---

## Confidence to Implement

**Score: 95/100 — Very High**

Go is a well-suited choice for an HTTP+queue+storage+provider-call service: standard library is strong for this shape, concurrency is straightforward, deploy is a single binary. The author already chose chi/echo/gin-class routers and pgx/sqlc-class DB libs implicitly. Only mild risk: if the team later wants ML/inference in-process (PRD 03 reference embeddings, drift detection), they'll have to call out to Python via gRPC/HTTP rather than do it in-Go — this ADR addresses that future seam explicitly.
