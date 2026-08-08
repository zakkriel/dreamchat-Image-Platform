# DreamChat Image Platform

Standalone Go service that generates, stores, retrieves, versions, and serves
persistent images for DreamChat worlds.

Implementation status: the Phase 0–7 track is complete (auth, identities,
styles, async generation, cost pre-flight, packs, retrieval-before-generation,
delivery, provider routing, rate limits, RLS, webhooks — see
`IMPLEMENTATION_STATUS.md`, the single source of truth for sequencing), plus
the combined governance/cost contract `POST /v1/generations` (ADR-P002) and
the cost-optimization waves (`docs/superpowers/plans/2026-08-07-cost-optimization-waves.md`).

Integration status: the `dreamchat-world-backend` service is a **live consumer**
through the **pull** contract (`POST /v1/generations` → poll `GET /v1/jobs/{id}` →
`GET /v1/jobs/{id}/assets`), and the frontend renders portraits end to end through
it. Outbound webhooks are deliberately **not** on that path — they are a future
latency hint, never the readiness mechanism. See "Integration state" and
"Before production (integration track)" in `IMPLEMENTATION_STATUS.md`, and
`docs/api/integration-quickstart.md` to integrate.

## Authoritative docs

- `DECISIONS.md` — locked stack, env vars, provider interface, deferrals.
- `docs/api/openapi.yaml` — canonical API contract (version in the file's
  `info.version`; `api/openapi.yaml` is a byte-identical mirror enforced by CI).
- `docs/api/integration-quickstart.md` — **start here to integrate**: the verified
  end-to-end call sequence (token → style → identity → generation → poll → assets),
  the governance envelope, required polling backoff, and the config prerequisite
  that otherwise makes every generation return `422`.
- `IMPLEMENTATION_STATUS.md` — what's done / what's next.
- `docs/architecture/` — overview + component boundaries.
- `migrations/` — goose migrations (`go run ./cmd/migrate up`).

## Layout

```
/cmd/api               # HTTP API entrypoint
/cmd/worker            # asynq worker entrypoint
/internal/auth         # bearer-token auth (Phase 1)
/internal/assets       # asset retrieval and storage metadata (Phase 4+)
/internal/identities   # visual identity service (Phase 2)
/internal/jobs         # generation job service (Phase 3)
/internal/providers    # provider adapters (mock + bfl skeleton)
/internal/styles       # style profile service
/internal/storage      # S3-compatible object storage client
/internal/telemetry    # logger + request_id plumbing
/internal/db           # sqlc-generated queries
/internal/http         # router + middleware
/internal/config       # env-var config loader
/api/openapi.yaml      # mirror of docs/api/openapi.yaml
/migrations            # SQL migrations
```

## Dev loop

```bash
make start
```

One command, no manual steps: infra containers, migrations, dev tokens, the API
and worker (as host `go run` processes, so a code change costs a ~5s restart
instead of an image rebuild), the playground with its tokens pre-filled, and a
browser tab. Ctrl-C stops everything it started. Warm restart is ~10s.

`make dev` is the all-in-docker variant: same stack, but the API and worker run
as containers you must rebuild after every Go change.

```bash
curl -i http://localhost:8081/health
```

### Published host ports

`docker-compose.yml` publishes **Postgres on host `5433`** (container port stays
`5432`) and the **API on host `8081`** (container port stays `8080`). The sibling
`dreamchat-world-backend` stack owns host `5432` and host `8080` in the shared dev
environment, and both must be able to run concurrently. `make migrate` and
`.env.example` default to `localhost:5433` to match, and `playground/.env.example`
proxies to `localhost:8081`. Redis (`6379`) and MinIO (`9000`/`9001`) are still
published on their default host ports — remap them the same way if a sibling stack
ever claims one.

Expected:

- `HTTP/1.1 200 OK`
- Body `{"status":"ok"}`
- Header `X-Request-Id: <uuid>`
- One structured INFO log line per request with `request_id`, `method`, `path`,
  `status`, `duration_ms`.

`make seed` prints one `dci_dev_*` token to stdout once — never logged again
and never stored in raw form.

## Tests, lint, CI

```bash
make test
go vet ./...
golangci-lint run
```

CI runs `go vet`, `go build`, `go test`, `openapi-spec-validator`, `sqlc vet`,
and applies migrations to a throwaway Postgres via `go run ./cmd/migrate up`
(goose), asserting the expected tables + `goose_db_version` exist and the schema
is at the migration head. Both numbers are asserted explicitly in
`.github/workflows/ci.yml` — currently **24 base tables** (21 baseline objects
plus the three Chunk 1 tables) at **migration head 19** — and CI also proves the
round-trip `up → down-to 11 → up` and that stepping below the baseline is
refused.

## Provider adapters

`internal/providers/` implements the `ImageProvider` interface from
`DECISIONS.md`:

- `mock/` — deterministic placeholder bytes, works without provider keys.
  Synthetic: it backs identity/pack routes only with
  `ALLOW_SYNTHETIC_PROVIDERS=true` (default false everywhere).
- `bfl/` — real Black Forest Labs adapter (`flux-pro-1.1`, scene/artifact
  work; prompt-only, so not identity-capable). Selected via
  `IMAGE_PROVIDER=bfl`; requires `BFL_API_KEY`.
- `fal/` — real reference-conditioned fal.ai adapter (FLUX.1 Kontext multi;
  identity/pack-capable, requires identity anchor references). Registered
  when `FAL_KEY` is set.
