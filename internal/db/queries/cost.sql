-- Cost-control pre-flight queries (docs/architecture/cost-control.md §3).
-- Phase 4 implements steps 4–7 (price lookup → estimate → budget reservation).

-- EstimateOperationCost loads the active price for (provider × model ×
-- operation) and returns the pre-flight estimate for `units` of work. No row
-- means there is no active price entry → fail closed (no_price_entry).
-- name: EstimateOperationCost :one
SELECT id AS price_id,
       unit_type,
       currency,
       (price_per_unit * sqlc.arg(units)::int)::numeric(14, 4) AS estimated_amount,
       (price_per_unit * sqlc.arg(units)::int)::numeric(14, 4)::text AS estimated_text
FROM provider_model_prices
WHERE provider_id = sqlc.arg(provider_id)
  AND model_id = sqlc.arg(model_id)
  AND operation_type = sqlc.arg(operation_type)
  AND is_active = true
ORDER BY effective_from DESC
LIMIT 1;

-- ListBudgetsForReservation returns every budget that could apply to a
-- request: the tenant-scope budget(s) plus any token / world / user scoped
-- budgets matching the request's identifiers. The caller picks which to
-- enforce (tenant always; then the narrowest applicable scope).
-- name: ListBudgetsForReservation :many
SELECT id, scope_type, scope_id, period, status,
       limit_amount, reserved_amount, spent_amount, currency
FROM cost_budgets
WHERE tenant_id = sqlc.arg(tenant_id)
  AND (
       (scope_type = 'tenant' AND scope_id = sqlc.arg(tenant_id))
    OR (scope_type = 'token'  AND scope_id = sqlc.arg(token_id))
    OR (scope_type = 'world'  AND scope_id = sqlc.arg(world_id))
    OR (scope_type = 'user'   AND scope_id = sqlc.arg(user_id))
  );

-- ReserveActiveBudget atomically holds `amount` against an active budget.
-- The conditional WHERE is the consistency point: concurrent requests that
-- would collectively oversell the limit see all-but-one fail (no row
-- returned → budget_exceeded).
-- name: ReserveActiveBudget :one
UPDATE cost_budgets
SET reserved_amount = reserved_amount + sqlc.arg(amount),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'active'
  AND reserved_amount + spent_amount + sqlc.arg(amount) <= limit_amount
RETURNING id;

-- ReservePausedBudget records a hold against a paused budget without
-- enforcing the limit (paused = recording only; never deny).
-- name: ReservePausedBudget :one
UPDATE cost_budgets
SET reserved_amount = reserved_amount + sqlc.arg(amount),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'paused'
RETURNING id;

-- InsertCostReservation records the reservation for a job. status=reserved
-- on success; status=failed with failure_reason on no_price_entry /
-- budget_exceeded.
-- name: InsertCostReservation :one
INSERT INTO cost_reservations (
    id, generation_job_id, tenant_id,
    estimated_amount, reserved_amount, currency, status, failure_reason
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8
)
RETURNING id, generation_job_id, tenant_id, estimated_amount, reserved_amount,
          actual_amount, currency, status, failure_reason, created_at, updated_at;
