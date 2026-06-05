# Rate Limits

## Purpose

Rate limits protect cost, providers, and service stability. They sit at
the boundary of the API and run before any handler logic so denials are
cheap.

## Limit dimensions

The platform enforces six distinct limit types. They are independent —
exceeding any one is a `429 Too Many Requests`.

### 1. Request rate limits

Per-token requests-per-minute / requests-per-hour caps. Backed by a
sliding-window or token-bucket counter in Redis (ADR-013).

| Dimension | Default | Headers |
|---|---|---|
| `requests_per_minute` | 60 | `X-RateLimit-Requests-Per-Minute`, `*-Remaining`, `*-Reset` |
| `requests_per_hour` | 1000 | `X-RateLimit-Requests-Per-Hour`, `*-Remaining`, `*-Reset` |

Independent of generation cost — counts every API call including reads.

### 2. Concurrent job limits

Cap on the number of `generation_job` rows in `queued` or `running` state
for a given token at any moment.

| Dimension | Default |
|---|---|
| `concurrent_running_jobs` | 5 per token |

When at the cap, new generation requests fail with `429`
`concurrent_jobs_exceeded`. Cancelling a job releases capacity.

### 3. Daily cost limits

Cap on the sum of (`reserved_amount` + `spent_amount`) on a
token-scoped `cost_budget` with `period=daily`. Backed by the cost
control system (`docs/architecture/cost-control.md`); the budget
table **is** the counter.

When the pre-flight estimation (`cost-control.md` §3 step 6) would push
the reservation over the cap, the request fails with `429`
`budget_exceeded` naming the offending `cost_budget.id`. Same surface
applies to tenant-scope budgets that contain the token.

| Dimension | Default |
|---|---|
| `daily_cost_usd` | Configured per token; no platform default. |

### 4. Monthly cost limits

Same mechanism as daily cost limits, with `cost_budget.period=monthly`.
Useful for monthly billing alignment and for budgets that span heavy /
quiet days.

| Dimension | Default |
|---|---|
| `monthly_cost_usd` | Configured per token / tenant; no platform default. |

### 5. Provider-specific limits

The router (ADR-007) carries its own circuit-breaker state per
provider (open / half-open / closed). When `open` for a provider, all
new requests routed to it fail with `503` `provider_unavailable`
regardless of the per-token limits above. Operators can also disable a
provider via `POST /v1/admin/providers/{id}/disable` (PLANNED) — same
effect.

Provider-specific limits aren't usually expressed as per-token; they
protect the provider itself and the platform's reputation with the
provider.

### 6. Token-specific limits

Some tokens are pinned tighter than the defaults — typically
`dci_test_` development tokens or partner integrations under
evaluation. Configured per `api_token` row; overrides the defaults
above.

Pinning a `dci_dev_` token to (e.g.) `requests_per_minute = 10` and
`daily_cost_usd = "5.00"` prevents accidental local-dev runaway.

## Headers

Response headers for the per-token limits:

```txt
X-RateLimit-Requests-Per-Minute: 60
X-RateLimit-Requests-Per-Minute-Remaining: 42
X-RateLimit-Requests-Per-Minute-Reset: 1717593600

X-RateLimit-Concurrent-Jobs: 5
X-RateLimit-Concurrent-Jobs-Remaining: 2

X-RateLimit-Daily-Cost-USD: 50.00
X-RateLimit-Daily-Cost-USD-Remaining: 12.40
X-RateLimit-Daily-Cost-USD-Reset: 1717632000
```

`*-Remaining` on cost dimensions is computed as
`limit_amount - reserved_amount - spent_amount` for the current
period.

## Error responses

All limit errors use the standard `application/problem+json` shape
(ADR-014) with one of:

```txt
rate_limit_exceeded         # request rate
concurrent_jobs_exceeded
budget_exceeded             # daily or monthly cost
provider_unavailable        # provider-specific (router/circuit-breaker)
```

`budget_exceeded` responses include `extensions.budget_id` and
`extensions.scope_type` so the client / operator can find the budget
in `GET /v1/admin/cost-budgets`.

Example:

```json
{
  "type": "https://docs.dreamchat.ai/errors/budget-exceeded",
  "title": "Cost budget exceeded",
  "status": 429,
  "detail": "Daily cost budget for token tok_xyz has been reached.",
  "request_id": "req_123",
  "extensions": {
    "budget_id": "budget_abc",
    "scope_type": "token",
    "scope_id": "tok_xyz",
    "limit_amount": "50.00",
    "currency": "USD"
  }
}
```

## Why cost limits are first-class

Generation requests vary enormously in cost: one POST may produce a
single small artifact (cents) or a full character pack at premium
quality (dollars). A request-rate cap that's tight enough for the
premium case is far too tight for the cheap case, and vice versa.

The cost budget is the only dimension that captures real impact
correctly, which is why the platform reserves cost pre-flight rather
than just counting requests.

## Related docs

- `docs/architecture/cost-control.md` — data model and pre-flight
  pipeline.
- `docs/runbooks/cost-spike.md` — how operators respond when cost
  limits start firing.
- `docs/api/errors.md` — error vocabulary.
- `docs/api/openapi.yaml` — `/v1/admin/cost-budgets`,
  `/v1/admin/cost-reservations`, `/v1/admin/price-book` (all PLANNED).

---

## Confidence to Implement

**Score: 90/100 — Very High** *(was 75; +15 after cost-control pipeline specified)*

Six clearly distinct dimensions, each backed by a concrete data
structure (Redis counters for request rate and concurrency; Postgres
`cost_budget` for daily/monthly cost; router circuit-breaker for
provider; `api_token` overrides for per-token). The previously-open
`estimated_cost_per_day` question is resolved by the cost-control
pipeline. Subtracting points only because the exact per-tier default
values (60/min, 5 concurrent jobs, no default cost cap) are placeholders
that need real-world calibration after the platform sees traffic.
