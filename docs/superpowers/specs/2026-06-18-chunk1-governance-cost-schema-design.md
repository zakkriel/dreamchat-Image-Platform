# Chunk 1 — Schema for the Governance + Cost Contract

> **Status:** approved design, pre-implementation.
> **Date:** 2026-06-18
> **Scope:** Chunk 1 of the Combined Governance Envelope + Cost-Optimization program.
> Builds directly on Chunk 0 (goose migration tooling, ADR-P001). It is the first
> chunk to add schema and the first to exercise the reversibility harness under a
> real change.
> One chunk = one plan = one PR.

## 1. Goal & boundaries

Add the **database structure** the governance + cost contract needs, and **prove
the goose harness reverses real migrations** above the irreversible baseline floor
(version 11). This chunk is **schema only**.

It ships, in order:

1. The deferred Chunk-0 **down-guard**: `down` / `down-to` refuse to cross below
   the baseline floor, while staying allowed at and above it.
2. **Expand-only** schema: nullable columns on existing tables and new tables —
   no backfills, no destructive changes, no `NOT NULL` enforcement on existing
   tables. (expand → backfill → contract, per ADR-P001.)
3. **Reversible migrations**: every new migration ships a real, tested
   `-- +goose Down`; CI proves `up → down-to 11 → up` over the whole post-baseline
   stack.
4. **Regenerated sqlc** with zero codegen drift, preserving the existing 0009 +
   seed exclusions.

### Explicitly out of scope (Chunks 2+)

No request DTOs, no OpenAPI changes, no governance verification logic, no
router/cost-routing logic, no grid slicing, no transform operations. Just tables,
columns, RLS, and reversibility.

## 2. What exists today (the starting point)

- **Harness:** `internal/migrate/migrate.go` wraps goose (`Up`, `Down`, `DownTo`,
  `Status`, `Version`) over the embedded `migrations.FS`; `BaselineVersion = 11`.
  `cmd/migrate/main.go` dispatches `up|down|down-to|status|version|bootstrap`.
- **Baseline:** migrations `0001…0011` are the frozen, **irreversible** baseline —
  their `Down` is a guarded no-op (`SELECT … WHERE false`). Head version is **11**.
- **No down-guard yet:** `migrate.Down` / `migrate.DownTo` currently delegate
  straight to goose. Nothing stops `down-to 0` from attempting to roll the
  baseline back. ADR-P001 listed the guard as a fast-follow.
- **RLS model (`migrations/0009_rls_tenant_isolation.sql`, excluded from sqlc):**
  - Two roles: `image_platform_api` (LOGIN, **no** BYPASSRLS — request path) and
    `image_platform_system` (LOGIN, **BYPASSRLS** — system/worker/admin).
  - **Direct tenant tables** (have `tenant_id TEXT`): `ENABLE` + `FORCE ROW LEVEL
    SECURITY` + the canonical, text-safe, deny-by-default policy
    `tenant_id = NULLIF(current_setting('app.current_tenant', true), '')` for both
    `USING` and `WITH CHECK`.
  - **Child tables** (no `tenant_id`; ownership via a parent FK): a parent-join
    `EXISTS` policy against the parent's `tenant_id`.
  - **Global reference tables** (`provider_models`, `provider_routes`,
    `provider_model_prices`): deliberately **not** RLS-protected.
- **sqlc (`sqlc.yaml`):** schema is a hand-picked list of migration files —
  `0001, 0003, 0004, 0005, 0007, 0008, 0010`. It **excludes** the DML-only seeds
  (`0002, 0006, 0011`) and the RLS migration (`0009`). `make generate` runs
  `sqlc generate`; CI asserts `git diff --exit-code` (zero drift).
- **CI (`.github/workflows/ci.yml`, `migrations` job):** applies the full set via
  `go run ./cmd/migrate up`, then asserts table count **21** (20 baseline tables +
  `goose_db_version`), goose head version **11**, the fal seed, and a battery of
  column/index/RLS assertions. There is **no** `up → down → up` cycle over the real
  migrations yet — reversibility is proven only by the `TestGooseRoundTrip` canary
  in `internal/migrate/migrate_integration_test.go`.
