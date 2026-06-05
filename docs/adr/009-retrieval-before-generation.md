# ADR-009 — Use Retrieval Before Generation

## Status

Accepted for initial implementation.

## Context

DreamChat Image Platform needs a clean, independently testable architecture for persistent visual assets.

## Decision

Every generation request checks existing assets before creating a new job.

## Consequences

Positive:

- This controls cost, improves speed, and preserves visual continuity.
- The decision supports standalone testing and future evolution.

Tradeoffs:

- Requires explicit contracts and discipline.
- May feel heavier than a quick direct integration, but it prevents coupling and drift.

## Notes

This ADR can be revisited after the first production benchmark.

## Confidence to Implement

**Score: 85/100 — High**

Concretely: every generation handler calls `assetRepository.find(identityID, variantKey, version, styleProfileID)` before creating a job; if found, return cached + skip the worker. The 4-tier match in PRD 05 (exact → variant → fallback → generate) maps to indexed queries on `visual_assets`. Cache-hit telemetry is straightforward. The point I'd negotiate: "variant match" needs a compatibility matrix between variant tags (which expressions / time-of-days are acceptable substitutes) — not specified here.
