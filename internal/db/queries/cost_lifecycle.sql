-- Cost-reservation terminal lifecycle (docs/architecture/cost-control.md §3
-- steps 9–10). Phase 4B: commit on job success, release on terminal failure.
-- Idempotency lives in the reservation status guard: a reservation moves
-- reserved → committed or reserved → released at most once, and the budget
-- holds are processed only inside that single guarded transition.

-- MarkReservationBudgetExceeded turns a freshly-inserted `reserved` row into a
-- `failed` one when the budget hold is denied. The estimate stays for audit;
-- reserved_amount is zeroed because the savepoint already rolled back every
-- budget increment and hold this reservation made.
-- name: MarkReservationBudgetExceeded :exec
UPDATE cost_reservations
SET status = 'failed',
    failure_reason = sqlc.arg(failure_reason),
    reserved_amount = 0,
    updated_at = now()
WHERE id = sqlc.arg(id);

-- InsertBudgetHold records that `reserved_amount` was credited against
-- `cost_budget_id` for this reservation. Written in the same savepoint as the
-- budget increment so a denied reservation rolls the hold back too. Release /
-- commit reverse exactly the rows recorded here.
-- name: InsertBudgetHold :exec
INSERT INTO cost_reservation_budget_holds (
    id, cost_reservation_id, cost_budget_id, reserved_amount, status
) VALUES (
    $1, $2, $3, $4, 'reserved'
);

-- CommitReservationForJob flips a reservation reserved → committed exactly
-- once. When provider adapters reported actual spend, sum those committed-job
-- events; when they did not, fall back to the held estimate. Failed fallback
-- attempts are included when they carry a provider-reported actual because a
-- provider can bill a failed request too. No row means the reservation was not
-- in `reserved` and the lifecycle caller performs an idempotent no-op.
-- name: CommitReservationForJob :one
UPDATE cost_reservations cr
SET status = 'committed',
    actual_amount = COALESCE((
        SELECT SUM(gce.actual_cost_usd)::numeric(14, 4)
        FROM generation_cost_events gce
        WHERE gce.job_id = cr.generation_job_id
          AND gce.cost_reservation_id = cr.id
          AND gce.actual_cost_usd IS NOT NULL
          AND COALESCE(gce.metadata->>'actual_inferred_from_estimate', 'false') <> 'true'
    ), cr.estimated_amount),
    updated_at = now()
WHERE cr.generation_job_id = sqlc.arg(generation_job_id)
  AND cr.status = 'reserved'
  AND cr.id = (
      SELECT j.cost_reservation_id
      FROM generation_jobs j
      WHERE j.id = sqlc.arg(generation_job_id)
  )
RETURNING id, estimated_amount, reserved_amount, actual_amount, currency, tenant_id;

-- ReleaseReservationForJob flips a reservation reserved → released exactly
-- once. actual_amount stays NULL unless a provider reported a partial charge
-- before the job failed (e.g. preview succeeded, final failed) — cost-
-- control.md §3 step 10: "commit the partial actual and release the unused
-- remainder". Unlike CommitReservationForJob, this does NOT fall back to
-- estimated_amount when no actual was reported — a plain (no partial charge)
-- release must leave actual_amount null, not silently charge the estimate
-- for a job that never got a provider bill. No row returned → no-op.
-- name: ReleaseReservationForJob :one
UPDATE cost_reservations cr
SET status = 'released',
    actual_amount = (
        SELECT SUM(gce.actual_cost_usd)::numeric(14, 4)
        FROM generation_cost_events gce
        WHERE gce.job_id = cr.generation_job_id
          AND gce.cost_reservation_id = cr.id
          AND gce.actual_cost_usd IS NOT NULL
          AND COALESCE(gce.metadata->>'actual_inferred_from_estimate', 'false') <> 'true'
    ),
    updated_at = now()
WHERE cr.generation_job_id = sqlc.arg(generation_job_id)
  AND cr.status = 'reserved'
  AND cr.id = (
      SELECT j.cost_reservation_id
      FROM generation_jobs j
      WHERE j.id = sqlc.arg(generation_job_id)
  )
