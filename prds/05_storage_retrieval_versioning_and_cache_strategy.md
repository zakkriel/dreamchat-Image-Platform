# PRD 05 — Storage, Retrieval, Versioning, and Cache Strategy

## 1. Purpose

This document defines how the DreamChat Image Platform stores, retrieves, versions, and reuses visual assets.

The core goal is to make visual assets persistent and reusable.

DreamChat should not regenerate images when a suitable asset already exists.

## 2. Core Principle

Retrieve before generate.

The service should always try to find an existing suitable asset before requesting a new generation from a provider/model.

The default flow should be:

1. exact asset match
2. reusable variant match
3. usable fallback asset
4. generate missing asset only if required

## 3. Why Storage and Retrieval Matter

Storage and retrieval are not just infrastructure details.

They are part of the product promise.

DreamChat is a persistent world. The visual layer should remember too.

Without storage and retrieval:

- characters become visually unstable
- places become visually unstable
- costs grow unnecessarily
- latency gets worse
- user trust decreases
- repeated generation makes the world feel inconsistent

## 4. Asset Storage Requirements

The service must store:

- original generated file
- low-res preview derivative
- high-res final derivative
- thumbnail derivative
- metadata
- generation job link
- visual identity link
- version link
- style profile link
- provider/model metadata
- prompt metadata
- quality/cost/latency metadata

## 5. Storage Backends

Initial storage can use S3-compatible object storage.

Recommended structure:

```text
/{tenant_id}/{world_id}/characters/{character_id}/{visual_identity_version}/{asset_role}/{asset_id}/preview.webp
/{tenant_id}/{world_id}/characters/{character_id}/{visual_identity_version}/{asset_role}/{asset_id}/final.webp
/{tenant_id}/{world_id}/places/{place_id}/{visual_identity_version}/{asset_role}/{asset_id}/preview.webp
/{tenant_id}/{world_id}/places/{place_id}/{visual_identity_version}/{asset_role}/{asset_id}/final.webp
/{tenant_id}/{world_id}/artifacts/{artifact_id}/{artifact_version}/{asset_role}/{asset_id}/preview.webp
/{tenant_id}/{world_id}/artifacts/{artifact_id}/{artifact_version}/{asset_role}/{asset_id}/final.webp
```

The exact path can change, but the service must maintain structured metadata in the database.

The object path alone should not be the source of truth.

## 6. Asset Metadata Model

Each asset should store:

```json
{
  "asset_id": "asset_001",
  "tenant_id": "tenant_abc",
  "world_id": "world_42",
  "entity_type": "character|place|artifact",
  "entity_id": "char_789",
  "visual_identity_id": "vis_char_123",
  "visual_identity_version": 1,
  "asset_role": "neutral_front_portrait",
  "asset_state": "active|archived|rejected|superseded",
  "style_profile_id": "style_001",
  "style_profile_version": 1,
  "quality_tier": "draft|standard|premium",
  "resolution_outputs": {
    "thumbnail": {"url": "...", "width": 256, "height": 256},
    "preview": {"url": "...", "width": 768, "height": 432},
    "final": {"url": "...", "width": 1920, "height": 1080}
  },
  "variant_tags": {
    "expression": "serious",
    "angle": "three_quarter",
    "time_of_day": null,
    "weather": null,
    "place_state": null
  },
  "generation": {
    "job_id": "job_123",
    "provider_id": "provider_x",
    "model_id": "model_y",
    "prompt_version": "prompt_v3",
    "seed": "seed_char_789_v1",
    "reference_asset_ids": ["asset_000"],
    "generated_at": "2026-06-05T00:00:00Z"
  },
  "metrics": {
    "preview_latency_ms": 5200,
    "final_latency_ms": 24000,
    "estimated_cost_usd": 0.02,
    "cache_hit": false
  }
}
```

## 7. Retrieval Dimensions

Assets should be retrievable by:

- tenant
- world
- entity type
- entity id
- visual identity id
- visual identity version
- asset role
- style profile
- expression
- angle
- place state
- time of day
- weather
- quality tier
- resolution tier
- asset state
- recency
- preferred/favorite marker

## 8. Retrieval Algorithm

### 8.1 Exact Match

An exact match uses:

- same entity id
- same visual identity version
- same style profile version
- same asset role
- same variant tags
- requested quality tier or better
- active asset state

If found, return it.

### 8.2 Variant Match

A variant match allows nearby assets.

Examples:

- requested serious expression but neutral is acceptable fallback
- requested night place view but establishing view can be used until night view is generated
- requested high-res but preview exists and can be returned first

