#!/usr/bin/env bash
# One command, no manual steps: brings the whole local platform up and keeps it
# in the foreground. Ctrl-C stops everything it started.
#
#   infra      postgres + redis + minio (docker compose)
#   tunnel     public MinIO origin, ONLY when a real provider key is present —
#              fal downloads reference images from its own servers, so a
#              localhost presigned URL fails with file_download_error
#   migrate    goose (idempotent)
#   tokens     reused from .dev/tokens.env while they still authenticate,
#              otherwise seeded fresh; injected into the playground so the
#              Connection panel is pre-filled
#   api/worker `go run` on the HOST, not in docker: a code change costs a ~5s
#              restart instead of a ~60s image rebuild
#   playground vite, opened in a browser tab
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."
ROOT="$PWD"
RUN_DIR="$ROOT/.dev"
mkdir -p "$RUN_DIR/bin"

API_HOST_PORT="${API_HOST_PORT:-8081}"
UI_PORT="${UI_PORT:-5174}"
MINIO_PORT="${MINIO_PORT:-9000}"

log() { printf '\033[1;36m[dev]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[dev]\033[0m %s\n' "$*"; }
die() {
	printf '\033[1;31m[dev]\033[0m %s\n' "$*" >&2
	exit 1
}

# ---------------------------------------------------------------- host env ---
# ./.env holds real provider keys (gitignored). Load it without exporting
# anything the compose file needs to interpolate itself.
if [[ -f "$ROOT/.env" ]]; then
	set -a
	# shellcheck disable=SC1091
	source "$ROOT/.env"
	set +a
fi

PIDS=()

# Kill a process and everything it spawned. `npm run dev` execs vite as a child
# and cloudflared forks, so signalling only the recorded PID leaves a server
# holding :5174 or a tunnel exposing the bucket. Children first, so nothing is
# reparented to init and missed.
kill_tree() {
	local pid="$1" child
	[[ -n "$pid" ]] || return 0
	for child in $(pgrep -P "$pid" 2>/dev/null); do
		kill_tree "$child"
	done
	kill "$pid" 2>/dev/null || true
}

cleanup() {
	trap - INT TERM EXIT
	log "stopping…"
	for pid in "${PIDS[@]:-}"; do
		kill_tree "$pid"
	done
	wait 2>/dev/null || true
	log "host processes stopped. Infra containers left running (make down to remove)."
}
trap cleanup INT TERM EXIT

# ------------------------------------------------------------------- infra ---
log "starting infra (postgres, redis, minio)…"
docker compose up -d postgres redis minio minio-init >/dev/null
# The API and worker run on the host now; a container copy would double-consume
# the job queue and fight for port 8081.
docker compose stop image-platform-api image-platform-worker >/dev/null 2>&1 || true

for _ in $(seq 1 60); do
	docker compose exec -T postgres pg_isready -U image_platform >/dev/null 2>&1 && break
	sleep 1
done
docker compose exec -T postgres pg_isready -U image_platform >/dev/null 2>&1 ||
	die "postgres never became ready"

