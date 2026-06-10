# Phase 5A — Confidence Rate Index

Per-action confidence for the Phase 5A deliverable (pack fan-out basics:
character/place pack endpoints, minimal pack planning, the pack create
transaction, worker batch orchestration with partial completion, pack status
lifecycle), built from PRD 04 §4–§5 + ADR-008 with the Phase 5A task
constraints overriding where they conflict (variant keys stay opaque; no
retrieval/reuse; no preview-first).

Confidence here means "the implementation matches the contract, the behavior
matches the spec + the task's pack rules, and the code is verified against a
real Postgres."

Rubric (matches the repo-wide rubric):

- 90–100 — **Very High**: Concrete spec, mature primitive, low novel logic.
- 75–89  — **High**: Clear with minor ambiguity or follow-up.
- 60–74  — **Medium**: Material ambiguity or external coupling.
- 40–59  — **Low**: Significant ambiguity or quality risk.
- <40    — **Very Low**: Highly uncertain or out of scope.

| # | Action | Confidence | Explanation (what would raise / lower it) |
|---|--------|-----------:|-------------------------------------------|
| 1 | Pack request endpoints (`POST /v1/characters/{id}/generate-pack`, `POST /v1/places/{id}/generate-pack`) with the artifact handler's validation ladder (provider gate → body/tenant_id rejection → required fields → tier/policy enums → style 422 → identity 422). | 93 | Mirrors a proven path; both routes share one handler body via a `packKind` table because the two request schemas are field-identical. Each 4xx branch has a unit test. Identity resolution uses `identities.Repository.GetByOwner` (tenant + world + owner_type + path id) and never creates identities. |
| 2 | Pack planning: per-kind defaults (3 character / 2 place), verbatim opaque override, order-preserving de-dup, ≤ 12 cap → 400. | 95 | Pure function with dedicated unit tests including the de-dup-below-cap edge. Defaults are the task's exact lists; 5B owns expansion. |
| 3 | Create transaction: job insert → `asset_packs` insert (`status=planned`, identity/style/pack_type/job/token links) → `asset_pack_id` link → `cost.Reserve` with `Units = len(variant_keys)` → idempotency row → commit → one pack enqueue. | 90 | Additive to the Phase 4 single-transaction flow (`AssetPackSpec` on the params; nothing about the artifact path changed — all Phase 3/4 tests pass unmodified). Failed pre-flight reuses 4B semantics exactly: committed failed job + failed reservation carrying the full `N × price` estimate, never enqueued (integration-tested at 0.0300 for a 3-variant pack). |
| 4 | Idempotency replay returns the same `job_id` + `asset_pack_id`, no duplicate pack/items. | 92 | The replay path reads `asset_pack_id` straight off the replayed job row. Integration test replays before AND after worker completion (the latter must echo `status=completed` and still show 3 items, not 6). |
| 5 | Worker fan-out (`TaskGeneratePack` / `EnqueueGeneratePack` / `ProcessPack`): distinct task type + handler registered in cmd/worker; per-variant attempt → generate → upload → `visual_assets` insert → `asset_pack_items` insert; per-item failures recorded without aborting the batch. | 90 | The single-artifact path is untouched (separate file, separate handler). Assets carry `asset_type=character_portrait|place_scene`, `variant_key`, `visual_identity_id`, `provider_id=mock`, `model_id=pm_mock_v1`. Cap below 95 because `attempt_number` is reused as the variant index (frustration_log Entry 60) — harmless now, revisit at Phase 7. |
| 6 | Terminal rule: all-success → pack `completed` + job `completed`; partial → `completed_with_warnings` + job `completed` with the successful ids; zero → pack `failed` + job `failed` (retryable=false) + `Release`. One pack-level `generation_cost_events` row the finalizer stamps. | 92 | Each of the three rows has a unit test (fakes) and an integration test (Postgres + budget movement). Pack status is written before job status so a partial terminal write can't strand the pack at `in_progress`. |
| 7 | Retry safety: 4B terminal short-circuit (completed→Commit / failed→Release, never re-fan-out) plus an existing-items skip keyed on `UNIQUE (asset_pack_id, variant_key)`. | 88 | Both layers unit-tested; the Postgres retry test asserts attempt count, item count, and budget all unchanged on a second `ProcessPack`. Cap at 88 because a retry of a *partially-failed* mid-run job re-attempts the failed variants (generous, not harmful) — acceptable for the mock provider, worth re-deriving when real-provider retries land. |
| 8 | Cost rule for 5A: hold `N × price`, commit in full on any success, release in full on total failure; proportional reconciliation deferred. | 90 | Documented in code (worker terminal block), in frustration_log Entry 58, and asserted by the partial-failure integration test (reservation `committed`, spent moved by the full 0.0300). The deferral is the task's own explicit non-goal. |
| 9 | Pack visibility through `GET /v1/jobs/{job_id}` only: `asset_pack_id` + `final_asset_ids` on the job response; no new pack GET endpoint. | 93 | One mapping line + the spec field. The OpenAPI touch (asset_pack_id on `GenerationJobAccepted`/`GenerationJob`, v0.5.1) is the minimal change that makes the required response fields representable — see Entry 57 for the conflict resolution. |
| 10 | sqlc surface: `InsertAssetPack`, `SetGenerationJobAssetPack`, `UpdateAssetPackStatus`, `InsertAssetPackItem`, `GetAssetPackByID` + `ListAssetPackItems` (tests/retry-skip); `InsertVisualAsset` extended with nullable `visual_identity_id`. | 94 | `sqlc generate` clean; no new tables (packs shipped in 0001) so CI's 18-table assertion is untouched and no migration was added. The visual-asset extension is nil for artifacts (Entry 61). |
| 11 | Integration cleanup reordered for `asset_pack_items` → `visual_assets`/`asset_packs` → `visual_identities` FK chain. | 93 | Full integration suite runs green repeatedly against one database, including the pre-existing Phase 3/4A/4B tests. |
| 12 | **PR #11 patch** — pack insert moved after a successful pre-flight (denied requests create no pack; enqueue failure marks the pack `failed`), so no `asset_packs` row can sit at `planned` for a job that will never run. | 93 | Frustration_log Entry 64. Integration-asserted on both paths: budget-exceeded (no pack row, no `asset_pack_id` in the 422, no job link) and enqueue failure (job failed, reservation released, pack failed, none planned). |
| 13 | **PR #11 patch** — `InsertPackItemWithAsset` commits the `visual_assets` row + `asset_pack_items` row in one transaction (column mapping shared via `assets.InsertWithQueries`), so a failed item insert rolls the asset back and retries can't orphan/duplicate a variant's asset. | 92 | Frustration_log Entry 65. The fail-then-retry unit test asserts no provider re-calls for delivered variants and exactly one asset per variant; the partial-failure integration test pins the no-orphans invariant. |

## Aggregate

- **Mean across actions**: ~92 — **Very High**
- **Floor (lowest single action)**: 88 — the partial-mid-run retry nuance (Entry 59), documented rather than hidden.
