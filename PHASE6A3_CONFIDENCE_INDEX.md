# Phase 6A3 Confidence Index — Pack Reuse-First + Completeness Storage

**Overall: 90/100 — Very High**

Phase 6A3 makes the **pack** fan-out paths
(`POST /v1/characters/{character_id}/generate-pack`,
`POST /v1/places/{place_id}/generate-pack`) retrieval-first. At creation, before
any cost reservation or enqueue, the handler resolves every required template
role through the Phase 6A1 identity/matrix retrieval layer (exact → compatible →
preview → generated_required, gated by `fallback_policy`) and splits roles into
**reused** (a ready asset satisfies them) and **missing**. Reused roles are
persisted as `asset_pack_items` pointing at the existing assets in the create
transaction; pricing is **misses-only**; all-hits packs complete synchronously
with no provider work; partial packs enqueue and the worker generates only the
missing roles. Pack completeness (`required_roles`/`delivered_roles`/`missing_roles`)
is stored on `asset_packs`. Packs reuse via the 6A1 identity/matrix layer — **not**
the 6A2 artifact prompt-hash, and there is no new pack hash or reimplemented matrix.

## What shipped

| # | Deliverable | Where | Confidence |
|---|---|---|---|
| 1 | Per-role retrieval + reused/missing split at creation | `internal/http/handlers/packs_handler.go` (`planPackReuse`) | 91 |
| 2 | Misses-only pricing (`Units = len(missing)`) + all-hits routing | `internal/http/handlers/packs_handler.go` (`generate`) | 92 |
| 3 | All-hits completed pack job (no reserve/attempt/enqueue) | `internal/jobs/service.go` (`CreateCompletedPackReuseJob`) | 90 |
| 4 | Partial pack: reused items + completeness in the create tx | `internal/jobs/service.go` (`insertPack`, `insertReusedPackItems`) | 90 |
| 5 | Completeness schema (`required/delivered/missing_roles`) | `migrations/0004_pack_completeness.up.sql` | 93 |
| 6 | Pack completeness queries (insert status/roles, completeness update) | `internal/db/queries/asset_packs.sql`, `generation_jobs.sql` | 92 |
| 7 | Worker: generate only missing roles + finalize completeness | `internal/jobs/worker_pack.go`, `internal/jobs/repository.go` | 90 |
| 8 | sqlc/CI/migration plumbing (`0004`, table count stays 18) | `sqlc.yaml`, `.github/workflows/ci.yml` | 93 |
| 9 | Unit + handler + integration tests | `packs_handler_test.go`, `worker_pack_test.go`, `pack_integration_test.go` | 90 |

## Per-role retrieval wiring point (91)

The reuse decision is made in the **handler at creation time**, before
`cost.Reserve` and enqueue — the same reason as 6A2: a reused role must not
create a cost reservation, and misses-only pricing needs the decision before
reservation. For each required role the handler builds a `RetrievalQuery` from
the pack request (`tenant_id` from the principal, `world_id`,
`visual_identity_id`, `entity_type` = character|place, `variant_key` = the role,
`style_profile_id`, effective `quality_tier`, `fallback_policy`,
`state_version = 1`) and calls the existing `Retriever.Retrieve`. A
non-`generated_required` result allowed by the policy is a reused item; the
deterministic tie-break is the 6A1 one (no new ordering). `state_version = 1`
because pack assets are generated at the entity default state, so that is what
reuse must look for — confirmed by `TestEndToEndGeneratedPackAssetIsRetrievable`
(6A1) and the new regeneration integration tests.

The role set itself moved to creation: the full required role list
(`resolvePackPlan`) was already resolved in the handler; 6A3 runs retrieval over
it there. `input_payload.variant_keys` stays the **full** role set, so the
worker is unchanged in shape — see §"reused vs missing".

## Reused vs missing split (91)

`planPackReuse` returns `([]jobs.PackReuseItem, missing []string)`:

- A role is **reused** when `Retrieve` returns a usable asset for the policy
  (`match_type != generated_required`, `asset != nil`) **and** that asset is not
  already claimed by an earlier role. Each reused item records `variant_key`,
  `asset_id`, `match_type`, and `sort_order` (the role's index).
- A role is **missing** when retrieval returns `generated_required`, the policy
  disallows the only hit, or the hit's asset was already claimed.
- **Asset claimed at most once.** `asset_pack_items` has
  `UNIQUE (asset_pack_id, visual_asset_id)`, so two roles cannot share one asset;
  the second is demoted to missing and generated fresh (Entry 89). This keeps
  every delivered role's item dedicated and never violates the constraint.
- When the retriever is unwired (nil), every role is missing — the exact pre-6A3
  "generate the whole pack" behavior (nil-safe).

## Misses-only pricing math (92)

`Units = len(missing)`. A 7-role character pack with 5 reused roles prices **2**
(`2 × $0.0100 = $0.0200`); zero misses never reaches the reserve path (all-hits
completes synchronously with **zero** reservation). The reused roles incur no
cost and no provider attempt. Pinned by `TestPackMixedHitsPricesMissesOnly`
(unit, `Units == 2`) and `TestPackPartialReuseChargesMissesOnly` (integration:
a full-reference regeneration reuses 5, prices 4 → `$0.0400`, spend rises by
misses-only to `$0.1100`, 4 provider attempts for the second job).

## Completeness schema (93) — migration / CI / sqlc

Additive columns on `asset_packs` (no new table — **table count stays 18**):

```
required_roles  TEXT[] NOT NULL DEFAULT '{}'
delivered_roles TEXT[] NOT NULL DEFAULT '{}'
missing_roles   TEXT[] NOT NULL DEFAULT '{}'
```

Role identity is the `variant_key`/role name from the 5B pack template, so the
arrays are directly comparable with `asset_pack_items.variant_key`. Migration
`0004_pack_completeness.up.sql` is wired into all three places the task
enumerated: `sqlc.yaml`'s `schema:` list (previously `0001`/`0003`), CI's
`migrations` job (explicit `psql -f 0004`), and a new CI assertion that the three
columns exist; the table-count assertion stays **18** (columns only). Verified
against a live Postgres: all four migrations apply, count is 18, columns present.
`make generate` + `sqlc vet` run clean.

`InsertAssetPack` gained a `status` parameter (a normal pack is `planned`, an
all-hits pack is `completed` at creation) plus the three role arrays;
`UpdateAssetPackCompleteness` lets the worker finalize delivered/missing at the
terminal step.

## All-hits vs partial job lifecycle (90)

- **All-hits** (every required role reused): `Service.CreateCompletedPackReuseJob`
  — the pack analogue of `CreateCompletedCacheHitJob`. In one transaction it
  inserts the generation job at `status=completed` (aggregate `cache_result`,
  `final_asset_ids` = reused assets, `cost_estimate_usd=0`, `actual_cost_usd=0`,
  **no** `cost_reservation_id`), the `asset_packs` row at `status=completed`
  with full completeness (all delivered, none missing), the job→pack link, and
  one `asset_pack_items` row per reused role. **No** reservation, provider
  attempt, enqueue, or S3 write. Shares only the idempotency machinery.
- **Partial** (some missing): `CreateAndEnqueue` reserves `Units = len(missing)`,
  inserts the `asset_packs` row (`planned`) + reused items + creation-time
  completeness (delivered = reused, missing = misses) in the create transaction,
  links the job, and enqueues. The worker fans out the full role list but its
  existing items-skip treats the pre-inserted reused items as already delivered,
  so it generates **only** the missing roles (no regeneration, no duplicates).
- **Worker terminal status from completeness**: all required delivered →
  `completed`; some missing/failed → `completed_with_warnings`; none delivered →
  `failed` (preserves 5A semantics). The worker recomputes delivered/missing from
  the items + required set and writes them via `UpdateAssetPackCompleteness`,
  idempotent on an asynq retry.

## Cost behavior on reuse (91)

- **All-hits pack**: reserved/spent unchanged, **no** `cost_reservation` row, no
  `provider_attempt`, no positive-cost `generation_cost_event`,
  `actual_cost_usd = 0`. (`TestPackRegenerationAllHitsReusesAndChargesNothing`:
  asset / attempt / reservation counts and budget spend are all identical
  before/after the all-hits regeneration.)
- **Partial pack**: reserved/spent reflect only the missing roles. Reused roles
  incur no cost and no provider attempt
  (`TestPackPartialReuseChargesMissesOnly`: 4 misses priced, exactly 4 provider
  attempts on the second job).

## fallback_policy gating (90)

The handler delegates gating to the 6A1 `Retrieve`; a non-`generated_required`
result is a hit, a `generated_required` (or a hit the policy disallows) is a
miss. `TestPackFallbackPolicyGatesReuse` drives the **real** retriever over an
in-memory candidate source: with a single neutral-front candidate, the
three-quarter role is a `compatible_match` (a hit under `compatible_only`) and
the side-angle role is only a `preview_fallback` (a miss under `compatible_only`,
a hit under `preview_allowed`).

## Idempotency (91)

Unchanged. Same body + key returns the same pack job and the same
`asset_pack_id`, creates no duplicate jobs/packs/items/reservations, and reuses
the same existing assets. The partial path uses the existing `CreateAndEnqueue`
idempotency; `CreateCompletedPackReuseJob` reuses the same `replayExisting`
machinery. A replay whose pack has since completed routes through the all-hits
path and **echoes the prior job's live status** (not the forced `queued`),
preserving the replay contract (Entry 91). Pinned by
`TestPackIdempotencyReplayReturnsSameJobAndPack`.

## Tests (90)

- **Handler unit** (`packs_handler_test.go`): all-hits → completed pack reuse
  job, no enqueue/reservation, reused items reference existing asset ids,
  completeness all-delivered; mixed → `Units == missing count`, reused items
  persisted, full role set carried to the worker; zero hits → whole pack priced;
  `fallback_policy` gating (compatible hit vs preview miss under
  `compatible_only`). The handler tests drive the **real** `assets.Retriever`
  over a fake `CandidateSource`, so the gating is exercised end-to-end.
- **Worker unit** (`worker_pack_test.go`): reused roles are not regenerated (no
  provider call), appear in `final_asset_ids`, and completeness is stored
  (all delivered / none missing); a failed role stays in `missing_roles` with
  `completed_with_warnings`.
- **Integration** (`pack_integration_test.go`, Postgres): all-hits regeneration
  mints no new `visual_assets`/`provider_attempts`/`cost_reservations` and moves
  no budget; partial regeneration prices misses-only and generates only the
  missing roles; the existing partial-failure test now also asserts the
  `missing_roles` column. All existing pack/artifact/retrieval/cost tests stay
  green.

## Explicit non-goals (unchanged)

No artifact changes (6A2 done), no `force_regenerate`/regeneration endpoint, no
preview-first delivery, no S3 read APIs/presigned URLs, no provider routing, no
real world-state safety filter (matrix §2 stays a stub), no embedding retrieval,
no rate limits/RLS/webhooks/admin retry. No second new table. No OpenAPI change
(the 202 stays an acceptance envelope; the completed/completeness state is
observed via `GET /v1/jobs/{id}` and the pack row).

## Where confidence is < 95

- **Aggregate `cache_result` (90)**: a single enum cannot express "5 exact + 2
  compatible"; the documented choice is the weakest reuse tier, with the precise
  story in the completeness columns (Entry 90). Defensible, but a coarse field.
- **All-hits route on idempotency replay (90)**: the replay-vs-fresh status
  distinction in `respondPackAllHits` is subtle; covered by an integration test,
  but it is the kind of edge that only the real Postgres path surfaced
  (Entry 91).
- **Same-asset dedup (90)**: demoting a second role that resolves to an
  already-claimed asset to "missing" is the conservative reading of the
  `UNIQUE (asset_pack_id, visual_asset_id)` constraint (Entry 89); correct and
  tested, but a reviewer may prefer a different resolution.
