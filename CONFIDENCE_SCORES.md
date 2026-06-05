# Confidence Scores — Summary

Every PRD/spec/ADR/schema/runbook in this repo carries a per-file confidence-to-implement score (added at the bottom of each markdown/YAML/SQL file; for JSON schemas it lives in a sibling `*.confidence.md`). This file is the index.

**Changelog**

- **2026-06-05 (latest)** — Benchmark corpus populated:
  - `prds/schemas/benchmark_corpus_template.md` replaced with 100 real cases (25 characters, 25 places, 25 artifacts, 25 consistency stress tests), explicit 1–5 scoring rubric on 10 quality dimensions, 10-item operational pass/fail checklist, scoring policy with capability-mapping floors, result-row schema for the runner.
  - All 100 JSON cases validate; benchmark_ids unique; `required_capability` distribution (25 identity_capable, 10 pack_capable, 65 scene_capable) ties cleanly to PRD 03 §8.
  - Score shift: `prds/schemas/benchmark_corpus_template.md` **60 → 88**.
  - See `frustration_log.md` entry 16.
- **2026-06-05 (earlier)** — Variant compatibility matrix specified:
  - New `docs/architecture/variant-compatibility-matrix.md` — four match outcomes (`exact_match`, `compatible_match`, `preview_fallback`, `invalid_match`), five-step retrieval rule, `fallback_policy` enum (`none`, `compatible_only`, `preview_allowed`, `any_existing`), twelve variant dimensions, and per-entity rules for characters / places / artifacts.
  - The product-safety rule ("fallback must never visually contradict known world state") is now explicit and overrides every other matrix rule.
  - OpenAPI v0.4.0 adds `FallbackPolicy` and `MatchType` enums; six new fields on `VisualAsset` (`variant_family`, `state_version`, `compatibility_tags`, `fallback_allowed`, `fallback_rank`, `is_identity_anchor`); `fallback_policy` on `AssetSearchRequest` and the three generation request bodies; `match_type` / `compatibility_score` / `fallback_reason` on `AssetSearchResponse`. Breaking: `match_type` values renamed (e.g. `exact` → `exact_match`).
  - `docs/architecture/asset-versioning.md`, `prds/05`, and `docs/adr/009` updated to reference and consume the matrix.
  - Score shifts: ADR-009 **85 → 92**; `asset-versioning.md` **82 → 90**; PRD 05 **88 → 92**; `openapi.yaml` 93 → 94. New file `variant-compatibility-matrix.md` at **90**.
  - See `frustration_log.md` entry 15.
- **2026-06-05 (later)** — Doc-quality patches landed:
  - All 15 ADRs rewritten with real Alternatives Considered + Tradeoffs + Revisit When sections (per the Superpowers documentation-confidence task). Cross-cutting "ADR boilerplate" risk resolved.
  - PRD 03 Provider Capability Floor added (§8) — provider capability levels, routing rules, and 4-of-5 acceptance tests. PRD 03 score **65 → 82**.
  - Admin Control Surface specified — new `docs/architecture/admin-control-surface.md`, planned admin endpoints in `docs/api/openapi.yaml` (v0.3.0), 4 new admin scopes documented in `docs/api/authentication.md`, runbooks rewritten with endpoint mappings + manual fallbacks + audit-event expectations. Runbook scores: provider-failure **75 → 85**, failed-jobs **78 → 88**, cost-spike **72 → 85**.
  - See `frustration_log.md` entries 12–14.
- **2026-06-05** — OpenAPI drift resolved. `docs/api/openapi.yaml` is canonical; `prds/schemas/image_platform_openapi_draft.yaml` is deprecated. Scores shifted: canonical openapi.yaml **88 → 95**, PRD 02 **82 → 88**, PRD 05 **85 → 88**. See `frustration_log.md` entry 11.

**Rubric**

- 90–100 — **Very High**: Spec is concrete, primitives are mature, low novel logic, would ship without follow-up questions.
- 75–89 — **High**: Clear with minor ambiguity or external dependency.
- 60–74 — **Medium**: Achievable but has material ambiguity, novel logic, or external coupling.
- 40–59 — **Low**: Significant ambiguity or ML/quality risk.
- <40 — **Very Low**: Highly uncertain or out of scope.

Score is "my confidence I could implement the file end-to-end without further human input on requirements." Process notes / open questions / surprises are in [`frustration_log.md`](./frustration_log.md).

