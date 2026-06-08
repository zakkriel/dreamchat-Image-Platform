# DreamChat Image Platform

Standalone Go service that generates, stores, retrieves, versions, and serves
persistent images for DreamChat worlds.

This repository is Phase 0: the walking skeleton — project layout, local dev
loop, and CI. Auth, OpenAPI handlers, codegen wiring, and business logic land
in later phases.

## Authoritative docs

- `DECISIONS.md` — locked stack, env vars, provider interface, deferrals.
- `docs/api/openapi.yaml` — canonical API contract (v0.5.0).
- `docs/db/initial_schema.sql` — DB schema (mirrored to `migrations/0001_initial.up.sql`).
- `docs/architecture/` — overview + component boundaries.

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
and applies the migration to a throwaway Postgres asserting the 17 tables
exist.

## Provider adapters

`internal/providers/` implements the `ImageProvider` interface from
`DECISIONS.md`:

- `mock/` — deterministic placeholder bytes, works without provider keys.
- `bfl/` — skeleton: `Capabilities()` only; other methods return
  `providers.ErrNotImplemented`. Selected via `IMAGE_PROVIDER=bfl`; missing
  `BFL_API_KEY` fails fast at config load.
