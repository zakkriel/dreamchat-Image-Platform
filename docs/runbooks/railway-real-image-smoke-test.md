# Runbook — Railway Real-Image Smoke Test

**Single goal:** generate **one real image end-to-end on Railway** using the BFL
provider, and open the resulting image URL in a browser.

This runbook is intentionally narrow. It is **not** production hardening, an
admin/dashboard rollout, or a platform-expansion guide. It gets exactly one real
artifact generated and delivered.

> ⚠️ **Playground is dev-only.** The `playground/` app is a local development
> aid. Do **not** deploy it as a public app, and do **not** wire it to real
> staging tokens. For this smoke test, if you use the playground at all, point it
> at the Railway API **manually from your own machine** with a short-lived token.
> The deployable surfaces here are only the **API** and **worker** services.

---

## 1. Railway service graph

Create one Railway **project** with five components:

```txt
                +------------------------+
                |   API service          |   public networking ON
                |   Docker BINARY=api    |   healthcheck: /health
                +-----------+------------+
                            |
        +-------------------+--------------------+
        |                   |                    |
+-------v------+   +--------v-------+   +--------v---------+
|  Postgres    |   |    Redis       |   |  S3-compatible   |
|  (Railway)   |   |   (Railway)    |   |  object storage  |
+-------^------+   +--------^-------+   +--------^---------+
        |                   |                    |
        +-------------------+--------------------+
                            |
                +-----------+------------+
                |   Worker service       |   no public domain
                |   Docker BINARY=worker |   reads jobs from Redis,
                +------------------------+   writes images to S3
```

| Component | What it is | Public? |
|-----------|------------|---------|
| **API service** | Built from the repo `Dockerfile` with build arg `BINARY=api`. Serves `/health` + `/v1/*`. | **Yes** (public domain) |
| **Worker service** | Built from the same `Dockerfile` with build arg `BINARY=worker`. Consumes asynq jobs from Redis, calls the provider, writes images to S3. | No |
| **Postgres** | Railway Postgres plugin (`Add` → `Database` → `PostgreSQL`). | Internal |
| **Redis** | Railway Redis plugin (`Add` → `Database` → `Redis`). | Internal |
| **S3-compatible storage** | **Railway Storage Buckets** are the preferred Railway-native option (they are S3-compatible). External S3-compatible providers — Cloudflare R2, Backblaze B2, AWS S3, or a public MinIO — also work. The smoke test only requires an S3-compatible endpoint reachable by the API and worker. | Endpoint must be reachable by the API and worker; for the presigned download URL to open in a browser it must also be reachable from your machine |

### How API and worker differ

- Same image, same env vars, **different `BINARY` build arg** (`api` vs `worker`).
- **API** has public networking + a `/health` healthcheck and serves HTTP.
- **Worker** has **no** public domain and **no** healthcheck; it long-polls Redis
  for `generate_artifact` tasks, runs the provider call, and writes the image to S3.
- Only the **worker** actually talks to the image provider. Both must therefore
  share the same `IMAGE_PROVIDER` / `BFL_API_KEY` / S3 / Postgres / Redis config.

---

## 2. Required environment variables

Set these on **both** the API and worker services (the worker can omit
`APP_PORT` and `OPENAPI_DOCS_ENABLED`, but it is harmless to set them on both).
Railway also exposes each plugin's connection string as a variable you can
reference with `${{ Postgres.DATABASE_URL }}` style references.

