# Phase 7C-2 Confidence Index — Rate Limiting + Hard Concurrent Job Caps

**Overall: 90/100 — Very High**

Phase 7C-2 is **slice 2 of 4** of Phase 7C (Production Controls). It adds two
boundary controls and nothing else: (1) **per-token request-rate limiting** at
the authenticated `/v1` boundary, backed by a fixed-window Redis counter; and
(2) a **hard per-token concurrent generation-job cap**, enforced inside the
create transaction under a transaction-scoped advisory lock, before any cost
reservation or enqueue. RLS / tenant isolation (7C-3) and provider fallback
chains + webhooks (7C-4) are **not** in this slice. **No new table — count
stays 18** (migration `0008` adds three `api_tokens` columns + one index).
OpenAPI `0.9.0 → 0.10.0`, strictly additive, mirrored.

## What shipped

| # | Deliverable | Where | Confidence |
|---|---|---|---|
| 1 | `internal/ratelimit` fixed-window limiter (rpm + rph), nil-safe | `ratelimit/ratelimit.go` | 92 |
| 2 | Atomic INCR+PEXPIRE Lua script (TTL created with first increment) | `ratelimit/redis.go` | 93 |
| 3 | Reusable Redis client from `RedisAddr`/`RedisPassword`, closed on shutdown | `ratelimit/redis.go`, `cmd/api/main.go` | 92 |
| 4 | `/v1` middleware after auth, before handlers; counts every authenticated request incl. admin | `ratelimit/middleware.go`, `http/router.go` | 91 |
| 5 | `429 rate_limit_exceeded` + `Retry-After` + `X-RateLimit-Requests-Per-*` headers | `ratelimit/middleware.go`, `httperr` | 91 |
| 6 | Redis fail-open (allow + warn + omit headers); concurrent cap unaffected | `ratelimit/middleware.go` | 92 |
| 7 | Hard concurrent cap in `CreateAndEnqueue` before reserve/insert/enqueue | `jobs/service.go` | 90 |
| 8 | Transaction-scoped advisory lock per token (reuses Phase 6A4 helper) | `jobs/service.go` (`concurrentLockKey`, `AcquireSupersedeLock`) | 90 |
| 9 | Cap counts `queued|running|preview_ready`; not `completed|failed|cancelled` | `CountLiveGenerationJobsByToken` | 92 |
| 10 | Idempotency replay (pre-check + in-tx under lock) always wins over the cap | `jobs/service.go`, `routing.go` | 91 |
| 11 | Cache-hit completions exempt (no live slot, not cap-checked) | `CreateCompletedCacheHitJob`/`...PackReuseJob` (unchanged) | 92 |
| 12 | `429 concurrent_jobs_exceeded`, **no** Retry-After, `X-RateLimit-Concurrent-Jobs[-Remaining]` | `routing.go`, handlers, `httperr` | 90 |
| 13 | Per-token overrides `rate_limit_rpm`/`rate_limit_rph`/`max_concurrent_jobs` | `migration 0008`, `api_tokens.sql`, `auth` | 91 |
| 14 | Effective limits resolved at auth, carried on `Principal`; cap threaded via params | `auth/principal.go`, `auth/middleware.go`, `CreateAndEnqueueParams` | 91 |
| 15 | Cost limits unchanged (`422 no_price_entry`/`budget_exceeded`) | `cost` (untouched) | 95 |
| 16 | Additive OpenAPI `0.9.0 → 0.10.0`, mirrored | `api/openapi.yaml`, `docs/api/openapi.yaml` | 92 |
| 17 | Limiter + middleware unit tests; concurrent-cap + handler integration tests | `*_test.go` | 89 |

## Request-rate limiter (92)

`Limiter.Allow` increments two aligned fixed-window keys per request:

```txt
rate_limit:token:<token_id>:rpm:<yyyyMMddHHmm>   ttl 1m
rate_limit:token:<token_id>:rph:<yyyyMMddHH>     ttl 1h
```

The increment and TTL are one atomic Lua execution (`INCR`; `PEXPIRE` only when
`count == 1`), so a connection drop can never leave a key without an expiry —
the failure mode that two independent `INCR`/`EXPIRE` calls would risk.
Allowed while `count <= limit` on **both** windows. A denied request **still
increments** the counter (documented fixed-window trade-off) and `Reset` is the
next aligned boundary as a Unix timestamp. `Retry-After` is seconds to the
blocking window's reset (hour when it is the one exceeded, else minute).

