-- +goose Up
-- Phase 7C-2: Rate Limiting + Hard Concurrent Job Caps.
--
-- Additive only. No new tables (table count stays 18): this migration adds
-- three nullable per-token limit-override columns to api_tokens and one
-- supporting index for the concurrent-job count.

-- Per-token limit overrides. NULL means "use the platform default":
--   rate_limit_rpm       -> requests-per-minute   (default 60)
--   rate_limit_rph       -> requests-per-hour     (default 1000)
--   max_concurrent_jobs  -> live generation jobs  (default 5)
-- Admin/dev tokens can be pinned higher than the defaults so the per-token
-- request-rate limiter (which throttles every authenticated /v1 request,
-- including admin endpoints) does not starve operators.
ALTER TABLE api_tokens
    ADD COLUMN rate_limit_rpm INT,
    ADD COLUMN rate_limit_rph INT,
    ADD COLUMN max_concurrent_jobs INT;

-- Supporting index for the hard concurrent-job cap. The cap counts live jobs
-- per token:
--   SELECT count(*) FROM generation_jobs
--   WHERE requested_by_token_id = $1
--     AND status IN ('queued','running','preview_ready');
-- A composite (requested_by_token_id, status) index serves that filter
-- directly. This is an index, not a table — table count remains 18.
CREATE INDEX IF NOT EXISTS idx_generation_jobs_token_status
    ON generation_jobs (requested_by_token_id, status);

-- +goose Down
-- Baseline migration: irreversible floor. Roll back by restoring from backup.
SELECT 'baseline migration 0008 is irreversible' WHERE false;
