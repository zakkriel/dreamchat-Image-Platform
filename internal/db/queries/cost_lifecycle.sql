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
-- once. No row returned means the reservation was not in `reserved` (already
-- committed / released / failed) → caller treats it as a no-op and moves no
-- budget. actual_amount = estimated_amount (Phase 4B: no provider-reported
-- reconciliation).
-- name: CommitReservationForJob :one
UPDATE cost_reservations
SET status = 'committed',
    actual_amount = estimated_amount,
    updated_at = now()
WHERE generation_job_id = sqlc.arg(generation_job_id)
  AND status = 'reserved'
RETURNING id, estimated_amount, reserved_amount, actual_amount, currency, tenant_id;

-- ReleaseReservationForJob flips a reservation reserved → released exactly
-- once. actual_amount stays NULL (job failed, nothing charged). No row
-- returned → no-op.
-- name: ReleaseReservationForJob :one
UPDATE cost_reservations
SET status = 'released',
    updated_at = now()
WHERE generation_job_id = sqlc.arg(generation_job_id)
  AND status = 'reserved'
RETURNING id, estimated_amount, reserved_amount, actual_amount, currency, tenant_id;

-- ListReservedBudgetHolds returns the still-reserved holds for a reservation.
-- Processed once inside the guarded transition; marking each hold committed /
-- released afterwards is belt-and-suspenders against a partial retry.
-- name: ListReservedBudgetHolds :many
SELECT id, cost_budget_id, reserved_amount
FROM cost_reservation_budget_holds
WHERE cost_reservation_id = $1
  AND status = 'reserved';

-- CommitBudgetHold moves a hold's amount from reserved → spent on the budget.
-- GREATEST guards against a negative reserved_amount if accounting ever drifts.
-- name: CommitBudgetHold :exec
UPDATE cost_budgets
SET reserved_amount = GREATEST(reserved_amount - sqlc.arg(amount), 0),
    spent_amount = spent_amount + sqlc.arg(amount),
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
-- onto the most recent cost event for a job (the one the worker wrote for the
-- terminal attempt). Returns the number of rows touched so the finalizer can
-- insert one if the worker never wrote it.
-- name: UpdateLatestJobCostEvent :execrows
UPDATE generation_cost_events
SET estimated_cost_usd = sqlc.arg(estimated_cost_usd),
    actual_cost_usd = sqlc.arg(actual_cost_usd),
    status = sqlc.arg(status)
WHERE id = (
    SELECT gce.id FROM generation_cost_events gce
    WHERE gce.job_id = sqlc.arg(job_id)
    ORDER BY gce.created_at DESC
    LIMIT 1
);

-- InsertFinalizerCostEvent writes a cost event carrying estimated/actual when
-- the worker never managed to write one (best-effort fallback).
-- name: InsertFinalizerCostEvent :exec
INSERT INTO generation_cost_events (
    id, tenant_id, job_id, token_id, operation,
    estimated_cost_usd, actual_cost_usd, status
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8
);
