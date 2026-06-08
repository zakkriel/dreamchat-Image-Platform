# Confidence Scores — Summary

Every PRD/spec/ADR/schema/runbook in this repo carries a per-file confidence-to-implement score (added at the bottom of each markdown/YAML/SQL file; for JSON schemas it lives in a sibling `*.confidence.md`). This file is the index.

**Changelog**

- **2026-06-05 (latest)** — SQL schema gap closed:
  - `docs/db/initial_schema.sql` rewritten to match the OpenAPI v0.5.0 data model end-to-end. 17 tables (was 9), 42 indexes, 35 CHECK constraints. Adds the 7 missing tables (`asset_packs`, `asset_pack_items`, `provider_attempts`, `provider_model_prices`, `cost_budgets`, `cost_reservations`, `provider_routes`) plus `visual_identity_versions` for canonical-version audit.
  - Existing tables extended: `tenant_id` on every tenant-scoped table; `visual_assets` gains `variant_family`, `state_version`, `compatibility_tags`, `fallback_allowed`, `fallback_rank`, `is_identity_anchor` (variant-compatibility-matrix v1 fields are now first-class columns, not JSONB); `provider_models` gains `preview_capability`; `generation_jobs` gains `cost_reservation_id` (FK added via ALTER after `cost_reservations` is created), `fallback_policy`, `cache_result`.
  - Indexes target the seven hot paths: tenant-scoped lookups, generation_jobs by status, visual_identity owner lookup, asset retrieval by `(visual_identity_id, variant_key, state_version)`, cost budget lookup, active price-book lookup (partial unique index on `is_active`), active provider routes, idempotency-key lookup, provider attempts by job.
  - Schema passes `pglast` (Postgres grammar parser) without errors.
  - Score shift: `docs/db/initial_schema.sql` **85 → 92**. Aggregate stays at **89**; minimum file score floor unchanged at 80.
  - See `frustration_log.md` entry 18.
- **2026-06-05 (earlier)** — Cost-control + preview-capability + observability thresholds:
  - New `docs/architecture/cost-control.md` — price book, budgets, reservations, 11-step pre-flight estimation pipeline, behavior rules, failure modes. `daily_cost_usd` rate-limit dimension now has a defined backing model.
  - `docs/api/rate-limits.md` rewritten with six distinct limit dimensions (request rate, concurrent jobs, daily cost, monthly cost, provider-specific, token-specific), each tied to a concrete data structure. Score **75 → 90**.
  - `docs/api/openapi.yaml` v0.5.0: replaces `PriceBookEntry` with full `ProviderModelPrice` (operation_type / unit_type enums, effective dating, is_active); rebuilds `CostBudget` to spec (scope_type / period / limit_amount / reserved_amount / spent_amount / status); adds `CostReservation` with full lifecycle + `GET /v1/admin/cost-reservations`; adds `PreviewCapability` enum on `ProviderModel`; returns `estimated_cost_usd` and `cost_reservation_id` on `GenerationJobAccepted`. Splits price-book endpoints into POST/GET/PUT-by-id. Adds POST for cost-budgets.
  - `docs/runbooks/cost-spike.md` updated to use the new schema field names and `POST /v1/admin/price-book` (new entry) for incident review.
  - `docs/architecture/observability.md` adds explicit numeric alert thresholds (latency, failure rate, queue, cost, cache/retrieval, consistency) with warning/critical bands. Score **78 → 88**.
  - `prds/06_delivery_pipeline_performance_cost_and_rollout.md` adds §3.0 Provider preview capability — `true_preview`/`derived_preview`/`no_preview` modes and router rules. Score **75 → 85**.
  - `docs/adr/010-preview-first-delivery.md` rewritten to make preview-first explicitly provider-dependent. Score **78 → 88**.
  - `docs/architecture/provider-adapters.md` router inputs now include `ProviderCapability` + `PreviewCapability`.
  - See `frustration_log.md` entry 17.
- **2026-06-05 (earlier)** — Benchmark corpus populated:
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
| PRDs (`prds/`) | **89** | 88 | 80 | 95 | 10 (1 deprecated, excluded) |
| ADRs (`docs/adr/`) | **90** | 90 | 85 | 95 | 15 |
| API specs (`docs/api/`) | **88** | 90 | 85 | 94 | 9 |
| Architecture (`docs/architecture/`) | **88** | 88 | 85 | 90 | 11 (incl. cost-control.md) |
| DB (`docs/db/`) | **92** | 92 | 92 | 92 | 1 |
| Guidelines (`docs/guidelines/`) | **90** | 90 | 85 | 95 | 4 |
| Runbooks (`docs/runbooks/`) | **87** | 88 | 80 | 90 | 5 |
| Schemas (`docs/schemas/`) | **90** | 89 | 88 | 95 | 4 |
| **All files** | **89** | 90 | 80 | 95 | **59** |

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
| **85** | `06_delivery_pipeline_performance_cost_and_rollout.md` | *(was 75)* preview capability classification + router rules; cost-control backed by spec |
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
| **88** | `010-preview-first-delivery.md` *(was 78; provider-dependent preview capability)* |
| 95 | `011-s3-object-storage.md` |
| 95 | `012-postgres-source-of-truth.md` |
| 85 | `013-redis-queue-mvp.md` |
| 95 | `014-standard-errors.md` |
| 95 | `015-serve-api-docs.md` |

