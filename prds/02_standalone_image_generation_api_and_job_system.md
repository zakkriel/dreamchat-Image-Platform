# PRD 02 — Standalone Image Generation API and Job System

## 1. Purpose

This document defines the standalone API and job system for the DreamChat Image Platform.

The Image Platform must work independently from the DreamChat web app.

A developer should be able to call the Image Platform directly, generate assets, inspect jobs, retrieve outputs, benchmark providers, and validate consistency without launching the full DreamChat product.

## 2. Product Principle

The service should be API-first.

The web app should never call an image provider directly.

The web app should call DreamChat Image Platform endpoints, and the Image Platform should decide:

- whether to retrieve an existing asset
- whether to generate a new asset
- which backend/model/provider to use
- whether to produce preview only or preview + final
- how to store metadata
- how to version the output
- whether the request should be rejected, delayed, retried, or downgraded

## 3. Core Responsibilities

The API service is responsible for:

- accepting generation requests
- validating request schemas
- resolving style profiles
- checking existing assets before generation
- creating jobs
- routing work to image providers/backends
- storing preview and final images
- storing metadata
- exposing job status
- exposing asset retrieval
- tracking cost, latency, errors, and cache hits
- supporting backend/provider abstraction
- supporting future self-hosted models

## 4. Service Boundaries

### 4.1 The Image Platform Owns

- image-generation jobs
- image provider routing
- visual identity data
- asset metadata
- asset storage references
- cache lookup
- preview/final derivatives
- variant pack generation
- style profile interpretation
- consistency metadata
- cost/latency logging

### 4.2 The DreamChat Core App Owns

- world state
- scene state
- canonical facts
- character narrative identity
- location narrative identity
- knowledge boundaries
- user permissions
- play-mode vs creator-mode visibility
- when a character/place/artifact becomes important enough to request visual assets

### 4.3 Shared Contract

The DreamChat core app sends only the visual context that is safe and useful for generation.

The Image Platform should not require omniscient world-state access to generate normal play-mode assets.

The core app should decide what is known/perceived/visible.

## 5. API Design Principles

The API should be:

- REST-compatible for easy testing
- async by default for generation jobs
- idempotent where possible
- cache-aware
- provider-agnostic
- style-profile driven
- metadata-rich
- secure by tenant/world
- observable
- usable from CLI/Postman/scripts

## 6. Core Endpoints

### 6.1 Health and Metadata

#### `GET /v1/health`

Returns service status.

#### `GET /v1/models`

Returns available model/provider capabilities as exposed through the Image Platform abstraction.

The response should not expose secrets or raw provider credentials.

#### `GET /v1/capabilities`

Returns supported feature flags:

- text-to-image
- image-to-image
- reference-image conditioning
- multi-reference generation
- low-res preview
- high-res final
- LoRA/adapters
- inpainting/editing
- batch generation
- provider fallback

### 6.2 Character Generation

#### `POST /v1/characters/generate-pack`

Creates or extends a character visual asset pack.

Used when a character becomes important enough to need persistent visuals.

Request includes:

- `world_id`
- `character_id`
- `visual_identity_id` optional
- `character_visual_profile`
- `style_profile`
- `pack_template`
- `quality_tier`
- `latency_tier`
- `generate_preview`
- `generate_final`
- `idempotency_key`

Returns:

- `job_id`
- `status`
- `estimated_preview_eta_ms`
- `estimated_final_eta_ms`

### 6.3 Place Generation

#### `POST /v1/places/generate-pack`

Creates or extends a place/location visual asset pack.

Used when a place becomes important enough to need persistent visuals.

Request includes:

- `world_id`
- `place_id`
- `visual_identity_id` optional
- `place_visual_profile`
- `style_profile`
- `pack_template`
- `quality_tier`
- `latency_tier`
- `generate_preview`
- `generate_final`
- `idempotency_key`

Returns:

- `job_id`
- `status`
- `estimated_preview_eta_ms`
- `estimated_final_eta_ms`

### 6.4 Artifact Generation

#### `POST /v1/artifacts/generate`

Generates a visual for an artifact/context object.

Artifacts usually do not need as strong identity consistency as characters and places, but they still need versioning.

Request includes:

- `world_id`
- `artifact_id`
- `artifact_visual_profile`
- `style_profile`
- `variant_type`
- `quality_tier`
- `latency_tier`
- `generate_preview`
- `generate_final`
- `idempotency_key`

### 6.5 Regeneration

#### `POST /v1/images/regenerate`

Regenerates an asset or variant deliberately.

Regeneration should require a reason.

Valid reasons:

- `quality_failure`
- `style_change`
- `identity_drift`
- `canonical_visual_change`
- `user_requested`
- `provider_failure`
- `new_version_required`

The response should link the new asset to the prior asset/version.

### 6.6 Job Status

#### `GET /v1/jobs/{job_id}`

Returns:

- job status
- requested outputs
- completed outputs
- preview assets
- final assets
- errors/warnings
- provider attempts
- timing metadata
- cost metadata

Job statuses:

- `queued`
- `planning`
- `retrieving_existing_assets`
- `generating_preview`
- `preview_ready`
- `generating_final`
- `final_ready`
- `completed`
- `completed_with_warnings`
- `failed`
- `cancelled`

