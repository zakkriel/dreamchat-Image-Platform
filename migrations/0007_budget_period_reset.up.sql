-- Phase 7C-1c: lazy budget period reset.
--
-- Daily/monthly budgets previously behaved like lifetime budgets because their
-- spent_amount counter never rolled over. period_start anchors each budget to
-- the start of its current UTC window so the reservation path can lazily reset
-- spent_amount (and clear an exceeded status) when the window has elapsed.
--
-- This migration only ADDS a column to cost_budgets — no new table. The table
-- count stays 18.

ALTER TABLE cost_budgets
    ADD COLUMN period_start TIMESTAMPTZ NOT NULL DEFAULT now();

-- Backfill existing rows to the start of their current UTC window so the very
-- first post-migration reservation does not see a stale lifetime counter:
--   daily   → UTC date floor
--   monthly → first day of the current UTC month
-- The window start is computed in UTC and stored back as a timestamptz.
UPDATE cost_budgets
SET period_start = CASE period
    WHEN 'daily'   THEN date_trunc('day',   now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'
    WHEN 'monthly' THEN date_trunc('month', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'
    ELSE now()
END;
