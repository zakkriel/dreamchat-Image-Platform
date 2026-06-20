-- CONVENTION: queries here list visual_assets columns EXPLICITLY (not SELECT *).
-- When a migration adds a column, append it to the matching RETURNING/SELECT
-- lists below, or sqlc emits a per-query *Row type and the build breaks.
-- name: GetVisualAssetByID :one
SELECT id, tenant_id, world_id, visual_identity_id, asset_type, variant_key,
       variant_family, version, state_version, style_profile_id,
       style_profile_version, quality_tier, status,
       compatibility_tags, fallback_allowed, fallback_rank, is_identity_anchor,
       low_res_url, high_res_url, thumbnail_url,
       provider_id, model_id, provider_route_id,
       prompt_hash, seed, reference_asset_ids,
       generation_job_id, metadata, generated_at,
       created_at, updated_at, superseded_by_asset_id,
       anchor_asset_id, derive_from,
       parent_asset_id, crop_index, crop_box, expression_key
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
       created_at, updated_at, superseded_by_asset_id,
       anchor_asset_id, derive_from,
       parent_asset_id, crop_index, crop_box, expression_key
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

-- name: FindReadyArtifactByPromptHash :one
-- Phase 6A2 single-artifact exact reuse: the narrow deterministic lookup behind
-- the artifact retrieval-before-generation path. Artifacts have no durable
-- visual identity in the generation path (they are asset_type = 'artifact',
-- variant_key = 'default'), so reuse is keyed on the deterministic artifact
-- render hash stored in prompt_hash rather than on identity/matrix retrieval.
-- Owner (tenant/world) + style + quality + the render hash must all match and
-- the asset must be reusable (status = 'ready'). style_profile_version is
-- optional: when provided it must match exactly (the render hash already folds
-- it in, so this is a belt-and-suspenders narrowing). A stable id ASC tie-break
-- keeps the single returned row deterministic when several ready rows share the
-- same hash (e.g. a re-run that raced before this reuse path existed).
SELECT id, tenant_id, world_id, visual_identity_id, asset_type, variant_key,
       variant_family, version, state_version, style_profile_id,
       style_profile_version, quality_tier, status,
       compatibility_tags, fallback_allowed, fallback_rank, is_identity_anchor,
       low_res_url, high_res_url, thumbnail_url,
       provider_id, model_id, provider_route_id,
       prompt_hash, seed, reference_asset_ids,
       generation_job_id, metadata, generated_at,
       created_at, updated_at, superseded_by_asset_id,
       anchor_asset_id, derive_from,
       parent_asset_id, crop_index, crop_box, expression_key
FROM visual_assets
WHERE tenant_id = sqlc.arg('tenant_id')
  AND world_id = sqlc.arg('world_id')
  AND asset_type = 'artifact'
  AND variant_key = 'default'
  AND style_profile_id = sqlc.arg('style_profile_id')
  AND quality_tier = sqlc.arg('quality_tier')
  AND prompt_hash = sqlc.arg('prompt_hash')
  AND status = 'ready'
  AND (sqlc.narg('style_profile_version')::int IS NULL
       OR style_profile_version = sqlc.narg('style_profile_version'))
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
       created_at, updated_at, superseded_by_asset_id,
       anchor_asset_id, derive_from,
       parent_asset_id, crop_index, crop_box, expression_key
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
       created_at, updated_at, superseded_by_asset_id,
       anchor_asset_id, derive_from,
       parent_asset_id, crop_index, crop_box, expression_key
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
-- The new asset always lands status='ready'. version is supplied by the caller
-- (Phase 6A4): the normal generate path passes 1 (the prior schema DEFAULT);
-- a forced regeneration passes prior_max_version + 1 so versions stay monotonic
-- across regenerations of a slot, archived predecessors included.
INSERT INTO visual_assets (
    id, tenant_id, world_id, visual_identity_id, asset_type, variant_key,
    variant_family, compatibility_tags, fallback_allowed, fallback_rank,
    style_profile_id, style_profile_version,
    quality_tier, status, version,
    low_res_url, high_res_url, thumbnail_url,
    provider_id, model_id, provider_route_id, prompt_hash, seed,
    generation_job_id, metadata, generated_at
) VALUES (
    $1, $2, $3, $4, $5, $6,
    $7, $8, $9, $10,
    $11, $12,
    $13, 'ready', sqlc.arg('version'),
    $14, $15, $16,
    $17, $18, $19, $20, $21,
    $22, $23, now()
)
RETURNING id, tenant_id, world_id, visual_identity_id, asset_type, variant_key,
          variant_family, version, state_version, style_profile_id,
          style_profile_version, quality_tier, status,
          compatibility_tags, fallback_allowed, fallback_rank, is_identity_anchor,
          low_res_url, high_res_url, thumbnail_url,
          provider_id, model_id, provider_route_id,
          prompt_hash, seed, reference_asset_ids,
          generation_job_id, metadata, generated_at,
          created_at, updated_at, superseded_by_asset_id,
          anchor_asset_id, derive_from,
          parent_asset_id, crop_index, crop_box, expression_key;

