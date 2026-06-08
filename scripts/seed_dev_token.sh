#!/usr/bin/env bash
# Seed one dev API token. Prints the raw secret value once — it is NEVER stored.
# Storage is token_prefix + sha256(secret || API_TOKEN_PEPPER).
set -euo pipefail

PEPPER="${API_TOKEN_PEPPER:-dev-pepper-change-me}"
TENANT_ID="${SEED_TENANT_ID:-tenant_dev}"
TOKEN_NAME="${SEED_TOKEN_NAME:-dev seed token}"

PREFIX="dci_dev_$(LC_ALL=C tr -dc 'a-z0-9' </dev/urandom | head -c 8)"
SECRET="$(LC_ALL=C tr -dc 'A-Za-z0-9' </dev/urandom | head -c 32)"
RAW="${PREFIX}_${SECRET}"
HASH="$(printf '%s%s' "${SECRET}" "${PEPPER}" | sha256sum | awk '{print $1}')"
TOKEN_ID="tok_$(LC_ALL=C tr -dc 'a-z0-9' </dev/urandom | head -c 16)"

docker compose exec -T postgres psql -U image_platform -d image_platform -v ON_ERROR_STOP=1 <<SQL
INSERT INTO api_tokens (id, tenant_id, token_prefix, token_hash, name, owner_type, scopes, environment, status)
VALUES ('${TOKEN_ID}', '${TENANT_ID}', '${PREFIX}', '${HASH}', '${TOKEN_NAME}', 'tenant', ARRAY['images:read','images:write'], 'dev', 'active');
SQL

cat <<EOF

================================================================
Dev token created. The raw value is printed ONCE — save it now.
  Token ID    : ${TOKEN_ID}
  Tenant ID   : ${TENANT_ID}
  Prefix      : ${PREFIX}
  Scopes      : images:read, images:write
  Environment : dev

  Authorization: Bearer ${RAW}
================================================================
EOF
