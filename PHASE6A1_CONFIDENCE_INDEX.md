# Phase 6A1 Confidence Index — Retrieval Substrate / Asset Search

**Overall: 90/100 — Very High**

Phase 6A1 builds and exposes the retrieval decision layer that
"retrieval-before-generation" (ADR-009) will consume. It consumes the Phase
5B classifier (`ClassifyVariant`) and compatibility matrix
(`CompareVariants`) without reimplementing them, adds the exact/candidate SQL,
and wires `POST /v1/assets/search`. **No generation, pack, cost, or preview
behavior changes** — this PR is substrate only. Retrieval-before-generation
inside the artifact/pack paths stays 6A2/6A3.

## What shipped

| # | Deliverable | Where | Confidence |
|---|---|---|---|
| 1 | Deterministic retrieval library (4 typed outcomes, policy-gated) | `internal/assets/retrieval.go` | 92 |
| 2 | Exact + candidate + compat-tag SQL | `internal/db/queries/visual_assets.sql` | 93 |
| 3 | Retrieval-facing repository methods | `internal/assets/repository.go` | 93 |
| 4 | `POST /v1/assets/search` handler + router wiring | `internal/http/handlers/assets_handler.go`, `internal/http/router.go` | 91 |
| 5 | Additive OpenAPI (`style_profile_version`, `quality_tier` on `AssetSearchRequest`) | both `openapi.yaml`, `apigen` | 95 |
| 6 | Unit + handler + integration tests | `internal/assets/retrieval_test.go`, `internal/http/handlers/handlers_test.go`, `internal/assets/retrieval_integration_test.go` | 90 |

## Retrieval algorithm (92)

- Order: **exact → compatible → preview → generated_required**, gated by
  `fallback_policy` (`none | compatible_only | preview_allowed |
  any_existing`).
- Exact (`FindExact`) matches owner + variant + state + style, optional
  style-profile-version / quality-tier, and `status = 'ready'`. The matrix is
  **not** consulted for exact — it is a direct key match.
- Compatible/preview classify the requested variant and each candidate with
  `ClassifyVariant`, then call `CompareVariants(entityType, requested,
  candidate)`. The matrix is **not** reimplemented in retrieval.
- Deterministic ordering when several qualify: **outcome tier → compatibility
  score → fallback_rank → lowest id** (single final tie-break; the SQL
  `ORDER BY` mirrors it). Asserted stable over repeated runs.
- Safety invariants: non-`ready` never reusable; unknown variants match only
  on an exact key (never compatible); identity anchors are never compatible
  substitutes (excluded in SQL *and* `candidateReusable`); strong-emotion /
  state / outfit / place-state / strict variants stay governed by
  `CompareVariants`.
- `−8`: the product-safety filter (matrix §2) is a deliberate stub
  (`passesWorldStateSafetyFilter` always returns true) — see "Stub" below.

## SQL (93)

- `FindExactVisualAsset`, `ListVisualAssetCandidates`,
  `ListVisualAssetCandidatesByCompatTag` added; no `dbgen` hand-edits
  (`sqlc generate`, `sqlc vet` clean).
- Uses the existing indexes: `idx_visual_assets_identity_variant`,
  `idx_visual_assets_identity_family`, and the GIN
  `idx_visual_assets_compat_tags` (array overlap).
- Candidates are scoped to one identity, the requested `state_version` (state
  is strict per matrix §7.4/§8.4) and the same `style_profile_id` (a
  substitution never silently changes visual style), exclude anchors, and are
  `ready`-only.
- **No migration. Table count stays 18** (asserted against a fresh local
  Postgres 16 + the three existing migrations).

## Handler / API (91 / 95)

- `POST /v1/assets/search` requires `images:read`, is tenant-scoped (tenant
  from the auth principal, **never** the body), and validates required fields
  (`world_id`, `visual_identity_id`, `owner_type`∈{character,place},
  `variant_key`, `style_profile_id`, `state_version`) → `400 invalid_request`.
- Unknown `fallback_policy` → `400 invalid_request` (validated in the decision
  layer via `ErrInvalidFallbackPolicy`, surfaced by the handler).
- The endpoint and the `AssetSearchRequest`/`AssetSearchResponse` schemas and
  `MatchType`/`FallbackPolicy` enums already existed (v0.4.0) — they were
  **wired, not redesigned**. The only additions are two optional request
  fields (`style_profile_version`, `quality_tier`), strictly additive, mirrored
  byte-identically into both spec copies, version bumped `0.5.2 → 0.5.3` with a
  changelog stanza; `make generate` is idempotent.

## Tests (90)

- **Unit** (`retrieval_test.go`): exact; tenant/world/identity/style/state
  dimensions; style-profile-version; non-ready never returned (all four
  statuses); policy `none`/`compatible_only`/`preview_allowed`/`any_existing`;
  neutral/warm-from-matrix; day/night strict; unknown-variant-exact-only;
  anchors-not-substituted; deterministic ordering; invalid policy.
- **Handler** (`handlers_test.go`): exact / compatible / preview /
  generated_required responses; tenant scoping; missing required fields;
  invalid `fallback_policy`; missing `images:read` scope (403).
- **Integration** (`retrieval_integration_test.go`, Postgres): exact;
  compatible (`expression_warm` ← `neutral_front`); day→night generate;
  failed/archived/pending excluded; cross-tenant excluded; deterministic
  ordering (5×); GIN compatibility_tags path. All prior phases' integration
  tests stay green.

## Product-safety hook (stub)

`passesWorldStateSafetyFilter(query, candidate, requested, candidateVariant)
bool` is a clearly named final gate in `retrieval.go` that **always returns
true** in 6A1, with a comment stating real world-state safety filtering
(matrix §2 — a fallback must never visually contradict known world state) is
deferred: it needs world-state hints (scene mood, recent canonical events)
the retrieval call does not yet carry. It is wired into the compatible/preview
selection so growing it later is a single-function change with no call-site
churn.

## Explicit deferrals (unchanged in 6A1)

Retrieval-before-generation inside artifact/pack generation, `force_regenerate`,
misses-only pack pricing, reused pack items, all-hits completion,
pack-completeness storage, new migration/tables, preview-first delivery, S3
read APIs / presigned URLs, BFL/provider routing & capability checks, admin
retry/cancel, rate limits, RLS, webhooks, period reset, regeneration endpoint,
embedding/similarity retrieval, and real world-state safety filtering — all
remain out of scope (6A2 / 6A3 / 6B / 7).
