-- name: InsertGenerationJob :one
INSERT INTO generation_jobs (
    id, tenant_id, world_id, job_type, status,
    requested_by_token_id, input_payload, fallback_policy, cache_result
) VALUES (
    $1, $2, $3, $4, 'queued',
    $5, $6, $7, $8
)
RETURNING id, tenant_id, world_id, job_type, status,
          requested_by_token_id, visual_identity_id, asset_pack_id,
          input_payload, requested_outputs, fallback_policy, cache_result,
          preview_asset_ids, final_asset_ids,
          error_code, error_message, retryable,
          cost_reservation_id, cost_estimate_usd, actual_cost_usd,
          queue_duration_ms, generation_duration_ms,
          created_at, updated_at, started_at, completed_at;

-- name: InsertCompletedCacheHitJob :one
-- Phase 6A2 single-artifact exact reuse: insert a generation job that is
-- already terminal (status = 'completed') because an existing ready asset
-- satisfied the request via the deterministic artifact render hash. No provider
-- work runs, so the job carries no cost_reservation_id, a zero estimate, and a
-- zero actual cost — the request is genuinely free. cache_result is fixed to
-- 'exact_match' and final_asset_ids points at the reused asset. This row is
-- never enqueued and the worker never processes it, so the terminal-job
-- finalizer (which would otherwise commit a reservation) is never invoked on it.
INSERT INTO generation_jobs (
    id, tenant_id, world_id, job_type, status,
    requested_by_token_id, input_payload, requested_outputs,
    fallback_policy, cache_result, final_asset_ids,
    cost_estimate_usd, actual_cost_usd, completed_at
) VALUES (
    $1, $2, $3, $4, 'completed',
    $5, $6, sqlc.arg('requested_outputs'),
    $7, 'exact_match', sqlc.arg('final_asset_ids'),
    0, 0, now()
)
RETURNING id, tenant_id, world_id, job_type, status,
          requested_by_token_id, visual_identity_id, asset_pack_id,
          input_payload, requested_outputs, fallback_policy, cache_result,
          preview_asset_ids, final_asset_ids,
          error_code, error_message, retryable,
          cost_reservation_id, cost_estimate_usd, actual_cost_usd,
          queue_duration_ms, generation_duration_ms,
          created_at, updated_at, started_at, completed_at;

-- SetGenerationJobCost links a job to its cost_reservation and records the
-- pre-flight estimate. Run inside the create transaction, after the
-- reservation row exists.
-- name: SetGenerationJobCost :exec
UPDATE generation_jobs
SET cost_reservation_id = sqlc.arg(cost_reservation_id),
    cost_estimate_usd = sqlc.arg(cost_estimate_usd),
    updated_at = now()
WHERE id = sqlc.arg(id);

-- name: GetGenerationJobByID :one
SELECT id, tenant_id, world_id, job_type, status,
       requested_by_token_id, visual_identity_id, asset_pack_id,
       input_payload, requested_outputs, fallback_policy, cache_result,
       preview_asset_ids, final_asset_ids,
       error_code, error_message, retryable,
       cost_reservation_id, cost_estimate_usd, actual_cost_usd,
       queue_duration_ms, generation_duration_ms,
       created_at, updated_at, started_at, completed_at
FROM generation_jobs
WHERE id = $1
  AND tenant_id = $2;

-- name: GetGenerationJobByIDUnchecked :one
SELECT id, tenant_id, world_id, job_type, status,
       requested_by_token_id, visual_identity_id, asset_pack_id,
       input_payload, requested_outputs, fallback_policy, cache_result,
       preview_asset_ids, final_asset_ids,
       error_code, error_message, retryable,
       cost_reservation_id, cost_estimate_usd, actual_cost_usd,
       queue_duration_ms, generation_duration_ms,
       created_at, updated_at, started_at, completed_at
FROM generation_jobs
WHERE id = $1;

-- name: MarkGenerationJobRunning :one
UPDATE generation_jobs
SET status = 'running',
    started_at = now(),
    updated_at = now()
WHERE id = $1
  AND tenant_id = $2
RETURNING id, tenant_id, world_id, job_type, status,
          requested_by_token_id, visual_identity_id, asset_pack_id,
          input_payload, requested_outputs, fallback_policy, cache_result,
          preview_asset_ids, final_asset_ids,
          error_code, error_message, retryable,
          cost_reservation_id, cost_estimate_usd, actual_cost_usd,
          queue_duration_ms, generation_duration_ms,
          created_at, updated_at, started_at, completed_at;

-- name: MarkGenerationJobCompleted :one
UPDATE generation_jobs
SET status = 'completed',
    final_asset_ids = $3,
    completed_at = now(),
    updated_at = now()
WHERE id = $1
  AND tenant_id = $2
RETURNING id, tenant_id, world_id, job_type, status,
          requested_by_token_id, visual_identity_id, asset_pack_id,
          input_payload, requested_outputs, fallback_policy, cache_result,
          preview_asset_ids, final_asset_ids,
          error_code, error_message, retryable,
          cost_reservation_id, cost_estimate_usd, actual_cost_usd,
          queue_duration_ms, generation_duration_ms,
          created_at, updated_at, started_at, completed_at;

-- name: MarkGenerationJobFailed :one
UPDATE generation_jobs
SET status = 'failed',
    error_code = $3,
    error_message = $4,
    retryable = $5,
    completed_at = now(),
    updated_at = now()
WHERE id = $1
  AND tenant_id = $2
RETURNING id, tenant_id, world_id, job_type, status,
          requested_by_token_id, visual_identity_id, asset_pack_id,
          input_payload, requested_outputs, fallback_policy, cache_result,
          preview_asset_ids, final_asset_ids,
          error_code, error_message, retryable,
          cost_reservation_id, cost_estimate_usd, actual_cost_usd,
          queue_duration_ms, generation_duration_ms,
          created_at, updated_at, started_at, completed_at;