# ------------------------------------------------------------------ tunnel ---
# Only worth its cost (and its exposure) when a real provider will fetch
# reference images. With mock alone, localhost is reachable by the browser.
PUBLIC_ENDPOINT="http://localhost:${MINIO_PORT}"
if [[ -n "${FAL_KEY:-}${BFL_API_KEY:-}" ]]; then
	URL_FILE="$RUN_DIR/tunnel.url"
	TUNNEL_LOG="$RUN_DIR/tunnel.log"
	EXISTING=""
	[[ -f "$URL_FILE" ]] && EXISTING="$(cat "$URL_FILE")"

	if [[ -n "$EXISTING" ]] && curl -fsS --max-time 5 "$EXISTING/minio/health/live" >/dev/null 2>&1; then
		log "reusing tunnel $EXISTING"
		PUBLIC_ENDPOINT="$EXISTING"
	elif command -v cloudflared >/dev/null 2>&1; then
		log "opening public MinIO tunnel (a provider key is configured)…"
		: >"$TUNNEL_LOG"
		cloudflared tunnel --url "http://localhost:${MINIO_PORT}" --no-autoupdate \
			>>"$TUNNEL_LOG" 2>&1 &
		PIDS+=("$!")
		for _ in $(seq 1 40); do
			PUBLIC_ENDPOINT="$(grep -o 'https://[a-z0-9-]*\.trycloudflare\.com' "$TUNNEL_LOG" | head -1 || true)"
			[[ -n "$PUBLIC_ENDPOINT" ]] && break
			sleep 0.5
		done
		[[ -n "$PUBLIC_ENDPOINT" ]] || die "cloudflared never printed a tunnel URL (see $TUNNEL_LOG)"
		printf '%s\n' "$PUBLIC_ENDPOINT" >"$URL_FILE"
		log "tunnel $PUBLIC_ENDPOINT"
		warn "the image-platform bucket allows anonymous download — objects are world-readable while this tunnel is up"
	else
		warn "cloudflared not installed; real fal/bfl generation will fail with file_download_error"
		warn "  brew install cloudflared"
	fi
fi

# --------------------------------------------------------------- child env ---
export POSTGRES_DSN="postgres://image_platform:image_platform@localhost:5433/image_platform?sslmode=disable"
export POSTGRES_SYSTEM_DSN="$POSTGRES_DSN"
export REDIS_ADDR="localhost:6379"
export S3_BUCKET="image-platform"
export S3_REGION="us-east-1"
export S3_ENDPOINT="http://localhost:${MINIO_PORT}"
export S3_PUBLIC_ENDPOINT="$PUBLIC_ENDPOINT"
export S3_ACCESS_KEY_ID="minioadmin"
export S3_SECRET_ACCESS_KEY="minioadmin"
export S3_USE_PATH_STYLE="true"
export IMAGE_PROVIDER="${IMAGE_PROVIDER:-mock}"
# Mock is synthetic, so identity/pack routes are barred unless this is on. Local
# only — production must fail those requests closed instead.
export ALLOW_SYNTHETIC_PROVIDERS="${ALLOW_SYNTHETIC_PROVIDERS:-true}"
export API_TOKEN_PEPPER="${API_TOKEN_PEPPER:-dev-pepper-change-me}"
export OPENAPI_DOCS_ENABLED="true"
export APP_PORT="$API_HOST_PORT"
export ENVIRONMENT="dev"
export LOG_LEVEL="${LOG_LEVEL:-info}"

# ---------------------------------------------------------------- migrate ----
log "applying migrations…"
go run ./cmd/migrate up >"$RUN_DIR/migrate.log" 2>&1 ||
	{
		cat "$RUN_DIR/migrate.log"
		die "migrations failed"
	}

# ------------------------------------------------------------ api + worker ---
# Build explicitly, then exec the binaries. Two reasons, both about control:
#   - `cmd | sed &` records the PID of *sed*, so the cleanup trap would kill the
#     prefixer and leave the server holding port 8081. Process substitution
#     keeps $! pointing at the real process.
#   - `go run` wraps the binary in another process, so a signal has one more
#     hop to survive. A compile error also surfaces here, before anything else
#     has been started.
# The build is incremental; a warm rebuild is ~1s.
log "building api + worker…"
go build -o "$RUN_DIR/bin/api" ./cmd/api
go build -o "$RUN_DIR/bin/worker" ./cmd/worker

log "starting api (:${API_HOST_PORT}) and worker…"
"$RUN_DIR/bin/api" > >(sed $'s/^/\033[35m[api]\033[0m /') 2>&1 &
PIDS+=("$!")
"$RUN_DIR/bin/worker" > >(sed $'s/^/\033[34m[wrk]\033[0m /') 2>&1 &
PIDS+=("$!")

for _ in $(seq 1 90); do
	curl -fsS "http://localhost:${API_HOST_PORT}/health" >/dev/null 2>&1 && break
	sleep 1
