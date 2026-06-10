# Phase 5B Confidence Index — Variant Logic

**Overall: 90/100 — Very High**

Phase 5B gives the Phase 5A opaque `variant_key` deterministic meaning
without implementing retrieval-before-generation (that stays 6A). Three
deliverables plus a pure compatibility-matrix library for 6A to consume
later.

## What shipped

| # | Deliverable | Where | Confidence |
|---|---|---|---|
| 1 | Deterministic, table-driven variant classification | `internal/assets/variants.go` | 95 |
| 2 | Compatibility/provenance fields stamped on generated pack assets | `internal/db/queries/visual_assets.sql`, `internal/assets/repository.go`, `internal/jobs/worker_pack.go` | 93 |
| 3 | Named pack-template selection (`pack_template`, custom override) | `internal/assets/packtemplates.go`, `internal/http/handlers/packs_handler.go`, both `openapi.yaml` | 92 |
| 4 | Pure compatibility-matrix library (built + tested, **not** wired to DB) | `internal/assets/compatibility.go` | 85 |

## Classification (95)

- `ClassifyVariant(entityType, key)` is table-driven and deterministic;
  returned tags/compat-tag slices are copies (mutation can't poison the
  table — unit-tested).
- Covers the PRD 04 §4.4 / §5.4 character and place role vocabularies,
  aliases the asset-versioning.md spellings, and matches every worked
  example in the task (`neutral_front_portrait`, `expression_smiling`,
  `expression_angry`, `day_view`, `night_view`, `rainy_view`).
- Unknown keys classify **unsafely** — family `unknown`, no compatibility
  tags, `fallback_allowed=false`, lowest fallback rank — never
  generic-safe.
- −5: `surprised`/`sad`/`afraid` are classified `strong_emotion` (strict)
  as a product-safety choice the matrix doesn't explicitly mandate
  (frustration log Entry 67).

## Asset field population (93)

- `InsertVisualAsset` now writes `variant_family`, `compatibility_tags`,
  `fallback_allowed`, `fallback_rank`, and structured `metadata` (variant
  tags + derived family). Columns pre-existed (0001); no migration, table
  count stays 18.
- The single-artifact path is untouched and writes safe defaults
  (frustration log Entry 69) — Phase 3/4 tests stay green.
- Verified end-to-end against Postgres: an expression pack produces rows
  with populated families, `generic_presence` on the neutral portrait,
  `fallback_allowed=false` on the strong-emotion variant, and queryable
  `compatibility_tags` (array-overlap GIN query asserted).

## Pack templates (92)

- `character_minimal_portrait_pack` is the **PRD 04 §4.2 starter pack (7
  roles)** and `place_minimal_scene_pack` is the **PRD 04 §5.2 starter pack
  (6 roles)** — "minimum/starter" means the same thing in the PRD and in
  code. The handler's no-template default is *derived from* the named
  minimal template (one source of truth; a unit test locks them equal), so
  omitting `pack_template` and selecting it explicitly produce identical
  fan-out, pricing, and ordered keys.
- PRD spellings (`warm_or_smiling_expression`, `serious_or_tense_expression`,
  `angry_or_defensive_expression`, `surprised_or_shocked_expression`,
  `calm_or_empty_view`, `busy_or_active_view`) classify deterministically.
- Precedence `variant_keys > pack_template > minimal default`; templates
  set `pack_type` to the template name, explicit keys produce
  `*_custom_pack`. Unknown / cross-entity template → `400 invalid_request`.
- Additive OpenAPI change in both mirrored copies (`0.5.1 → 0.5.2`);
  `make generate` idempotent; mirror diff + spec-validator clean.

## Compatibility matrix (85)

- `CompareVariants(entityType, requested, candidate) → {outcome, score}`
  consults families **and** structured tags (angle, time_of_day, weather,
  crowd). Strict families and unknown variants only match on exact key;
  product-safety override returns `invalid_match` when in doubt.
- Representative pairs unit-tested: exact, neutral↔warm compatible,
  angle preview-fallback, strong-emotion invalid, day↔night invalid,
  rain↔clear invalid, unknown invalid (except exact). Deterministic.
- −15: this is the consciously-deferred surface. The matrix is pure and
  tested but **not** wired to any DB retrieval (6A), the product-safety
  world-state filter (matrix §2) is not implemented, and a couple of
  outcome/score choices follow the task's test spec over the matrix doc
  where they disagreed (frustration log Entry 68). These are 6A's calls.

## Explicit deferrals / non-goals (all honored)

- No retrieval, `POST /v1/assets/search`, skip-generation-when-exists,
  `fallback_policy` behavior, or regeneration (6A).
- No preview-first delivery, `preview_ready`, S3 reads, presigned URLs (6B).
- No BFL/provider routing, capability checks, rate limits, RLS, webhooks (7).
- No pack-completeness storage (no column exists; deferred — Entry 71).
- No state-version / identity-version mechanics changes.

## CI gates verified locally

`go vet` · `go build` · `go test ./...` · `golangci-lint run` (and
`--build-tags integration`) · `make generate` idempotent · `sqlc vet` ·
`openapi-spec-validator` (both copies) · mirror diff identical ·
`go test -tags=integration ./...` against a fresh Postgres · table count = 18.
