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
  "amount_usd": "50.00",
  "window": "day",
  "downgrade_to_tier": "draft"
}
```

This caps spend at the scope and instructs the router to downgrade to draft
quality once the soft warning fires.

| Endpoint | Scope | Status |
|---|---|---|
| `PUT /v1/admin/cost-budgets/{id}` | `admin:costs` | **PLANNED** |
| Future CLI: `dci-admin costs budget set <id> --amount-usd 50 --downgrade-to draft` | — | planned |
| **MANUAL** fallback: `UPDATE cost_budgets SET amount_usd = '50.00', downgrade_to_tier = 'draft' WHERE id = '<budget_id>';` — write audit event by hand. | — | manual |

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

### 4c. Force draft tier for the offending token (last resort)

If the spike is from a single client repeatedly generating, lower their cap
via the token's budget:

- Find their token-scoped budget via §3.
- Set `downgrade_to_tier: "draft"` and `hard_limit_pct: 80`.

(This requires a token-scoped budget to exist. If not, create one — `POST
/v1/admin/cost-budgets` will be added alongside the PUT in the same
implementation slice.)

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
- **Update the price book** if the cost-per-call was higher than estimated:
  ```http
  PUT /v1/admin/price-book/{provider_model_id}
  Content-Type: application/json
  { "operation": "generate_final", "estimated_cost_usd": "0.08",
    "source": "incident_review_2026-06-05" }
  ```
  Scope `admin:costs` — **PLANNED**.

## 7. Audit events expected

| When | event_type | Source |
|---|---|---|
| §4a budget change | `admin.cost_budget.updated` | Endpoint emits |
| §4b route disable | `admin.route.disabled` | Endpoint emits |
| §6 price-book update | `admin.price_book.updated` | Endpoint emits |
| Any **MANUAL** action | Written by hand via planned `POST /v1/admin/audit-events` | Operator |

Verify in `GET /v1/admin/audit-events` (planned).

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
