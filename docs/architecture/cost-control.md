# Cost Control — Price Book, Budgets, and Pre-Flight Estimation

> **Status**: spec for implementation. The data model and pipeline below are
> the contract the platform implements. Admin endpoints exposing this surface
> are defined in `docs/api/openapi.yaml` under the `Admin` tag and marked
> **PLANNED** until built.

## 1. Purpose

Generation is the platform's dominant cost. To make rate limits, budgets,
and runbook mitigations actually work, the platform needs:

- A **price book** that knows what each provider call costs.
- A **budget** model that can be set per tenant / token / world / user and
  per day / month.
- A **reservation** primitive that holds budget against an in-flight job
  so concurrent traffic can't oversell the limit.
- A **pre-flight estimation pipeline** that runs the four together on every
  generation request.

This document defines all four.

## 2. Data model

The three core entities are defined in `docs/api/openapi.yaml` under the
matching schemas; this section is the canonical description.

### 2.1 `provider_model_price`

| Field | Type | Notes |
|---|---|---|
| `id` | string | PK. |
| `provider_id` | string | e.g. `bfl`. |
| `model_id` | string | e.g. `flux-2-klein`. |
| `operation_type` | enum | `text_to_image` / `image_to_image` / `upscale` / `variant_pack` / `edit`. |
| `unit_type` | enum | `image` / `megapixel` / `second` / `credit` / `request`. The unit `price_per_unit` is denominated in. |
| `price_per_unit` | string | Fixed-decimal in `currency`. |
| `currency` | string | ISO 4217. Default `USD`. |
| `effective_from` | timestamp | When this price became active. |
| `effective_to` | timestamp / null | Null while the entry is current. Set when superseded. |
| `is_active` | bool | Convenience flag synchronized with `effective_from <= now < effective_to`. |
| `source` | string | Provenance (e.g. `provider_pricing_page`). |
| `notes` | string | Free-form. |

Multiple entries per (provider × model × operation_type) are allowed and
expected; selecting "the current price" is `is_active = true` ordered by
`effective_from DESC` LIMIT 1.

### 2.2 `cost_budget`

| Field | Type | Notes |
|---|---|---|
| `id` | string | PK. |
| `tenant_id` | string | Tenant that owns the budget. Always set, even for narrower scopes. |
| `scope_type` | enum | `tenant` / `token` / `world` / `user`. |
| `scope_id` | string | Identifier within the scope. For `scope_type=tenant`, equal to `tenant_id`. |
| `period` | enum | `daily` / `monthly`. |
| `limit_amount` | string | Hard cap per period, in `currency`. |
| `reserved_amount` | string | Sum of in-flight reservations for current period. Read-only. |
| `spent_amount` | string | Sum of committed actuals for current period. Read-only. |
| `currency` | string | Default `USD`. |
| `status` | enum | `active` (enforcing) / `paused` (recording only) / `exceeded` (rejecting new reservations until period reset or limit raised). |

Stacking semantics: a request that targets a narrower scope (token, world,
user) must fit inside **both** its own budget and the parent `tenant`
budget. The reservation step (§3 step 7) checks every applicable budget;
the most-restrictive failure is reported.

### 2.3 `cost_reservation`

| Field | Type | Notes |
|---|---|---|
| `id` | string | PK. |
| `generation_job_id` | string | The job this reservation belongs to. |
| `tenant_id` | string | For partitioning. |
| `estimated_amount` | string | Pre-flight cost estimate from the price book. |
| `reserved_amount` | string | Actually held against the budget. Normally equal to estimate; may include a configurable safety margin. |
| `actual_amount` | string / null | Committed cost when the provider reports back. Null until terminal. |
| `currency` | string | Default `USD`. |
| `status` | enum | `reserved` (in flight) / `committed` (job succeeded, charged actual) / `released` (job cancelled or failed, budget returned) / `failed` (could not reserve — e.g. budget exceeded). |
| `failure_reason` | string | Populated when `status=failed` (`budget_exceeded`, `no_price_entry`). |
| `created_at`, `updated_at` | timestamp | |

