#!/usr/bin/env bash
# ---------------------------------------------------------------------------
# seed_visual_fixtures.sh — LOCAL / DEV ONLY
#
# Seeds a small, self-consistent graph directly into the local Postgres +
# MinIO so the playground's Asset-search and job/asset gallery panels have
# realistic data to retrieve and render WITHOUT first running a generation:
#
#   * 1 style profile        (style_fixture)
#   * 2 visual identities     (vi_fix_character, vi_fix_place)
#   * 4 ready visual_assets   attached to those identities, with PNG bytes
#                             uploaded to their deterministic MinIO keys.
#
# The asset-search exact-match predicate matches on
#   tenant + world + visual_identity_id + variant_key + state_version +
#   style_profile_id + status='ready'
# (internal/db/queries/visual_assets.sql :: FindExactVisualAsset), so every
# fixture asset is attached to a real visual identity and style profile and
# is directly retrievable by /v1/assets/search with the inputs printed below.
#
# This is NOT a product upload API and adds NO runtime API surface. It reuses
# the same local conventions the existing seed scripts and docker-compose use:
#   * Postgres rows via `docker compose exec -T postgres psql`
#   * Object bytes via the existing `minio/mc` image (the `minio-init` service),
#     written to assets/<asset_id>/{thumb,low,high}.png — the keys the read
#     surface presigns (internal/storage/storage.go).
#
# Requires the local stack to be up (`make dev` / `make up`).
#
# Override defaults via env:
#   SEED_TENANT_ID  (default: tenant_dev — matches the seed token scripts)
#   SEED_WORLD_ID   (default: world_dev)
#   S3_BUCKET       (default: image-platform)
# ---------------------------------------------------------------------------
set -euo pipefail

cd "$(dirname "$0")/.."

TENANT_ID="${SEED_TENANT_ID:-tenant_dev}"
WORLD_ID="${SEED_WORLD_ID:-world_dev}"
BUCKET="${S3_BUCKET:-image-platform}"
STYLE_ID="style_fixture"
FIXTURE_DIR="$(pwd)/playground/fixtures"

if [[ ! -d "$FIXTURE_DIR" ]]; then
  echo "fixture dir not found: $FIXTURE_DIR" >&2
  exit 1
fi

# fixture file | asset_id | visual_identity_id | owner_id | asset_type | variant_key | variant_family
FIXTURES=(
  "fixture_blue.png|asset_fix_char_neutral|vi_fix_character|character_fix_1|character_portrait|neutral|neutral"
  "fixture_violet.png|asset_fix_char_smiling|vi_fix_character|character_fix_1|expression|smiling|warm"
  "fixture_green.png|asset_fix_place_estab|vi_fix_place|place_fix_1|place_scene|establishing|establishing"
  "fixture_amber.png|asset_fix_place_night|vi_fix_place|place_scene_owner|place_scene|night|establishing"
)

echo "Seeding fixtures: 1 style + 2 visual identities + ${#FIXTURES[@]} assets (tenant=${TENANT_ID}, world=${WORLD_ID})..."

