#!/usr/bin/env bash
# Seed one dev ADMIN API token carrying only the Phase 4B admin scope
# (admin:costs). Prints the raw secret value once — it is NEVER stored.
# Storage is token_prefix + sha256(secret || API_TOKEN_PEPPER).
#
# This is deliberately separate from `make seed`: the normal dev token must
# NOT carry admin:costs (admin endpoints are gated on it).
set -euo pipefail

PEPPER="${API_TOKEN_PEPPER:-dev-pepper-change-me}"
TENANT_ID="${SEED_ADMIN_TENANT_ID:-tenant_dev}"
TOKEN_NAME="${SEED_ADMIN_TOKEN_NAME:-dev admin token (admin:costs)}"

PREFIX="dci_admin_$(LC_ALL=C tr -dc 'a-z0-9' </dev/urandom | head -c 8)"
SECRET="$(LC_ALL=C tr -dc 'A-Za-z0-9' </dev/urandom | head -c 32)"
RAW="${PREFIX}_${SECRET}"
HASH="$(printf '%s%s' "${SECRET}" "${PEPPER}" | sha256sum | awk '{print $1}')"
TOKEN_ID="tok_admin_$(LC_ALL=C tr -dc 'a-z0-9' </dev/urandom | head -c 12)"

docker compose exec -T postgres psql -U image_platform -d image_platform -v ON_ERROR_STOP=1 <<SQL
INSERT INTO api_tokens (id, tenant_id, token_prefix, token_hash, name, owner_type, scopes, environment, status)
VALUES ('${TOKEN_ID}', '${TENANT_ID}', '${PREFIX}', '${HASH}', '${TOKEN_NAME}', 'tenant', ARRAY['admin:costs'], 'dev', 'active');
SQL

cat <<EOF

================================================================
Dev ADMIN token created. The raw value is printed ONCE — save it now.
  Token ID    : ${TOKEN_ID}
  Tenant ID   : ${TENANT_ID}
  Prefix      : ${PREFIX}
  Scopes      : admin:costs
  Environment : dev

  Authorization: Bearer ${RAW}
================================================================
EOF