## Fail-open (92)

The middleware treats any store error as **allow**: it logs a warning, omits
rate-limit headers, and calls the next handler. A Redis outage therefore weakens
only request-rate limiting. The concurrent-job cap is Postgres-backed and still
holds. The limiter is also nil-safe (no Redis ⇒ disabled pass-through), so the
existing suite runs without Redis.

## Hard concurrent cap (90)

Inside the existing `CreateAndEnqueue` transaction, before any side effect:

1. `AcquireSupersedeLock("concurrent:<token_id>")` —
   `pg_advisory_xact_lock(hashtextextended(...))`, auto-released at
   commit/rollback. Same-token creates serialize here.
2. If an idempotency key is present, look it up **under the lock**; if the
   `(token, key)` row already exists, roll back and replay — the cap is **not**
   evaluated. This closes the concurrent same-key duplicate race.
3. `CountLiveGenerationJobsByToken` (`status IN
   ('queued','running','preview_ready')`, backed by
   `idx_generation_jobs_token_status`).
4. `count >= max_concurrent_jobs` → roll back, return
   `ErrConcurrentJobsExceeded` (handler → `429 concurrent_jobs_exceeded`). No
   reservation, job, idempotency row, or enqueue is created.
5. Otherwise proceed (reserve → insert → idempotency → enqueue).

`preview_ready` counts because it is **not** terminal — the job may still run
final generation. `Retry-After` is intentionally omitted: concurrency clears at
a terminal state, not a predictable window.

## Idempotency-wins ordering (91)

The cap is never moved ahead of either replay point. The handler pre-check
(`LookupReplay`) returns the existing job before `CreateAndEnqueue`; an
in-transaction same-key conflict is caught at step 2 under the lock. A request
that resolves to a replay is never `429`-denied, even when the token is at the
cap, and creates no new load. Integration tests cover both paths.

## What is explicitly NOT here

- **No RLS** — no `ENABLE/FORCE ROW LEVEL SECURITY`, no policies, no
  `app.current_tenant` GUC, no connection/session tenant plumbing, no second DB
  pool. Deferred to 7C-3 because it needs deliberate session plumbing and must
  not be bundled with request throttling.
- **No provider fallback chains, webhooks, or circuit breaker** — 7C-4.
- **No cost-enforcement change** — budgets stay `422`.
- **No sliding window / token bucket / distributed fairness** — fixed window,
  per token, by design.

## Tests

- **Unit** (`internal/ratelimit`): under/over rpm + rph, denied-still-increments,
  atomic-TTL-on-first-increment, window reset, Retry-After math, header math,
  store-error bubbles (fail-open input), per-token override beats default,
  nil/disabled limiter; middleware: disabled/no-principal pass-through,
  under-limit reaches handler with headers, over-limit `429` problem+json with
  headers + Retry-After, Redis-failure fail-open, admin throttled unless higher
  override.
- **Integration** (`internal/jobs`, build tag `integration`): below cap proceeds;
  at cap with queued/running/preview_ready denies; terminal jobs don't count;
  cancel frees a slot; parallel creates cannot exceed the cap (advisory lock);
  pre-check replay at cap returns existing; in-tx replay at cap returns existing;
  concurrent same-key at cap converges on one job, none denied; cache-hit
  exemption consumes no slot; denial has no side effects (no job/reservation/
  idempotency/enqueue); artifact/style-preview/pack generation at cap → `429
  concurrent_jobs_exceeded`; under-cap reserves + enqueues; request-rate over
  limit (real Redis) blocks before handler side effects.

## Residual risk

- Default limits (60/min, 1000/hr, 5 concurrent) are placeholders pending
  real-traffic calibration; all three are per-token overridable.
- Fixed-window boundary bursting (up to ~2× nominal across a boundary) is
  accepted and documented; tightening would require a sliding window.
- S3-backed worker integration tests need MinIO (CI provides it); the 7C-2 cap
  tests deliberately avoid the worker so they need only Postgres (+ Redis for
  the one request-rate handler test).
