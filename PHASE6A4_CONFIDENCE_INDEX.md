# Phase 6A4 Confidence Index — Forced Regeneration (Supersede-on-Regenerate)

**Overall: 90/100 — Very High**

Phase 6A4 adds a request-level way to bypass reuse (`force_regenerate`, default
`false`) and the supersede semantics that make a forced regeneration meaningful:
the regenerated asset becomes the single `ready` row retrieval returns, and its
predecessor for the exact slot is archived and linked forward. A forced
regeneration is a **real** generation — it reserves cost, makes a provider
attempt, enqueues, and writes a new asset; there is no free/cache-hit regenerate.
Default (omitted/`false`) behavior is byte-for-byte Phase 6A2/6A3. This closes
Phase 6A.

## What shipped

| # | Deliverable | Where | Confidence |
|---|---|---|---|
| 1 | `force_regenerate` request field (additive, both spec mirrors + apigen) | `api/openapi.yaml`, `docs/api/openapi.yaml`, `internal/http/apigen/apigen.gen.go` | 93 |
| 2 | Artifact bypass: gate reuse on `!force`, carry flag on payload | `internal/http/handlers/artifacts_handler.go` | 92 |
| 3 | Pack bypass: skip `planPackReuse`, all roles missing, whole-pack pricing | `internal/http/handlers/packs_handler.go` | 92 |
| 4 | Supersede column (additive, nullable, self-FK) | `migrations/0005_supersede_on_regenerate.up.sql` | 93 |
| 5 | Supersede queries (lock, max version, archive) + versioned insert | `internal/db/queries/visual_assets.sql` | 91 |
| 6 | Artifact supersede tx (lock → max → insert ready → archive prior) | `internal/assets/supersede.go`, `repository.go` | 90 |
| 7 | Pack-role supersede tx (reuses `InsertPackItemWithAsset`'s tx) | `internal/jobs/repository.go` (`InsertPackItemWithAssetSuperseding`) | 90 |
| 8 | Worker force routing (artifact + pack) | `internal/jobs/worker.go`, `worker_pack.go` | 91 |
| 9 | sqlc/CI/migration plumbing (`0005`, table count stays 18) | `sqlc.yaml`, `.github/workflows/ci.yml` | 93 |
| 10 | Unit + handler + integration tests (incl. concurrency) | `*_handler_test.go`, `worker*_test.go`, `*integration_test.go` | 90 |

## Bypass wiring point — per path (92)

The bypass decision is made in the **handler**, the same place 6A2/6A3 make the
reuse decision, before any cost reservation or enqueue.

- **Artifact** (`ArtifactsHandler.Generate`): `forceRegenerate := req.ForceRegenerate != nil && *req.ForceRegenerate`.
  The 6A2 reuse block is gated `if h.Reuse != nil && !forceRegenerate` — when
  forced the handler falls straight through to `CreateAndEnqueue` (reserve +
  enqueue), exactly like a cold miss. `force_regenerate=true` is written onto
  `input_payload` so the worker knows to supersede (only set when true, so a
  default request's payload stays byte-for-byte the 6A2 shape).
- **Pack** (`PacksHandler.generate`): when forced, `planPackReuse` is skipped
  entirely; `missing = variantKeys` (all roles), `reuseItems = nil`. The existing
  partial path then prices `Units = len(missing)` = the whole pack and generates
  every role. The all-hits branch (`len(missing) == 0`) is naturally not taken.
  `force_regenerate=true` is carried on the payload.

## Supersede slot predicate — exact, never matrix (91)

Supersede archives **only** the prior `ready` row(s) of the *exact* slot being
regenerated, using the same predicate as that path's reuse lookup:

- **Artifact slot** = `tenant + world + asset_type='artifact' + variant_key='default' + style + quality + prompt_hash`
  — the `FindReadyArtifactByPromptHash` predicate (`ArchivePriorReadyArtifactSlot`).
- **Pack-role slot** = `tenant + world + visual_identity + variant + state_version + style + quality`
  — the `FindExactVisualAsset` predicate (`ArchivePriorReadyVariantSlot`).

There is no `compatibility_tags` / `fallback_rank` / matrix logic in the archive
predicate, so a forced regenerate can never archive a compatible or preview
neighbor — only the identical slot. `ArtifactSlot`/`VariantSlot`
(`internal/assets/supersede.go`) are constructed from the same request-derived
fields the reuse lookup uses (the worker reads them off `input_payload`), so the
archive predicate and the reuse predicate stay in lock-step by construction.

## Versioning rule (91)

The new asset's `version = COALESCE(max(version) for the slot, 0) + 1`, where the
max is taken over **all** rows of the slot (ready *and* archived), so versions are
monotonic across regenerations. `InsertVisualAsset` gained an explicit `version`
parameter; the normal generate path passes `0` → defaulted to `1` in
`InsertWithQueries` (the prior schema `DEFAULT 1`), so non-forced inserts are
unchanged. Computed inside the supersede transaction under the slot lock
(`MaxVersionForArtifactSlot` / `MaxVersionForVariantSlot`).

## Transaction + ordering (90)

Insert precedes archive: `superseded_by_asset_id` FKs `visual_assets(id)`, so the
new row must exist before predecessors can point at it. Within one transaction:

1. `pg_advisory_xact_lock(hashtextextended(slot_key, 0))` — serializes the slot.
2. `MAX(version)` over the slot → next version.
3. insert the new asset `ready` at the next version.
4. archive every prior `ready` row of the slot (`status='archived'`,
   `superseded_by_asset_id = new id`, `id <> new id`).

Because steps 3–4 are one transaction, committed readers flip atomically from
old-ready to new-ready — never zero, never two ready rows. The artifact path runs
this in `assets.SupersedeAndInsertArtifact` (its own tx); the pack path runs it
inside `InsertPackItemWithAssetSuperseding`, the same tx that appends the
`asset_pack_items` row (so a delivered regenerated variant is atomic).

## Concurrency (90)

Two concurrent forced regenerations of the same slot serialize on the advisory
lock and produce versions N+1 and N+2 (never duplicates); afterward exactly one
ready asset remains (the latest) and all priors are archived and linked forward.
Pinned by `TestIntegrationArtifactSupersedeConcurrent` (real Postgres): a v1 seed
plus two concurrent `SupersedeAndInsertArtifact` calls yield ready=v3 with v1/v2
archived+linked.

## Cost behavior on forced regen (92)

Forced regeneration uses the existing `CreateAndEnqueue` path with no new
service primitive, so cost accounting is identical to a cold generation:

- **Forced artifact**: one `text_to_image` image priced, one reservation, one
  provider attempt, one new asset. No cache-hit job.
- **Forced pack**: priced for **all** required roles (`Units = len(roles)`), one
  reservation, one provider attempt per role, one new asset per role. No reused
  items, no misses-only discount, no all-hits synchronous completion.

A forced regenerate of a slot that had a prior asset increases budget spend by
the full generation cost; the prior asset is archived (not deleted), the new one
is ready. Pinned by `TestEndToEndArtifactForceRegenerateSupersedes` and
`TestPackForceRegenerateSupersedesAndChargesFullPack`.

## Retrieval-after-supersede proof (92)

6A1 retrieval (`FindExact` / `FindReadyArtifactByPromptHash`) is `status='ready'`
only with `ORDER BY id ASC`. After supersede the predecessor is `archived`, so the
regenerated row is the only ready one and is returned — no retrieval change, no
version ordering added. `TestEndToEndArtifactForceRegenerateSupersedes` asserts a
non-forced repeat after a forced regenerate reuses the regenerated asset
(`cache_result=exact_match`, `final_asset_ids=[new id]`), not the archived
predecessor.

## Idempotency (90)

`force_regenerate` is part of the request body, hence part of the idempotency
request-hash: a forced and an identical non-forced request are different requests
→ different jobs. A replay of the same forced request (same body + key) returns
the same job and creates no duplicate generation/supersede (the existing
idempotency machinery on `CreateAndEnqueue`; the worker's terminal short-circuit
prevents a re-fan-out / re-supersede on retry). Two distinct forced requests each
regenerate and each supersede — the second supersedes the first.

## OpenAPI (93)

Strictly additive: a new optional `force_regenerate: boolean` (default `false`) on
`GenerateArtifactRequest`, `GenerateCharacterPackRequest`, and
`GeneratePlacePackRequest`. No field removed or made required, no response schema
change (the supersede chain lives on `visual_assets.superseded_by_asset_id` and is
observed via existing reads). Version bumped `0.5.3 → 0.5.4` with a changelog
stanza; `api/openapi.yaml` and `docs/api/openapi.yaml` are byte-for-byte mirrors;
`make generate` regenerated `apigen` and `git diff --exit-code` is clean.

## Schema / plumbing (93)

One additive nullable column, `visual_assets.superseded_by_asset_id TEXT
REFERENCES visual_assets(id)` (migration `0005`). No new table — the public BASE
TABLE count stays **18**. Added to `sqlc.yaml`'s `schema:` list and the CI
`migrations` job's `psql -f` sequence; a new CI assertion checks the column
exists; the table-count assertion is unchanged at 18. `sqlc vet` clean. The
existing visual_assets SELECT/RETURNING column lists were extended with the new
column so sqlc keeps returning the `VisualAsset` table struct.

## Tests (90)

- **Handler**: artifact `force_regenerate:true` with a hit bypasses reuse
  (`CreateAndEnqueue`, not `CreateCompletedCacheHitJob`); `false`/omitted still a
  cache hit. Pack `force_regenerate:true` bypasses `planPackReuse` (Units = all
  roles, no reused items, no all-hits) even when every role hits; `false` still
  all-hits.
- **Worker unit**: forced artifact routes to `SupersedeAndInsertArtifact` with the
  exact slot, archives + links the prior, versions new = prior+1; forced pack
  routes every role to `InsertPackItemWithAssetSuperseding`.
- **Integration (Postgres)**: artifact supersede + concurrency
  (`internal/assets`), full artifact end-to-end (archive/link/version/cost/reuse,
  S3-gated), full pack end-to-end (all-new assets, full-pack pricing,
  archive/link). All existing 5A/5B/6A1/6A2/6A3/cost tests remain green.

## Risks / residual (why not higher)

- The artifact full end-to-end integration test (`TestEndToEndArtifactForceRegenerateSupersedes`)
  is S3-gated and skips without MinIO; locally the artifact supersede **SQL** is
  covered without S3 by the `internal/assets` integration tests, and the pack
  end-to-end runs against real Postgres. CI runs all of them.
- Supersede is whole-unit only (whole artifact / whole pack). Per-role/subset
  regeneration is an explicit non-goal.
- The advisory-lock key is a hash of the slot string; collisions would only
  over-serialize unrelated slots (correctness-safe, never a missed lock).