done
curl -fsS "http://localhost:${API_HOST_PORT}/health" >/dev/null 2>&1 ||
	die "api never became healthy"

# ----------------------------------------------------------------- tokens ---
# A raw token is unrecoverable once printed, so cache it and only re-seed when
# the cached one stops authenticating (fresh volume, rotated pepper, …).
TOKENS_FILE="$RUN_DIR/tokens.env"
DEV_TOKEN=""
ADMIN_TOKEN=""
if [[ -f "$TOKENS_FILE" ]]; then
	# shellcheck disable=SC1090
	source "$TOKENS_FILE"
fi

# A token is "still good" when the API does not reject it as unauthenticated or
# unauthorized. Anything else — including 404 from an admin surface that has
# nothing configured yet — proves the credential itself was accepted. Checking
# for 2xx instead would re-seed a fresh admin token on every single run.
token_valid() {
	[[ -n "$1" ]] || return 1
	local code
	code="$(curl -s -o /dev/null -w '%{http_code}' \
		-H "Authorization: Bearer $1" \
		"http://localhost:${API_HOST_PORT}$2")"
	[[ "$code" != "401" && "$code" != "403" && "$code" != "000" ]]
}

if ! token_valid "${DEV_TOKEN:-}" "/v1/styles"; then
	log "seeding tenant token…"
	DEV_TOKEN="$(bash scripts/seed_dev_token.sh | awk '/Authorization: Bearer/ {print $3}')"
	[[ -n "$DEV_TOKEN" ]] || die "could not seed a tenant token"
fi
if ! token_valid "${ADMIN_TOKEN:-}" "/v1/admin/webhook-endpoint"; then
	log "seeding admin token…"
	ADMIN_TOKEN="$(bash scripts/seed_admin_token.sh | awk '/Authorization: Bearer/ {print $3}')"
	[[ -n "$ADMIN_TOKEN" ]] || die "could not seed an admin token"
fi
printf 'DEV_TOKEN=%s\nADMIN_TOKEN=%s\n' "$DEV_TOKEN" "$ADMIN_TOKEN" >"$TOKENS_FILE"

# --------------------------------------------------------------- playground ---
# Vite reads .env.local at dev-server start; the app pre-fills the Connection
# panel from these, so no copy/paste.
cat >"$ROOT/playground/.env.local" <<EOF
# Written by scripts/dev.sh on every run — do not edit, do not commit.
VITE_API_TARGET=http://localhost:${API_HOST_PORT}
VITE_DEV_TENANT_TOKEN=${DEV_TOKEN}
VITE_DEV_ADMIN_TOKEN=${ADMIN_TOKEN}
EOF

[[ -d "$ROOT/playground/node_modules" ]] || (log "npm install…" && cd playground && npm install >/dev/null)

log "starting playground (:${UI_PORT})…"
# Subshell so vite resolves its config from playground/; kill_tree reaches the
# npm and vite children underneath it.
(cd playground && npm run dev) > >(sed $'s/^/\033[32m[ui ]\033[0m /') 2>&1 &
PIDS+=("$!")

for _ in $(seq 1 60); do
	# -f without -S: a not-yet-listening port is expected here, not an error.
	curl -fs -o /dev/null "http://localhost:${UI_PORT}/" && break
	sleep 0.5
done

# macOS only; harmless elsewhere — the URL is printed below either way.
open "http://localhost:${UI_PORT}/" 2>/dev/null || true

cat <<EOF

  $(printf '\033[1;32mready\033[0m')
  ui        http://localhost:${UI_PORT}          (tokens pre-filled)
  api       http://localhost:${API_HOST_PORT}    docs at /docs
  assets    ${PUBLIC_ENDPOINT}
  provider  ${IMAGE_PROVIDER}$([[ -n "${FAL_KEY:-}" ]] && echo " (+fal configured — pin provider_id=fal to use it)")

  Ctrl-C stops the api, worker, playground and tunnel.

EOF

wait