### 6.7 Asset Retrieval

#### `GET /v1/assets/{asset_id}`

Returns asset metadata and URLs.

#### `GET /v1/characters/{character_id}/assets`

Returns character assets filtered by:

- style profile
- expression
- angle
- version
- quality tier
- asset state

#### `GET /v1/places/{place_id}/assets`

Returns place assets filtered by:

- style profile
- time of day
- state
- weather
- angle/view
- version
- quality tier

#### `GET /v1/artifacts/{artifact_id}/assets`

Returns artifact assets filtered by:

- style profile
- version
- state
- quality tier

### 6.8 Style Preview

#### `POST /v1/styles/preview`

Generates a small preview for a style profile.

Used to test style prompts/presets before using them on important assets.

### 6.9 Style Validation

#### `POST /v1/styles/validate`

Validates whether a style profile is structurally valid and supported by the selected backend capabilities.

## 7. Request Model

All generation requests should include a common envelope.

```json
{
  "request_id": "req_123",
  "tenant_id": "tenant_abc",
  "world_id": "world_42",
  "requested_by": {
    "user_id": "user_7",
    "source": "dreamchat_web_app|admin_tool|benchmark_runner|api_client"
  },
  "visibility_mode": "play|creator|debug",
  "known_world_scope": "perceived|public|creator_authoritative",
  "style_profile": {},
  "quality_tier": "draft|standard|premium",
  "latency_tier": "fast|balanced|quality",
  "generate_preview": true,
  "generate_final": true,
  "idempotency_key": "stable-key"
}
```

## 8. Response Model

Generation endpoints return jobs, not direct final images.

```json
{
  "job_id": "job_123",
  "status": "queued",
  "entity_type": "character",
  "entity_id": "char_789",
  "requested_outputs": ["preview", "final"],
  "estimated_preview_eta_ms": 5000,
  "estimated_final_eta_ms": 25000,
  "links": {
    "job": "/v1/jobs/job_123"
  }
}
```

When preview or final assets are ready, job status returns asset references.

```json
{
  "job_id": "job_123",
  "status": "preview_ready",
  "assets": [
    {
      "asset_id": "asset_001",
      "asset_role": "neutral_front_portrait",
      "resolution_tier": "preview",
      "url": "https://...",
      "metadata": {}
    }
  ]
}
```

## 9. Job Lifecycle

### 9.1 Standard Flow

1. Validate request.
2. Resolve style profile.
3. Resolve or create visual identity.
4. Check existing assets.
5. Reuse assets where possible.
6. Create generation plan for missing assets.
7. Queue preview generation.
8. Store preview assets.
9. Return preview-ready status.
10. Queue final generation if requested.
11. Store final assets.
12. Mark job complete.
13. Log cost, latency, provider, warnings, and errors.

### 9.2 Retrieval-Only Flow

If the requested assets already exist and match, the job should complete without provider generation.

Status can be:

- `completed`
- `cache_hit: true`
- `generation_performed: false`

### 9.3 Partial Success

If some assets succeed and others fail, the job may end as:

- `completed_with_warnings`

The web app should still be able to use successful outputs.

## 10. Provider Abstraction

The API should define internal provider capability metadata.

Examples:

```json
{
  "provider_id": "provider_x",
  "backend_type": "external_api|self_hosted|local_dev",
  "capabilities": {
    "text_to_image": true,
    "image_to_image": true,
    "multi_reference": true,
    "lora": false,
    "fast_preview": true,
    "high_res_final": true
  },
  "supported_aspect_ratios": ["1:1", "4:3", "16:9", "9:16"],
  "max_batch_size": 8,
  "estimated_cost_class": "low|medium|high",
  "estimated_latency_class": "fast|medium|slow"
}
```

Provider details should be logged internally but not leak unnecessarily to end users.

## 11. Security and Permissions

The service must enforce:

- tenant isolation
- world isolation
- asset permissions
- signed URLs or equivalent access control
- internal service authentication
- idempotency controls
- rate limits
- abuse controls

The service should never allow a user to retrieve another user’s private world assets without permission.

## 12. Acceptance Criteria

The PRD is implemented when:

- the service can run independently from the web app
- developers can call generation endpoints directly
- jobs are async and inspectable
- preview and final outputs are supported
- character/place/artifact asset types are supported
- asset retrieval endpoints work
- provider/model details are abstracted
- job status includes useful metadata
- cost and latency are logged
- cache hit vs generation is visible
- API contracts can be tested in isolation

---

## Confidence to Implement

**Score: 82/100 — High**

The API surface, request/response envelopes, job lifecycle, and provider-capability metadata are all concrete enough to translate to handlers, services, and DB tables. Async-job + retrieval-only + partial-success flows are standard patterns. Two sources of friction lower the score: (1) this PRD's endpoint shapes diverge from `docs/api/openapi.yaml` (entity_id in body vs. in path; richer status enum; `tenant_id` vs. world-only) — see `frustration_log.md` entry 6 — so an implementer has to pick a winner first; (2) "decide which backend to use" + cost/latency tiers imply a router whose policies aren't fully specified (thresholds, fallback rules, downgrade behavior). The latter is implementable as a stub with explicit TODOs.

