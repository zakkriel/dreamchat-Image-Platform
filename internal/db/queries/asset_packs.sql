-- Pack orchestration queries (Phase 5A, ADR-008). The platform — not the
-- provider adapter — owns pack fan-out: the create transaction inserts the
-- asset_packs row alongside the generation_jobs row, and the worker writes
-- one asset_pack_items row per generated variant.

-- name: InsertAssetPack :one
-- The create transaction inserts the asset_packs row alongside the
-- generation_jobs row. status is a parameter (Phase 6A3): a normal pack that
-- still has roles to generate is inserted 'planned' for the worker to advance,
-- while an all-hits reuse pack is inserted 'completed' directly (no worker run).
-- required_roles/delivered_roles/missing_roles record pack completeness at
-- creation: required = every template role, delivered = roles already satisfied
-- by a reused asset, missing = roles awaiting generation.
INSERT INTO asset_packs (
    id, tenant_id, world_id, visual_identity_id, pack_type,
    style_profile_id, quality_tier, status,
    required_roles, delivered_roles, missing_roles,
    created_by_job_id, created_by_token_id
) VALUES (
    $1, $2, $3, $4, $5,
    $6, $7, sqlc.arg('status'),
    sqlc.arg('required_roles'), sqlc.arg('delivered_roles'), sqlc.arg('missing_roles'),
    $8, $9
)
RETURNING id, tenant_id, world_id, visual_identity_id, pack_type,
          style_profile_id, style_profile_version, visual_identity_version,
          quality_tier, status,
          required_roles, delivered_roles, missing_roles,
          created_by_job_id, created_by_token_id,
          created_at, updated_at;

-- UpdateAssetPackCompleteness records the final delivered-vs-missing required
-- roles a pack run resolved to (Phase 6A3). The worker calls it at the terminal
-- step so a consumer can read pack completeness off asset_packs directly.
-- name: UpdateAssetPackCompleteness :exec
UPDATE asset_packs
SET delivered_roles = sqlc.arg('delivered_roles'),
    missing_roles = sqlc.arg('missing_roles'),
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND status IN ('planned', 'in_progress');

-- SetGenerationJobAssetPack links the job to the pack it created. Run inside
-- the create transaction, after both rows exist.
-- UpdateAssetPackCompletenessForJob is the retry-safe variant. It allows the
-- same generation job to correct a terminal pack status after a bookkeeping
-- failure, but never lets an unrelated/stale job rewrite the pack.
-- name: UpdateAssetPackCompletenessForJob :exec
UPDATE asset_packs
SET delivered_roles = sqlc.arg('delivered_roles'),
    missing_roles = sqlc.arg('missing_roles'),
    updated_at = now()
WHERE id = sqlc.arg('id')
  AND created_by_job_id = sqlc.arg('generation_job_id');

-- name: SetGenerationJobAssetPack :exec
UPDATE generation_jobs
SET asset_pack_id = sqlc.arg(asset_pack_id),
    updated_at = now()
WHERE id = sqlc.arg(id);

-- name: UpdateAssetPackStatus :exec
UPDATE asset_packs
SET status = sqlc.arg(status),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status IN ('planned', 'in_progress');

-- UpdateAssetPackStatusForJob is the retry-safe variant. Terminal statuses
-- may be corrected only by the generation job that owns the pack.
-- name: UpdateAssetPackStatusForJob :exec
UPDATE asset_packs
SET status = sqlc.arg(status),
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND created_by_job_id = sqlc.arg(generation_job_id)
  AND status IN ('planned', 'in_progress', 'completed', 'completed_with_warnings', 'failed');

-- name: InsertAssetPackItem :exec
INSERT INTO asset_pack_items (
    id, asset_pack_id, visual_asset_id, variant_key, sort_order
) VALUES (
    $1, $2, $3, $4, $5
);

-- name: GetAssetPackByID :one
SELECT id, tenant_id, world_id, visual_identity_id, pack_type,
       style_profile_id, style_profile_version, visual_identity_version,
       quality_tier, status,
       required_roles, delivered_roles, missing_roles,
       created_by_job_id, created_by_token_id,
       created_at, updated_at
FROM asset_packs
WHERE id = $1;

-- name: ListAssetPackItems :many
SELECT id, asset_pack_id, visual_asset_id, variant_key, sort_order, created_at
FROM asset_pack_items
WHERE asset_pack_id = $1
ORDER BY sort_order, created_at;
