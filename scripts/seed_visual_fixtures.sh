#!/usr/bin/env bash
# ---------------------------------------------------------------------------
# seed_visual_fixtures.sh — LOCAL / DEV ONLY
#
# Seeds a handful of ready visual_assets directly into the local Postgres +
# MinIO so the playground's asset-search and job/asset gallery panels have
# something to retrieve and render WITHOUT first running a real generation.
#
# This is NOT a product upload API and adds NO runtime API surface. It reuses
# the same local conventions the existing seed scripts and docker-compose use:
#   * Postgres rows via `docker compose exec -T postgres psql`
#   * Object bytes via the existing `minio/mc` image (the `minio-init` service),
#     written to the deterministic keys the read surface presigns:
#       assets/<asset_id>/{thumb,low,high}.png   (internal/storage/storage.go)
#
# Requires the local stack to be up (`make dev` / `make up`).
#
# Override defaults via env:
#   SEED_TENANT_ID        (default: tenant_dev — matches the seed token scripts)
#   SEED_WORLD_ID         (default: world_dev)
#   SEED_STYLE_PROFILE_ID (default: unset/NULL — set to a real style id if you
#                          want style-filtered asset search to match)
# ---------------------------------------------------------------------------
set -euo pipefail

cd "$(dirname "$0")/.."

TENANT_ID="${SEED_TENANT_ID:-tenant_dev}"
WORLD_ID="${SEED_WORLD_ID:-world_dev}"
STYLE_PROFILE_ID="${SEED_STYLE_PROFILE_ID:-}"
BUCKET="${S3_BUCKET:-image-platform}"
FIXTURE_DIR="$(pwd)/playground/fixtures"

if [[ ! -d "$FIXTURE_DIR" ]]; then
  echo "fixture dir not found: $FIXTURE_DIR" >&2
  exit 1
fi

# fixture file | asset_id | asset_type | variant_key | variant_family
FIXTURES=(
  "fixture_blue.png|asset_fix_blue|character_portrait|neutral|neutral"
  "fixture_green.png|asset_fix_green|place_scene|establishing|establishing"
  "fixture_amber.png|asset_fix_amber|artifact|artifact|document_clean"
  "fixture_violet.png|asset_fix_violet|expression|smiling|warm"
)

if [[ -n "$STYLE_PROFILE_ID" ]]; then
  STYLE_SQL_VALUE="'${STYLE_PROFILE_ID}'"
else
  STYLE_SQL_VALUE="NULL"
fi

echo "Seeding ${#FIXTURES[@]} visual-asset fixtures (tenant=${TENANT_ID}, world=${WORLD_ID})..."

# --- 1. Insert rows -------------------------------------------------------
{
  echo "BEGIN;"
  for spec in "${FIXTURES[@]}"; do
    IFS='|' read -r _file asset_id asset_type variant_key variant_family <<<"$spec"
    high="s3://${BUCKET}/assets/${asset_id}/high.png"
    low="s3://${BUCKET}/assets/${asset_id}/low.png"
    thumb="s3://${BUCKET}/assets/${asset_id}/thumb.png"
    cat <<SQL
INSERT INTO visual_assets
  (id, tenant_id, world_id, asset_type, variant_key, variant_family, version,
   state_version, style_profile_id, quality_tier, status, fallback_allowed,
   low_res_url, high_res_url, thumbnail_url, metadata, generated_at)
VALUES
  ('${asset_id}', '${TENANT_ID}', '${WORLD_ID}', '${asset_type}', '${variant_key}',
   '${variant_family}', 1, 1, ${STYLE_SQL_VALUE}, 'standard', 'ready', true,
   '${low}', '${high}', '${thumb}', '{"seeded_by":"seed_visual_fixtures.sh"}', now())
ON CONFLICT (id) DO UPDATE SET
   world_id = EXCLUDED.world_id,
   variant_key = EXCLUDED.variant_key,
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
  IFS='|' read -r file asset_id _type _vk _vf <<<"$spec"
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
  Assets : asset_fix_blue, asset_fix_green, asset_fix_amber, asset_fix_violet

Try the playground "Asset search" panel with world_id=${WORLD_ID},
or GET /v1/assets/asset_fix_blue to see presigned image URLs.
================================================================
EOF