RETURNING id, estimated_amount, reserved_amount, actual_amount, currency, tenant_id;

-- CommitReservationByID is the worker-safe lifecycle variant. A worker keeps
-- the reservation id it read before provider work, so a stale task can never
-- finalize a newer reservation attached to the same reusable job id.
-- name: CommitReservationByID :one
UPDATE cost_reservations cr
SET status = 'committed',
    actual_amount = COALESCE((
        SELECT SUM(gce.actual_cost_usd)::numeric(14, 4)
        FROM generation_cost_events gce
        WHERE gce.job_id = cr.generation_job_id
          AND gce.cost_reservation_id = cr.id
          AND gce.actual_cost_usd IS NOT NULL
          AND COALESCE(gce.metadata->>'actual_inferred_from_estimate', 'false') <> 'true'
    ), cr.estimated_amount),
    updated_at = now()
WHERE cr.id = sqlc.arg(reservation_id)
  AND cr.generation_job_id = sqlc.arg(generation_job_id)
  AND cr.status = 'reserved'
RETURNING id, estimated_amount, reserved_amount, actual_amount, currency, tenant_id;

-- ReleaseReservationByID is the worker-safe release variant. It targets the
-- reservation captured by the task rather than whatever reservation a retry
-- may have attached to the job id later.
-- name: ReleaseReservationByID :one
UPDATE cost_reservations cr
SET status = 'released',
    actual_amount = (
        SELECT SUM(gce.actual_cost_usd)::numeric(14, 4)
        FROM generation_cost_events gce
        WHERE gce.job_id = cr.generation_job_id
          AND gce.cost_reservation_id = cr.id
          AND gce.actual_cost_usd IS NOT NULL
          AND COALESCE(gce.metadata->>'actual_inferred_from_estimate', 'false') <> 'true'
    ),
    updated_at = now()
WHERE cr.id = sqlc.arg(reservation_id)
  AND cr.generation_job_id = sqlc.arg(generation_job_id)
  AND cr.status = 'reserved'
RETURNING id, estimated_amount, reserved_amount, actual_amount, currency, tenant_id;

-- GetTerminalReservationForReconciliation locks a terminal reservation while
-- a late provider event is folded into its already-finalized accounting.
-- name: GetTerminalReservationForReconciliation :one
SELECT id, generation_job_id, tenant_id, estimated_amount, actual_amount, currency, status
FROM cost_reservations
WHERE id = sqlc.arg(reservation_id)
  AND generation_job_id = sqlc.arg(generation_job_id)
  AND status IN ('committed', 'released')
FOR UPDATE;

-- SumReportedReservationActual excludes the synthetic estimate fallback event.
-- Only provider-reported actuals participate in late reconciliation.
-- name: SumReportedReservationActual :one
SELECT SUM(gce.actual_cost_usd)::numeric(14, 4) AS reported_actual
FROM generation_cost_events gce
WHERE gce.cost_reservation_id = sqlc.arg(reservation_id)
  AND gce.job_id = sqlc.arg(generation_job_id)
  AND gce.actual_cost_usd IS NOT NULL
  AND COALESCE(gce.metadata->>'actual_inferred_from_estimate', 'false') <> 'true';

-- ReconcileReservationActual records the provider-reported total and returns
-- the signed delta from the amount already charged at terminalization.
-- name: ReconcileReservationActual :one
UPDATE cost_reservations
SET actual_amount = sqlc.arg(actual_amount),
    updated_at = now()
WHERE id = sqlc.arg(reservation_id)
  AND generation_job_id = sqlc.arg(generation_job_id)
  AND status IN ('committed', 'released')
RETURNING (actual_amount - COALESCE(sqlc.arg(previous_actual), 0::numeric))::numeric(14, 4) AS delta_amount;

-- ListFinalizedBudgetHolds returns all budget rows already terminalized for a
-- reservation. Late provider billing adjusts spent on each applicable scope.
-- name: ListFinalizedBudgetHolds :many
SELECT id, cost_budget_id, reserved_amount
FROM cost_reservation_budget_holds
WHERE cost_reservation_id = sqlc.arg(reservation_id)
  AND status IN ('committed', 'released');