## Aggregate

| Group | Avg | Median | Min | Max | Files |
|---|---:|---:|---:|---:|---:|
| PRDs (`prds/`) | **88** | 88 | 75 | 95 | 10 (1 deprecated, excluded) |
| ADRs (`docs/adr/`) | **89** | 90 | 78 | 95 | 15 |
| API specs (`docs/api/`) | **86** | 88 | 75 | 94 | 9 |
| Architecture (`docs/architecture/`) | **86** | 88 | 78 | 90 | 10 |
| DB (`docs/db/`) | **85** | 85 | 85 | 85 | 1 |
| Guidelines (`docs/guidelines/`) | **90** | 90 | 85 | 95 | 4 |
| Runbooks (`docs/runbooks/`) | **87** | 88 | 80 | 90 | 5 |
| Schemas (`docs/schemas/`) | **90** | 89 | 88 | 95 | 4 |
| **All files** | **88** | 89 | 75 | 95 | **58** |

## Per-file scores

### PRDs (`prds/`)

| Score | File | Headline |
|---:|---|---|
| 95 | `00_README.md` | Index doc; clear principle |
| 90 | `01_image_platform_vision_and_scope.md` | Vision is sharp; some quality outcomes provider-dependent |
| 88 | `02_standalone_image_generation_api_and_job_system.md` | *(was 82)* OpenAPI drift resolved; router policy still open |
| **82** | `03_character_and_place_consistency_system.md` | *(was 65)* Provider Capability Floor added; consistency now testable |
| 80 | `04_asset_packs_variants_and_expressions.md` | Pack templates + asset roles enumerated; trigger thresholds open |
| **92** | `05_storage_retrieval_versioning_and_cache_strategy.md` | *(was 88)* variant compatibility matrix now defined; retrieval is deterministic |
| 75 | `06_delivery_pipeline_performance_cost_and_rollout.md` | Phased rollout solid; preview-first needs provider support |
| 85 | `07_superpowers_implementation_prompt.md` | Meta-build prompt; stack choice conflicts with docs |
| _N/A_ | `schemas/image_platform_openapi_draft.yaml` | **DEPRECATED** — points at `docs/api/openapi.yaml` |
| 90 | `schemas/image_platform_data_model.json` | Cleanest spec in pack; near-1:1 to DDL |
| **88** | `schemas/benchmark_corpus_template.md` | *(was 60)* 100 real cases + rubric + result-row schema |

### ADRs (`docs/adr/`)

| Score | File |
|---:|---|
| 95 | `001-standalone-image-platform.md` |
| 95 | `002-go-api-and-workers.md` |
| 95 | `003-openapi-first.md` |
| 90 | `004-bearer-token-auth.md` |
| 88 | `005-store-only-hashed-tokens.md` |
| 90 | `006-async-generation-jobs.md` |
| 85 | `007-provider-adapters.md` |
| 85 | `008-asset-state-first.md` |
| **92** | `009-retrieval-before-generation.md` *(was 85; variant matrix now defined)* |
| 78 | `010-preview-first-delivery.md` |
| 95 | `011-s3-object-storage.md` |
| 95 | `012-postgres-source-of-truth.md` |
| 85 | `013-redis-queue-mvp.md` |
| 95 | `014-standard-errors.md` |
| 95 | `015-serve-api-docs.md` |

All ADRs share a templated Context/Tradeoffs/Notes block; the *decision* sentence is the differentiator. See `frustration_log.md` entry 5.

### API specs (`docs/api/`)

| Score | File |
|---:|---|
| **94** | `openapi.yaml` *(v0.4.0; variant-compatibility-matrix support: FallbackPolicy + MatchType enums, six new VisualAsset fields, fallback_policy on search + generation; 29 paths, 33 schemas, 123 refs resolve, validates against OpenAPI 3.1.0)* |
| 90 | `authentication.md` *(now documents tenant inference and admin scopes)* |
| 92 | `errors.md` |
| 85 | `idempotency.md` |
| 90 | `jobs.md` |
| 88 | `models.md` |
| 75 | `rate-limits.md` |
| 85 | `styles.md` |
| 85 | `assets.md` |

### Architecture (`docs/architecture/`)

