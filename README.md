# DreamChat Image Platform

Standalone Go service that generates, stores, retrieves, versions, and serves
persistent images for DreamChat worlds.

Implementation status: the Phase 0–7 track is complete (auth, identities,
styles, async generation, cost pre-flight, packs, retrieval-before-generation,
delivery, provider routing, rate limits, RLS, webhooks — see
`IMPLEMENTATION_STATUS.md`, the single source of truth for sequencing), plus
the combined governance/cost contract `POST /v1/generations` (ADR-P002) and
the cost-optimization waves (`docs/superpowers/plans/2026-08-07-cost-optimization-waves.md`).

## Authoritative docs

- `DECISIONS.md` — locked stack, env vars, provider interface, deferrals.
- `docs/api/openapi.yaml` — canonical API contract (version in the file's
  `info.version`; `api/openapi.yaml` is a byte-identical mirror enforced by CI).
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
make dev
curl -i http://localhost:8080/health
```

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
(goose), asserting the 20 baseline tables + `goose_db_version` exist and the
schema is at version 11.

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
