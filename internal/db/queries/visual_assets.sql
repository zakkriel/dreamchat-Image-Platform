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

-- name: FindExactVisualAsset :one
-- Phase 6A1 retrieval substrate: the exact-match lookup behind
-- RetrievalResult.exact_match. Owner (tenant/world/visual_identity) + variant
-- + state + style must all match and the asset must be reusable (status =
-- 'ready' — the existing status vocabulary's "active asset"; there is no
-- 'active' status). style_profile_version and quality_tier are optional: when
-- provided they must match exactly. NOTE: quality ordering does not yet exist
-- as a comparable concept in the schema, so quality_tier uses exact equality
-- rather than ">= requested" (documented in internal/assets/retrieval.go).
-- A stable id ASC tie-break keeps the single returned row deterministic when
-- several exact rows exist (e.g. regenerations).
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
WHERE tenant_id = sqlc.arg('tenant_id')
  AND world_id = sqlc.arg('world_id')
  AND visual_identity_id = sqlc.arg('visual_identity_id')
  AND variant_key = sqlc.arg('variant_key')
  AND state_version = sqlc.arg('state_version')
  AND style_profile_id = sqlc.arg('style_profile_id')
  AND status = 'ready'
  AND (sqlc.narg('style_profile_version')::int IS NULL
       OR style_profile_version = sqlc.narg('style_profile_version'))
  AND (sqlc.narg('quality_tier')::text IS NULL
       OR quality_tier = sqlc.narg('quality_tier'))
ORDER BY id ASC
LIMIT 1;

-- name: ListVisualAssetCandidates :many
-- Phase 6A1 retrieval substrate: candidate ready assets for the same owner
-- and visual identity that the compatibility matrix
-- (internal/assets/compatibility.go) may approve as a compatible_match or
-- preview_fallback. State is strict (matrix §7.4/§8.4) so candidates share the
-- requested state_version; style is held constant (same style_profile_id) so a
-- substitution never silently changes the visual style. Identity anchors are
-- excluded here (matrix §10.1: anchors are reference inputs, never display
-- substitutes). Uses idx_visual_assets_identity_variant /
-- idx_visual_assets_identity_family. Deterministic order: fallback_rank ASC
-- (lower = preferred) then id ASC as the final tie-break.
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
WHERE tenant_id = sqlc.arg('tenant_id')
  AND world_id = sqlc.arg('world_id')
  AND visual_identity_id = sqlc.arg('visual_identity_id')
  AND state_version = sqlc.arg('state_version')
  AND style_profile_id = sqlc.arg('style_profile_id')
  AND status = 'ready'
  AND is_identity_anchor = false
ORDER BY fallback_rank ASC NULLS LAST, id ASC;

-- name: ListVisualAssetCandidatesByCompatTag :many
-- Phase 6A1 retrieval substrate: a compatibility_tags-optimized candidate
-- lookup over the same owner/identity/state/style scope as
-- ListVisualAssetCandidates, narrowed to assets whose compatibility_tags
-- overlap the requested set (e.g. {generic_presence}). Backed by the GIN index
-- idx_visual_assets_compat_tags. Used when retrieval only needs tag-eligible
-- candidates (and exercised directly by the integration tests). Anchors are
-- excluded; deterministic order matches ListVisualAssetCandidates.
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
WHERE tenant_id = sqlc.arg('tenant_id')
  AND world_id = sqlc.arg('world_id')
  AND visual_identity_id = sqlc.arg('visual_identity_id')
  AND state_version = sqlc.arg('state_version')
  AND style_profile_id = sqlc.arg('style_profile_id')
  AND status = 'ready'
  AND is_identity_anchor = false
  AND compatibility_tags && sqlc.arg('compatibility_tags')::text[]
ORDER BY fallback_rank ASC NULLS LAST, id ASC;

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