-- AdjustBudgetSpent applies a late provider-cost delta. A negative delta is
-- valid when an estimate fallback was replaced by a lower reported actual.
-- name: AdjustBudgetSpent :exec
UPDATE cost_budgets
SET spent_amount = GREATEST(spent_amount + sqlc.arg(delta_amount), 0),
    updated_at = now()
WHERE id = sqlc.arg(id);

-- SetGenerationJobActualCostForReservation prevents an old reservation's late
-- event from overwriting the actual for a newer retry attached to the same job.
-- name: SetGenerationJobActualCostForReservation :exec
UPDATE generation_jobs
SET actual_cost_usd = sqlc.arg(actual_cost_usd),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND cost_reservation_id = sqlc.arg(cost_reservation_id);

-- UpsertIdentityCostLedgerActualForJob folds only a late actual delta into the
-- identity ledger. Estimated totals were accounted for by the original
-- terminalization when applicable; a late charge after a plain release creates
-- an actual-only row if necessary.
-- name: UpsertIdentityCostLedgerActualForJob :exec
INSERT INTO identity_cost_ledger (
    id, tenant_id, visual_identity_id,
    cost_estimated_total, cost_actual_total, currency
)
SELECT sqlc.arg(ledger_id),
       j.tenant_id,
       COALESCE(NULLIF(j.visual_identity_id, ''),
                NULLIF(j.input_payload->>'identity_id', ''),
                NULLIF(j.input_payload->>'visual_identity_id', '')),
       0,
       sqlc.arg(delta_amount),
       sqlc.arg(currency)
FROM generation_jobs j
WHERE j.id = sqlc.arg(generation_job_id)
  AND COALESCE(NULLIF(j.visual_identity_id, ''),
               NULLIF(j.input_payload->>'identity_id', ''),
               NULLIF(j.input_payload->>'visual_identity_id', '')) IS NOT NULL
ON CONFLICT (visual_identity_id) DO UPDATE
SET cost_actual_total = identity_cost_ledger.cost_actual_total + EXCLUDED.cost_actual_total,
    currency = EXCLUDED.currency,
    updated_at = now();

-- ListReservedBudgetHolds returns the still-reserved holds for a reservation.
-- Processed once inside the guarded transition; marking each hold committed /
-- released afterwards is belt-and-suspenders against a partial retry.
-- name: ListReservedBudgetHolds :many
SELECT id, cost_budget_id, reserved_amount
FROM cost_reservation_budget_holds
WHERE cost_reservation_id = $1
  AND status = 'reserved';

-- CommitBudgetHold moves the held estimate out of reserved and records the
-- provider-reconciled actual in spent. The reservation was held for the
-- worst-case plan, so reserved_amount is the held amount while actual_amount
-- may be lower when a provider reports a cheaper outcome.
-- name: CommitBudgetHold :exec
UPDATE cost_budgets
SET reserved_amount = GREATEST(reserved_amount - sqlc.arg(reserved_amount), 0),
    spent_amount = spent_amount + sqlc.arg(actual_amount),
    updated_at = now()
WHERE id = sqlc.arg(id);

-- ReleaseBudgetHold returns a hold's amount to the budget: drop reserved,
-- leave spent untouched.
-- name: ReleaseBudgetHold :exec
UPDATE cost_budgets
SET reserved_amount = GREATEST(reserved_amount - sqlc.arg(amount), 0),
    updated_at = now()
WHERE id = sqlc.arg(id);

-- MarkBudgetHoldStatus records the hold's terminal state. The WHERE guard on
-- status='reserved' makes a re-run a no-op.
-- name: MarkBudgetHoldStatus :exec
UPDATE cost_reservation_budget_holds
SET status = sqlc.arg(status),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'reserved';

-- SetGenerationJobActualCost records the committed actual on the job row.
-- name: SetGenerationJobActualCost :exec
UPDATE generation_jobs
SET actual_cost_usd = sqlc.arg(actual_cost_usd),
    updated_at = now()
WHERE id = sqlc.arg(id);

