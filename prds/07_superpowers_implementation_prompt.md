# Superpowers Implementation Prompt — DreamChat Image Platform

You are implementing the DreamChat Image Platform as a standalone API-first service.

Read all PRDs in this package before building.

## Product context

DreamChat is a persistent AI RPG world product.

The core product is not image generation. The core product is persistent world state, dynamic NPCs, memory, relationships, in-world time, backstage updates, narration, and continuity.

The Image Platform is a supporting visual continuity layer.

It must generate, store, retrieve, version, and serve reusable image assets for characters, places, and artifacts.

The web app should be one client of this service, not the place where image logic lives.

## Main build goal

Build a standalone API service that can be tested independently from the DreamChat web app.

The service must support:

- character visual identity
- place visual identity
- artifact/context image generation
- character asset packs
- place asset packs
- async generation jobs
- preview image first
- high-res/final image later
- asset storage
- asset retrieval
- retrieval-before-generation logic
- style profiles
- provider/model abstraction
- metadata logging
- cost and latency telemetry

## Important product requirements

### 1. Character consistency

Once a character is visually created, future generated images should keep the character identifiable.

Do not rely only on raw prompts.

Persist:

- canonical visual traits
- visual identity version
- style profile
- anchor assets
- reference assets
- seed/consistency key
- provider/model metadata

### 2. Place consistency

Once a place is visually created, future generated images should keep the place identifiable.

Persist:

- location type
- architecture/layout cues
- recurring landmarks
- palette/mood
- lighting defaults
- visual identity version
- anchor assets
- state variants

### 3. Fast preview + final quality

Generation should return a fast low-res/preview image first.

High-res/final generation should happen async and update the job/asset when ready.

The web app must not be blocked by final generation.

### 4. Asset packs

When an important character or place is created, generate a small reusable pack.

Character starter pack:

- neutral front portrait
- neutral 3/4 portrait
- side angle portrait
- warm/smiling expression
- serious/tense expression
- angry/defensive expression
- surprised/shocked expression

Place starter pack:

- establishing wide view
- closer atmospheric view
- day view
- night view
- calm/empty view
- busy/active view

### 5. Storage and retrieval

The platform must store generated images and retrieve them later.

Before generating, always check:

1. exact asset match
2. variant match
3. fallback match
4. generate only if needed

### 6. Style flexibility

Do not hardcode one DreamChat visual style.

Support:

- open style prompt
- preset style profile
- future LoRA/adapters/style packs
- style profile versioning

### 7. Provider abstraction

Do not hardcode provider-specific logic into the API contract.

Create provider adapters behind an internal abstraction.

Initial implementation may use a mock provider or one real provider, but the interface must support future providers and self-hosted models.

## Recommended implementation stack

Use a stack suitable for a standalone service.

Suggested default:

- TypeScript/Node or Python/FastAPI
- Postgres for metadata
- S3-compatible storage for assets
- Redis/queue for async jobs if needed
- OpenAPI contract
- local filesystem storage allowed for first dev prototype, but keep storage abstraction clean

## Required endpoints

Implement or stub these endpoints:

- `GET /v1/health`
- `GET /v1/models`
- `GET /v1/capabilities`
- `POST /v1/characters/generate-pack`
- `POST /v1/places/generate-pack`
- `POST /v1/artifacts/generate`
- `POST /v1/images/regenerate`
- `GET /v1/jobs/{job_id}`
- `GET /v1/assets/{asset_id}`
- `GET /v1/characters/{character_id}/assets`
- `GET /v1/places/{place_id}/assets`
- `GET /v1/artifacts/{artifact_id}/assets`
- `POST /v1/styles/preview`
- `POST /v1/styles/validate`

## Required data entities

Implement data structures or database tables for:

- generation job
- visual identity
- style profile
- asset
- asset pack
- provider attempt
- generation metrics

## Build phases

### Phase 1 — Skeleton

Build the service with:

- health endpoint
- models/capabilities endpoint
- in-memory or DB-backed jobs
- mock image provider
- local file/object storage abstraction

### Phase 2 — Asset metadata

Add:

- asset model
- visual identity model
- style profile model
- job metadata
- preview/final fields

### Phase 3 — Generation endpoints

Add:

- character pack generation
- place pack generation
- artifact generation
- preview/final simulation or real provider calls

### Phase 4 — Retrieval-before-generation

Add:

- exact match
- variant match
- fallback match
- cache status metadata
- generation skipped when asset exists

### Phase 5 — Benchmark runner

Add a simple script or endpoint that runs a benchmark corpus and records:

- latency
- cost estimate
- provider/model
- output ids
- failures

### Phase 6 — Web app ready

Make sure the web app can:

- request a pack
- poll job status
- retrieve preview asset
- update to final asset
- request best existing asset for character/place/artifact

## Important non-goals

Do not implement in the first version:

- video generation
- animation
- marketplace
- public creator image economy
- end-user model training
- complex moderation console
- full LoRA training UI
- every-message generation

## Acceptance test examples

### Character consistency test

1. Create character visual identity.
2. Generate starter pack.
3. Request a serious expression later.
4. Confirm it uses same visual identity/version and reference/seed metadata.
5. Confirm the new image is linked to the same character and remains retrievable.

### Place consistency test

1. Create place visual identity.
2. Generate starter pack.
3. Request night version later.
4. Confirm it uses same place identity/version.
5. Confirm landmarks and architectural cues remain stable.

### Cache test

1. Generate character neutral portrait.
2. Request same asset again.
3. Confirm no new provider generation happens.
4. Response should report exact cache hit.

### Preview/final test

1. Request character pack.
2. Job returns queued.
3. Preview assets become available first.
4. Final assets become available later.
5. Same job tracks both states.

## Final instruction

Keep the Image Platform independent.

Do not mix DreamChat world-state logic into this service.

This service should only manage persistent visual assets, generation jobs, provider routing, style profiles, consistency metadata, storage, retrieval, and performance/cost telemetry.

---

## Confidence to Implement

**Score: 85/100 — High**

This is a meta-prompt that summarizes PRDs 01–06 into actionable build phases (skeleton → asset metadata → generation endpoints → retrieval-before-generation → benchmark runner → web-app-ready) with concrete acceptance tests (character consistency, place consistency, cache test, preview/final test). The build sequence is well ordered. Note: this PRD suggests "TypeScript/Node or Python/FastAPI" as the default stack, while the in-repo `docs/superpowers_implementation_prompt.md` and ADR-002 explicitly choose **Go**. An implementer should treat the docs/ADR choice as authoritative — see `frustration_log.md` entry 6 for related drift.