| Variable | API | Worker | Notes |
|----------|:---:|:------:|-------|
| `APP_PORT` | ✅ | – | `8080`. Railway routes its public port to this. |
| `ENVIRONMENT` | ✅ | ✅ | `dev` for the smoke test (keeps `/docs` open + docs default on). Must match the seeded token's environment. |
| `LOG_LEVEL` | ✅ | ✅ | `info`. |
| `WORKER_CONCURRENCY` | ✅ | ✅ | Worker parallelism, e.g. `4`. Only the worker acts on it; safe on both. |
| `POSTGRES_DSN` | ✅ | ✅ | Tenant pool DSN. For the smoke test point it at the Railway Postgres superuser URL (superuser bypasses RLS). |
| `POSTGRES_SYSTEM_DSN` | ✅ | ✅ | System/BYPASSRLS pool. For the smoke test set it to the **same** value as `POSTGRES_DSN`. |
| `REDIS_ADDR` | ✅ | ✅ | `host:port` (no scheme). Derive from the Railway Redis private URL. |
| `REDIS_PASSWORD` | ✅ | ✅ | Railway Redis password. |
| `S3_BUCKET` | ✅ | ✅ | Bucket name. |
| `S3_REGION` | ✅ | ✅ | e.g. `us-east-1` / `auto` (R2). |
| `S3_ENDPOINT` | ✅ | ✅ | **Publicly reachable** S3 endpoint URL. |
| `S3_ACCESS_KEY_ID` | ✅ | ✅ | |
| `S3_SECRET_ACCESS_KEY` | ✅ | ✅ | |
| `S3_USE_PATH_STYLE` | ✅ | ✅ | `true` for MinIO/most non-AWS providers; `false` for AWS S3 virtual-hosted style. |
| `S3_PRESIGN_TTL` | ✅ | ✅ | Presigned read-URL lifetime, e.g. `15m`. Make it generous enough to click. |
| `IMAGE_PROVIDER` | ✅ | ✅ | **`bfl`** for the scene/artifact smoke test; **`fal`** to prefer the reference-conditioned provider for character packs. |
| `BFL_API_KEY` | ✅ | ✅ | Black Forest Labs API key. Required when `IMAGE_PROVIDER=bfl`. |
| `FAL_KEY` | ⚠️ | ⚠️ | fal.ai API key. Required when `IMAGE_PROVIDER=fal`; set it (on both services) to smoke-test recurring-character **pack** generation — see §9. |
| `API_TOKEN_PEPPER` | ✅ | ✅ | Must be **identical** on both services and on the seed-token run, or auth fails. |
| `OPENAPI_DOCS_ENABLED` | ✅ | – | `true` for the smoke test. |

> The `migrate` and `seed-token` one-off commands need `POSTGRES_DSN` (both) and
> `API_TOKEN_PEPPER` (seed-token only). When run via `railway run` these are
> injected automatically from the service variables.

### Railway build arg (`BINARY`)

The repo `Dockerfile` reads `ARG BINARY=api`. Railway exposes every service
variable as a Docker build argument, so set a service variable:

- API service: `BINARY=api`
- Worker service: `BINARY=worker`

Optionally point each service's **Config-as-code** path at the checked-in files:

- API service → `deploy/railway/api.json` (sets `/health` healthcheck)
- Worker service → `deploy/railway/worker.json`

---

## 3. Migration procedure (no Docker Compose, no psql)

A self-contained runner is built into the repo at `cmd/migrate`. It embeds
`migrations/0*.up.sql`, applies them in filename order, prints each filename,
and exits non-zero on the first error.

> **How `cmd/migrate` and `cmd/seed-token` are executed.** The
> `railway run --service api go run ./cmd/migrate` and `go run ./cmd/seed-token`
> commands are **local** commands: you run them from a repo checkout on a machine
> that has **Go installed**, with the Railway service environment variables
> injected (that is what `railway run --service <svc>` does). The deployed
> API/worker images do **not** contain Go and do **not** contain these commands —
> each deployed image contains only the single selected `BINARY` (`api` or
> `worker`). If you instead want to run migration or seeding **inside Railway** as
> a one-off service/container, configure a one-off build of the same `Dockerfile`
> with the build arg `BINARY=migrate` or `BINARY=seed-token` (the Dockerfile
> builds `./cmd/${BINARY}`), and run it once against the database.

Run it as a one-off against the Railway database. Easiest from a local checkout
with the Railway CLI (it injects the service env, including `POSTGRES_DSN`):

```bash
# from a repo checkout, linked to the Railway project + API service
railway run --service api go run ./cmd/migrate
```

Or run it anywhere with an explicit DSN (use the Railway Postgres **public** URL
if running off-platform):

```bash
POSTGRES_DSN='postgres://USER:PASS@HOST:PORT/DB?sslmode=require' go run ./cmd/migrate
```

Expected output:

```txt
applying 0001_initial.up.sql
applying 0002_seed_mock_provider.up.sql
...
applying 0010_webhooks.up.sql
migrate: applied 10 migration(s)
```

The seed migrations (`0002`, `0006`) load the mock **and** BFL provider
model/route/price rows the route resolver needs, so no extra provider setup is
required.

> `cmd/migrate` does not track applied migrations — it targets a **fresh**
> staging database. Re-running against an already-migrated DB fails fast on the
> first existing object; that is expected.

---

## 4. Staging token procedure (no Docker Compose, no psql)

A second one-off runner lives at `cmd/seed-token`. It inserts one row into
`api_tokens`, storing only `token_prefix` + `sha256(secret || API_TOKEN_PEPPER)`
(identical to the auth middleware), and prints the raw bearer value **once**.

```bash
railway run --service api go run ./cmd/seed-token
```

Or explicitly:

```bash
POSTGRES_DSN='postgres://USER:PASS@HOST:PORT/DB?sslmode=require' \
API_TOKEN_PEPPER='<same pepper as the services>' \
SEED_TOKEN_ENVIRONMENT=dev \
go run ./cmd/seed-token
```

It accepts (all optional):

| Env | Default | Meaning |
|-----|---------|---------|
| `SEED_TENANT_ID` | `tenant_dev` | Tenant the token belongs to. |
| `SEED_TOKEN_NAME` | `staging seed token` | Label stored on the row. |
| `SEED_TOKEN_SCOPES` | scope set below | Comma-separated override. |
| `SEED_TOKEN_PREFIX_KIND` | `dev` | `dev` or `admin`; sets the `dci_<kind>_` prefix and default scopes. |
| `SEED_TOKEN_ENVIRONMENT` | `ENVIRONMENT` else `dev` | Must equal the API's `ENVIRONMENT`. |

Default **normal** token scopes (enough for this smoke test):

```txt
images:read images:write styles:read styles:write jobs:read
```

`SEED_TOKEN_PREFIX_KIND=admin` switches the defaults to `admin:costs admin:jobs`
— not needed for the smoke test.

Save the printed `Authorization: Bearer dci_dev_..._...` value; it is shown once.

> This is **staging/dev ops tooling, not user management.** It creates a single
> bearer token for testing and nothing else.

---

## 5. Deploy order

1. **Postgres** — add the Railway Postgres plugin; note its connection URL.
2. **Redis** — add the Railway Redis plugin; note its host/port + password.
3. **S3-compatible storage** — create a Railway Storage Bucket (preferred,
   S3-compatible) or a bucket on an external S3-compatible provider (Cloudflare
   R2, Backblaze B2, AWS S3, public MinIO); note endpoint, region, bucket, access
   key, secret.
4. **API service** — new service from this GitHub repo, Dockerfile builder,
   `BINARY=api`, public networking on, healthcheck `/health`, all env vars from §2.
5. **Worker service** — new service from the same repo, Dockerfile builder,
   `BINARY=worker`, no public domain, same env vars (minus `APP_PORT`).
6. **Run migrations** — §3.
7. **Seed one token** — §4.

---

## 6. Exact smoke test

Set shell variables from your deploy:

```bash
export API="https://<your-api-service>.up.railway.app"
export TOKEN="dci_dev_xxxxxxxx_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"   # from §4
```

### 6.1 Health

```bash
curl -i "$API/health"
# -> HTTP/1.1 200 OK
# -> {"status":"ok"}
```

### 6.2 Create one style profile

```bash
curl -sS -X POST "$API/v1/styles" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "smoke-test style",
    "style_mode": "open_prompt",
    "positive_prompt": "a serene mountain lake at golden hour, highly detailed",
    "default_quality_tier": "standard"
  }'
# -> {"id":"style_...", ...}   # capture the id
export STYLE_ID="style_..."
```

### 6.3 Create one artifact generation job

`artifact_id` and `world_id` are caller-chosen identifiers.

```bash
curl -sS -X POST "$API/v1/artifacts/smoke-artifact-1/generate" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d "{
    \"world_id\": \"smoke-world-1\",
    \"style_profile_id\": \"$STYLE_ID\",
    \"description\": \"a serene mountain lake at golden hour\",
    \"quality_tier\": \"standard\"
  }"
# -> 202 Accepted
# -> {"job_id":"job_...","status":"queued", ...}   # capture the job_id
export JOB_ID="job_..."
```

Because `IMAGE_PROVIDER=bfl` is set, the route resolver prefers the BFL route and
the worker dispatches the job to the **real** BFL provider.

### 6.4 Poll job status until completed

```bash
curl -sS "$API/v1/jobs/$JOB_ID" \
  -H "Authorization: Bearer $TOKEN"
# -> {"id":"job_...","status":"queued|running|completed", ...}
```

Repeat until `status` is `completed` (BFL calls typically take a few–tens of
seconds). If it goes to `failed`, see §8.

### 6.5 Fetch job assets

```bash
curl -sS "$API/v1/jobs/$JOB_ID/assets" \
  -H "Authorization: Bearer $TOKEN"
```

The response is a `JobAssetsResponse`; each asset carries presigned download URLs:

```json
{
  "assets": [
    {
      "id": "asset_...",
      "status": "ready",
      "final_download_url": "https://<s3-endpoint>/<bucket>/...&X-Amz-Signature=...",
      "preview_download_url": "https://<s3-endpoint>/...",
      "thumbnail_download_url": "https://<s3-endpoint>/...",
      "url_expires_at": "2026-06-14T12:34:56Z"
    }
  ]
}
```