-- UpdateLatestJobCostEvent stamps the estimated/actual cost and final status
-- onto the most recent cost event for this reservation. Returns the number of
-- rows touched so the finalizer can insert one if the worker never wrote it.
-- name: UpdateLatestJobCostEvent :execrows
UPDATE generation_cost_events latest
SET estimated_cost_usd = sqlc.arg(estimated_cost_usd),
    -- Worker-written events carry per-call actuals. Only stamp the aggregate
    -- fallback when this reservation has no provider-reported actual at all;
    -- otherwise writing the reservation total onto the latest event would make
    -- a later SUM(actual_cost_usd) double-count preview/failed-route calls.
    actual_cost_usd = CASE
        WHEN EXISTS (
            SELECT 1
            FROM generation_cost_events observed
            WHERE observed.job_id = sqlc.arg(job_id)
              AND observed.cost_reservation_id = sqlc.arg(cost_reservation_id)
              AND observed.actual_cost_usd IS NOT NULL
              AND COALESCE(observed.metadata->>'actual_inferred_from_estimate', 'false') <> 'true'
        ) THEN latest.actual_cost_usd
        ELSE sqlc.arg(actual_cost_usd)
    END,
    metadata = CASE
        WHEN EXISTS (
            SELECT 1
            FROM generation_cost_events observed
            WHERE observed.job_id = sqlc.arg(job_id)
              AND observed.cost_reservation_id = sqlc.arg(cost_reservation_id)
              AND observed.actual_cost_usd IS NOT NULL
              AND COALESCE(observed.metadata->>'actual_inferred_from_estimate', 'false') <> 'true'
        ) THEN latest.metadata
        ELSE latest.metadata || jsonb_build_object('actual_inferred_from_estimate', true)
    END,
    status = sqlc.arg(status)
WHERE latest.id = (
    SELECT gce.id FROM generation_cost_events gce
    WHERE gce.job_id = sqlc.arg(job_id)
      AND gce.cost_reservation_id = sqlc.arg(cost_reservation_id)
    ORDER BY gce.created_at DESC
    LIMIT 1
);

-- InsertFinalizerCostEvent writes a cost event carrying estimated/actual when
-- the worker never managed to write one (best-effort fallback).
-- name: InsertFinalizerCostEvent :exec
INSERT INTO generation_cost_events (
    id, tenant_id, job_id, cost_reservation_id, token_id, operation,
    estimated_cost_usd, actual_cost_usd, status, metadata
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9,
    jsonb_build_object('billable_operation', 'finalizer', 'actual_inferred_from_estimate', true)
);

-- UpsertIdentityCostLedgerForJob adds one committed reservation's estimated and
-- actual amounts to the identity lifetime ledger. Prefer the first-class
-- generation_jobs.visual_identity_id, with payload fallbacks for older rows that
-- predate that column being populated by the jobs service. Jobs without an
-- identity (for example ad-hoc artifacts) intentionally produce no row.
-- name: UpsertIdentityCostLedgerForJob :exec
INSERT INTO identity_cost_ledger (
    id, tenant_id, visual_identity_id,
    cost_estimated_total, cost_actual_total, currency
)
SELECT sqlc.arg(ledger_id),
       j.tenant_id,
       COALESCE(NULLIF(j.visual_identity_id, ''),
                NULLIF(j.input_payload->>'identity_id', ''),
                NULLIF(j.input_payload->>'visual_identity_id', '')),
       sqlc.arg(estimated_amount),
       sqlc.arg(actual_amount),
       sqlc.arg(currency)
FROM generation_jobs j
WHERE j.id = sqlc.arg(generation_job_id)
  AND COALESCE(NULLIF(j.visual_identity_id, ''),
               NULLIF(j.input_payload->>'identity_id', ''),
               NULLIF(j.input_payload->>'visual_identity_id', '')) IS NOT NULL
ON CONFLICT (visual_identity_id) DO UPDATE
SET cost_estimated_total = identity_cost_ledger.cost_estimated_total + EXCLUDED.cost_estimated_total,
    cost_actual_total = identity_cost_ledger.cost_actual_total + EXCLUDED.cost_actual_total,
    currency = EXCLUDED.currency,
    updated_at = now();