### 8.3 Fallback Match

A fallback match is used when the web app needs something quickly.

Examples:

- profile icon crop from neutral portrait
- establishing view for location
- artifact placeholder if final is not ready

### 8.4 Generate Missing Asset

Generate only if:

- no suitable asset exists
- current asset is rejected
- identity version changed
- style profile changed
- user explicitly asks for regeneration
- asset is missing required resolution tier

## 9. Cache Strategy

### 9.1 Cache Keys

Cache keys should include:

- entity type
- entity id
- visual identity version
- style profile version
- asset role
- variant tags
- quality tier
- resolution tier

Example:

```text
character:char_789:v1:style_001_v2:expression_serious:three_quarter:standard:preview
```

### 9.2 Cache Hit Types

The service should track:

- `exact_cache_hit`
- `variant_cache_hit`
- `fallback_cache_hit`
- `miss_generated`
- `miss_queued`

### 9.3 Cache Logging

Every retrieval/generation decision should log:

- requested asset
- selected asset
- match type
- why generation was or was not needed
- cost avoided estimate if useful

## 10. Versioning Model

### 10.1 Visual Identity Version

Visual identity version changes when canonical visual identity changes.

Examples:

- character receives permanent scar
- character ages significantly
- character transforms
- place is destroyed
- place is rebuilt
- style migration intentionally re-anchors a world

### 10.2 Asset Version

Asset version changes when a specific asset is regenerated or superseded.

### 10.3 Artifact Version

Artifact version changes when the object itself changes.

Examples:

- letter is burned/torn/altered
- map receives new markings
- weapon is broken
- photograph is corrupted

## 11. Invalidation Rules

### 11.1 Identity Change

If character/place visual identity changes, new generation should use a new visual identity version.

Old assets remain accessible but should not be used by default.

### 11.2 Style Profile Change

If the world style profile changes, old assets can remain valid for history but new assets should use the new style profile version.

The product may later support batch restyling, but not initially.

### 11.3 Quality Rejection

If the user or system rejects an asset, mark it as `rejected`.

Rejected assets should not be selected for normal retrieval.

### 11.4 Superseded Assets

If an asset is replaced, mark the old one as `superseded` but keep it for audit/history.

## 12. Asset States

Recommended states:

- `active`
- `pending_final`
- `archived`
- `rejected`
- `superseded`
- `deleted_pending_retention`

## 13. Deletion and Retention

The service should support deletion policies, but initial implementation can be simple.

Important principles:

- respect user/world deletion
- delete or anonymize private assets when required
- avoid orphaned files
- keep audit logs as allowed/required
- separate public/shared assets from private world assets later

## 14. Retrieval API Behavior

The retrieval endpoints should allow `allow_fallback=true`.

Example:

```http
GET /v1/characters/char_789/assets?asset_role=expression_serious&resolution=preview&allow_fallback=true
```

Possible response:

```json
{
  "match_type": "variant_cache_hit",
  "requested": {
    "asset_role": "expression_serious",
    "resolution": "preview"
  },
  "returned_asset": {
    "asset_id": "asset_001",
    "asset_role": "neutral_front_portrait",
    "url": "https://..."
  },
  "generation_recommended": true,
  "generation_request_hint": {
    "endpoint": "/v1/characters/generate-pack",
    "missing_roles": ["expression_serious"]
  }
}
```

## 15. Acceptance Criteria

This PRD is implemented when:

- assets are stored with structured metadata
- preview/final/thumbnail outputs are supported
- assets can be retrieved by character/place/artifact id
- cache match type is reported
- retrieval occurs before generation
- rejected/superseded assets are not selected by default
- visual identity versions control asset reuse
- style profile versions control asset reuse
- asset metadata includes provider/model/prompt/seed/job details
- storage paths are not the only source of truth

---

## Confidence to Implement

**Score: 85/100 — High**

Storage layout, asset metadata schema, the retrieval algorithm (exact → variant → fallback → generate), cache hit types, asset states, and invalidation rules are all spelled out concretely enough to translate into repository code and SQL. The data model maps cleanly to the `visual_assets` table in `docs/db/initial_schema.sql`. Subtracting a few points because "variant match" semantics ("serious expression but neutral is acceptable fallback") imply a *compatibility matrix* between variant tags that isn't given — an implementer has to invent or extract it from product judgment. Also, the example `AssetQueryResponse` with `generation_recommended` + `generation_request_hint` is a nice idea but the docs spec (`docs/api/openapi.yaml`) doesn't expose it — another reconciliation gap.