- **Target tables for this chunk:** `generation_jobs`, `visual_assets`,
  `visual_identities`, `cost_reservations` (all defined in
  `migrations/0001_initial.sql`). There is **no** separate `generation_requests`
  table — `generation_jobs` is the persisted request/job.

### Tenant ID is TEXT

`tenant_id` is `TEXT` throughout this repo (e.g. `tenant_it_jobs`), never `uuid`.
Any new tenant-scoped table follows suit, and RLS policies must compare text
without casting (a uuid cast would raise at runtime). New tables therefore use
`tenant_id TEXT NOT NULL`.

## 3. Decisions locked during brainstorming

- **Down-guard floor:** allow target `== 11`, refuse target `< 11`. `down-to 11`
  must succeed — it is exactly the CI round-trip's midpoint and leaves the baseline
  intact. The source prompt's literal "target ≤ 11 → error" was corrected here
  because it would break the stated round-trip.
- **Migration granularity:** several cohesive, independently-reversible migrations
  (`0012…0017`), not one combined file.
- **Cost ledger:** a new `identity_cost_ledger` table accumulating lifetime cost
  per `visual_identity`. `cost_reservations` is **unchanged** — its existing
  `estimated_amount` / `actual_amount` already encode the estimated-vs-actual
  distinction; the per-identity accumulator carries `cost_estimated_total` /
  `cost_actual_total`.
- **Target tables:** governance + cost-routing fields → `generation_jobs`; anchor
  lineage (`anchor_asset_id`, `derive_from`) → **both** `visual_assets` and
  `generation_jobs`; grid fields + parent/child → `visual_assets`.
- **New-table RLS:** `sprite_sheet_contract` and `identity_cost_ledger` are direct
  tenant tables; `sprite_sheet_slice` is a child of `sprite_sheet_contract`. RLS
  lands in its own migration, excluded from sqlc (same rationale as 0009).

## 4. The down-guard (lands first)

The guard lives in `internal/migrate/migrate.go` — the single choke point shared by
the CLI and any library caller — keyed off `BaselineVersion`.

```go
// DownTo refuses to roll the schema below the irreversible baseline floor.
// down-to 11 is allowed (goose leaves v11 applied); down-to 10 and below error.
func DownTo(db *sql.DB, version int64) error {
        if version < BaselineVersion {
                return fmt.Errorf(
                        "down-to refused: target version %d is below the irreversible "+
                                "baseline floor %d; the baseline cannot be rolled back "+
                                "(restore from backup instead)", version, BaselineVersion)
        }
        if err := gooseInit(); err != nil {
                return err
        }
        return goose.DownTo(db, ".", version)
}

// Down refuses a single-step rollback that would cross into/below the baseline.
// Allowed only at v12+ (a step from v11 would roll back a baseline migration).
func Down(db *sql.DB) error {
        if err := gooseInit(); err != nil {
                return err
        }
        current, err := goose.GetDBVersion(db)
        if err != nil {
                return err
        }
        if current <= BaselineVersion {
                return fmt.Errorf(
                        "down refused: current version %d is at or below the "+
                                "irreversible baseline floor %d; a single-step down would "+
                                "roll back a baseline migration (restore from backup instead)",
                        current, BaselineVersion)
        }
        return goose.Down(db, ".")
}
```

Notes:

- `gooseInit()` must run before `goose.GetDBVersion` (it sets the dialect and
  base FS). `DownTo`'s arithmetic guard runs before any DB work.
- `down-to 11` when already at 11 is a clean no-op (nothing has `version > 11`).
- The `cmd/migrate` dispatch is unchanged; the guard is enforced in the library so
  every caller is covered.

### Down-guard tests (TDD — failing first)

