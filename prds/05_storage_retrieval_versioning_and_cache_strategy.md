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

The deterministic rules backing this section live in
`docs/architecture/variant-compatibility-matrix.md`. That document defines
the four match outcomes (`exact_match`, `compatible_match`,
`preview_fallback`, `invalid_match`), the per-entity rules for characters /
places / artifacts, the `fallback_policy` request field that controls which
outcomes count as a hit, and the product-safety rule that overrides
everything else.

### 8.1 Exact Match

An exact match uses:

- same entity id
- same visual identity version
- same `state_version`
- same style profile version
- same asset role / variant_key
- same variant tags (`expression`, `angle`, `time_of_day`, `weather`, etc.)
- requested quality tier or better
- active asset state

If found, return `match_type = exact_match`.

### 8.2 Compatible Match

A compatible match returns a stored asset that is **product-safe to
substitute** under the variant compatibility matrix. Examples per the matrix:

- requested `smiling` expression → return `warm_expression` (same family).
- requested `serious` expression → return `tense` (same mild-emotion family).
- requested `establishing_wide_day` → return `establishing_wide` for generic
  place card.
- requested clean artifact display → return the default clean variant.

Returned without UX warning. Never used for strong-emotion character
variants, day↔night place substitution, altered artifact states, or any
substitution that would contradict known world state. Return
`match_type = compatible_match` with `fallback_reason` naming the matrix
rule.

### 8.3 Preview Fallback

A preview fallback is shown **temporarily** while the correct variant is
generated. Only allowed when `fallback_policy ∈ {preview_allowed, any_existing}`.
Examples per the matrix:

- show `neutral_front` portrait while a mild expression variant generates.
- show `establishing_wide` while a `night_view` generates.
- show generic document artwork while the specific `signed_document`
  variant generates.

UX must mark the result as provisional and refresh when the real asset
lands. Return `match_type = preview_fallback`.

### 8.4 Generate Missing Asset

Generate only if:

- no suitable asset exists under §§8.1–8.3,
- current asset is rejected,
- identity version or `state_version` changed,
- style profile changed,
- user explicitly asks for regeneration,
- asset is missing required resolution tier,
- the matrix returns `invalid_match` for every candidate (strong-emotion
  request, altered-state place, document-state mismatch, etc.).

Return `match_type = generated_required` from the search endpoint and
queue the generation per ADR-009.

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

Retrieval endpoints take a `fallback_policy` (default `compatible_only`).
Allowed values: `none`, `compatible_only`, `preview_allowed`, `any_existing`
(admin/debug only). See
`docs/architecture/variant-compatibility-matrix.md` §5 for semantics.

Example:

```http
POST /v1/assets/search
Content-Type: application/json
Authorization: Bearer <token>

{
  "owner_type": "character",
  "owner_id": "char_789",
  "variant_key": "serious_expression",
  "fallback_policy": "preview_allowed"
}
```

Possible response (matrix returned a preview fallback):

```json
{
  "match_type": "preview_fallback",
  "compatibility_score": 0.45,
  "fallback_reason": "character.expression.serious→neutral_front.preview_only",
  "assets": [
    {
      "id": "asset_001",
      "asset_type": "character_portrait",
      "variant_key": "neutral_front",
      "variant_family": "neutral",
      "state_version": 1,
      "low_res_url": "...",
      "high_res_url": "..."
    }
  ],
  "generation_recommended": true
}
```

The client renders the preview, displays it as provisional, and on the next
poll (after generation completes) receives an `exact_match` with the real
`serious_expression` asset.

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

**Score: 88/100 — High** *(was 85; +3 after `AssetSearchResponse` was updated in the canonical OpenAPI on 2026-06-05 to expose `match_type` and `generation_recommended` — the cache-hit telemetry the PRD asks for is now in the wire contract.)*

Storage layout, asset metadata schema, the retrieval algorithm (exact → variant → fallback → generate), cache hit types, asset states, and invalidation rules are all spelled out concretely enough to translate into repository code and SQL. The data model maps cleanly to the `visual_assets` table in `docs/db/initial_schema.sql`. Remaining friction: "variant match" semantics ("serious expression but neutral is acceptable fallback") imply a *compatibility matrix* between variant tags that isn't given — an implementer has to invent or extract it from product judgment. This is the natural next low-confidence item to address.

