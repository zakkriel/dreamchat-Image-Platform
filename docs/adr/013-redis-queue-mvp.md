# ADR-013 — Use Redis for MVP queue and short-lived state (NATS later if needed)

## Status

Accepted for initial implementation. NATS JetStream is a documented future option.

## Context

ADR-006 makes generation async; that requires a queue between the API and the worker. The same component should also host the platform's short-lived state: idempotency locks (ADR-006), rate-limit counters (`docs/api/rate-limits.md`), and short-TTL caches for token lookups.

The queue choice affects: at-least-once vs. at-most-once semantics, retry mechanics, ops complexity, local-dev parity, and the upgrade path when the platform grows.

## Decision

Use **Redis** for the MVP queue (via `asynq`, `river-style` patterns, or a small Redis Streams worker pool), idempotency locks (`SETNX`), rate-limit counters (token-bucket Lua scripts), and short-lived caches. **NATS JetStream is the documented next step** when at-least-once durability and event-stream semantics matter more than operational simplicity.

## Alternatives considered

- **Redis (chosen).** Simple MVP, well-understood, local-dev parity is one docker-compose service. Multiple production-grade Go libraries (`asynq`, `taskq`, custom Streams). Good enough for asynchronous job processing at the platform's projected volume.
- **NATS JetStream.** Strong event-stream semantics, at-least-once delivery, replay, consumer groups, KV store. Better long-term fit if we add webhooks, multi-stage pipelines, or event-driven integrations. More moving parts to operate at MVP, and the team's ops experience with Redis is greater. Adopt when reliability needs cross the threshold.
- **Postgres-only queue** (e.g. `river` or a hand-rolled `SELECT FOR UPDATE SKIP LOCKED` pattern). Fewer components, transactional with the rest of the metadata writes ("create job + enqueue work" in one tx). Worse for very high throughput and for separating queue state from primary DB. Worth reconsidering if we ever want to remove Redis from the dev stack.
- **Kafka.** Right answer for event-sourcing-shaped workloads. Massive ops overhead for our scale. Not justified.
- **RabbitMQ.** Mature AMQP option. Adds a new protocol the team doesn't already operate. NATS or Redis wins on simplicity.
- **Cloud-managed queue** (SQS, Cloud Tasks). Removes ops burden but ties us to a cloud and complicates local dev. May make sense once cloud is chosen.

## Tradeoffs

- **+** One Redis covers queue, idempotency, rate limit, and short-cache — fewer moving parts at MVP.
- **+** `SETNX` makes the idempotency-first-writer pattern (per `docs/api/idempotency.md`) a one-liner.
- **+** Asynq/river give exponential-backoff retry, scheduled jobs, dead-letter queues out of the box.
- **+** docker-compose with Redis is well-understood by devs.
- **−** Default Redis is single-node (data loss on crash unless AOF persistence + replicas configured carefully). At-least-once delivery requires deliberate config.
- **−** Job visibility timeout / ack semantics need attention; "provider accepted but worker crashed before ack" is the known ambiguous case.
- **−** Queue state and Postgres state are separate — eventual consistency between "job enqueued" and "job in queue" needs the outbox pattern or careful tx ordering.

## Consequences

- `internal/queue` exposes `Enqueue(ctx, task)` and `Consume(ctx, handler)` against Redis via `asynq` or equivalent.
- Idempotency middleware uses `SETNX dci:idempotency:{token_id}:{key} → job_id` with TTL = 24h.
- Rate limit middleware uses a Lua token-bucket script keyed on `token_id` and dimension (`req/min`, `jobs/hour`, etc.).
- Outbox pattern: write the `generation_job` row + the queue task in the same DB tx (job row marked `queued`); a small relay process pushes from outbox to Redis. (Or accept eventual consistency and tolerate rare duplicate enqueues — `idempotency_keys` deduplicates.)

## Revisit when

- A failure investigation traces back to Redis losing in-flight tasks (move to JetStream or Postgres-backed queue).
- We need event-sourcing or webhook fan-out (`generation_job.preview_ready` → many subscribers) — that's NATS's wheelhouse.
- Worker throughput requires queue features Redis doesn't have natively (priority queues with strong fairness, very-long-delay scheduled tasks).
- Cloud provider is chosen and a managed equivalent (SQS+EventBridge, Cloud Tasks+PubSub) becomes the lower-ops option.

---

## Confidence to Implement

**Score: 85/100 — High**

Asynq, river, or a small custom Redis-Streams worker pool all fit MVP. Idempotency locks via `SETNX`, rate-limiting via token-bucket Lua scripts — all standard. Subtracting points because "Redis is enough for MVP" works until visibility/at-least-once semantics matter (provider call accepted but worker crash), at which point either Postgres-based queue (`river`) or NATS JetStream becomes preferable. The ADR acknowledges this as a known follow-up.