## 3. Pre-flight cost estimation pipeline

Every generation request follows this sequence. The pipeline is
deterministic and runs *before* the worker is enqueued so cost denials
return synchronously and don't waste provider attempts.

1. **Request received.** API handler validates payload (per OpenAPI).
2. **Tenant resolved.** Auth middleware (ADR-004) maps the bearer token
   to `tenant_id` and attaches it to the request context. Clients must
   not send `tenant_id` in the body.
3. **Provider / model / operation selected (or predicted).** The router
   (ADR-007) picks a `provider_model` and the `operation_type` implied by
   the request (e.g. character pack with quality `standard` →
   `bfl/flux-2-klein/variant_pack`). For multi-operation jobs (pack with
   preview + final), each operation is estimated separately and summed.
4. **Price loaded from price book.** SELECT the active
   `provider_model_price` row for (provider × model × operation_type).
   Cache for the duration of the request to avoid repeated lookups.
5. **Estimated cost calculated.** `estimated_amount = price_per_unit ×
   units(request)` where `units` is derived from the request (image
   count for `unit_type=image`; output area for `megapixel`; etc.).
   Sum across operations for batch / pack jobs.
6. **Budget checked.** For every applicable budget (tenant always; plus
   token / world / user when set), verify
   `limit_amount - reserved_amount - spent_amount >= estimated_amount`
   for the current period. Apply the safety margin from configuration
   if set.
7. **Estimated amount reserved.** INSERT a `cost_reservation` row with
   `status=reserved`, atomically incrementing `reserved_amount` on every
   applicable budget. This step is the consistency point — concurrent
   requests must not be able to over-reserve. Use a single transaction
   per request that updates all relevant budget rows with
   `SELECT ... FOR UPDATE` or equivalent.
8. **Job enqueued.** Create the `generation_job` row referencing the
   reservation; push to the worker queue (ADR-013). Return
   `202 Accepted` with `job_id`, `estimated_cost_usd`,
   `cost_reservation_id`.
9. **On success, actual cost committed.** When the job terminates
   `completed` and the provider reports actual cost, transition the
   reservation to `committed` with `actual_amount`. Subtract the
   reservation from `reserved_amount` and add the actual to
   `spent_amount` on every applicable budget atomically.
10. **On failure / cancel, reservation released.** Transition to
    `released`. Subtract from `reserved_amount` on every applicable
    budget; do **not** add to `spent_amount`. If the provider partially
    charged (e.g. preview succeeded, final failed), commit the partial
    actual and release the unused remainder.
11. **Cost event emitted.** Insert a `generation_cost_event` row for
    telemetry (existing table in `docs/db/initial_schema.sql`). This
    row carries provider, model, operation, estimated, actual,
    duration, and job ID — the same fields the cost-spike runbook
    queries via `GET /v1/admin/cost-events`.

## 4. Behavior rules

### 4.1 No price entry → fail closed (with explicit escape hatch)

If §3 step 4 finds no active `provider_model_price` row for the selected
(provider × model × operation_type), the platform **must reject the
request** with `provider_unpriced` error code (HTTP 503).

The single exception: a route may be explicitly flagged
`allow_unpriced_provider = true` for internal testing. Such a route MUST
NOT serve production traffic — the router refuses to use it unless the
caller's token carries `admin:*` scope.

### 4.2 Estimated cost returned in generation-job responses

The `202 Accepted` response from every generation endpoint includes:

- `job_id`
- `estimated_cost_usd`
- `currency`
- `cost_reservation_id`
- `status: queued`

Job status responses (`GET /v1/jobs/{job_id}`) carry both
`cost_estimate_usd` (the pre-flight number) and `actual_cost_usd`
(populated after `committed`). Cost-spike investigations can join these
back through the `cost_reservation` row.

### 4.3 Estimated vs. actual

Jobs always store both:

