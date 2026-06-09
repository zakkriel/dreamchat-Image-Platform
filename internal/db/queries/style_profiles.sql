-- name: ListStyleProfilesByTenant :many
SELECT id, tenant_id, world_id, name, style_mode, style_profile_version,
       positive_prompt, negative_prompt, loras, reference_style_asset_ids,
       default_quality_tier, status, metadata, created_at, updated_at
FROM style_profiles
WHERE tenant_id = $1
  AND status = 'active'
  AND world_id IS NULL
ORDER BY created_at DESC;

-- name: CreateStyleProfile :one
INSERT INTO style_profiles (
    id, tenant_id, world_id, name, style_mode,
    positive_prompt, negative_prompt, default_quality_tier, status
) VALUES (
    $1, $2, NULL, $3, $4,
    $5, $6, $7, 'active'
)
RETURNING id, tenant_id, world_id, name, style_mode, style_profile_version,
          positive_prompt, negative_prompt, loras, reference_style_asset_ids,
          default_quality_tier, status, metadata, created_at, updated_at;

-- name: GetStyleProfileByID :one
SELECT id, tenant_id, world_id, name, style_mode, style_profile_version,
       positive_prompt, negative_prompt, loras, reference_style_asset_ids,
       default_quality_tier, status, metadata, created_at, updated_at
FROM style_profiles
WHERE id = $1
  AND tenant_id = $2
  AND status = 'active';
