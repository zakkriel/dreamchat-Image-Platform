# Phase 7C-1 Confidence Index — Admin Job Control + Budget Period Reset

**Overall: 90/100 — Very High**

Phase 7C-1 is **slice 1 of 3** of Phase 7C (Production Controls). It adds three
production controls and nothing else: (1) **admin cancel** of a non-terminal
job, which reclaims its reserved cost exactly once; (2) **admin retry** of a
failed job, which re-reserves cost against the **persisted resolved route**
and never re-resolves; and (3) **lazy budget period reset**, so daily/monthly
budgets enforce per actual UTC window instead of behaving like lifetime caps.
Rate limiting + RLS (7C-2) and provider fallback chains + webhooks (7C-3) are
**not** in this slice. **No new table — count stays 18** (migration `0007` adds
one column). OpenAPI `0.8.0 → 0.9.0`, strictly additive, mirrored.

## What shipped

| # | Deliverable | Where | Confidence |
|---|---|---|---|
| 1 | `POST /v1/admin/jobs/{job_id}/cancel` (scope `admin:jobs`, tenant from principal) | `router.go`, `admin_jobs_handler.go`, `adminjobs.CancelJob` | 92 |
| 2 | Cancel state machine (queued/running/preview_ready → cancelled; 409 from terminal; idempotent from cancelled) | `adminjobs.CancelJob`, `CancelGenerationJob`/`LockGenerationJobForUpdate` queries | 91 |
| 3 | Cancel releases reservation exactly once, atomic with the status flip | `cost.Lifecycle.ReleaseInTx`, `adminjobs.CancelJob` | 91 |
| 4 | In-flight cancel guard: guarded worker output persistence | `jobs.guarded_persist.go`, `worker.go`, `LockGenerationJobForUpdate` | 89 |
| 5 | Worker treats `cancelled` as terminal (no provider/upload/asset/commit) | `internal/jobs/worker.go` (`Process` short-circuit, `finishCancelled`) | 91 |
| 6 | `POST /v1/admin/jobs/{job_id}/retry` (scope `admin:jobs`, failed-only) | `router.go`, `admin_jobs_handler.go`, `adminjobs.RetryJob` | 91 |
| 7 | Retry re-reserves on persisted route, never re-resolves | `adminjobs.reserveInputFromRow`, `RetryResetGenerationJob` | 91 |
| 8 | Retry denial (no price / budget) leaves job failed, no partial reservation | `adminjobs.RetryJob` (rollback on `res.Failed()`) | 90 |
| 9 | Retry enqueue-failure mirrors create cleanup (mark failed + release) | `adminjobs.enqueueRetry` | 89 |
| 10 | Lazy budget period reset (atomic, UTC, idempotent) | `migration 0007`, `ResetBudgetPeriodIfElapsed`, `cost.reserveBudgets` | 90 |
| 11 | `period_start` additive on admin budget surface | `admin_cost.sql`, `admincost.go`, `admin_cost_handler.go` | 90 |
| 12 | `admin:jobs` scope + seed | `router.go`, `scripts/seed_admin_token.sh` | 93 |
| 13 | Additive OpenAPI `0.8.0 → 0.9.0`, mirrored | `api/openapi.yaml`, `docs/api/openapi.yaml` | 92 |
| 14 | Handler + worker unit tests; cost/service + adminjobs + e2e integration tests | `*_test.go` | 88 |

## Cancel state machine (91)

`CancelJob` runs one transaction: lock the `generation_jobs` row
(`LockGenerationJobForUpdate ... FOR UPDATE`), read its status, then:

- `queued | running | preview_ready` → `CancelGenerationJob` sets
  `status=cancelled`, `completed_at=now()`, `error_code=cancelled`, a useful
  `error_message`, `retryable=false`; then `ReleaseInTx` releases the
  reservation in the **same** transaction.
- `completed | failed` → `ErrInvalidState` → `409 invalid_state`.
- `cancelled` → idempotent: re-run the (no-op) release, return the existing job
  with `200`.
- missing / cross-tenant (`ErrNoRows`) → `ErrNotFound` → `404`.

Tenant comes from the authenticated principal; the path/body never supply it.
Proven by `TestCancelQueuedReleasesReservation`,
`TestCancelRunningAndPreviewReady`, `TestCancelTerminalStatesRejected`,
`TestCancelMissingOrCrossTenant`, `TestCancelIdempotentDoesNotDoubleRelease`,
and the handler mapping tests.

## Cost integrity on cancel (91)

The status flip and `cost.Lifecycle.ReleaseInTx` share one transaction, so a
cancelled job's budget hold is reclaimed atomically. Release is idempotent (the
`WHERE ... status='reserved'` guard on `ReleaseReservationForJob`), so a repeat
cancel does not double-release — `reserved_amount` returns to `0.0000` and
stays there (`TestCancelIdempotentDoesNotDoubleRelease`). A live job therefore
has at most one live reservation, and a cancelled job's hold is never leaked.

## In-flight cancel guard (89) — the most important part

A conditional `MarkCompleted`/`MarkPreviewReady` is **not** enough: the worker
inserts the asset before transitioning the job, so a naive flow could insert an
asset for a job that a cancel had just flipped. This slice replaces the
asset-then-status sequence with **guarded, atomic** writes:

- `InsertFinalAssetAndCompleteJobIfNotCancelled` and
  `InsertPreviewAssetAndMarkPreviewReadyIfNotCancelled` each open one
  transaction, **lock the job row**, and check the status. If `cancelled`, they
  insert nothing and transition nothing, returning `PersistSkippedCancelled`.
  Otherwise they insert the asset (forced jobs supersede their slot in the same
  transaction) and transition the job atomically (`PersistPersisted`).
- Admin cancel takes the **same** row lock, so cancel and persist serialize:
  whichever commits first wins. If cancel wins, the worker's guarded write sees
  `cancelled` and skips; the worker then releases (idempotent) and returns
  `nil` (no error, no retry, no cost commit). If the worker wins, cancel sees
  `completed`/`preview_ready` and behaves accordingly.

**Documented limitation (intentional):** cancel does not preempt a provider
HTTP call already in flight. The guarantee is narrower and exact — *provider
work may complete, but its result is never recorded as job output if the job
was cancelled before persistence.* Proven by
`TestWorkerCancelledJobShortCircuits`,
`TestWorkerCancelDuringProviderWorkSkipsPersist`,
`TestWorkerCancelDuringPreviewSkipsPersist`, and the end-to-end
`TestEndToEndCancelQueuedThenWorker`.

## Retry: no re-resolution, fresh reservation (91)

`RetryJob` validates the job is `failed` under a row lock, then re-reserves cost
via `cost.Reserver.Reserve` using `reserveInputFromRow` — which reads
`provider_id`, `model_id`, `operation_type`, and `units` straight from
`input_payload` (persisted by the create path) and **never** invokes the route
resolver (the adminjobs service has no resolver dependency at all — that is the
structural proof). On success it links the fresh reservation and reopens the job
to `queued`, clearing `error_code`/`error_message`/`retryable`/`started_at`/
`completed_at` and `final_asset_ids`, while preserving `preview_asset_ids` so a
preview-first job resumes at final. Reservation + reset + cost link commit in one
transaction; the enqueue follows. Proven by
`TestRetryFailedJobReReservesAndEnqueues` (fresh reservation priced at the mock
`0.0100`, linked to the same job, enqueued once) and the end-to-end
`TestEndToEndRetryThenWorkerCompletes` (worker completes the same job and the
reservation is committed exactly once).

## Retry denial + enqueue failure (90 / 89)

A denied reservation returns the cost sentinel
(`cost.ErrNoPriceEntry` → `422 no_price_entry`, `cost.ErrBudgetExceeded` →
`422 budget_exceeded`), leaves the job `failed` with its failure fields intact,
does not enqueue, and creates **no** live reservation — the transaction rolls
back the speculative failed reservation too
(`TestRetryDeniedByNoPriceLeavesJobFailed`,
`TestRetryDeniedByBudgetLeavesJobFailed`). An enqueue failure after the retry
commit mirrors the create path: mark the job failed, mark a pack job's pack
failed, and release the fresh reservation, so no `queued` job is left without a
task.

## Lazy budget period reset (90)

`cost_budgets.period_start` (migration `0007`, additive column; backfilled to
the current UTC window; **no new table**, count stays 18) anchors each budget to
its window. `ResetBudgetPeriodIfElapsed` runs inside `cost.reserveBudgets` (the
reservation transaction), before the limit is enforced:

- advances `period_start` (daily → `date_trunc('day', now() AT TIME ZONE 'UTC')`,
  monthly → `date_trunc('month', ...)`, stored back as UTC);
- `spent_amount = 0`;
- `status = CASE WHEN 'exceeded' THEN 'active' ELSE status END` (a `paused`
  budget stays paused);
- **leaves `reserved_amount` untouched** — a live hold opened just before the
  reset survives until its job terminates.

It is idempotent under concurrency: the conditional `WHERE period_start <
window` plus the row lock the `UPDATE` takes (held until the outer transaction
commits) means only the first of two racing reservations actually resets — no
double-zero, no lost hold. Proven by `TestBudgetResetDailyAdvancesWindowAndAdmits`,
`TestBudgetResetMonthlyAdvancesWindowAndAdmits`,
`TestBudgetResetPausedStaysPaused`,
`TestBudgetResetConcurrentDoesNotDoubleSpend`, and
`TestRetryAcrossBudgetWindowBoundary` (a previously exceeded budget admits a
retry in a fresh period).

## Deferred (out of scope for this slice)

Rate limiting, RLS, provider fallback chains, webhooks, cron/scheduler budget
reset, provider HTTP cancellation/preemption, and route re-resolution on retry
are all explicitly **not** implemented here. Rate limiting + RLS is Phase 7C-2;
provider fallback chains + webhooks is Phase 7C-3.

## Residual risk (10)

- The guarded-persist row lock serializes cancel against output persistence;
  provider work already dispatched still runs to completion (best-effort cancel
  for `running`), as documented.
- `operation_type`/`units` are read from `input_payload`; jobs created before
  this slice persisted them fall back to `text_to_image`/`1` — correct for the
  platform's only current operation, but worth revisiting if more operations or
  multi-unit pricing land.
