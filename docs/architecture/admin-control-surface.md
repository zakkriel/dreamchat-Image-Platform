# Admin Control Surface (planned)

> **Status: required admin surface for implementation. Not yet served.**
>
> Endpoints described here are referenced by the operational runbooks
> (`docs/runbooks/provider-failure.md`, `docs/runbooks/failed-jobs.md`,
> `docs/runbooks/cost-spike.md`). They are defined in `docs/api/openapi.yaml`
> under the `Admin` tag with explicit `**PLANNED**` markers. None of this
> tooling exists in code yet — this document is the specification for it.

## Purpose

Every runbook prescribes operator actions during incidents: disabling a failing
provider, retrying a job, lowering a cost budget, etc. Without a defined admin
surface, those actions are either impossible (the operator has nothing to call)
or ad-hoc (the operator runs SQL by hand, which is fast, scary, and not
auditable).

This document defines the minimum admin surface required to make the runbooks
executable. It distinguishes between **documented endpoints** (the platform
must serve these), **CLI commands** (alternative for ops users), and **manual
actions** (last resort, with audit expectations).

## Required runbook action mapping

Every action a runbook can prescribe must map to **one** of:

1. A documented admin HTTP endpoint (preferred).
2. A documented future CLI command (for actions that need a TTY or interactive
   confirmation; the CLI calls the same endpoints).
3. A clearly marked manual action (e.g. raw SQL — only when no automated path
   is appropriate, must be audited).

Runbooks may not say "disable the provider" without pointing at one of these.

## Admin scopes

Admin endpoints require admin scopes. The auth middleware (ADR-004) enforces
these alongside the existing `images:*`, `jobs:*`, `styles:*`, `models:*`,
`admin:tokens`, `admin:costs`, `admin:providers` scopes.

| Scope | Grants |
|---|---|
| `admin:providers` | View, disable, and re-enable provider models. |
| `admin:routes` | View, disable, and re-enable provider routes. |
| `admin:jobs` | View, retry, and cancel generation jobs. |
| `admin:costs` | View and edit the price book and cost budgets, view cost events. |
| `admin:tokens` | (existing) Issue, list, and revoke API tokens. |

Each admin scope is granted independently. A token may have `admin:jobs`
without `admin:costs`. The runbooks specify which scope each action needs.

## Required admin endpoints

The following endpoints are required for runbook coverage. Full schemas live in
`docs/api/openapi.yaml` (marked `**PLANNED**`).

### Provider controls

- `GET /v1/admin/providers` — list providers and their current status
  (`active | degraded | disabled`), capability levels, current circuit-breaker
  state, last failure timestamp.
- `POST /v1/admin/providers/{provider_id}/disable` — disable a provider; the
  router stops routing new jobs to it. Body: `{ reason: string }`. Emits
  `admin.provider.disabled` audit event.
- `POST /v1/admin/providers/{provider_id}/enable` — re-enable a previously
  disabled provider. Emits `admin.provider.enabled` audit event.

### Route controls

A route is the (provider × asset_type × quality_tier × latency_tier)
combination used by the router (ADR-007). Disabling a route is narrower than
disabling a provider — useful when a provider is broken for character packs
but fine for artifacts.

- `GET /v1/admin/routes` — list routing rules and their status.
- `POST /v1/admin/routes/{route_id}/disable` — disable a single route. Body:
  `{ reason: string }`. Emits `admin.route.disabled`.
- `POST /v1/admin/routes/{route_id}/enable` — re-enable. Emits
  `admin.route.enabled`.

### Job controls

- `GET /v1/admin/jobs` — list jobs with filters: `status`, `provider_id`,
  `created_after`, `created_before`, `world_id`, `token_id`, `tenant_id`.
  Used by failed-jobs runbook to find retryable / cancellable work.
- `POST /v1/admin/jobs/{job_id}/retry` — create a new generation job that
  references the original. Body: `{ reason: string }`. Returns the new
  `job_id`. Emits `admin.job.retried`.
- `POST /v1/admin/jobs/{job_id}/cancel` — cancel a queued or running job.
  Body: `{ reason: string }`. Emits `admin.job.cancelled`.

### Cost controls

- `GET /v1/admin/price-book` — list per-(provider_model × operation) prices
  used for pre-flight cost estimation (referenced by `docs/api/rate-limits.md`).
- `PUT /v1/admin/price-book/{provider_model_id}` — update price entry. Body:
  `{ operation, estimated_cost_usd, source, notes }`. Emits
  `admin.price_book.updated`.
- `GET /v1/admin/cost-budgets` — list budgets (per-tenant, per-world,
  per-token, per-day).
- `PUT /v1/admin/cost-budgets/{budget_id}` — update a budget cap. Body:
  `{ amount_usd, soft_warning_pct, hard_limit_pct, downgrade_to_tier }`.
  Emits `admin.cost_budget.updated`.
- `GET /v1/admin/cost-events` — query the cost_event log with filters by
  token, provider, asset_type, world, time range.

## Audit events

Every admin write endpoint emits an `audit_event` row with:

- `event_type` (e.g. `admin.provider.disabled`)
- `actor_token_id` (the admin token that performed the action)
- `resource_type` and `resource_id`
- `metadata` JSON containing the action's `reason` and any other relevant
  parameters

These events are the source of truth for "who did what during the incident."
Runbooks reference the expected event types so an operator can verify the
audit trail.

## Future CLI

A future `dci-admin` CLI may wrap these endpoints for interactive use:

```
dci-admin providers list
dci-admin providers disable <provider_id> --reason "..."
dci-admin jobs retry <job_id> --reason "..."
dci-admin costs budget set <budget_id> --amount-usd 100
```

The CLI is not in MVP scope. Runbooks reference both endpoint and the planned
CLI command for each action.

## Manual actions (escape hatch)

A runbook step may prescribe a manual action when no admin endpoint exists
yet. Such steps must:

- be explicitly marked `**MANUAL**`,
- describe exactly what the operator does (SQL query, queue command, etc.),
- name the audit_event that must be written by hand afterwards (using the
  `POST /v1/admin/audit-events` endpoint, when that lands).

Manual actions are a temporary bridge between "we know this needs to happen"
and "we've built the endpoint for it."

## Implementation order

When the platform implements this surface, the recommended order is:

1. `GET /v1/admin/providers` and `POST .../{id}/disable|enable` (covers
   provider-failure runbook).
2. `GET /v1/admin/jobs` with filters and `POST .../{id}/retry|cancel`
   (covers failed-jobs runbook).
3. `GET /v1/admin/cost-events` and `GET /v1/admin/cost-budgets` (read-only
   surface for cost-spike investigation).
4. `PUT .../cost-budgets/{id}` and route-level disable (cost-spike
   mitigation).
5. Price-book CRUD (enables pre-flight cost estimation, supports
   `rate-limits.md` `estimated_cost_per_day` dimension).
