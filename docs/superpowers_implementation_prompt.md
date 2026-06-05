# Superpowers Implementation Prompt — DreamChat Image Platform Tech Foundation

Build the first technical foundation for the DreamChat Image Platform as a standalone Go API service.

## Product Context

This service is not the full DreamChat app.

It is a standalone visual asset platform that generates, stores, retrieves, versions, and serves persistent images for DreamChat characters, places, and artifacts.

Core requirements:

- Character consistency
- Place consistency
- Visual identity records
- Asset packs and variants
- Preview-first / high-res final delivery
- Retrieval before generation
- Provider abstraction
- OpenAPI / Swagger docs
- Bearer token authentication with scopes
- Postgres metadata
- Redis queue/cache/rate-limits
- S3-compatible asset storage
- Cost and latency telemetry

## Tech Decisions

Use:

```txt
Go API
Go worker
Postgres
Redis
S3-compatible storage
OpenAPI-first contract
Swagger UI / Redoc docs
Mock image provider first
One real provider adapter later
```

Do not use:

```txt
Node for this service
Python for MVP
LangGraph for MVP
microservices for MVP
provider-specific logic in handlers
raw prompt-to-image integration in the web app
```

## Required Deliverables

1. Go service skeleton.
2. `/health` endpoint.
3. `/docs` Swagger page.
4. `/openapi.json` or served OpenAPI spec.
5. Bearer token middleware.
6. Scope checking.
7. Token hash storage pattern.
8. Postgres schema/migrations.
9. Redis-backed job queue.
10. Mock provider adapter.
11. Visual identity CRUD for characters/places.
12. Generate character pack endpoint.
13. Generate place pack endpoint.
14. Asset search endpoint.
15. Job status endpoint.
16. Structured error responses.
17. Request IDs.
18. Structured logs.
19. Basic telemetry events.
20. Tests for auth, job lifecycle, and mock provider.

## API Contract

Use `/api/openapi.yaml` as the source of truth.

Implement at least these endpoints:

```txt
GET /health
GET /docs
GET /openapi.json

POST /v1/characters/{character_id}/visual-identity
GET  /v1/characters/{character_id}/visual-identity
POST /v1/characters/{character_id}/generate-pack

POST /v1/places/{place_id}/visual-identity
GET  /v1/places/{place_id}/visual-identity
POST /v1/places/{place_id}/generate-pack

POST /v1/assets/search
GET  /v1/assets/{asset_id}
GET  /v1/jobs/{job_id}
GET  /v1/styles
GET  /v1/models
```

## Implementation Rules

- Use async jobs for generation.
- Do not block generation endpoints waiting for final images.
- Always check existing assets before creating generation work.
- Store all metadata in Postgres.
- Store image files in S3-compatible storage.
- Store only hashed API tokens.
- Never log raw bearer tokens.
- Use consistent problem-details errors.
- All generated assets must include provider/model/prompt hash/seed/variant/version metadata.
- Use mock provider in local and tests.

## First Demo Success Criteria

Using only the API and Swagger docs, a developer can:

1. Authenticate with a dev bearer token.
2. Create a character visual identity.
3. Generate a character pack with the mock provider.
4. Poll job status until completed.
5. Retrieve stored assets.
6. Create a place visual identity.
7. Generate a place pack.
8. Search assets by owner ID and variant.
9. See request IDs and structured logs.
10. See OpenAPI docs in browser.
