-- +goose Up
-- 0003_cost_lifecycle
--
-- Phase 4B: complete the cost-reservation lifecycle (commit on success /
-- release on terminal failure — docs/architecture/cost-control.md §3 steps
-- 9–10) and the minimum admin cost surface.
--
-- Phase 4A holds the pre-flight estimate against the tenant budget plus the
-- narrowest applicable scope budget, but it does NOT persist *which* budget
-- rows were incremented. Release/commit must reverse exactly what was held —
-- a broad "update every budget by tenant/scope" would double-count or miss
-- rows when budgets are created/edited between reserve and finalize. This
-- table records each hold so the terminal transition reverses precisely the
-- rows that were credited.
--
-- See frustration_log.md (Phase 4B) for the rationale.

CREATE TABLE IF NOT EXISTS cost_reservation_budget_holds (
    id TEXT PRIMARY KEY,
    cost_reservation_id TEXT NOT NULL REFERENCES cost_reservations(id),
    cost_budget_id TEXT NOT NULL REFERENCES cost_budgets(id),
    reserved_amount NUMERIC(14, 4) NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('reserved', 'committed', 'released')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (cost_reservation_id, cost_budget_id)
);

-- Lookup of the holds for a reservation when committing/releasing.
CREATE INDEX IF NOT EXISTS idx_cost_reservation_budget_holds_reservation
    ON cost_reservation_budget_holds(cost_reservation_id);
-- Reverse lookup: which holds reference a given budget.
CREATE INDEX IF NOT EXISTS idx_cost_reservation_budget_holds_budget
    ON cost_reservation_budget_holds(cost_budget_id);

-- +goose Down
-- Baseline migration: irreversible floor. Roll back by restoring from backup.
SELECT 'baseline migration 0003 is irreversible' WHERE false;