These run against the **baseline alone** (no new migrations required), so the guard
can be implemented and proven before any schema lands:

- `down-to 10` (or any `< 11`) → error containing `refused`; version unchanged at 11.
- `down-to 11` → succeeds (no-op); version still 11.
- at v11, `Down()` → error containing `refused`; version still 11.

A later test (after the new migrations exist) confirms `Down()` at v17 steps to 16,
and `down-to 11` from v17 returns to 11.

## 5. Migrations (0012–0017)

All column additions to existing tables are **nullable, no default**. Defaults and
`NOT NULL` arrive in the contract phase once writers exist. Brand-new tables may use
`NOT NULL` / defaults freely (no existing rows → no backfill).

**D-4 JSONB rule** — every JSONB blob is typed `JSONB` (not `JSON`) and carries a
schema-layer shape check:

```sql
CHECK (<col> IS NULL OR <col> ? 'schema_version')
```

The `?` (key-exists) operator is JSONB-only and legal in a `CHECK`. Because every
new column is `NULL` for all existing rows, the constraint validates trivially at
add time (no backfill, no table rewrite). Value/content validation of the envelope
is Chunk 2+ application logic.

### 0012_governance_envelope — `generation_jobs`

| column | type | notes |
|---|---|---|
| `governance_envelope` | `JSONB` | + schema_version CHECK |
| `classification_id` | `TEXT` | |
| `visibility` | `TEXT` | no value CHECK yet (vocabulary is Chunk 2 logic) |
| `content_class` | `TEXT` | stored opaque — never parsed here |
| `authorized_by` | `TEXT` | |
| `governance_verified_at` | `TIMESTAMPTZ` | |

**Down:** `DROP COLUMN` for all six.

### 0013_cost_routing — `generation_jobs`

| column | type | notes |
|---|---|---|
| `intent` | `TEXT` | `CHECK (intent IS NULL OR intent IN ('draft','commit'))` |
| `transform_only` | `BOOLEAN` | nullable, no default |
| `transform` | `JSONB` | + schema_version CHECK |
| `max_megapixels` | `NUMERIC(6,2)` | |
| `lazy` | `BOOLEAN` | nullable, no default |

**Down:** `DROP COLUMN` for all five.

### 0014_anchor_lineage — `visual_assets` and `generation_jobs`

| column | type | notes |
|---|---|---|
| `anchor_asset_id` | `TEXT REFERENCES visual_assets(id)` | FK; NULL for existing rows so the constraint validates trivially |
| `derive_from` | `TEXT` | soft reference, no FK — target type (asset vs job) is router-defined in Chunk 2 |