| Score | File |
|---:|---|
| 90 | `overview.md` |
| 90 | `component-boundaries.md` |
| 88 | `data-model.md` |
| 88 | `job-lifecycle.md` |
| 78 | `observability.md` |
| 82 | `provider-adapters.md` |
| **90** | `asset-versioning.md` *(was 82; now references the variant-compatibility matrix)* |
| 85 | `security-and-auth.md` |
| 88 | `admin-control-surface.md` *(planned admin endpoints + scopes + audit + CLI hooks)* |
| **90** | `variant-compatibility-matrix.md` *(new; four match outcomes, fallback policy, per-entity rules)* |

### DB (`docs/db/`)

| Score | File |
|---:|---|
| 85 | `initial_schema.sql` |

### Guidelines (`docs/guidelines/`)

| Score | File |
|---:|---|
| 95 | `documentation-guidelines.md` |
| 92 | `go-service-guidelines.md` |
| 90 | `implementation-guidelines.md` |
| 85 | `testing-strategy.md` |

### Runbooks (`docs/runbooks/`)

| Score | File |
|---:|---|
| 80 | `local-development.md` |
| **85** | `provider-failure.md` *(was 75)* |
| **88** | `failed-jobs.md` *(was 78)* |
| 90 | `token-rotation.md` |
| **85** | `cost-spike.md` *(was 72)* |

### Schemas (`docs/schemas/`)

| Score | File |
|---:|---|
| 88 | `visual_identity.schema.json` (rationale in `visual_identity.confidence.md`) |
| 88 | `visual_asset.schema.json` (rationale in `visual_asset.confidence.md`) |
| 90 | `generation_job.schema.json` (rationale in `generation_job.confidence.md`) |
| 95 | `style_profile.schema.json` (rationale in `style_profile.confidence.md`) |

### Repo root

| Score | File |
|---:|---|
| 95 | `docs/README.md` |
| 90 | `docs/superpowers_implementation_prompt.md` |

## Lowest-confidence items (work to do first)

1. **`docs/api/rate-limits.md` (75)** — `estimated_cost_per_day` requires a price book + pre-flight cost estimation pipeline. (Admin surface now documents the price-book endpoints; estimation pipeline is the remaining decision.)
2. **`prds/06_delivery_pipeline_performance_cost_and_rollout.md` (75)** — preview-first only delivers UX value when the provider supports a true fast-preview path.
3. **`docs/architecture/observability.md` (78)** — alert thresholds (what counts as "high"?) need numbers before they can be wired.
4. **`docs/adr/010-preview-first-delivery.md` (78)** — same provider-capability dependency as PRD 06.

## Cross-cutting risks

- ~~**OpenAPI drift**~~ — **RESOLVED 2026-06-05**. `docs/api/openapi.yaml` is now the canonical contract; the PRD draft is a deprecated pointer. See `frustration_log.md` entry 11.
- ~~**All 15 ADRs share an identical templated Context/Tradeoffs section**~~ — **RESOLVED 2026-06-05**. All 15 ADRs rewritten with project-specific Alternatives, Tradeoffs, and Revisit When sections. See `frustration_log.md` entry 12.
- ~~**Visual consistency outcome ≠ consistency-system code**~~ — **RESOLVED 2026-06-05**. PRD 03 §8 Provider Capability Floor pins minimum provider capability + routing rules + 4-of-5 acceptance tests. See `frustration_log.md` entry 13.
- ~~**Runbooks assume admin tooling that isn't built**~~ — **RESOLVED 2026-06-05** at the spec level. `docs/architecture/admin-control-surface.md` defines the surface; runbooks now map every action to a planned endpoint, planned CLI, or **MANUAL** fallback. Implementation of the endpoints is the remaining work. See `frustration_log.md` entry 14.
- ~~**Variant-compatibility matrix**~~ — **RESOLVED 2026-06-05**. `docs/architecture/variant-compatibility-matrix.md` defines four match outcomes, the `fallback_policy` enum, twelve variant dimensions, and per-entity rules for characters / places / artifacts. The product-safety rule ("fallback must never visually contradict known world state") overrides every other matrix rule. See `frustration_log.md` entry 15.
- **No open cross-cutting risks remaining at the documentation level.** The remaining gaps are item-specific (benchmark corpus, cost-budget reservation pipeline, preview-first provider dependency, observability thresholds, product-safety filter substance) and tracked above in "Lowest-confidence items."
