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
- **+** Cache-hit telemetry (`exact_match | compatible_match | preview_fallback | generated_required`) becomes a first-class metric and a debuggable signal when costs jump.
- **+** Compatible with ADR-008's identity-first model (retrieval queries are well-typed against `visual_assets`).
- **+** The variant-compatibility matrix is now defined (`docs/architecture/variant-compatibility-matrix.md`), so "variant match" is deterministic per-entity rather than implementer-invented.
- **−** Lookup overhead on every generation call (small with proper indexes; ~1 ms expected at MVP volumes).
- **−** Risk of staleness if visual identity changed but cache returned an old asset — invalidation rules (PRD 05 §11) must be respected.
- **−** Product-safety filter (matrix §2 — "fallback must never visually contradict known world state") is a stub at MVP; growing it requires hints from the caller about scene state.

## Consequences

- Every generation handler calls the retrieval layer (which consults `docs/architecture/variant-compatibility-matrix.md`) before creating a `generation_job`.
- `generation_jobs.cache_result` records the match type (`exact_match | compatible_match | preview_fallback | generated_required`) for telemetry.
- Search and generation endpoints accept a `fallback_policy` (`none | compatible_only | preview_allowed | any_existing`) controlling which match types count as a hit.
- The retrieval indexes on `visual_assets` (per `docs/db/initial_schema.sql`) must support the matrix-driven queries without table scans (composite index on `(visual_identity_id, variant_key, state_version)` plus the new `variant_family` and `compatibility_tags` filters).
- The router (ADR-007) skips provider calls when retrieval returns exact or compatible match and the request didn't force regeneration. For `preview_fallback` it still creates the generation job *and* returns the fallback for immediate UI use.

## Revisit when

- ~~The variant-compatibility matrix becomes too coarse~~ — **matrix now exists** at `docs/architecture/variant-compatibility-matrix.md`. Revisit if a class of substitutions consistently produces user complaints (rule was too lax) or a class of generations should have been cache hits (rule was too strict). Treat the matrix as versioned data; rule changes are reviewed like ADRs.
- Cache hit rate drops below a threshold despite no canonical changes (investigation: identity version churn, style profile churn, or variant_tags drift).
- We add embedding-based fuzzy fallback for "best effort" retrieval — that becomes a fifth match type (`embedding_match`) between `preview_fallback` and `generated_required`.
- The product-safety filter (matrix §2) grows from a stub to a real check against world-state hints — that's an API + product decision, not a retrieval one.

---

## Confidence to Implement

**Score: 92/100 — Very High** *(was 85; +7 after the variant-compatibility matrix landed at `docs/architecture/variant-compatibility-matrix.md`)*

Every generation handler calls the retrieval layer before creating a job; the retrieval layer is a deterministic function backed by the matrix table, returning one of four typed outcomes. SQL queries on `visual_assets` are well-defined. Cache-hit telemetry distinguishes exact / compatible / preview / generated. The previously-open question — "which variants substitute for which?" — is closed by the matrix. Remaining 8 points reflect the product-safety filter (matrix §2) being intentionally a stub at MVP — it grows over time as the product surfaces more world-state hints to the retrieval call.
