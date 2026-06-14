# Runbook — Cost Spike

> **Some controls referenced below are PLANNED — required admin surface for
> implementation, not yet served.** See
> `docs/architecture/admin-control-surface.md`.

## 1. Symptoms

- `estimated_cost_usd` per hour above the alerting threshold.
- One token or world is responsible for a disproportionate fraction of cost.
- `asset_cache_hit_count / asset_cache_miss_count` ratio dropped recently.
- Same client repeatedly regenerating the same asset (idempotency miss).
- High-cost models being used for low-priority work.

## 2. Cost event query

Identify the source. Group by token, world, provider, asset_type.

### By token

```http
GET /v1/admin/cost-events?created_after=<ISO8601>&limit=500
Authorization: Bearer <admin-token>   # scope: admin:costs
```

Aggregate client-side by `token_id` (or use the planned
`GET /v1/admin/cost-events/aggregate?group_by=token_id`).

| Endpoint | Scope | Status |
|---|---|---|
| `GET /v1/admin/cost-events` | `admin:costs` | **PLANNED** |
| Future CLI: `dci-admin costs events --since 1h --group-by token` | — | planned |
| **MANUAL** fallback: `SELECT token_id, SUM(actual_cost_usd::numeric) FROM generation_cost_events WHERE created_at > now() - interval '1 hour' GROUP BY token_id ORDER BY 2 DESC LIMIT 20;` — record in audit log. | — | manual |

### By world / provider / asset type

Same endpoint, group differently:

- by `world_id` → which world is over budget?
- by `provider_id` and `model_id` → is one model dominating cost?
- by `asset_type` → are character packs (typically `pack_capable` providers)
  outweighing artifacts?

### By cache miss

```http
GET /v1/admin/cost-events?created_after=<ISO8601>
                        &cache_result=miss_generated
```

A spike in `miss_generated` cost events with falling `exact_cache_hit` is a
strong signal that retrieval is failing (variant compatibility too narrow,
identity version churning, style profile churning).

## 3. Budget inspection

Find the relevant budgets:

```http
GET /v1/admin/cost-budgets
Authorization: Bearer <admin-token>   # scope: admin:costs
```

| Endpoint | Scope | Status |
|---|---|---|
| `GET /v1/admin/cost-budgets` | `admin:costs` | **PLANNED** |
| Future CLI: `dci-admin costs budgets list` | — | planned |

Identify the budget(s) at the affected scope (tenant, world, token, or global)
and their current utilization (`amount_usd` vs. recent spend).

## 4. Mitigation

Pick one or more, in order of preference:

### 4a. Temporarily lower a budget (preferred — bounded blast radius)

```http
PUT /v1/admin/cost-budgets/{budget_id}
Authorization: Bearer <admin-token>   # scope: admin:costs
Content-Type: application/json

{
  "limit_amount": "50.00",
  "status": "active"
}
```

This caps spend at the scope. The pre-flight estimation pipeline
(`docs/architecture/cost-control.md` §3) immediately starts rejecting new
reservations once the period's
`reserved_amount + spent_amount >= limit_amount`. To stop new
generation entirely without changing the cap, set `status: "paused"`
(records cost but does not deny) or `status: "exceeded"` (denies new
reservations until the period resets or the limit is raised).

To **force quality downgrade** for the affected scope rather than deny,
use 4b (disable the high-tier route) so the router falls through to a
cheaper alternative.

| Endpoint | Scope | Status |
|---|---|---|
| `PUT /v1/admin/cost-budgets/{id}` | `admin:costs` | **PLANNED** |
| Future CLI: `dci-admin costs budget set <id> --limit-usd 50` | — | planned |
| **MANUAL** fallback: `UPDATE cost_budgets SET limit_amount = '50.00', status = 'active' WHERE id = '<budget_id>';` — write audit event by hand. | — | manual |

### 4b. Disable an expensive route

If one model is responsible for most of the cost, disable the route rather
than the whole provider:

```http
POST /v1/admin/routes/{route_id}/disable
Authorization: Bearer <admin-token>   # scope: admin:routes
Content-Type: application/json

{ "reason": "Cost spike; high-tier model used for low-priority world; routing away" }
```

| Endpoint | Scope | Status |
|---|---|---|
| `POST /v1/admin/routes/{id}/disable` | `admin:routes` | **PLANNED** |
| Future CLI: `dci-admin routes disable <route_id> --reason "..."` | — | planned |

### 4c. Tighten the offending token's budget (last resort)

If the spike is from a single client repeatedly generating, tighten
their token-scoped cap:

- Find the token-scoped budget via §3 (filter `scope_type=token`,
  `scope_id=<their token_id>`).
- Lower `limit_amount` or set `status: "exceeded"` to block new
  reservations immediately. The period reset (daily / monthly) will
  return them to normal once the underlying behavior is understood.

If no token-scoped budget exists, create one:

```http
POST /v1/admin/cost-budgets
Authorization: Bearer <admin-token>   # scope: admin:costs
Content-Type: application/json

{
  "tenant_id": "<tenant>",
  "scope_type": "token",
  "scope_id": "<their token_id>",
  "period": "daily",
  "limit_amount": "5.00",
  "currency": "USD",
  "status": "active"
}
```

| Endpoint | Scope | Status |
|---|---|---|
| `POST /v1/admin/cost-budgets` | `admin:costs` | **PLANNED** |
| `PUT /v1/admin/cost-budgets/{id}` | `admin:costs` | **PLANNED** |

## 5. Confirm mitigation

After applying §4, wait 5–10 minutes and re-query:

```http
GET /v1/admin/cost-events?created_after=<5_minutes_ago>
                        &<same filters as §2>
```

The cost rate should drop. Verify the budget's recent spend curve flattens.

If the rate does not drop, the mitigation didn't reach the cause — re-check
§2 with broader filters, and consider §4b (disable route) if you started
with §4a (lower budget).

## 6. Follow-up

After the incident:

- **Improve retrieval-before-generation** — if the spike was driven by cache
  misses, investigate variant compatibility matrix gaps (PRD 05) or identity
  version churn.
- **Add a budget alert** at the scope that surprised you (tenant, world,
  token) if one wasn't already set.
- **Add per-world / per-session cost caps** if a runaway world session caused
  this.
- **Review idempotency usage** — if the spike was driven by a client retrying
  without an `Idempotency-Key`, file a bug against that client.
- **Update the price book** if the cost-per-call was higher than estimated.
  Post a *new* `provider_model_price` entry rather than editing in place
  (the previous entry's `effective_to` is auto-set on POST so audit
  preserves history). See `docs/architecture/cost-control.md` §2.1.

  ```http
  POST /v1/admin/price-book
  Content-Type: application/json

  {
    "provider_id": "bfl",
    "model_id": "flux-2-klein",
    "operation_type": "variant_pack",
    "unit_type": "image",
    "price_per_unit": "0.08",
    "currency": "USD",
    "effective_from": "2026-06-05T18:00:00Z",
    "source": "incident_review_2026-06-05"
  }
  ```
  Scope `admin:costs` — **PLANNED**.

- **Inspect live reservations** to see what's currently held against
  the budget:
  ```http
  GET /v1/admin/cost-reservations?status=reserved&tenant_id=<t>&limit=200
  ```
  Scope `admin:costs` — **PLANNED**. Useful when a budget reads as full
  but actual spend looks low (lots of in-flight work that hasn't
  reconciled).

## 7. Audit events expected

| When | event_type | Source |
|---|---|---|
| §4a budget change | `admin.cost_budget.updated` | Endpoint emits |
| §4b route disable | `admin.route.disabled` | Endpoint emits |
| §4c create budget | `admin.cost_budget.created` | Endpoint emits |
| §6 price-book update | `admin.price_book.created` (new entry) and `admin.price_book.updated` (previous entry's `effective_to` set) | Endpoints emit |
| Any **MANUAL** action | Record in the incident ticket | Operator |

> **Note (audit trail).** The served admin cost endpoints (price-book and
> cost-budget writes) write `audit_events` rows automatically, in the same
> transaction as the change. There is **no** `POST /v1/admin/audit-events` /
> `GET /v1/admin/audit-events` endpoint — those are **non-MVP / planned**. For
> **MANUAL** actions, record them in the incident ticket; to review the
> automatic trail, query the `audit_events` table directly (read-only SQL).

---

## Confidence to Implement

**Score: 85/100 — High** *(was 72; +13 after admin control surface specified)*

Every diagnostic and mitigation step now maps to a planned endpoint with a
defined scope and example body, plus a manual SQL fallback for the
implementation gap. The price-book, cost-budgets, and cost-events surfaces
are explicitly defined in `docs/api/openapi.yaml` and supported by schemas.
The remaining unknowns are the actual budget aggregation pipeline (does the
platform compute live spend from cost_events or pre-aggregated rollups?)
and the price-book → pre-flight estimation flow — both decisions belong in
follow-up work, not in this runbook.
