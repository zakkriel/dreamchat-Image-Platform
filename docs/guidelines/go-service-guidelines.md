# Go Service Guidelines

## Project Shape

Recommended structure:

```txt
/cmd/api
/cmd/worker
/internal/auth
/internal/assets
/internal/identities
/internal/jobs
/internal/providers
/internal/styles
/internal/storage
/internal/telemetry
/internal/db
/internal/http
/api/openapi.yaml
/docs
```

## Dependency Direction

Handlers call services.
Services call repositories and adapters.
Adapters do not call services.
Repositories do not call HTTP handlers.

```txt
HTTP -> Service -> Repository / Provider / Storage
```

## Context

Every public method should accept `context.Context`.

## Error Handling

Use typed domain errors and map them to API problem details at the HTTP boundary.

## Configuration

Use environment variables for:

```txt
DATABASE_URL
REDIS_URL
S3_ENDPOINT
S3_BUCKET
S3_ACCESS_KEY_ID
S3_SECRET_ACCESS_KEY
PROVIDER_*_API_KEY
TOKEN_PEPPER
```

## Secrets

Never commit secrets.
Never log secrets.
Never log raw bearer tokens.

## Migrations

Use explicit SQL migrations.
Adding a column? Update the matching explicit query `RETURNING`/`SELECT` lists — this repo lists columns explicitly, not `*`, so sqlc otherwise emits a per-query `*Row` type and the build breaks.

## Generated OpenAPI Types

If using OpenAPI code generation, generated code should be isolated in a generated package and not manually edited.

---

## Confidence to Implement

**Score: 92/100 — Very High**

Standard idiomatic Go layout (cmd/+internal/), correct dependency direction (HTTP→Service→Repo/Provider), `context.Context` everywhere, env-var config, typed domain errors mapped at the HTTP boundary, explicit migrations. Nothing here is unusual. Only minor uncertainty is whether to use `sqlc` vs. hand-written queries vs. an ORM — choosable.
