-- Admin cost surface (docs/architecture/admin-control-surface.md §"Cost
-- controls"). Phase 4B: price-book create/list/get/update, cost-budget
-- create/list/update, cost-reservation list, and the audit-event write that
-- every mutation shares a transaction with.

-- ---------------------------------------------------------------------------
-- Price book
-- ---------------------------------------------------------------------------

-- SupersedePreviousActivePrice ends the current active entry for a
-- (provider × model × operation_type): clears is_active and stamps
-- effective_to. Run in the same transaction as the new INSERT so there is
-- never a window with zero (or two) active rows. :execrows reports whether an
-- entry was actually superseded.
-- name: SupersedePreviousActivePrice :execrows
UPDATE provider_model_prices
SET is_active = false,
    effective_to = now(),
    updated_at = now()
WHERE provider_id = sqlc.arg(provider_id)
  AND model_id = sqlc.arg(model_id)
  AND operation_type = sqlc.arg(operation_type)
  AND is_active = true;

-- InsertProviderModelPrice creates a new active price entry. effective_from is
-- now(); effective_to is NULL (current).
-- name: InsertProviderModelPrice :one
INSERT INTO provider_model_prices (
    id, provider_id, model_id, operation_type, unit_type,
    price_per_unit, currency, effective_from, effective_to, is_active,
    source, notes
) VALUES (
    sqlc.arg(id), sqlc.arg(provider_id), sqlc.arg(model_id), sqlc.arg(operation_type), sqlc.arg(unit_type),
    sqlc.arg(price_per_unit)::numeric, sqlc.arg(currency), now(), NULL, true,
    sqlc.arg(source), sqlc.arg(notes)
)
RETURNING id, provider_id, model_id, operation_type, unit_type,
          price_per_unit::text AS price_per_unit, currency,
          effective_from, effective_to, is_active, source, notes,
          created_at, updated_at;

-- name: ListProviderModelPrices :many
SELECT id, provider_id, model_id, operation_type, unit_type,
       price_per_unit::text AS price_per_unit, currency,
       effective_from, effective_to, is_active, source, notes,
       created_at, updated_at
FROM provider_model_prices
ORDER BY provider_id, model_id, operation_type, effective_from DESC;

-- name: GetProviderModelPrice :one
SELECT id, provider_id, model_id, operation_type, unit_type,
       price_per_unit::text AS price_per_unit, currency,
       effective_from, effective_to, is_active, source, notes,
       created_at, updated_at
FROM provider_model_prices
WHERE id = $1;

-- UpdateProviderModelPrice mutates only the editable fields (effective_to,
-- is_active, notes). COALESCE keeps unspecified fields unchanged.
-- name: UpdateProviderModelPrice :one
UPDATE provider_model_prices
SET effective_to = CASE WHEN sqlc.arg(set_effective_to)::bool THEN sqlc.narg(effective_to) ELSE effective_to END,
    is_active = COALESCE(sqlc.narg(is_active), is_active),
    notes = COALESCE(sqlc.narg(notes), notes),
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING id, provider_id, model_id, operation_type, unit_type,
          price_per_unit::text AS price_per_unit, currency,
          effective_from, effective_to, is_active, source, notes,
          created_at, updated_at;

-- ---------------------------------------------------------------------------
-- Cost budgets
-- ---------------------------------------------------------------------------

-- InsertCostBudget creates a budget. reserved_amount and spent_amount are
-- platform-owned and start at 0; callers may not set them.
-- name: InsertCostBudget :one
INSERT INTO cost_budgets (
    id, tenant_id, scope_type, scope_id, period,
    limit_amount, currency, status
) VALUES (
    sqlc.arg(id), sqlc.arg(tenant_id), sqlc.arg(scope_type), sqlc.arg(scope_id), sqlc.arg(period),
    sqlc.arg(limit_amount)::numeric, sqlc.arg(currency), sqlc.arg(status)
)
RETURNING id, tenant_id, scope_type, scope_id, period,
          limit_amount::text AS limit_amount,
          reserved_amount::text AS reserved_amount,
          spent_amount::text AS spent_amount,
          currency, status, created_at, updated_at;

-- name: ListCostBudgets :many
SELECT id, tenant_id, scope_type, scope_id, period,
       limit_amount::text AS limit_amount,
       reserved_amount::text AS reserved_amount,
       spent_amount::text AS spent_amount,
       currency, status, created_at, updated_at
FROM cost_budgets
ORDER BY tenant_id, scope_type, scope_id, period;

-- name: GetCostBudget :one
SELECT id, tenant_id, scope_type, scope_id, period,
       limit_amount::text AS limit_amount,
       reserved_amount::text AS reserved_amount,
       spent_amount::text AS spent_amount,
       currency, status, created_at, updated_at
FROM cost_budgets
WHERE id = $1;

-- UpdateCostBudget mutates only limit_amount and status. reserved_amount,
-- spent_amount, and the scope/period identity stay platform-owned and fixed.
-- name: UpdateCostBudget :one
UPDATE cost_budgets
SET limit_amount = COALESCE(sqlc.narg(limit_amount), limit_amount),
    status = COALESCE(sqlc.narg(status), status),
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING id, tenant_id, scope_type, scope_id, period,
          limit_amount::text AS limit_amount,
          reserved_amount::text AS reserved_amount,
          spent_amount::text AS spent_amount,
          currency, status, created_at, updated_at;

-- ---------------------------------------------------------------------------
-- Cost reservations (read-only admin list)
-- ---------------------------------------------------------------------------

-- name: ListCostReservationsAdmin :many
SELECT id, generation_job_id, tenant_id,
       estimated_amount::text AS estimated_amount,
       reserved_amount::text AS reserved_amount,
       actual_amount,
       currency, status, failure_reason, created_at, updated_at
FROM cost_reservations
WHERE (sqlc.narg(tenant_id)::text IS NULL OR tenant_id = sqlc.narg(tenant_id))
  AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status))
  AND (sqlc.narg(created_after)::timestamptz IS NULL OR created_at >= sqlc.narg(created_after))
  AND (sqlc.narg(created_before)::timestamptz IS NULL OR created_at <= sqlc.narg(created_before))
ORDER BY created_at DESC
LIMIT sqlc.arg(row_limit);

-- ---------------------------------------------------------------------------
-- Audit events
-- ---------------------------------------------------------------------------

-- InsertAuditEvent records a state-changing admin action. Written in the same
-- transaction as the mutation; if it fails, the mutation fails.
-- name: InsertAuditEvent :exec
INSERT INTO audit_events (
    id, tenant_id, event_type, actor_token_id,
    resource_type, resource_id, metadata
) VALUES (
    $1, $2, $3, $4, $5, $6, $7
);