All ADRs share a templated Context/Tradeoffs/Notes block; the *decision* sentence is the differentiator. See `frustration_log.md` entry 5.

### API specs (`docs/api/`)

| Score | File |
|---:|---|
| **94** | `openapi.yaml` *(v0.5.0; adds cost-control + preview-capability spec: ProviderModelPrice, CostBudget rebuilt, CostReservation, PreviewCapability enum, estimated_cost_usd on GenerationJobAccepted; 30 paths, 43 schemas, 147 refs resolve)* |
| 90 | `authentication.md` *(documents tenant inference + admin scopes)* |
| 92 | `errors.md` |
| 85 | `idempotency.md` |
| 90 | `jobs.md` |
| 88 | `models.md` |
| **90** | `rate-limits.md` *(was 75; six dimensions + cost-control backing)* |
| 85 | `styles.md` |
| 85 | `assets.md` |

### Architecture (`docs/architecture/`)

| Score | File |
|---:|---|
| 90 | `overview.md` |
| 90 | `component-boundaries.md` |
| 88 | `data-model.md` |
| 88 | `job-lifecycle.md` |
| **88** | `observability.md` *(was 78; numeric alert thresholds)* |
| 82 | `provider-adapters.md` |
| **90** | `asset-versioning.md` *(was 82; now references the variant-compatibility matrix)* |
| 85 | `security-and-auth.md` |
| 88 | `admin-control-surface.md` *(planned admin endpoints + scopes + audit + CLI hooks)* |
| 90 | `variant-compatibility-matrix.md` *(four match outcomes, fallback policy, per-entity rules)* |
| **90** | `cost-control.md` *(new; price book + budgets + reservations + 11-step pre-flight pipeline)* |

### DB (`docs/db/`)

| Score | File |
|---:|---|
| **92** | `initial_schema.sql` *(was 85; 17 tables matching v0.5.0 data model, 42 indexes, 35 CHECK constraints, pglast-validated)* |

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

1. **`docs/architecture/asset-versioning.md` (82)** — variant_family is now a proper column in the SQL schema, but the doc still describes it as a JSONB tag. Prose update follow-up.
2. **`docs/architecture/provider-adapters.md` (82)** — the router policy is still prose; once we have benchmark results we can encode it as a rule table.

No remaining item is below **82**. The documentation phase is effectively complete.

## Cross-cutting risks

- ~~**OpenAPI drift**~~ — **RESOLVED 2026-06-05**. `docs/api/openapi.yaml` is now the canonical contract; the PRD draft is a deprecated pointer. See `frustration_log.md` entry 11.
- ~~**All 15 ADRs share an identical templated Context/Tradeoffs section**~~ — **RESOLVED 2026-06-05**. All 15 ADRs rewritten with project-specific Alternatives, Tradeoffs, and Revisit When sections. See `frustration_log.md` entry 12.
- ~~**Visual consistency outcome ≠ consistency-system code**~~ — **RESOLVED 2026-06-05**. PRD 03 §8 Provider Capability Floor pins minimum provider capability + routing rules + 4-of-5 acceptance tests. See `frustration_log.md` entry 13.
- ~~**Runbooks assume admin tooling that isn't built**~~ — **RESOLVED 2026-06-05** at the spec level. `docs/architecture/admin-control-surface.md` defines the surface; runbooks now map every action to a planned endpoint, planned CLI, or **MANUAL** fallback. Implementation of the endpoints is the remaining work. See `frustration_log.md` entry 14.
- ~~**Variant-compatibility matrix**~~ — **RESOLVED 2026-06-05**. `docs/architecture/variant-compatibility-matrix.md` defines four match outcomes, the `fallback_policy` enum, twelve variant dimensions, and per-entity rules for characters / places / artifacts. The product-safety rule ("fallback must never visually contradict known world state") overrides every other matrix rule. See `frustration_log.md` entry 15.
- **No open cross-cutting risks remaining at the documentation level.** The remaining gaps are item-specific (benchmark corpus, cost-budget reservation pipeline, preview-first provider dependency, observability thresholds, product-safety filter substance) and tracked above in "Lowest-confidence items."
