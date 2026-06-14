# Runbook — Provider Failure

> **Some controls referenced below are PLANNED — required admin surface for
> implementation, not yet served.** See
> `docs/architecture/admin-control-surface.md`.

## 1. Symptoms

- Increased generation_job failure rate concentrated on one provider.
- Provider timeout / capacity errors in job results.
- Queue depth increasing because work is failing-then-retrying instead of
  completing.
- `cost_event.actual_cost_usd` missing for recent jobs.
- Web app users reporting blank previews or generation failures.

## 2. Detection metric

The on-call dashboard should surface:

- `provider_call_failure_count` per (provider, model) in the last 15 min.
- `generation_job_failure_count` filtered by `error_code` starting with
  `provider_`.
- Circuit-breaker state per provider (via `GET /v1/admin/providers`,
  field `circuit_breaker_state`).
- Queue depth (`queue_depth` metric).

Trigger: failure rate for one provider > 25% over a 5-minute window, OR
circuit-breaker transitions to `open`.

## 3. Confirm the scope of the failure

Query failing jobs for the affected provider:

```http
GET /v1/admin/jobs?status=failed&provider_id=<provider_id>
                 &created_after=<ISO8601>
Authorization: Bearer <admin-token>   # scope: admin:jobs
```

| Endpoint | Scope | Status |
|---|---|---|
| `GET /v1/admin/jobs` | `admin:jobs` | **PLANNED** |
| Future CLI: `dci-admin jobs list --provider <id> --status failed` | — | planned |
| **MANUAL** fallback while planned endpoints are unavailable: `SELECT id, error_code FROM generation_jobs WHERE status='failed' AND input_payload->>'provider_id' = '<provider_id>' AND created_at > now() - interval '30 minutes';` — record action in audit log by hand. | — | manual |

Note whether failures are concentrated on:

- a single model (route-level mitigation)
- the whole provider (provider-level mitigation)

## 4. Disable the provider or route

Pick **one** based on §3 scope:

### Route-level disable (preferred when only one route is failing)

```http
POST /v1/admin/routes/{route_id}/disable
Authorization: Bearer <admin-token>   # scope: admin:routes
Content-Type: application/json

{ "reason": "Sustained provider_timeout on character_portrait/high/balanced" }
```

| Endpoint | Scope | Status |
|---|---|---|
| `POST /v1/admin/routes/{id}/disable` | `admin:routes` | **PLANNED** |
| Future CLI: `dci-admin routes disable <route_id> --reason "..."` | — | planned |

### Provider-level disable (when the provider is broadly degraded)

```http
POST /v1/admin/providers/{provider_id}/disable
Authorization: Bearer <admin-token>   # scope: admin:providers
Content-Type: application/json

{ "reason": "Provider status page reports incident; routing all traffic away" }
```

| Endpoint | Scope | Status |
|---|---|---|
| `POST /v1/admin/providers/{id}/disable` | `admin:providers` | **PLANNED** |
| Future CLI: `dci-admin providers disable <provider_id> --reason "..."` | — | planned |

## 5. Confirm the provider / route is disabled

```http
GET /v1/admin/providers     # scope: admin:providers — PLANNED
GET /v1/admin/routes        # scope: admin:routes — PLANNED
```

Check the affected resource's `status` is `disabled`, `disabled_reason` is set,
and `disabled_at` is recent.

## 6. Decide on retrying or leaving failed jobs

Apply the retry policy from `docs/architecture/job-lifecycle.md`:

- Retryable: `provider_timeout`, `provider_capacity_error`, `provider_unknown_error` (within retry budget).
- Not retryable: `provider_content_rejected`, `provider_auth_failed`, `provider_invalid_request`.

For retryable jobs, requeue via:

```http
POST /v1/admin/jobs/{job_id}/retry      # scope: admin:jobs — PLANNED
Content-Type: application/json

{ "reason": "Provider X recovered; retrying jobs failed during incident window" }
```

| Endpoint | Scope | Status |
|---|---|---|
| `POST /v1/admin/jobs/{id}/retry` | `admin:jobs` | **PLANNED** |
| Future CLI: `dci-admin jobs retry <job_id> --reason "..."` | — | planned |

For non-retryable jobs, leave them in `failed` status. Their `error_code`
documents the reason; the audit trail (§8) carries the incident link.

## 7. Re-enable after recovery

When the provider's status page reports recovery and a small sample of test
jobs succeeds:

```http
POST /v1/admin/providers/{provider_id}/enable
POST /v1/admin/routes/{route_id}/enable
```

Both require `admin:providers` / `admin:routes` respectively — both **PLANNED**.

After re-enabling:

1. Check `circuit_breaker_state` is `closed` (or, briefly, `half_open` while
   the router probes).
2. Watch `provider_call_failure_count` for 15 minutes.
3. If the rate stays low, the incident is closed.

## 8. Audit events expected

The following audit_events should be written during this runbook:

| When | event_type | Source |
|---|---|---|
| §4 disable | `admin.provider.disabled` or `admin.route.disabled` | Endpoint emits (planned endpoint) |
| §6 retry | `admin.job.retried` (one per retried job) | Endpoint emits (planned endpoint) |
| §7 enable | `admin.provider.enabled` or `admin.route.enabled` | Endpoint emits (planned endpoint) |
| Any **MANUAL** action | Record in the incident ticket | Operator |

> **Note (audit trail).** Served admin write endpoints write `audit_events`
> rows automatically, in the same transaction as the change. There is **no**
> `POST /v1/admin/audit-events` or `GET /v1/admin/audit-events` endpoint —
> those are **non-MVP / planned**. For **MANUAL** actions, record what you did
> in the incident ticket; do not assume an endpoint will capture it. To review
> the automatic trail, query the `audit_events` table directly (read-only SQL).
> The provider/route disable/enable rows above appear only once those admin
> endpoints are implemented (still **planned** — see
> `docs/architecture/admin-control-surface.md`).

## 9. Follow-up

- Open an incident note linking to the affected `audit_event` IDs.
- Classify failed jobs as retryable or terminal (see step 6).
- Update `docs/architecture/provider-adapters.md` if a new error mapping
  was needed.
- If this was the first time the route was disabled, add it to the
  benchmark corpus rotation.

---

## Confidence to Implement

**Score: 85/100 — High** *(was 75; +10 after admin control surface specified)*

The procedure now maps every action to a documented endpoint, a planned CLI command, or a clearly marked manual fallback. Endpoints are in `docs/api/openapi.yaml` under the Admin tag and the schemas validate. The remaining gap is that the endpoints themselves aren't built yet — once they ship, this runbook is directly executable. Until then, the manual SQL fallback is the bridge.
