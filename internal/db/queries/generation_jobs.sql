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

-- name: InsertCompletedPackReuseJob :one
-- Phase 6A3 all-hits pack reuse: the pack analogue of InsertCompletedCacheHitJob.
-- Every required role of the pack was satisfied by an existing ready asset, so
-- the pack job is already terminal (status = 'completed') with no provider work:
-- no cost_reservation_id, a zero estimate, a zero actual cost. Unlike the
-- artifact cache-hit (which is always exact_match), a pack aggregates several
-- per-role outcomes, so cache_result is a parameter (the weakest reuse tier
-- across the roles: exact_match | compatible_match | preview_fallback).
-- final_asset_ids points at the reused assets, in role order. This row is never
-- enqueued and the worker never processes it.
INSERT INTO generation_jobs (
    id, tenant_id, world_id, job_type, status,
    requested_by_token_id, input_payload, requested_outputs,
    fallback_policy, cache_result, final_asset_ids,
    cost_estimate_usd, actual_cost_usd, completed_at
) VALUES (
    $1, $2, $3, $4, 'completed',
    $5, $6, sqlc.arg('requested_outputs'),
    $7, sqlc.arg('cache_result'), sqlc.arg('final_asset_ids'),
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

-- LockGenerationJobForUpdate row-locks a job and returns its current status.
-- It is the serialization point that makes in-flight cancel safe (Phase 7C-1):
-- admin cancel and the worker's guarded asset-persist BOTH take this lock on
-- the same row, so whichever commits first wins and the loser observes the
-- committed status. ErrNoRows ⇒ missing or cross-tenant job (→ 404).
-- name: LockGenerationJobForUpdate :one
SELECT status
FROM generation_jobs
WHERE id = $1
  AND tenant_id = $2
FOR UPDATE;

-- CancelGenerationJob transitions a non-terminal job to cancelled (Phase 7C-1a).
-- The caller validates the source status under LockGenerationJobForUpdate first;
-- this write sets the terminal cancel fields in the same transaction as the
-- reservation release.
-- name: CancelGenerationJob :one
UPDATE generation_jobs
SET status = 'cancelled',
    error_code = 'cancelled',
    error_message = $3,
    retryable = false,
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

-- RetryResetGenerationJob reopens a failed job (Phase 7C-1b). It keeps the job
-- identity, payload, fallback policy, delivery mode, persisted resolved route,
-- and preview_asset_ids, but clears the terminal failure fields and the run
-- timestamps, links the fresh cost reservation + estimate, and clears
-- final_asset_ids so a prior failed-final attempt leaves no stale final output.
-- The caller validates the source status is 'failed' under
-- LockGenerationJobForUpdate and reserves cost in the same transaction.
-- name: RetryResetGenerationJob :one
UPDATE generation_jobs
SET status = 'queued',
    error_code = NULL,
    error_message = NULL,
    retryable = NULL,
    started_at = NULL,
    completed_at = NULL,
    final_asset_ids = '{}',
    cost_reservation_id = sqlc.arg(cost_reservation_id),
    cost_estimate_usd = sqlc.arg(cost_estimate_usd),
    actual_cost_usd = NULL,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND tenant_id = sqlc.arg(tenant_id)
RETURNING id, tenant_id, world_id, job_type, status,
          requested_by_token_id, visual_identity_id, asset_pack_id,
          input_payload, requested_outputs, fallback_policy, cache_result,
          preview_asset_ids, final_asset_ids,
          error_code, error_message, retryable,
          cost_reservation_id, cost_estimate_usd, actual_cost_usd,
          queue_duration_ms, generation_duration_ms,
          created_at, updated_at, started_at, completed_at;

-- LockGenerationJobRowForUpdate row-locks a job and returns the full row so the
-- retry path can read the persisted resolved route + payload under the same
-- lock it validates and reopens the job with.
-- name: LockGenerationJobRowForUpdate :one
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
  AND tenant_id = $2
FOR UPDATE;

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

-- name: MarkGenerationJobPreviewReady :one
-- Phase 7B two-phase generation: the preview tier landed. Flip the job to
-- preview_ready and record preview_asset_ids. This is committed BEFORE final
-- generation begins (a separate transaction from final persistence) so the
-- preview state is externally observable through the job read and the
-- job-assets read before the final asset exists. final_asset_ids stays empty
-- until MarkGenerationJobCompleted runs after final success.
UPDATE generation_jobs
SET status = 'preview_ready',
    preview_asset_ids = $3,
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
