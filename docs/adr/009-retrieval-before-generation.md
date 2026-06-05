# ADR-009 — Retrieval before generation

## Status

Accepted for initial implementation.

## Context

Image generation is the most expensive thing the platform does — both in dollars (provider fees) and in wall-clock latency (seconds to minutes). Most production usage is repeated: the same character appears in many scenes, the same place returns across sessions, the same artifact gets re-rendered when the UI refreshes. Without an explicit reuse policy, every render is a fresh generation.

This decision sets the default behavior for every generation request: do we always generate, or do we look for something usable first?

## Decision

Every generation handler runs a retrieval step **before** creating a job. The retrieval algorithm is the four-tier match in PRD 05: exact → variant → fallback → generate. Cache result type (`exact`, `variant`, `fallback`, `miss`) is recorded on the job and exposed in `AssetSearchResponse.match_type` so clients and telemetry know whether they got a cached or freshly generated asset.

## Alternatives considered

- **Always generate.** Simplest control flow, no cache reasoning, no compatibility-matrix product calls. Costs balloon, latency stays bad, and characters drift visually across calls because providers aren't deterministic. Rejected on cost and consistency grounds.
- **Client-side caching only** (web app keeps an LRU of asset IDs). Fine for one device. Breaks for multi-device sessions, shared world states, and admin/batch clients. Doesn't address the cost-per-generation question; the client only avoids re-fetching, not re-generating.
- **CDN with TTL.** Solves "byte caching" of the same URL. Doesn't answer the platform's actual question: "which asset is the right one for this variant request?" — that's a metadata search, not a byte cache.
- **Embedding-similarity retrieval.** Generate a query embedding, find the nearest stored asset. Powerful for "find something similar," wrong primitive for "find the canonical neutral_front_portrait at version 2." Use SQL retrieval for the canonical case; consider embeddings for fuzzy fallback later.

## Tradeoffs

- **+** Cost and latency wins translate directly to product UX and unit economics.
- **+** Forces clean cache keys (PRD 05's `entity:id:vid_version:style_version:asset_role:variant_tags:quality:resolution`) and metadata discipline.
- **+** Cache-hit telemetry (`exact|variant|fallback|miss`) becomes a first-class metric and a debuggable signal when costs jump.
- **+** Compatible with ADR-008's identity-first model (retrieval queries are well-typed against `visual_assets`).
- **−** Variant-compatibility matrix is product-shaped and currently unspecified — an implementer must invent it from PRD 04's variant lists.
- **−** Lookup overhead on every generation call (small with proper indexes; ~1 ms expected at MVP volumes).
- **−** Risk of staleness if visual identity changed but cache returned an old asset — invalidation rules (PRD 05 §11) must be respected.

## Consequences

- Every generation handler calls `assetRepository.find(identity_id, variant_key, version, style_profile_id)` before creating a `generation_job`.
- `generation_jobs.cache_result` records the match type for telemetry.
- The retrieval indexes on `visual_assets` (per `docs/db/initial_schema.sql`) must support the four-tier query without table scans.
- The router (ADR-007) skips provider calls when retrieval returns exact/variant/fallback and the request didn't force regeneration.

## Revisit when

- The variant-compatibility matrix becomes too coarse (assets being returned as "fallback" that the product wants to treat as cache miss) — formalize the matrix and version it.
- Cache hit rate drops below a threshold despite no canonical changes (investigation: identity version churn, style profile churn, or variant_tags drift).
- We add embedding-based fuzzy fallback for "best effort" retrieval — that becomes a fifth tier between fallback and generate.

---

## Confidence to Implement

**Score: 85/100 — High**

Concretely: every generation handler calls `assetRepository.find(identityID, variantKey, version, styleProfileID)` before creating a job; if found, return cached + skip the worker. The 4-tier match in PRD 05 (exact → variant → fallback → generate) maps to indexed queries on `visual_assets`. Cache-hit telemetry is straightforward. The point I'd negotiate: "variant match" needs a compatibility matrix between variant tags (which expressions / time-of-days are acceptable substitutes) — not specified here.
