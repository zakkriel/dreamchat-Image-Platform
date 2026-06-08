# ADR-006 — Use async jobs for generation

## Status

Accepted for initial implementation.

## Context

Image generation takes seconds to minutes depending on provider, model, and quality tier. It also fails in interesting ways (provider rate limits, capacity errors, content policy, transient timeouts) and needs retries. The platform's clients (web app, admin tools, scripts) need either an immediate response or a way to poll without holding HTTP connections open for the whole duration.

The synchronous vs. async choice affects: HTTP timeout configuration, client UX, retry semantics, partial-success handling, and the worker/queue topology.

## Decision

Generation endpoints accept a request, create a `generation_job` row, enqueue worker work, and return `202 Accepted` with a `job_id`. Clients poll `GET /v1/jobs/{job_id}` for status. The job carries preview_asset_ids and final_asset_ids separately, supporting preview-first delivery (ADR-010). Webhooks are a future addition, not MVP.

## Alternatives considered

- **Synchronous HTTP with long timeout.** Simplest client code. But ties up an API thread for 20–60 seconds per call, makes provider hiccups (timeouts, capacity errors) cascade into client timeouts, prevents partial-success delivery, and bursts of concurrent generation will exhaust the HTTP server's connection pool.
- **WebSocket push** from the API to the client. Faster perceived latency, no polling. But every client needs a stateful connection, scaling is harder (per-replica session affinity), middleboxes (corporate proxies, mobile networks) sometimes drop long-lived WS. The web app may add WS later as a polling replacement, but it shouldn't be the only path.
- **Server-Sent Events (SSE).** Cleaner than WebSocket for one-way push, still long-lived. Useful future enhancement, doesn't replace the job model.
- **Hybrid: sync for fast preview, async for final.** Mixes models, doubles the client logic, and the "fast preview" still depends on provider response time. Cleaner to make everything async with `preview_ready` as a job state.

## Tradeoffs

- **+** Provider variability is absorbed by the worker; API stays responsive.
- **+** Retries become tractable (the worker retries; the API caller doesn't have to).
- **+** Partial-success (preview ready, final pending) has a natural representation.
- **+** Scales horizontally — workers and API can be sized independently.
- **−** Two-step client UX (post then poll). Web app needs polling logic until webhooks/SSE land.
- **−** Job state must be persisted; idempotency keys (`docs/api/idempotency.md`) become important.
- **−** Failure visibility is asymmetric: client knows the job failed only when it polls.

## Consequences

- `generation_jobs` table is the source of truth for in-flight work.
- Worker process (`cmd/worker`) consumes a queue (Redis MVP, ADR-013) and writes back to the job + assets.
- `GET /v1/jobs/{job_id}` is the polling endpoint, returning `GenerationJobStatus` from `docs/api/openapi.yaml`.
- Webhooks (`generation_job.preview_ready`, `generation_job.completed`, `generation_job.failed`) are a planned addition.

## Revisit when

- Polling traffic becomes a meaningful fraction of total API load (push via SSE or WS makes economic sense).
- Provider latencies improve to the point where sync responses would be acceptable for some use cases (still keep async as the default to retain idempotency + telemetry shape).
- We need workflow orchestration beyond linear preview→final (e.g. multi-stage pipelines with conditional branches).

---

## Confidence to Implement

**Score: 90/100 — Very High**

202 Accepted + `job_id` + poll-or-webhook is a standard pattern. The job state machine in `docs/architecture/job-lifecycle.md` is explicit and finite. Redis-based queue (ADR-013) is enough for MVP. Mild uncertainty only around retry policy edge cases (provider accepted but response lost = ambiguous) and the future webhook surface — both are noted as MVP-deferable in the supporting docs.
