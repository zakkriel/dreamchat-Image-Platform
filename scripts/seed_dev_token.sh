#!/usr/bin/env bash
# Seed one dev API token. Prints the raw secret value once — it is NEVER stored.
# Storage is token_prefix + sha256(secret || API_TOKEN_PEPPER).
set -euo pipefail

PEPPER="${API_TOKEN_PEPPER:-dev-pepper-change-me}"
TENANT_ID="${SEED_TENANT_ID:-tenant_dev}"
TOKEN_NAME="${SEED_TOKEN_NAME:-dev seed token}"

# `head -c N /dev/urandom | tr | cut` — never `tr </dev/urandom | head`: under
# `set -o pipefail` the early-exiting `head` kills `tr` with SIGPIPE and the
# whole script exits 141. `head` bounds the read, `cut` drains its input.
rand_chars() { head -c 4096 /dev/urandom | LC_ALL=C tr -dc "$1" | cut -c "1-$2"; }

PREFIX="dci_dev_$(rand_chars 'a-z0-9' 8)"
SECRET="$(rand_chars 'A-Za-z0-9' 32)"
RAW="${PREFIX}_${SECRET}"
HASH="$(printf '%s%s' "${SECRET}" "${PEPPER}" | sha256sum | awk '{print $1}')"
TOKEN_ID="tok_$(rand_chars 'a-z0-9' 16)"

docker compose exec -T postgres psql -U image_platform -d image_platform -v ON_ERROR_STOP=1 <<SQL
INSERT INTO api_tokens (id, tenant_id, token_prefix, token_hash, name, owner_type, scopes, environment, status)
VALUES ('${TOKEN_ID}', '${TENANT_ID}', '${PREFIX}', '${HASH}', '${TOKEN_NAME}', 'tenant', ARRAY['images:read','images:write','styles:read','styles:write','jobs:read'], 'dev', 'active');
SQL

cat <<EOF

================================================================
Dev token created. The raw value is printed ONCE — save it now.
  Token ID    : ${TOKEN_ID}
  Tenant ID   : ${TENANT_ID}
  Prefix      : ${PREFIX}
  Scopes      : images:read, images:write, styles:read, styles:write, jobs:read
  Environment : dev

  Authorization: Bearer ${RAW}
================================================================
EOF
