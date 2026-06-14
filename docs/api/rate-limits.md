# Rate Limits

## Purpose

Rate limits protect the API, the queue, the providers, and the cost-control
path from being exhausted by a single token before the platform can react.
They run as close to the request boundary as possible so denials are cheap.

## What 429 owns vs what 422 owns

As of Phase 7C-2 the responsibility split is explicit and **must not blur**:

| Status | Codes | Owner |
|---|---|---|
| `429` | `rate_limit_exceeded`, `concurrent_jobs_exceeded` | This document (rate limiting) |
| `422` | `no_price_entry`, `budget_exceeded` | Cost control (`docs/architecture/cost-control.md`) |

Cost-budget enforcement is **not** a rate limit and is **not** a 429. A request
that is well-formed but cannot be priced or would exceed a budget fails the
cost pre-flight with `422`. Rate limiting never touches cost budgets, and the
cost path never returns 429. (Earlier drafts of this doc described cost limits
and provider circuit-breakers as 429 dimensions; that was aspirational and has
been reconciled with what actually ships.)

## 1. Request-rate limiting (`429 rate_limit_exceeded`)

Per-token requests-per-minute / requests-per-hour caps, enforced as middleware
on the authenticated `/v1` group. The middleware runs **after** auth (it needs
the resolved principal/token) and **before** route handlers and scope gates.

Every authenticated `/v1` request is counted — reads, writes, **and admin
endpoints**. Admin endpoints are deliberately throttled too; the mitigation is
to pin admin/partner tokens to higher per-token overrides (see §3), not to
exempt them.

| Dimension | Default | Headers |
|---|---|---|
| `requests_per_minute` | 60 | `X-RateLimit-Requests-Per-Minute`, `*-Remaining`, `*-Reset` |
| `requests_per_hour` | 1000 | `X-RateLimit-Requests-Per-Hour`, `*-Remaining`, `*-Reset` |

### Algorithm — fixed window with a Redis counter

Each request increments two counters (one per window), keyed on the token and
an aligned time bucket:

```txt
rate_limit:token:<token_id>:rpm:<yyyyMMddHHmm>
rate_limit:token:<token_id>:rph:<yyyyMMddHH>
```

The increment and the key's TTL are created **atomically** by a single Lua
script:

```lua
local count = redis.call("INCR", KEYS[1])
if count == 1 then
  redis.call("PEXPIRE", KEYS[1], ARGV[1])
end
return count
```

This guarantees that the first increment (the one that creates the key) also
sets its expiry. We never issue `INCR` and `EXPIRE` as two independent calls —
a connection drop between them could leave a key with no TTL and permanently
wedge the token's window.

A request is allowed while the post-increment count is `<= limit`. Both windows
must pass; the request is denied if either is exceeded.

### Fixed-window trade-off (documented on purpose)

* **A denied request still increments the counter.** This is intentional and
  affects `Remaining`/`Retry-After` math: a token that keeps hammering a closed
  window does not reset it.
* Because windows are aligned to clock boundaries, a burst straddling a boundary
  can be served at up to ~2× the nominal rate for one window. This is the
  accepted cost of a cheap, dependency-light fixed window. A sliding window or
  token bucket would smooth this at the price of more Redis state; out of scope
  here.

### Redis fail-open

Request-rate limiting **fails open**. If Redis is unreachable or errors:

* a warning is logged,
* the request is **allowed**,
* rate-limit headers are omitted.

A Redis outage therefore degrades request-rate limiting only. It does **not**
take down the API, and it does **not** weaken the concurrent-job cap, which is
Postgres-backed (§2).

### Headers and denial

On every allow (and on a `rate_limit_exceeded` denial) the middleware sets:

```txt
X-RateLimit-Requests-Per-Minute: 60
X-RateLimit-Requests-Per-Minute-Remaining: 42
X-RateLimit-Requests-Per-Minute-Reset: 1717593660
X-RateLimit-Requests-Per-Hour: 1000
X-RateLimit-Requests-Per-Hour-Remaining: 873
X-RateLimit-Requests-Per-Hour-Reset: 1717596000
```

`*-Reset` is a **Unix timestamp (seconds)** at which the current fixed window
resets (the next aligned boundary).

On denial the response is `429` `application/problem+json` with code
`rate_limit_exceeded` and a `Retry-After` header (seconds until the blocking
window resets — the hour window when it is the one exceeded, otherwise the
minute window).

## 2. Concurrent generation-job cap (`429 concurrent_jobs_exceeded`)

A hard cap on the number of **live** generation jobs a token may have at once.

