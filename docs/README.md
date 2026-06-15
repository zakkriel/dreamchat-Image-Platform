# DreamChat Image Platform — Technical Documentation Package

## Purpose

This package defines the technical architecture, ADRs, API contract, security model, patterns, and implementation guidelines for the DreamChat Image Platform.

The Image Platform is a standalone API service that generates, stores, retrieves, versions, and serves persistent visual assets for DreamChat worlds.

It supports:

- Character visual consistency
- Place visual consistency
- Asset packs for reusable variants
- Preview-first / high-resolution delivery
- Retrieval before regeneration
- Provider abstraction
- Bearer-token authentication
- OpenAPI / Swagger documentation
- Cost, latency, and quality telemetry

## Core Technical Decision

The first implementation should be built as a Go service.

Recommended stack:

```txt
Go API + Go workers
Postgres for metadata and source of truth
Redis for queues, short-lived cache, and rate limiting
S3-compatible object storage for image files
OpenAPI-first API contract
Swagger UI / Redoc documentation
Provider adapters for external image models
```

## What This Is Not

This is not the main DreamChat world engine.

This service should not own:

- Canonical story state
- NPC memory
- Backstage simulation
- Relationship logic
- In-world time progression
- Narration decisions

It owns visual asset generation and visual asset lifecycle only.

## Folder Structure

```txt
/api
  openapi.yaml
  authentication.md
  errors.md
  idempotency.md
  rate-limits.md
  jobs.md
  assets.md
  styles.md
  models.md

/architecture
  overview.md
  component-boundaries.md
  data-model.md
  job-lifecycle.md
  provider-adapters.md
  asset-versioning.md
  security-and-auth.md
  observability.md

/adr
  Architecture decision records.

/db
  initial_schema.sql

/guidelines
  go-service-guidelines.md
  implementation-guidelines.md
  testing-strategy.md
  documentation-guidelines.md

/runbooks
  local-development.md
  provider-failure.md
  provider-capability-misconfiguration.md
  failed-jobs.md
  token-rotation.md
  cost-spike.md

/schemas
  visual_identity.schema.json
  visual_asset.schema.json
  generation_job.schema.json
  style_profile.schema.json

superpowers_implementation_prompt.md
```

## Implementation Order

1. Implement OpenAPI-first skeleton.
2. Add bearer-token authentication.
3. Add Postgres schema and repositories.
4. Add async generation jobs.
5. Add mock image provider.
6. Add S3-compatible storage abstraction.
7. Add asset retrieval/search.
8. Add one real provider adapter.
9. Add Swagger docs and examples.
10. Add telemetry, cost events, request IDs, and rate limits.

## Strong Rule

Do not start from the model provider.

Start from the platform contract:

```txt
visual identity
asset pack
variant
version
job
provider adapter
storage
retrieval
telemetry
```

---

## Confidence to Implement

**Score: 95/100 — Very High**

This is the table-of-contents for the `docs/` tree and the project's source of truth for stack + folder layout + implementation order. The 10-step implementation order is sequenceable and well scoped. Confidence is high because every step it references has a backing spec in this directory.