### 6.6 Open the image and confirm it is real

```bash
# print just the final download URL
curl -sS "$API/v1/jobs/$JOB_ID/assets" -H "Authorization: Bearer $TOKEN" \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["assets"][0]["final_download_url"])'
```

Open that URL in a browser (or `curl -o out.png "<url>"`).

**Confirm it is real, not a mock placeholder:**

- ✅ It is a photographic/generated rendering of the prompt (a mountain lake).
- ✅ `provider_id` on the asset is `bfl`, not `mock`.
- ❌ The mock provider emits a deterministic synthetic placeholder (flat
  generated test pattern, not prompt-driven imagery). If you see that, the worker
  is still running `IMAGE_PROVIDER=mock` — fix the worker's env and redeploy.

If the URL opens to a real picture of the prompt → **done.**

---

## 7. Rollback notes

This change set only **adds** deployment docs, two one-off commands
(`cmd/migrate`, `cmd/seed-token`), embedded migrations wiring, and Railway config
files. Nothing in the running API/worker code paths changes.

- **Code rollback:** revert the PR / redeploy the previous image. The API and
  worker binaries are unaffected by these additions.
- **Stop spend:** set the worker's `IMAGE_PROVIDER=mock` (or scale the worker to
  0 / remove `BFL_API_KEY`) to immediately stop real provider calls. The mock
  provider keeps the pipeline working without cost.
- **Database:** there are no destructive operations. The smoke test only inserts
  one tenant token, one style, one job, and one asset. To reset a throwaway
  staging DB, delete and re-add the Railway Postgres plugin, then re-run §3–§4.
- **Token revocation:** the seeded token can be revoked with
  `UPDATE api_tokens SET status='revoked' WHERE token_prefix='dci_dev_...';`
- **Tear down:** delete the Railway services/plugins; the container filesystem is
  ephemeral, so nothing persists beyond the database and S3 bucket you control.

---

## 8. If the job fails

- `status=failed` → check the **worker** logs in Railway.
- Common causes: wrong/missing `BFL_API_KEY`, `IMAGE_PROVIDER` not `bfl` on the
  worker, S3 credentials/endpoint wrong (worker can't upload), or Redis not
  reachable (job never picked up — stuck `queued`).
- Auth `401` on API calls → `API_TOKEN_PEPPER` mismatch between the seed-token run
  and the API service, or the token's `environment` ≠ the API's `ENVIRONMENT`.
- Presigned URL won't open → `S3_ENDPOINT` is not publicly reachable, or
  `S3_PRESIGN_TTL` already expired (re-fetch §6.5).

---

## 9. Recurring-character pack smoke test (fal / FLUX.1 Kontext)

This exercises the first **real reference-conditioned** path (ADR-017). It is
distinct from §6 (BFL scene/artifact): character packs require a provider that can
hold a recurring character from **reference images**.

Prerequisites, in addition to §2:

- `FAL_KEY` set on **both** API and worker; `IMAGE_PROVIDER=fal` (so the resolver
  prefers fal for `pack_capable`). Leave `ALLOW_SYNTHETIC_PROVIDERS=false`.
- A visual identity with at least one **anchor asset** (`anchor_asset_ids`). The
  worker presigns each anchor's high-res object and passes it to fal as
  `image_urls`. **An identity with no anchor assets fails the pack closed** with
  `missing_reference_assets` — that is the correct, designed behavior, not a bug.

Steps:

1. Create/seed a character visual identity in your tenant and attach anchor
   asset(s) (a previously generated/ingested portrait of the character).
2. `POST /v1/characters/{character_id}/generate-pack` with a `world_id` and
   `style_profile_id` (see `docs/api/jobs.md`). The route resolves to
   `route_fal_text_to_image_pack`.
3. Poll the job (§6.4) and fetch pack items; confirm the rendered roles are the
   **same character** in different poses/roles (consistency is the whole point).

Failure triage:

- `missing_reference_assets` → the identity has no anchor assets; add one. The
  worker never silently generates a different character.
- The fal route never selected (request fails closed / resolves mock) → `FAL_KEY`
  not set on the **worker/API**, or `IMAGE_PROVIDER` not `fal`.
- fal `provider_failure` → check worker logs for the fal status/result; a presign
  TTL shorter than the provider backlog can make `image_urls` 403 (raise
  `S3_PRESIGN_TTL`).
- **Stop spend:** unset `FAL_KEY` (or set `IMAGE_PROVIDER=mock` with
  `ALLOW_SYNTHETIC_PROVIDERS` left false → packs fail closed, no real calls).
