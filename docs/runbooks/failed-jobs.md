# Runbook — Failed Jobs

> **Some controls referenced below are PLANNED — required admin surface for
> implementation, not yet served.** See
> `docs/architecture/admin-control-surface.md`.

## 1. Job failure symptoms

- `generation_job_failure_count` metric elevated.
- Web app surfacing `failed` status to users.
- Concentrated by `error_code` (one failure class) or scattered (many).
- Sometimes accompanied by `storage_upload_failed` if the post-generation
  upload step broke.

Common error codes (per `docs/api/errors.md`):

```
provider_timeout
provider_rate_limited
provider_content_rejected
provider_auth_failed
provider_capacity_error
storage_upload_failed
invalid_prompt_package
unknown_error
```

## 2. Query and filter failed jobs

```http
GET /v1/admin/jobs?status=failed
                 &error_code=<code>
                 &created_after=<ISO8601>
                 &limit=200
Authorization: Bearer <admin-token>   # scope: admin:jobs
```

| Endpoint | Scope | Status |
|---|---|---|
| `GET /v1/admin/jobs` | `admin:jobs` | **PLANNED** |
| Future CLI: `dci-admin jobs list --status failed --error-code <c>` | — | planned |
| **MANUAL** fallback: `SELECT id, error_code, error_message, retryable, input_payload->>'world_id' AS world_id, created_at FROM generation_jobs WHERE status='failed' AND error_code = '<code>' AND created_at > now() - interval '24 hours' ORDER BY created_at DESC LIMIT 200;` — record in audit log. | — | manual |

For provider-specific failures (`error_code` starts with `provider_`),
cross-reference the provider-failure runbook (`provider-failure.md`).

## 3. Investigation per job

For each failing job worth investigating:

1. `GET /v1/jobs/{job_id}` — confirm latest status and error fields.
2. `GET /v1/admin/jobs/{job_id}` (**PLANNED**) — full payload, retryable flag,
   provider_attempts, cost events.
3. Worker logs filtered by `job_id` and `request_id`.
4. Storage state — was the asset row partially written before failure?

## 4. Retry eligible jobs

Retry rules (per `docs/architecture/job-lifecycle.md`):

- **Safe to retry:** `provider_timeout`, `provider_capacity_error`, transient
  `storage_upload_failed`, network failures before provider acceptance.
- **Do not retry automatically:** `provider_content_rejected`,
  `provider_auth_failed`, `provider_invalid_request`, `invalid_prompt_package`.

```http
POST /v1/admin/jobs/{job_id}/retry
Authorization: Bearer <admin-token>   # scope: admin:jobs
Content-Type: application/json

{ "reason": "Provider recovered; retrying after incident <link>" }
```

For non-retryable codes that you intentionally want to retry (e.g. provider
config was wrong and is now fixed), set `force: true`:

```json
{ "reason": "Provider auth fixed at <ts>; force-retry batch", "force": true }
```

| Endpoint | Scope | Status |
|---|---|---|
| `POST /v1/admin/jobs/{id}/retry` | `admin:jobs` | **PLANNED** |
| Future CLI: `dci-admin jobs retry <job_id> --reason "..." [--force]` | — | planned |
| **MANUAL** fallback: re-enqueue by inserting a new `generation_jobs` row referencing the original `id` and pushing to the worker queue. Mark old row's `error_message` with a pointer to the new job. Write `admin.job.retried` audit_event by hand. | — | manual |

## 5. Cancel invalid jobs

For jobs that should not be retried (and should not stay in `failed` consuming
analytics noise):

```http
POST /v1/admin/jobs/{job_id}/cancel
Authorization: Bearer <admin-token>   # scope: admin:jobs
Content-Type: application/json

{ "reason": "Provider content policy violation; will not retry" }
```

| Endpoint | Scope | Status |
|---|---|---|
| `POST /v1/admin/jobs/{id}/cancel` | `admin:jobs` | **PLANNED** |
| Future CLI: `dci-admin jobs cancel <job_id> --reason "..."` | — | planned |

## 6. Partial-success handling

For jobs that reached `preview_ready` before failing on the final step:

- Keep completed preview assets (they're already in `visual_assets` with
  `status='preview_ready'` or `ready`).
- The job stays `failed` with `preview_asset_ids` populated.
- Allow regeneration for missing high-res variants via:
  ```http
  POST /v1/assets/{asset_id}/regenerate
  Authorization: Bearer <token>   # scope: images:write (existing)
  ```

This path uses the **existing** `images:write` scope — not admin — because
asset-level regeneration is a normal API operation that any owner can trigger.

## 7. Escalate provider-specific failures

If §2 shows a single provider is the source of most failures:

1. Follow steps 4–7 of `provider-failure.md` (disable provider/route,
   audit, re-enable after recovery).
2. Bulk-retry jobs failed during the incident window after provider is
   re-enabled (loop §4 over the filtered list, or use a planned future
   batch-retry endpoint `POST /v1/admin/jobs/batch-retry`).

## 8. Audit events expected

| When | event_type | Source |
|---|---|---|
| §4 retry | `admin.job.retried` (one per job) | Endpoint emits |
| §5 cancel | `admin.job.cancelled` (one per job) | Endpoint emits |
| Any **MANUAL** action | Written by hand via planned `POST /v1/admin/audit-events` | Operator |

## 9. Follow-up

- If a new failure class appeared, add it to `docs/api/errors.md` and the
  provider error normalization vocabulary in `docs/architecture/provider-adapters.md`.
- If the same class fires again, consider promoting it from "ad-hoc retry"
  to "automatic retry with backoff" (in the worker, not the runbook).
- Update on-call playbook with the new mapping.

---

## Confidence to Implement

**Score: 88/100 — High** *(was 78; +10 after admin control surface specified)*

Investigation steps assume `request_id` + `job_id` + provider request_id are
linked in logs (covered by observability). The retry/cancel actions now point
at concrete `POST /v1/admin/jobs/{id}/retry|cancel` endpoints with explicit
scopes and example bodies, plus a manual SQL fallback for the period before
they're built. Partial-success handling uses the already-defined
`POST /v1/assets/{id}/regenerate`. Score reflects: the runbook is fully
specified; only the implementation of the planned endpoints stands between
this doc and an executable procedure.