Added to **both** tables. On `visual_assets`, `anchor_asset_id` is a self-FK.
Note this is a **scalar** "derived-from-this-anchor" pointer, distinct from the
existing `visual_identities.anchor_asset_ids` / `visual_identity_versions.anchor_asset_ids`
**arrays** (the identity's set of anchors) — no collision, different tables and
different cardinality.

**Down:** `DROP COLUMN` on both tables (drops the FK with the column).

### 0015_grid_and_sprite_sheets

**On `visual_assets`:**

| column | type | notes |
|---|---|---|
| `parent_asset_id` | `TEXT REFERENCES visual_assets(id)` | self-FK; the grid/sheet parent |
| `crop_index` | `INT` | |
| `crop_box` | `JSONB` | + schema_version CHECK (geometry contract still evolving until the slicer lands) |
| `expression_key` | `TEXT` | |

**New table `sprite_sheet_contract`** (direct tenant table):

```sql
CREATE TABLE sprite_sheet_contract (
    id                  TEXT PRIMARY KEY,
    tenant_id           TEXT NOT NULL,
    world_id            TEXT,
    visual_identity_id  TEXT REFERENCES visual_identities(id),
    sheet_asset_id      TEXT REFERENCES visual_assets(id),    -- composite sheet image
    generation_job_id   TEXT REFERENCES generation_jobs(id),  -- producing job
    rows                INT,
    cols                INT,
    contract            JSONB,                                -- + schema_version CHECK
    status              TEXT,                                 -- no enum CHECK yet
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (contract IS NULL OR contract ? 'schema_version')
);
```

**New table `sprite_sheet_slice`** (child of `sprite_sheet_contract`):

```sql
CREATE TABLE sprite_sheet_slice (
    id                        TEXT PRIMARY KEY,
    sprite_sheet_contract_id  TEXT NOT NULL REFERENCES sprite_sheet_contract(id) ON DELETE CASCADE,
    crop_index                INT,
    crop_box                  JSONB,                          -- + schema_version CHECK
    expression_key            TEXT,
    asset_id                  TEXT REFERENCES visual_assets(id),  -- sliced child asset (nullable until sliced)
    created_at                TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (crop_box IS NULL OR crop_box ? 'schema_version')
);
CREATE INDEX idx_sprite_sheet_slice_contract ON sprite_sheet_slice (sprite_sheet_contract_id);
```

**Down:** `DROP TABLE sprite_sheet_slice`, `DROP TABLE sprite_sheet_contract`
(drops the index with the table), then `DROP COLUMN` the four `visual_assets`
columns.

### 0016_identity_cost_ledger (direct tenant table)

```sql
CREATE TABLE identity_cost_ledger (
    id                    TEXT PRIMARY KEY,
    tenant_id             TEXT NOT NULL,
    visual_identity_id    TEXT NOT NULL REFERENCES visual_identities(id),
    cost_estimated_total  NUMERIC(14,4) NOT NULL DEFAULT 0,
    cost_actual_total     NUMERIC(14,4) NOT NULL DEFAULT 0,
    currency              TEXT NOT NULL DEFAULT 'USD' CHECK (char_length(currency) = 3),
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (visual_identity_id)
);
```

One lifetime accumulator row per identity. The estimated-vs-actual split lives in
the two `*_total` columns. `cost_reservations` is untouched.

**Down:** `DROP TABLE identity_cost_ledger`.

### 0017_new_table_rls (RLS for the new tables — excluded from sqlc)

Kept in its own migration so sqlc never has to parse policy DDL — the same reason
0009 is excluded. This is the **first RLS migration with a real, reversible
`Down`**.

**Up** — explicit statements (no `DO` loop needed for three tables; explicit is
clearer and reversible):

- `sprite_sheet_contract` and `identity_cost_ledger` → direct pattern:
  `ENABLE ROW LEVEL SECURITY`, `FORCE ROW LEVEL SECURITY`, and
  `CREATE POLICY tenant_isolation … USING (tenant_id = NULLIF(current_setting('app.current_tenant', true), '')) WITH CHECK (same)`.
- `sprite_sheet_slice` → child pattern: `ENABLE` + `FORCE`, and a parent-join
  policy:

  ```sql
  CREATE POLICY tenant_isolation ON sprite_sheet_slice
    USING (EXISTS (
      SELECT 1 FROM sprite_sheet_contract p
      WHERE p.id = sprite_sheet_slice.sprite_sheet_contract_id
        AND p.tenant_id = NULLIF(current_setting('app.current_tenant', true), '')))
    WITH CHECK (EXISTS ( … same … ));
  ```

**Down:** `DROP POLICY tenant_isolation` and `DISABLE ROW LEVEL SECURITY` on all
three tables. Ordering during `down-to 11` is safe: 0017's Down removes the
policies before 0015/0016 drop the tables.

## 6. sqlc + CI

### sqlc

- Append `0012, 0013, 0014, 0015, 0016` to `sqlc.yaml`'s schema list so sqlc sees
  the new columns and the three new tables. **0017 stays excluded** (RLS), keeping
  the established exclusion rule intact.
- No new queries this chunk — sqlc regenerates **model structs only** (new fields
  on existing models, three new table models). `make generate` → commit. CI's
  `git diff --exit-code` enforces zero drift.

### CI `migrations` job (`.github/workflows/ci.yml`)

After the existing `go run ./cmd/migrate up`:

- Update the head-version assertion: `max(version_id)` is now **17** (was 11).
- Update the table-count assertion: **24** (21 + `sprite_sheet_contract`,
  `sprite_sheet_slice`, `identity_cost_ledger`).
- **Round-trip gate** (new):
  1. `go run ./cmd/migrate down-to 11` → assert version **11**, table count back to
     **21**, the three new tables absent.
  2. `go run ./cmd/migrate up` → assert version **17**, table count **24** again.
- **Down-guard negative check** (new): `go run ./cmd/migrate down-to 10` must exit
  non-zero (proves the guard refuses crossing below the floor).
- Add RLS assertions for the three new tables, mirroring the existing 0009 checks:
  `relrowsecurity AND relforcerowsecurity` is true and a `tenant_isolation` policy
  exists on each.

## 7. Reversibility & the harness milestone

This is the first chunk to prove the harness reverses real migrations. After it
lands, ADR-P001's reversibility claim — currently "proven by the
`TestGooseRoundTrip` canary, not by a CI step over the real migrations" and "No
such migration exists yet" — becomes false. Both statements are corrected in
ADR-P001 (see §8), citing the new CI round-trip step and the down-guard.

The `down-to`/`Down` guards and the round-trip gate together prove:

- migrations 0012–0017 each reverse cleanly,
- the stack returns to the baseline schema (21 tables, version 11),
- the baseline itself can never be crossed.

## 8. Rules compliance & docs

- **D-4 (validated JSONB + `schema_version`):** satisfied at the schema layer via
  `JSONB` typing + the key-exists `CHECK` on `governance_envelope`, `transform`,
  `crop_box` (on `visual_assets` and `sprite_sheet_slice`), and
  `sprite_sheet_contract.contract`. Runtime value validation is Chunk 2+.
- **D-9 (doc edits cite proving code):** the ADR-P001 update cites
  `internal/migrate/migrate.go` (the guards), migrations `0012–0017`, and the new
  `.github/workflows/ci.yml` round-trip step. **No new ADR** — the schema-shape
  decisions (JSONB-vs-columns, RLS pattern, ledger placement) are recorded in this
  spec. (Flag during review if an ADR-P002 is wanted instead.)
- **TDD:** failing test first for the down-guard, then each migration's
  reversibility proven (locally + CI round-trip).
- **One chunk = one PR; gate red → stop.** The PR description cites D-4, D-9, and
  ADR-P001.

## 9. Definition of done

- Down-guard added in `internal/migrate`, with tests proving `down-to <11` and
  single-step `down` at the floor both error, and `down-to 11` succeeds.
- Migrations `0012–0017` land, each with a real `-- +goose Down`.
- sqlc regenerated; `git diff --exit-code` clean in CI.
- CI proves `up → down-to 11 → up` over the post-baseline stack, asserts head 17 /
  24 tables and floor 11 / 21 tables, and proves `down-to 10` is refused.
- The three new tables are RLS-enabled+forced with `tenant_isolation` policies
  matching the platform pattern (direct for `sprite_sheet_contract` /
  `identity_cost_ledger`, parent-join for `sprite_sheet_slice`).
- ADR-P001 corrected (reversibility now CI-proven); PR cites rule IDs.

## 10. Test plan (summary)

| test | type | proves |
|---|---|---|
| down-guard: `down-to 10` refused | integration | guard math + loud error |
| down-guard: `down-to 11` no-op ok | integration | floor is inclusive-allowed |
| down-guard: `down` at v11 refused | integration | single-step floor guard |
| round-trip: up→down-to 11→up | integration + CI | 0012–0017 reverse cleanly |
| new-table presence after up | CI | 24 tables, head 17 |
| baseline restored after down-to 11 | CI | 21 tables, version 11 |
| RLS enabled+forced on new tables | CI | policies match platform pattern |
| sqlc no drift | CI | `git diff --exit-code` |
