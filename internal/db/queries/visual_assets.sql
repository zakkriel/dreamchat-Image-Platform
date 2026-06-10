-- name: GetVisualAssetByID :one
SELECT id, tenant_id, world_id, visual_identity_id, asset_type, variant_key,
       variant_family, version, state_version, style_profile_id,
       style_profile_version, quality_tier, status,
       compatibility_tags, fallback_allowed, fallback_rank, is_identity_anchor,
       low_res_url, high_res_url, thumbnail_url,
       provider_id, model_id, provider_route_id,
       prompt_hash, seed, reference_asset_ids,
       generation_job_id, metadata, generated_at,
       created_at, updated_at
FROM visual_assets
WHERE id = $1
  AND tenant_id = $2;

-- name: InsertVisualAsset :one
INSERT INTO visual_assets (
    id, tenant_id, world_id, visual_identity_id, asset_type, variant_key,
    variant_family, compatibility_tags, fallback_allowed, fallback_rank,
    quality_tier, status,
    low_res_url, high_res_url, thumbnail_url,
    provider_id, model_id, prompt_hash, seed,
    generation_job_id, metadata, generated_at
) VALUES (
    $1, $2, $3, $4, $5, $6,
    $7, $8, $9, $10,
    $11, 'ready',
    $12, $13, $14,
    $15, $16, $17, $18,
    $19, $20, now()
)
RETURNING id, tenant_id, world_id, visual_identity_id, asset_type, variant_key,
          variant_family, version, state_version, style_profile_id,
          style_profile_version, quality_tier, status,
          compatibility_tags, fallback_allowed, fallback_rank, is_identity_anchor,
          low_res_url, high_res_url, thumbnail_url,
          provider_id, model_id, provider_route_id,
          prompt_hash, seed, reference_asset_ids,
          generation_job_id, metadata, generated_at,
          created_at, updated_at;
