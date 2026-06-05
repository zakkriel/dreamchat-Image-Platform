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

## Generated OpenAPI Types

If using OpenAPI code generation, generated code should be isolated in a generated package and not manually edited.
