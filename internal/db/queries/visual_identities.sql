-- name: GetVisualIdentityByOwnerForUpdate :one
SELECT id, tenant_id, world_id, owner_type, owner_id, display_name,
       canonical_visual_traits, allowed_variation, forbidden_drift,
       style_profile_id, consistency_key, identity_seed,
       anchor_asset_ids, reference_asset_ids,
       current_version, current_state_version, status,
       created_at, updated_at
FROM visual_identities
WHERE tenant_id = $1
  AND world_id = $2
  AND owner_type = $3
  AND owner_id = $4
FOR UPDATE;

-- name: GetVisualIdentityByOwner :one
SELECT id, tenant_id, world_id, owner_type, owner_id, display_name,
       canonical_visual_traits, allowed_variation, forbidden_drift,
       style_profile_id, consistency_key, identity_seed,
       anchor_asset_ids, reference_asset_ids,
       current_version, current_state_version, status,
       created_at, updated_at
FROM visual_identities
WHERE tenant_id = $1
  AND world_id = $2
  AND owner_type = $3
  AND owner_id = $4;

-- name: GetVisualIdentityByOwnerAcrossWorlds :one
SELECT id, tenant_id, world_id, owner_type, owner_id, display_name,
       canonical_visual_traits, allowed_variation, forbidden_drift,
       style_profile_id, consistency_key, identity_seed,
       anchor_asset_ids, reference_asset_ids,
       current_version, current_state_version, status,
       created_at, updated_at
FROM visual_identities
WHERE tenant_id = $1
  AND owner_type = $2
  AND owner_id = $3
ORDER BY updated_at DESC
LIMIT 1;

-- name: GetVisualIdentityByID :one
SELECT id, tenant_id, world_id, owner_type, owner_id, display_name,
       canonical_visual_traits, allowed_variation, forbidden_drift,
       style_profile_id, consistency_key, identity_seed,
       anchor_asset_ids, reference_asset_ids,
       current_version, current_state_version, status,
       created_at, updated_at
FROM visual_identities
WHERE id = $1
  AND tenant_id = $2;

-- name: InsertVisualIdentity :one
INSERT INTO visual_identities (
    id, tenant_id, world_id, owner_type, owner_id, display_name,
    canonical_visual_traits, style_profile_id, consistency_key,
    current_version, status
) VALUES (
    $1, $2, $3, $4, $5, $6,
    $7, $8, $9,
    1, 'active'
)
RETURNING id, tenant_id, world_id, owner_type, owner_id, display_name,
          canonical_visual_traits, allowed_variation, forbidden_drift,
          style_profile_id, consistency_key, identity_seed,
          anchor_asset_ids, reference_asset_ids,
          current_version, current_state_version, status,
          created_at, updated_at;

-- name: UpdateVisualIdentityWithVersionBump :one
UPDATE visual_identities
SET display_name = $2,
    canonical_visual_traits = $3,
    style_profile_id = $4,
    consistency_key = $5,
    current_version = current_version + 1,
    updated_at = now()
WHERE id = $1
RETURNING id, tenant_id, world_id, owner_type, owner_id, display_name,
          canonical_visual_traits, allowed_variation, forbidden_drift,
          style_profile_id, consistency_key, identity_seed,
          anchor_asset_ids, reference_asset_ids,
          current_version, current_state_version, status,
          created_at, updated_at;

-- name: InsertVisualIdentityVersion :exec
INSERT INTO visual_identity_versions (
    visual_identity_id, version, reason, canonical_traits_snapshot
) VALUES (
    $1, $2, $3, $4
);
