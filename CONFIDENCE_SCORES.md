# Confidence Scores — Summary

Every PRD/spec/ADR/schema/runbook in this repo carries a per-file confidence-to-implement score (added at the bottom of each markdown/YAML/SQL file; for JSON schemas it lives in a sibling `*.confidence.md`). This file is the index.

**Changelog**

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
| PRDs (`prds/`) | **83** | 85 | 60 | 95 | 10 (1 deprecated, excluded) |
| ADRs (`docs/adr/`) | **89** | 90 | 78 | 95 | 15 |
| API specs (`docs/api/`) | **87** | 88 | 75 | 95 | 9 |
| Architecture (`docs/architecture/`) | **85** | 85 | 78 | 90 | 8 |
| DB (`docs/db/`) | **85** | 85 | 85 | 85 | 1 |
| Guidelines (`docs/guidelines/`) | **90** | 90 | 85 | 95 | 4 |
| Runbooks (`docs/runbooks/`) | **79** | 78 | 72 | 90 | 5 |
| Schemas (`docs/schemas/`) | **90** | 89 | 88 | 95 | 4 |
| **All files** | **86** | 88 | 60 | 95 | **56** |

## Per-file scores

### PRDs (`prds/`)

| Score | File | Headline |
|---:|---|---|
| 95 | `00_README.md` | Index doc; clear principle |
| 90 | `01_image_platform_vision_and_scope.md` | Vision is sharp; some quality outcomes provider-dependent |
| **88** | `02_standalone_image_generation_api_and_job_system.md` | *(was 82)* OpenAPI drift resolved; router policy still open |
| 65 | `03_character_and_place_consistency_system.md` | Data model fine; visual consistency is provider-quality dependent |
| 80 | `04_asset_packs_variants_and_expressions.md` | Pack templates + asset roles enumerated; trigger thresholds open |
| **88** | `05_storage_retrieval_versioning_and_cache_strategy.md` | *(was 85)* `match_type` now in canonical spec; variant compat matrix still open |
| 75 | `06_delivery_pipeline_performance_cost_and_rollout.md` | Phased rollout solid; preview-first needs provider support |
| 85 | `07_superpowers_implementation_prompt.md` | Meta-build prompt; stack choice conflicts with docs |
| _N/A_ | `schemas/image_platform_openapi_draft.yaml` | **DEPRECATED** — points at `docs/api/openapi.yaml` |
| 90 | `schemas/image_platform_data_model.json` | Cleanest spec in pack; near-1:1 to DDL |
| 60 | `schemas/benchmark_corpus_template.md` | Runner easy; corpus + scoring rubric under-specified |

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
| 85 | `009-retrieval-before-generation.md` |
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
| **95** | `openapi.yaml` *(was 88; canonical contract, OpenAPI 3.1.0 validated, 8 centralized enums, 76 refs resolve)* |
| 90 | `authentication.md` *(now documents tenant inference)* |
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
| 82 | `asset-versioning.md` |
| 85 | `security-and-auth.md` |

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
| 75 | `provider-failure.md` |
| 78 | `failed-jobs.md` |
| 90 | `token-rotation.md` |
| 72 | `cost-spike.md` |

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

1. **`prds/schemas/benchmark_corpus_template.md` (60)** — needs real prompts + a scoring rubric (human or LLM-judge) before the runner is useful.
2. **`prds/03_character_and_place_consistency_system.md` (65)** — pin a minimum provider capability (reference-image conditioning or LoRA) so the consistency claim is testable.
3. **`docs/runbooks/cost-spike.md` (72)** — depends on cost-budget reservation + admin controls that don't exist yet.
4. **`docs/runbooks/provider-failure.md` (75)** — needs admin endpoints/CLI to disable routes.
5. **`docs/api/rate-limits.md` (75)** — `estimated_cost_per_day` requires a price book + pre-flight cost estimation pipeline.
6. **`docs/runbooks/failed-jobs.md` (78)** — same admin-tooling gap as provider-failure.
7. **`docs/architecture/observability.md` (78)** — alert thresholds (what counts as "high"?) need numbers before they can be wired.

## Cross-cutting risks

- ~~**OpenAPI drift**~~ — **RESOLVED 2026-06-05**. `docs/api/openapi.yaml` is now the canonical contract; the PRD draft is a deprecated pointer. See `frustration_log.md` entry 11.
- **All 15 ADRs share an identical templated Context/Tradeoffs section** — they read as auto-generated and don't capture alternatives considered. Useful to revisit with real tradeoff content. (See `frustration_log.md` entry 5.)
- **Visual consistency outcome ≠ consistency-system code** — the platform can do everything right and the output can still drift if the chosen provider doesn't honor identity inputs. PRD 03 should specify a provider capability floor.
- **Runbooks assume admin tooling that isn't built** — provider-failure / failed-jobs / cost-spike all reference admin actions (disable provider, requeue jobs, lower limits) without backing endpoints.