| Dimension | Default |
|---|---|
| `max_concurrent_jobs` | 5 per token |

"Live" means a generation job in one of:

```txt
queued | running | preview_ready
```

`preview_ready` **counts as live**: a preview-ready job is not terminal — it may
still proceed to final generation — so it must still consume a slot. Terminal
statuses free the slot and are **not** counted:

```txt
completed | failed | cancelled
```

Cancelling a job (Phase 7C-1 admin cancel) therefore frees a concurrent slot.

### Scope: reserve/enqueue create path only

The cap is enforced **only** on the genuine reserve-and-enqueue create path
(`internal/jobs.Service.CreateAndEnqueue`), used by artifact generation, pack
generation, and style-preview generation. Instant cache-hit completions
(`CreateCompletedCacheHitJob`, `CreateCompletedPackReuseJob`) land a job
directly at `completed` without reserving cost or enqueuing — they occupy no
live slot and are **exempt** from the cap.

### Hard enforcement under parallel requests

The cap is **not** best-effort. Inside the create transaction, before any side
effect (cost reserve / job insert / idempotency insert / enqueue), the service:

1. Takes a **transaction-scoped advisory lock** keyed on the token
   (`pg_advisory_xact_lock(hashtextextended("concurrent:<token_id>"))`, reusing
   the Phase 6A4 helper), so concurrent creates for the same token serialize
   before counting.
2. If an idempotency key is present, checks **under the lock** whether its row
   already exists for `(token, key)`. If it does, the transaction rolls back and
   the existing job is replayed — the cap is **not** evaluated. This closes the
   concurrent same-key duplicate race.
3. Counts the token's live jobs:
   ```sql
   SELECT count(*) FROM generation_jobs
   WHERE requested_by_token_id = $1
     AND status IN ('queued', 'running', 'preview_ready');
   ```
   (Backed by `idx_generation_jobs_token_status`.)
4. If `count >= max_concurrent_jobs`, rolls back and returns
   `429 concurrent_jobs_exceeded`.
5. Otherwise continues normal creation.

A denial has **no side effects**: no reservation, no job row, no idempotency
row, no enqueue.

### No Retry-After

`concurrent_jobs_exceeded` does **not** carry `Retry-After`. Concurrency clears
when an in-flight job reaches a terminal state, not at a predictable wall-clock
time, so a `Retry-After` would be misleading.

It does carry, on generation-create responses and on the denial:

```txt
X-RateLimit-Concurrent-Jobs: 5
X-RateLimit-Concurrent-Jobs-Remaining: 2
```

### Idempotency always wins over the cap

A request that resolves to an idempotency replay returns the existing job and
creates no new load, so it must never be denied by the cap — even when the token
is at the cap. Both replay points bypass the cap:

* **Handler pre-check** (`LookupReplay`, before route resolution): an already
  recorded `(token, key)` returns the existing job before `CreateAndEnqueue` is
  ever called.
* **In-transaction conflict** (concurrent first-time duplicates): handled by
  step 2 above, under the advisory lock.

The concurrent cap is never moved ahead of either replay point.

## 3. Per-token overrides

`api_tokens` carries three nullable override columns (Phase 7C-2). `NULL` means
"use the platform default".

| Column | Overrides | Default |
|---|---|---|
| `rate_limit_rpm` | `requests_per_minute` | 60 |
| `rate_limit_rph` | `requests_per_hour` | 1000 |
| `max_concurrent_jobs` | concurrent live jobs | 5 |

These are resolved into the token's effective limits during auth and carried on
the request `Principal`, so neither the rate-limit middleware nor the jobs
service issues an extra query. The effective `max_concurrent_jobs` is threaded
to the jobs service via `CreateAndEnqueueParams` (the service does not read the
request context). Admin/partner tokens are pinned higher here; normal user
tokens use the defaults.

## Error responses

All limit errors use the standard `application/problem+json` shape:

```json
{
  "code": "rate_limit_exceeded",
  "message": "request rate limit exceeded",
  "request_id": "req_123"
}
```

```txt
rate_limit_exceeded         # request-rate cap (carries Retry-After)
concurrent_jobs_exceeded    # live-job cap (no Retry-After)
```

Cost-budget failures stay `422` with `no_price_entry` / `budget_exceeded` (see
`docs/architecture/cost-control.md`).

## Related docs

- `docs/architecture/cost-control.md` — cost pre-flight (the 422 path).
- `docs/api/openapi.yaml` — `429` responses + rate-limit headers.
- `docs/api/errors.md` — error vocabulary.