- **Estimated:** from §3 step 5; immutable after `reserved`.
- **Actual:** from the provider's billing response, written when the
  reservation transitions to `committed`. May be null if the provider
  doesn't report per-call cost — in which case `actual_amount =
  estimated_amount` is committed and a `cost_event.notes` line records
  that it's a fallback.

### 4.4 Reservations prevent overselling

The transaction in §3 step 7 is the only way to credit
`reserved_amount`. With proper row-locking, N concurrent requests for
the same budget that collectively exceed the limit will see N-1
succeed (until the limit) and the rest fail with `budget_exceeded`.

### 4.5 `daily_cost_usd` rate-limit dimension

The `daily_cost_usd` dimension referenced by `docs/api/rate-limits.md`
is derived as:

```
daily_cost_usd_for(token) =
    sum(reserved_amount on token-scope budget for today)
  + sum(spent_amount on token-scope budget for today)
```

The token-scope budget's `limit_amount` is the cap. If the request
would exceed it, the budget check (§3 step 6) returns
`budget_exceeded` before any provider work is started. There is no
separate rate-limit counter for daily cost — the budget *is* the
counter.

## 5. Failure modes

| Failure | Where | Status code | Surface |
|---|---|---|---|
| No price entry | §3 step 4 | 503 `provider_unpriced` | Reservation `failed`, `failure_reason=no_price_entry`. |
| Budget exceeded | §3 step 6/7 | 429 `budget_exceeded` | Reservation `failed`, `failure_reason=budget_exceeded`. Error body names the offending `cost_budget.id`. |
| Reservation race lost | §3 step 7 | 429 `budget_exceeded` | Same as above. |
| Provider charges more than estimated | §3 step 9 | None (logged) | `committed` records `actual_amount`. Cost-spike monitor watches the estimate-vs-actual ratio and warns at +50% / +100% (`observability.md`). |
| Provider doesn't report cost | §3 step 9 | None | `actual_amount = estimated_amount`; `cost_event.notes` flags `actual_inferred_from_estimate`. |

## 6. Operational integration

- **Rate limits** (`docs/api/rate-limits.md`) use this surface for
  cost-shaped dimensions.
- **Cost-spike runbook** (`docs/runbooks/cost-spike.md`) inspects
  `cost_events`, `cost_reservations`, and `cost_budgets` and mitigates
  via `PUT /v1/admin/cost-budgets/{id}` (lower `limit_amount` or set
  `status=paused`) and `POST /v1/admin/routes/{id}/disable` (turn off
  the expensive route).
- **Price-book updates** (incident review or pricing change) go via
  `POST /v1/admin/price-book` (new entry) rather than editing
  in-place; the previous entry gets its `effective_to` set so audit
  preserves history.

## 7. Open follow-ups

- **Configurable safety margin** on the reservation (`reserved_amount =
  estimated_amount × (1 + margin_pct)`) — pick a default (10%?) before
  enabling enforcement.
- **Quality-tier downgrade enforcement** — when a budget hits its soft
  warning (e.g. 80%), should the router auto-downgrade `quality_tier`
  to `draft`? Behavior is undefined here; currently a separate
  decision via `POST /v1/admin/routes/{id}/disable`.
- **Per-period reset semantics** — daily resets at tenant-local
  midnight or UTC? Spec'd as UTC for MVP; revisit when the platform
  serves customers across timezones.
- **Provider-reported cost reconciliation** — for providers that report
  with delay (hours), the reservation stays in `committed` with
  `actual_amount = estimated_amount` until reconciliation overwrites
  it. The reconciliation worker is unspecified here.

---

## Confidence to Implement

**Score: 90/100 — Very High**

The data model, the 11-step pipeline, and the failure surface are all
concrete. Postgres + row-level locks in §3 step 7 handle concurrent
correctness without exotic infra. The admin endpoints to manage the
price book, budgets, and reservations all exist in
`docs/api/openapi.yaml` (PLANNED). Subtracting points only for the four
open follow-ups in §7 — each is a defined decision, not a research
question.