-- name: InsertPreviewVisualAsset :one
-- Phase 7B two-phase preview tier. Identical column mapping to InsertVisualAsset
-- but the asset always lands status='preview_ready' (not 'ready'): it is the
-- lighter, earlier output of a preview_first job, committed and observable
-- before the final asset is generated. It is never a reuse target (the artifact
-- reuse lookup matches status='ready' only), so a preview row never satisfies a
-- later request. version is supplied by the caller (the preview path passes 1).
INSERT INTO visual_assets (
    id, tenant_id, world_id, visual_identity_id, asset_type, variant_key,
    variant_family, compatibility_tags, fallback_allowed, fallback_rank,
    style_profile_id, style_profile_version,
    quality_tier, status, version,
    low_res_url, high_res_url, thumbnail_url,
    provider_id, model_id, provider_route_id, prompt_hash, seed,
    generation_job_id, metadata, generated_at
) VALUES (
    $1, $2, $3, $4, $5, $6,
    $7, $8, $9, $10,
    $11, $12,
    $13, 'preview_ready', sqlc.arg('version'),
    $14, $15, $16,
    $17, $18, $19, $20, $21,
    $22, $23, now()
)
RETURNING id, tenant_id, world_id, visual_identity_id, asset_type, variant_key,
          variant_family, version, state_version, style_profile_id,
          style_profile_version, quality_tier, status,
          compatibility_tags, fallback_allowed, fallback_rank, is_identity_anchor,
          low_res_url, high_res_url, thumbnail_url,
          provider_id, model_id, provider_route_id,
          prompt_hash, seed, reference_asset_ids,
          generation_job_id, metadata, generated_at,
          created_at, updated_at, superseded_by_asset_id,
          anchor_asset_id, derive_from,
          parent_asset_id, crop_index, crop_box, expression_key;

-- name: AcquireSupersedeLock :exec
-- Phase 6A4 supersede concurrency control: a transaction-scoped advisory lock
-- keyed on the exact regeneration slot. Two concurrent forced regenerations of
-- the same slot serialize here, so they compute prior_max_version, archive prior
-- ready rows, and insert the new ready row one at a time — producing versions
-- N+1 and N+2 (never duplicate versions) and leaving exactly one ready row. The
-- lock auto-releases at commit/rollback. The slot key is built deterministically
-- by the caller (internal/assets) from the exact slot identity.
SELECT pg_advisory_xact_lock(hashtextextended(sqlc.arg('slot_key')::text, 0));

-- name: MaxVersionForArtifactSlot :one
-- Phase 6A4 supersede: the current max version across ALL rows (ready AND
-- archived) of the exact artifact reuse slot, so the regenerated asset's version
-- is COALESCE(max, 0) + 1 and stays monotonic even as predecessors are archived.
-- The slot predicate is byte-for-byte the FindReadyArtifactByPromptHash reuse
-- slot minus the status filter (we count archived rows too).
SELECT COALESCE(MAX(version), 0)::int AS max_version
FROM visual_assets
WHERE tenant_id = sqlc.arg('tenant_id')
  AND world_id = sqlc.arg('world_id')
  AND asset_type = 'artifact'
  AND variant_key = 'default'
  AND style_profile_id = sqlc.arg('style_profile_id')
  AND quality_tier = sqlc.arg('quality_tier')
  AND prompt_hash = sqlc.arg('prompt_hash');

-- name: ArchivePriorReadyArtifactSlot :exec
-- Phase 6A4 supersede: archive every still-ready row of the exact artifact slot
-- except the just-inserted new asset, linking each forward to it. Slot-scoped and
-- exact — the same predicate as the reuse lookup — so a forced regenerate never
-- archives a compatible/preview neighbor, only the identical slot. Runs in the
-- same transaction (under the slot lock) as the new asset's insert, so committed
-- readers flip atomically from old-ready to new-ready.
UPDATE visual_assets
SET status = 'archived',
    superseded_by_asset_id = sqlc.arg('new_asset_id'),
    updated_at = now()
WHERE tenant_id = sqlc.arg('tenant_id')
  AND world_id = sqlc.arg('world_id')
  AND asset_type = 'artifact'
  AND variant_key = 'default'
  AND style_profile_id = sqlc.arg('style_profile_id')
  AND quality_tier = sqlc.arg('quality_tier')
  AND prompt_hash = sqlc.arg('prompt_hash')
  AND status = 'ready'
  AND id <> sqlc.arg('new_asset_id');

-- name: MaxVersionForVariantSlot :one
-- Phase 6A4 supersede: the current max version across ALL rows (ready AND
-- archived) of the exact pack-role reuse slot. Mirrors MaxVersionForArtifactSlot
-- for the FindExactVisualAsset slot predicate (identity + variant + state + style
-- + quality), minus the status filter.
SELECT COALESCE(MAX(version), 0)::int AS max_version
FROM visual_assets
WHERE tenant_id = sqlc.arg('tenant_id')
  AND world_id = sqlc.arg('world_id')
  AND visual_identity_id = sqlc.arg('visual_identity_id')
  AND variant_key = sqlc.arg('variant_key')
  AND state_version = sqlc.arg('state_version')
  AND style_profile_id = sqlc.arg('style_profile_id')
  AND quality_tier = sqlc.arg('quality_tier');

-- name: ArchivePriorReadyVariantSlot :exec
-- Phase 6A4 supersede: archive every still-ready row of the exact pack-role slot
-- except the just-inserted new asset, linking each forward to it. Exact and
-- slot-scoped (the FindExactVisualAsset predicate) so a forced pack regenerate
-- never touches a compatible/preview neighbor.
UPDATE visual_assets
SET status = 'archived',
    superseded_by_asset_id = sqlc.arg('new_asset_id'),
    updated_at = now()
WHERE tenant_id = sqlc.arg('tenant_id')
  AND world_id = sqlc.arg('world_id')
  AND visual_identity_id = sqlc.arg('visual_identity_id')
  AND variant_key = sqlc.arg('variant_key')
  AND state_version = sqlc.arg('state_version')
  AND style_profile_id = sqlc.arg('style_profile_id')
  AND quality_tier = sqlc.arg('quality_tier')
  AND status = 'ready'
  AND id <> sqlc.arg('new_asset_id');