# --- 1. Insert style profile, identities, version rows, and assets --------
{
  echo "BEGIN;"

  # Style profile (FK target for identities and assets).
  cat <<SQL
INSERT INTO style_profiles (id, tenant_id, world_id, name, style_mode, positive_prompt, status)
VALUES ('${STYLE_ID}', '${TENANT_ID}', '${WORLD_ID}', 'Playground Fixture Style', 'open_prompt',
        'clean flat illustration, soft lighting', 'active')
ON CONFLICT (id) DO NOTHING;
SQL

  # Two visual identities (character + place).
  cat <<SQL
INSERT INTO visual_identities
  (id, tenant_id, world_id, owner_type, owner_id, display_name,
   canonical_visual_traits, style_profile_id, consistency_key,
   current_version, current_state_version, status)
VALUES
  ('vi_fix_character', '${TENANT_ID}', '${WORLD_ID}', 'character', 'character_fix_1',
   'Fixture Hero', '{"hair":"black","outfit":"blue cloak"}', '${STYLE_ID}', 'fix-hero', 1, 1, 'active'),
  ('vi_fix_place', '${TENANT_ID}', '${WORLD_ID}', 'place', 'place_fix_1',
   'Fixture Plaza', '{"setting":"sunlit plaza"}', '${STYLE_ID}', 'fix-plaza', 1, 1, 'active')
ON CONFLICT (id) DO NOTHING;

INSERT INTO visual_identity_versions (visual_identity_id, version, reason, canonical_traits_snapshot)
VALUES
  ('vi_fix_character', 1, 'initial', '{"hair":"black","outfit":"blue cloak"}'),
  ('vi_fix_place', 1, 'initial', '{"setting":"sunlit plaza"}')
ON CONFLICT (visual_identity_id, version) DO NOTHING;
SQL

  # Assets attached to the identities.
  for spec in "${FIXTURES[@]}"; do
    IFS='|' read -r _file asset_id vi_id _owner asset_type variant_key variant_family <<<"$spec"
    high="s3://${BUCKET}/assets/${asset_id}/high.png"
    low="s3://${BUCKET}/assets/${asset_id}/low.png"
    thumb="s3://${BUCKET}/assets/${asset_id}/thumb.png"
    cat <<SQL
INSERT INTO visual_assets
  (id, tenant_id, world_id, visual_identity_id, asset_type, variant_key, variant_family,
   version, state_version, style_profile_id, quality_tier, status, fallback_allowed,
   low_res_url, high_res_url, thumbnail_url, metadata, generated_at)
VALUES
  ('${asset_id}', '${TENANT_ID}', '${WORLD_ID}', '${vi_id}', '${asset_type}', '${variant_key}',
   '${variant_family}', 1, 1, '${STYLE_ID}', 'standard', 'ready', true,
   '${low}', '${high}', '${thumb}', '{"seeded_by":"seed_visual_fixtures.sh"}', now())
ON CONFLICT (id) DO UPDATE SET
   visual_identity_id = EXCLUDED.visual_identity_id,
   variant_key = EXCLUDED.variant_key,
   style_profile_id = EXCLUDED.style_profile_id,
   state_version = EXCLUDED.state_version,
   status = EXCLUDED.status,
   updated_at = now();
SQL
  done

  echo "COMMIT;"
} | docker compose exec -T postgres psql -U image_platform -d image_platform -v ON_ERROR_STOP=1

# --- 2. Upload object bytes to MinIO via the existing mc image ------------
# Build one mc script and run it in a throwaway container on the compose
# network (so `minio` resolves), with the fixtures mounted read-only.
MC_SCRIPT="mc alias set local http://minio:9000 minioadmin minioadmin >/dev/null"
for spec in "${FIXTURES[@]}"; do
  IFS='|' read -r file asset_id _vi _owner _type _vk _vf <<<"$spec"
  for variant in thumb low high; do
    MC_SCRIPT+=" && mc cp -q /fixtures/${file} local/${BUCKET}/assets/${asset_id}/${variant}.png"
  done
done
MC_SCRIPT+=" && echo 'fixtures uploaded'"

docker compose run --rm \
  -v "${FIXTURE_DIR}:/fixtures:ro" \
  --entrypoint /bin/sh \
  minio-init -c "${MC_SCRIPT}"

cat <<EOF

================================================================
Visual fixtures seeded (local/dev only).
  Tenant : ${TENANT_ID}
  World  : ${WORLD_ID}
  Style  : ${STYLE_ID}
  Identities : vi_fix_character (character_fix_1), vi_fix_place (place_fix_1)

Asset search inputs that return an exact_match (playground panel 7):
  world_id           = ${WORLD_ID}
  owner_type         = character
  visual_identity_id = vi_fix_character
  variant_key        = neutral          (or: smiling)
  style_profile_id   = ${STYLE_ID}
  state_version      = 1

  world_id           = ${WORLD_ID}
  owner_type         = place
  visual_identity_id = vi_fix_place
  variant_key        = establishing     (or: night)
  style_profile_id   = ${STYLE_ID}
  state_version      = 1

Note: /v1/assets/search rejects owner_type=artifact by design.
================================================================
EOF
