# Chunk 1 — Governance + Cost Schema Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the expand-only database structure for the governance + cost contract (governance envelope, cost-routing, anchor lineage, grid/sprite-sheet tables, per-identity cost ledger) as reversible goose migrations, and prove the harness reverses real migrations above the baseline floor.

**Architecture:** A `down`/`down-to` floor guard lands first in `internal/migrate`. Then six cohesive, individually-reversible migrations (`0012`–`0017`) add nullable columns to existing tables and three new tenant-scoped tables. Table/column DDL goes in migrations listed in `sqlc.yaml`; the RLS migration (`0017`) stays excluded from sqlc, exactly like `0009`. CI gains an `up → down-to 11 → up` round-trip gate plus a negative down-guard check.

**Tech Stack:** Go 1.25, goose v3 (`github.com/pressly/goose/v3`) via `internal/migrate`, pgx v5 stdlib, sqlc v1.27.0, PostgreSQL 15 (CI) / 13+ (local), GitHub Actions.

## Global Constraints

Copied verbatim from the spec (`docs/superpowers/specs/2026-06-18-chunk1-governance-cost-schema-design.md`). Every task's requirements implicitly include this section.

- **Expand-only this chunk.** Columns added to *existing* tables are **nullable, no default**. No backfills, no `NOT NULL` enforcement on existing tables, no destructive changes. Brand-new tables may use `NOT NULL`/defaults freely (no existing rows).
- **Every new migration ships a real, tested `-- +goose Down`.** Migrations `0012`–`0017`.
- **`BaselineVersion = 11` is the irreversible floor.** Never increment the constant. `down-to 11` is allowed; any target `< 11` (and single-step `down` at v11) must error loudly.
- **D-4 (validated JSONB):** every JSONB blob is typed `JSONB` (not `JSON`) and carries `CHECK (col IS NULL OR jsonb_exists(col, 'schema_version'))`. (`jsonb_exists(col, 'x')` is the function form of `col ? 'x'`; used here to avoid any sqlc `?`-operator parsing ambiguity.)
- **`tenant_id` is `TEXT`, never `uuid`.** New tenant-scoped tables use `tenant_id TEXT NOT NULL`. RLS policies compare text without casting.
- **sqlc:** append `0012`–`0016` to `sqlc.yaml`'s schema list. **NEVER** add `0017` (RLS) or any seed migration; keep the `0009` exclusion. Regenerate with sqlc v1.27.0; CI asserts `git diff --exit-code` (zero drift).
- **Do NOT touch the frozen baseline footprint:** leave `baselineTables` in `internal/migrate/bootstrap.go` and in `internal/migrate/migrate_integration_test.go` at the 20 baseline tables. The new tables are post-baseline (applied via `Up`, never stamped).
- **D-9:** doc edits cite proving code (file paths / test names).
- **TDD:** failing test first; minimal implementation; frequent commits; one chunk = one PR; gate red → stop.
- **Integration tests** are build-tagged `integration` and require `POSTGRES_DSN` (skip otherwise). **Never add `t.Parallel()`** to any test in `internal/migrate` — the goose wrappers mutate process-global state.

## Local Prerequisites

The migrate tests create throwaway databases, so they need a reachable Postgres and `POSTGRES_DSN`. Once per session:

```bash
docker run -d --name chunk1-pg \
  -e POSTGRES_USER=image_platform -e POSTGRES_PASSWORD=image_platform \
  -e POSTGRES_DB=image_platform -p 5432:5432 postgres:15-alpine
export POSTGRES_DSN="postgres://image_platform:image_platform@localhost:5432/image_platform?sslmode=disable"
```

`sqlc` v1.27.0 and `go tool oapi-codegen` must be available for `make generate` (see `.github/workflows/ci.yml`). `make generate` runs oapi-codegen then `sqlc generate`; this chunk changes no OpenAPI, so the only expected diff is under `internal/db/dbgen/`.

> **Push timing:** Run the per-task tests locally as you go, but push the branch only **after Task 8**. The CI table-count/version assertions and the new round-trip/RLS checks only line up once every migration *and* the CI update are present; pushing earlier yields an expected-but-noisy red.

---

### Task 1: Down-guard (`down` / `down-to` baseline floor)

The deferred Chunk-0 fast-follow. Self-contained, lands first, proven against the baseline alone (no new migrations needed). Spec §4.

**Files:**
- Modify: `internal/migrate/migrate.go` (add `fmt` import; guard `Down` and `DownTo`)
- Test: `internal/migrate/migrate_integration_test.go` (add three guard tests)
- Test: `cmd/migrate/main_integration_test.go` (add CLI guard test)

**Interfaces:**
- Consumes: `migrate.Up`, `migrate.DownTo(db, int64)`, `migrate.Down(db)`, `migrate.Version(db)`, `migrate.BaselineVersion`, `testdb.New` (all existing).
- Produces: `DownTo` errors when `target < BaselineVersion`; `Down` errors when current version `<= BaselineVersion`. Both errors contain the substring `refused`. Later tasks and CI rely on `down-to 11` succeeding and `down-to 10` failing.

- [ ] **Step 1: Write the failing tests**

Add to `internal/migrate/migrate_integration_test.go` (after the existing tests; `strings` is already imported):

```go
// TestDownToRefusesBelowBaseline proves DownTo rejects any target below the
// irreversible baseline floor and leaves the schema version untouched.
func TestDownToRefusesBelowBaseline(t *testing.T) {
	db, _ := testdb.New(t)
	if err := migrate.Up(db); err != nil {
		t.Fatalf("up: %v", err)
	}
	before, err := migrate.Version(db)
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if err := migrate.DownTo(db, migrate.BaselineVersion-1); err == nil {
		t.Fatal("expected down-to below baseline to be refused")
	} else if !strings.Contains(err.Error(), "refused") {
		t.Fatalf("error %q should mention 'refused'", err.Error())
	}
	after, err := migrate.Version(db)
	if err != nil {
		t.Fatalf("version after: %v", err)
	}
	if after != before {
		t.Fatalf("version changed from %d to %d despite refusal", before, after)
	}
}

// TestDownToBaselineAllowed proves DownTo to exactly the baseline floor succeeds
// (it is the CI round-trip's midpoint and leaves v11 applied).
func TestDownToBaselineAllowed(t *testing.T) {
	db, _ := testdb.New(t)
	if err := migrate.Up(db); err != nil {
		t.Fatalf("up: %v", err)
	}
	if err := migrate.DownTo(db, migrate.BaselineVersion); err != nil {
		t.Fatalf("down-to baseline should be allowed: %v", err)
	}
	v, err := migrate.Version(db)
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if v != migrate.BaselineVersion {
		t.Fatalf("version = %d, want %d", v, migrate.BaselineVersion)
	}
}

// TestDownRefusesAtBaseline proves a single-step Down at the floor is refused.
func TestDownRefusesAtBaseline(t *testing.T) {
	db, _ := testdb.New(t)
	if err := migrate.Up(db); err != nil {
		t.Fatalf("up: %v", err)
	}
	if err := migrate.DownTo(db, migrate.BaselineVersion); err != nil {
		t.Fatalf("down-to baseline: %v", err)
	}
	if err := migrate.Down(db); err == nil {
		t.Fatal("expected single-step down at baseline to be refused")
	} else if !strings.Contains(err.Error(), "refused") {
		t.Fatalf("error %q should mention 'refused'", err.Error())
	}
	v, err := migrate.Version(db)
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if v != migrate.BaselineVersion {
		t.Fatalf("version = %d, want %d (unchanged)", v, migrate.BaselineVersion)
	}
}
```

Add to `cmd/migrate/main_integration_test.go` (after `TestCLIUpAndStatus`):

```go
// TestCLIDownGuard proves the CLI refuses down-to below the baseline floor.
func TestCLIDownGuard(t *testing.T) {
	_, dsn := testdb.New(t)
	getenv := func(k string) string {
		if k == "POSTGRES_DSN" {
			return dsn
		}
		return ""
	}
	if err := run([]string{"up"}, getenv); err != nil {
		t.Fatalf("run up: %v", err)
	}
	if err := run([]string{"down-to", "10"}, getenv); err == nil {
		t.Fatal("expected down-to 10 to be refused below baseline")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run:
```bash
go test -tags=integration ./internal/migrate/ -run 'TestDownTo|TestDownRefuses' -v
go test -tags=integration ./cmd/migrate/ -run TestCLIDownGuard -v
```
Expected: `TestDownToRefusesBelowBaseline`, `TestDownRefusesAtBaseline`, and `TestCLIDownGuard` **FAIL** — the current `DownTo`/`Down` delegate straight to goose, so `down-to 10` silently runs the baseline no-op `Down`s and returns no error. `TestDownToBaselineAllowed` passes (it is already a no-op at head 11).

- [ ] **Step 3: Implement the guard**

In `internal/migrate/migrate.go`, add `"fmt"` to the import block:

```go
import (
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"

	"github.com/zakkriel/drchat-image-platform/migrations"
)
```

Replace the existing `Down` and `DownTo` functions with:

```go
// Down rolls back the most recently applied migration, refusing any step that
// would cross into or below the irreversible baseline floor (allowed only at
// v12+; a step from v11 would roll back a baseline migration). See
// docs/adr/ADR-P001-migration-tooling.md.
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
			"down refused: current version %d is at or below the irreversible "+
				"baseline floor %d; a single-step down would roll back a baseline "+
				"migration (restore from backup instead)", current, BaselineVersion)
	}
	return goose.Down(db, ".")
}

// DownTo rolls back to (and including) the given target version, refusing any
// target below the irreversible baseline floor. down-to 11 is allowed (goose
// leaves v11 applied); down-to 10 and below error. See
// docs/adr/ADR-P001-migration-tooling.md.
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
```

- [ ] **Step 4: Run the tests to verify they pass**

Run:
```bash
go test -tags=integration ./internal/migrate/ -run 'TestDownTo|TestDownRefuses' -v
go test -tags=integration ./cmd/migrate/ -run TestCLIDownGuard -v
go vet ./...
```
Expected: all PASS; `go vet` clean.

- [ ] **Step 5: Commit**

```bash
git add internal/migrate/migrate.go internal/migrate/migrate_integration_test.go cmd/migrate/main_integration_test.go
git commit -m "feat(migrate): guard down/down-to against crossing below baseline floor"
```

---

### Task 2: Migration 0012 — governance envelope on `generation_jobs`

Spec §5.0012. Also fixes the three baseline tests whose exact-head assertion breaks once head moves above 11.

**Files:**
- Create: `migrations/0012_governance_envelope.sql`
- Modify: `sqlc.yaml` (append the new schema file)
- Modify: `internal/db/dbgen/models.go` (regenerated, do not hand-edit)
- Modify: `internal/migrate/migrate_integration_test.go` (add presence test + a `columnExists` helper + relax three exact-head assertions)

**Interfaces:**
- Consumes: `generation_jobs` (from `0001`); `migrate.Up`, `migrate.BaselineVersion`, `testdb.New`.
- Produces: nullable columns `governance_envelope JSONB`, `classification_id TEXT`, `visibility TEXT`, `content_class TEXT`, `authorized_by TEXT`, `governance_verified_at TIMESTAMPTZ` on `generation_jobs`; the `columnExists(t, db, table, col) bool` helper consumed by Tasks 3–7.

- [ ] **Step 1: Write the failing test + helper, and relax the head assertions**

Add to `internal/migrate/migrate_integration_test.go`:

```go
// columnExists reports whether a column of the given name exists on a table in
// the public schema.
func columnExists(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(
		`SELECT count(*) FROM information_schema.columns
		 WHERE table_schema='public' AND table_name=$1 AND column_name=$2`,
		table, column).Scan(&n); err != nil {
		t.Fatalf("columnExists(%s.%s): %v", table, column, err)
	}
	return n > 0
}

// TestMigration0012Governance proves the governance envelope columns are applied.
func TestMigration0012Governance(t *testing.T) {
	db, _ := testdb.New(t)
	if err := migrate.Up(db); err != nil {
		t.Fatalf("up: %v", err)
	}
	for _, col := range []string{
		"governance_envelope", "classification_id", "visibility",
		"content_class", "authorized_by", "governance_verified_at",
	} {
		if !columnExists(t, db, "generation_jobs", col) {
			t.Fatalf("generation_jobs.%s missing after up", col)
		}
	}
}
```

In the same file, relax the exact-head assertion in **three** existing tests — `TestFreshUp`, `TestFreshBootstrap`, and `TestBaselineConvergence`. In each, replace:

```go
	if v != migrate.BaselineVersion {
		t.Fatalf("version = %d, want %d", v, migrate.BaselineVersion)
	}
```

with:

```go
	if v < migrate.BaselineVersion {
		t.Fatalf("version = %d, want >= baseline %d", v, migrate.BaselineVersion)
	}
```

(These tests assert the baseline is fully applied; once Chunk 1 migrations exist, `Up`/`Bootstrap` advance past 11, so the floor check becomes `>=`. The `baselineTables` existence checks in those tests stay unchanged.)

- [ ] **Step 2: Run the test to verify it fails**

Run:
```bash
go test -tags=integration ./internal/migrate/ -run TestMigration0012Governance -v
```
Expected: FAIL — `generation_jobs.governance_envelope missing after up` (migration does not exist yet).

- [ ] **Step 3: Create the migration**

Create `migrations/0012_governance_envelope.sql`:

```sql
-- +goose Up
-- Chunk 1 (Governance + Cost Contract): governance envelope fields on
-- generation_jobs. Expand-only — all columns nullable, no default, no backfill.
-- governance_envelope is a validated JSONB blob (rule D-4): JSONB + a
-- schema_version presence check. content_class is stored OPAQUE and never parsed
-- here (parsing/enforcement is Chunk 2+).
ALTER TABLE generation_jobs
    ADD COLUMN governance_envelope    JSONB,
    ADD COLUMN classification_id      TEXT,
    ADD COLUMN visibility             TEXT,
    ADD COLUMN content_class          TEXT,
    ADD COLUMN authorized_by          TEXT,
    ADD COLUMN governance_verified_at TIMESTAMPTZ,
    ADD CONSTRAINT generation_jobs_governance_envelope_schema_version
        CHECK (governance_envelope IS NULL OR jsonb_exists(governance_envelope, 'schema_version'));

-- +goose Down
ALTER TABLE generation_jobs
    DROP COLUMN IF EXISTS governance_envelope,
    DROP COLUMN IF EXISTS classification_id,
    DROP COLUMN IF EXISTS visibility,
    DROP COLUMN IF EXISTS content_class,
    DROP COLUMN IF EXISTS authorized_by,
    DROP COLUMN IF EXISTS governance_verified_at;
```

(The `CHECK` constraint is dropped automatically with `governance_envelope` — no explicit `DROP CONSTRAINT` needed.)

- [ ] **Step 4: Add the migration to sqlc and regenerate**

In `sqlc.yaml`, append under the `schema:` list, after `"migrations/0010_webhooks.sql"`:

```yaml
      - "migrations/0012_governance_envelope.sql"
```

Then regenerate:
```bash
make generate
```

- [ ] **Step 5: Run the test + verify codegen**

Run:
```bash
go test -tags=integration ./internal/migrate/ -run 'TestMigration0012Governance|TestFreshUp|TestFreshBootstrap|TestBaselineConvergence' -v
sqlc vet
go build ./...
git status --short
```
Expected: tests PASS; `sqlc vet` clean; build clean; `git status` shows only `migrations/0012_governance_envelope.sql`, `sqlc.yaml`, and regenerated `internal/db/dbgen/` files changed.

- [ ] **Step 6: Commit**

```bash
git add migrations/0012_governance_envelope.sql sqlc.yaml internal/db/dbgen/ internal/migrate/migrate_integration_test.go
git commit -m "feat(schema): 0012 governance envelope columns on generation_jobs"
```

---

### Task 3: Migration 0013 — cost-routing on `generation_jobs`

Spec §5.0013.

**Files:**
- Create: `migrations/0013_cost_routing.sql`
- Modify: `sqlc.yaml`
- Modify: `internal/db/dbgen/` (regenerated)
- Test: `internal/migrate/migrate_integration_test.go`

**Interfaces:**
- Consumes: `generation_jobs`; `columnExists` (Task 2); `migrate.Up`, `testdb.New`.
- Produces: nullable columns `intent TEXT` (`CHECK intent IN ('draft','commit')`), `transform_only BOOLEAN`, `transform JSONB`, `max_megapixels NUMERIC(6,2)`, `lazy BOOLEAN` on `generation_jobs`.

- [ ] **Step 1: Write the failing test**

Add to `internal/migrate/migrate_integration_test.go`:

```go
// TestMigration0013CostRouting proves the cost-routing columns are applied.
func TestMigration0013CostRouting(t *testing.T) {
	db, _ := testdb.New(t)
	if err := migrate.Up(db); err != nil {
		t.Fatalf("up: %v", err)
	}
	for _, col := range []string{
		"intent", "transform_only", "transform", "max_megapixels", "lazy",
	} {
		if !columnExists(t, db, "generation_jobs", col) {
			t.Fatalf("generation_jobs.%s missing after up", col)
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run:
```bash
go test -tags=integration ./internal/migrate/ -run TestMigration0013CostRouting -v
```
Expected: FAIL — `generation_jobs.intent missing after up`.

- [ ] **Step 3: Create the migration**

Create `migrations/0013_cost_routing.sql`:

```sql
-- +goose Up
-- Chunk 1: cost-routing fields on generation_jobs. Expand-only — all nullable,
-- no default. intent is constrained to the known (draft|commit) vocabulary;
-- transform is a validated JSONB blob (rule D-4).
ALTER TABLE generation_jobs
    ADD COLUMN intent         TEXT CHECK (intent IS NULL OR intent IN ('draft', 'commit')),
    ADD COLUMN transform_only BOOLEAN,
    ADD COLUMN transform      JSONB,
    ADD COLUMN max_megapixels NUMERIC(6, 2),
    ADD COLUMN lazy           BOOLEAN,
    ADD CONSTRAINT generation_jobs_transform_schema_version
        CHECK (transform IS NULL OR jsonb_exists(transform, 'schema_version'));

-- +goose Down
ALTER TABLE generation_jobs
    DROP COLUMN IF EXISTS intent,
    DROP COLUMN IF EXISTS transform_only,
    DROP COLUMN IF EXISTS transform,
    DROP COLUMN IF EXISTS max_megapixels,
    DROP COLUMN IF EXISTS lazy;
```

- [ ] **Step 4: Add to sqlc and regenerate**

Append to `sqlc.yaml` schema list:
```yaml
      - "migrations/0013_cost_routing.sql"
```
Then:
```bash
make generate
```

- [ ] **Step 5: Run the test + verify codegen**

Run:
```bash
go test -tags=integration ./internal/migrate/ -run TestMigration0013CostRouting -v
sqlc vet
go build ./...
git status --short
```
Expected: PASS; `sqlc vet` clean; build clean; only the migration, `sqlc.yaml`, and `internal/db/dbgen/` changed.

- [ ] **Step 6: Commit**

```bash
git add migrations/0013_cost_routing.sql sqlc.yaml internal/db/dbgen/ internal/migrate/migrate_integration_test.go
git commit -m "feat(schema): 0013 cost-routing columns on generation_jobs"
```

---

### Task 4: Migration 0014 — anchor lineage on `visual_assets` and `generation_jobs`

Spec §5.0014. `derive_from` is deferred (nullable `TEXT`, no FK — target type is router-defined in Chunk 2).

**Files:**
- Create: `migrations/0014_anchor_lineage.sql`
- Modify: `sqlc.yaml`
- Modify: `internal/db/dbgen/` (regenerated)
- Test: `internal/migrate/migrate_integration_test.go`

**Interfaces:**
- Consumes: `visual_assets`, `generation_jobs`; `columnExists`.
- Produces: nullable `anchor_asset_id TEXT REFERENCES visual_assets(id)` and `derive_from TEXT` on **both** `visual_assets` and `generation_jobs`.

- [ ] **Step 1: Write the failing test**

Add to `internal/migrate/migrate_integration_test.go`:

```go
// TestMigration0014AnchorLineage proves anchor lineage columns land on both
// visual_assets and generation_jobs.
func TestMigration0014AnchorLineage(t *testing.T) {
	db, _ := testdb.New(t)
	if err := migrate.Up(db); err != nil {
		t.Fatalf("up: %v", err)
	}
	for _, tbl := range []string{"visual_assets", "generation_jobs"} {
		for _, col := range []string{"anchor_asset_id", "derive_from"} {
			if !columnExists(t, db, tbl, col) {
				t.Fatalf("%s.%s missing after up", tbl, col)
			}
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run:
```bash
go test -tags=integration ./internal/migrate/ -run TestMigration0014AnchorLineage -v
```
Expected: FAIL — `visual_assets.anchor_asset_id missing after up`.

- [ ] **Step 3: Create the migration**

Create `migrations/0014_anchor_lineage.sql`:

```sql
-- +goose Up
-- Chunk 1: anchor lineage on the asset and job tables. Expand-only, nullable.
-- anchor_asset_id is a scalar "derived-from-this-anchor" FK to visual_assets
-- (distinct from the existing visual_identities.anchor_asset_ids ARRAY).
-- derive_from is a deferred soft reference (no FK): its target type (asset vs
-- job) is resolved by the Chunk 2 router. Existing rows are NULL, so the FK and
-- the new columns validate trivially at add time (no backfill).
ALTER TABLE visual_assets
    ADD COLUMN anchor_asset_id TEXT REFERENCES visual_assets(id),
    ADD COLUMN derive_from     TEXT;

ALTER TABLE generation_jobs
    ADD COLUMN anchor_asset_id TEXT REFERENCES visual_assets(id),
    ADD COLUMN derive_from     TEXT;

-- +goose Down
ALTER TABLE generation_jobs
    DROP COLUMN IF EXISTS anchor_asset_id,
    DROP COLUMN IF EXISTS derive_from;

ALTER TABLE visual_assets
    DROP COLUMN IF EXISTS anchor_asset_id,
    DROP COLUMN IF EXISTS derive_from;
```

- [ ] **Step 4: Add to sqlc and regenerate**

Append to `sqlc.yaml` schema list:
```yaml
      - "migrations/0014_anchor_lineage.sql"
```
Then:
```bash
make generate
```

- [ ] **Step 5: Run the test + verify codegen**

Run:
```bash
go test -tags=integration ./internal/migrate/ -run TestMigration0014AnchorLineage -v
sqlc vet
go build ./...
git status --short
```
Expected: PASS; `sqlc vet` clean; build clean; only the migration, `sqlc.yaml`, and `internal/db/dbgen/` changed.

- [ ] **Step 6: Commit**

```bash
git add migrations/0014_anchor_lineage.sql sqlc.yaml internal/db/dbgen/ internal/migrate/migrate_integration_test.go
git commit -m "feat(schema): 0014 anchor lineage on visual_assets + generation_jobs"
```

---

### Task 5: Migration 0015 — grid columns + sprite-sheet tables

Spec §5.0015. Note the deviation from the spec's `rows`/`cols`: they are named `row_count`/`col_count` here to avoid the SQL `ROWS` keyword and read clearer.

**Files:**
- Create: `migrations/0015_grid_and_sprite_sheets.sql`
- Modify: `sqlc.yaml`
- Modify: `internal/db/dbgen/` (regenerated)
- Test: `internal/migrate/migrate_integration_test.go`

**Interfaces:**
- Consumes: `visual_assets`, `visual_identities`, `generation_jobs`; `columnExists`, `testdb.TableExists`.
- Produces: nullable `parent_asset_id TEXT REFERENCES visual_assets(id)`, `crop_index INT`, `crop_box JSONB`, `expression_key TEXT` on `visual_assets`; tables `sprite_sheet_contract` (direct tenant table, has `tenant_id`) and `sprite_sheet_slice` (child of `sprite_sheet_contract` via `sprite_sheet_contract_id`). Consumed by Task 7 (round-trip) and Task 7's RLS migration.

- [ ] **Step 1: Write the failing test**

Add to `internal/migrate/migrate_integration_test.go`:

```go
// TestMigration0015GridAndSpriteSheets proves the grid columns and the two new
// sprite-sheet tables are applied.
func TestMigration0015GridAndSpriteSheets(t *testing.T) {
	db, _ := testdb.New(t)
	if err := migrate.Up(db); err != nil {
		t.Fatalf("up: %v", err)
	}
	for _, col := range []string{"parent_asset_id", "crop_index", "crop_box", "expression_key"} {
		if !columnExists(t, db, "visual_assets", col) {
			t.Fatalf("visual_assets.%s missing after up", col)
		}
	}
	for _, tbl := range []string{"sprite_sheet_contract", "sprite_sheet_slice"} {
		if !testdb.TableExists(t, db, tbl) {
			t.Fatalf("table %s missing after up", tbl)
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run:
```bash
go test -tags=integration ./internal/migrate/ -run TestMigration0015GridAndSpriteSheets -v
```
Expected: FAIL — `visual_assets.parent_asset_id missing after up`.

- [ ] **Step 3: Create the migration**

Create `migrations/0015_grid_and_sprite_sheets.sql`:

```sql
-- +goose Up
-- Chunk 1: grid parent/child fields on visual_assets plus the sprite-sheet
-- contract/slice tables. Expand-only for visual_assets (nullable columns). The
-- two new tables are tenant-scoped; their RLS lands in 0017 (kept out of sqlc,
-- like 0009). crop_box / contract are validated JSONB blobs (rule D-4).
-- row_count / col_count avoid the SQL ROWS keyword.
ALTER TABLE visual_assets
    ADD COLUMN parent_asset_id TEXT REFERENCES visual_assets(id),
    ADD COLUMN crop_index      INT,
    ADD COLUMN crop_box        JSONB,
    ADD COLUMN expression_key  TEXT,
    ADD CONSTRAINT visual_assets_crop_box_schema_version
        CHECK (crop_box IS NULL OR jsonb_exists(crop_box, 'schema_version'));

CREATE TABLE sprite_sheet_contract (
    id                  TEXT PRIMARY KEY,
    tenant_id           TEXT NOT NULL,
    world_id            TEXT,
    visual_identity_id  TEXT REFERENCES visual_identities(id),
    sheet_asset_id      TEXT REFERENCES visual_assets(id),     -- composite sheet image
    generation_job_id   TEXT REFERENCES generation_jobs(id),   -- producing job
    row_count           INT,
    col_count           INT,
    contract            JSONB,
    status              TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT sprite_sheet_contract_schema_version
        CHECK (contract IS NULL OR jsonb_exists(contract, 'schema_version'))
);

CREATE TABLE sprite_sheet_slice (
    id                        TEXT PRIMARY KEY,
    sprite_sheet_contract_id  TEXT NOT NULL REFERENCES sprite_sheet_contract(id) ON DELETE CASCADE,
    crop_index                INT,
    crop_box                  JSONB,
    expression_key            TEXT,
    asset_id                  TEXT REFERENCES visual_assets(id),  -- sliced child asset (nullable until sliced)
    created_at                TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT sprite_sheet_slice_crop_box_schema_version
        CHECK (crop_box IS NULL OR jsonb_exists(crop_box, 'schema_version'))
);

CREATE INDEX idx_sprite_sheet_slice_contract ON sprite_sheet_slice (sprite_sheet_contract_id);

-- +goose Down
DROP TABLE IF EXISTS sprite_sheet_slice;
DROP TABLE IF EXISTS sprite_sheet_contract;

ALTER TABLE visual_assets
    DROP COLUMN IF EXISTS parent_asset_id,
    DROP COLUMN IF EXISTS crop_index,
    DROP COLUMN IF EXISTS crop_box,
    DROP COLUMN IF EXISTS expression_key;
```

- [ ] **Step 4: Add to sqlc and regenerate**

Append to `sqlc.yaml` schema list:
```yaml
      - "migrations/0015_grid_and_sprite_sheets.sql"
```
Then:
```bash
make generate
```

- [ ] **Step 5: Run the test + verify codegen**

Run:
```bash
go test -tags=integration ./internal/migrate/ -run TestMigration0015GridAndSpriteSheets -v
sqlc vet
go build ./...
git status --short
```
Expected: PASS; `sqlc vet` clean; build clean. `internal/db/dbgen/models.go` now has `SpriteSheetContract` and `SpriteSheetSlice` model structs plus the new `VisualAsset` fields.

- [ ] **Step 6: Commit**

```bash
git add migrations/0015_grid_and_sprite_sheets.sql sqlc.yaml internal/db/dbgen/ internal/migrate/migrate_integration_test.go
git commit -m "feat(schema): 0015 grid columns + sprite_sheet_contract/slice tables"
```

---

### Task 6: Migration 0016 — `identity_cost_ledger`

Spec §5.0016. New table; `cost_reservations` is untouched.

**Files:**
- Create: `migrations/0016_identity_cost_ledger.sql`
- Modify: `sqlc.yaml`
- Modify: `internal/db/dbgen/` (regenerated)
- Test: `internal/migrate/migrate_integration_test.go`

**Interfaces:**
- Consumes: `visual_identities`; `testdb.TableExists`, `columnExists`.
- Produces: table `identity_cost_ledger` (direct tenant table, `tenant_id NOT NULL`, `UNIQUE(visual_identity_id)`, `cost_estimated_total`/`cost_actual_total NUMERIC(14,4) NOT NULL DEFAULT 0`). Consumed by Task 7.

- [ ] **Step 1: Write the failing test**

Add to `internal/migrate/migrate_integration_test.go`:

```go
// TestMigration0016IdentityCostLedger proves the per-identity cost ledger table
// is applied with its accumulator columns.
func TestMigration0016IdentityCostLedger(t *testing.T) {
	db, _ := testdb.New(t)
	if err := migrate.Up(db); err != nil {
		t.Fatalf("up: %v", err)
	}
	if !testdb.TableExists(t, db, "identity_cost_ledger") {
		t.Fatal("identity_cost_ledger missing after up")
	}
	for _, col := range []string{
		"tenant_id", "visual_identity_id", "cost_estimated_total", "cost_actual_total",
	} {
		if !columnExists(t, db, "identity_cost_ledger", col) {
			t.Fatalf("identity_cost_ledger.%s missing after up", col)
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run:
```bash
go test -tags=integration ./internal/migrate/ -run TestMigration0016IdentityCostLedger -v
```
Expected: FAIL — `identity_cost_ledger missing after up`.

- [ ] **Step 3: Create the migration**

Create `migrations/0016_identity_cost_ledger.sql`:

```sql
-- +goose Up
-- Chunk 1: per-identity lifetime-cost accumulator. One row per visual_identity.
-- The estimated-vs-actual distinction lives in the two *_total columns;
-- cost_reservations is intentionally unchanged (its estimated_amount/actual_amount
-- already encode that distinction at the reservation grain). Tenant-scoped; RLS
-- lands in 0017.
CREATE TABLE identity_cost_ledger (
    id                    TEXT PRIMARY KEY,
    tenant_id             TEXT NOT NULL,
    visual_identity_id    TEXT NOT NULL REFERENCES visual_identities(id),
    cost_estimated_total  NUMERIC(14, 4) NOT NULL DEFAULT 0,
    cost_actual_total     NUMERIC(14, 4) NOT NULL DEFAULT 0,
    currency              TEXT NOT NULL DEFAULT 'USD' CHECK (char_length(currency) = 3),
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (visual_identity_id)
);

-- +goose Down
DROP TABLE IF EXISTS identity_cost_ledger;
```

- [ ] **Step 4: Add to sqlc and regenerate**

Append to `sqlc.yaml` schema list:
```yaml
      - "migrations/0016_identity_cost_ledger.sql"
```
Then:
```bash
make generate
```

- [ ] **Step 5: Run the test + verify codegen**

Run:
```bash
go test -tags=integration ./internal/migrate/ -run TestMigration0016IdentityCostLedger -v
sqlc vet
go build ./...
git status --short
```
Expected: PASS; `sqlc vet` clean; build clean; `internal/db/dbgen/models.go` gains an `IdentityCostLedger` struct.

- [ ] **Step 6: Commit**

```bash
git add migrations/0016_identity_cost_ledger.sql sqlc.yaml internal/db/dbgen/ internal/migrate/migrate_integration_test.go
git commit -m "feat(schema): 0016 identity_cost_ledger accumulator table"
```

---

### Task 7: Migration 0017 — RLS for the new tables + full round-trip proof

Spec §3 / §5.0017 / §7. **`0017` is NOT added to `sqlc.yaml`** (RLS-only, same exclusion as `0009`). This task also adds the comprehensive reversibility test now that the whole stack exists.

**Files:**
- Create: `migrations/0017_new_table_rls.sql`
- Modify: `internal/migrate/migrate_integration_test.go` (RLS presence test + round-trip test + two helpers)
- **Do NOT modify `sqlc.yaml`.**

**Interfaces:**
- Consumes: `sprite_sheet_contract`, `sprite_sheet_slice`, `identity_cost_ledger` (Tasks 5–6); `migrate.Up`, `migrate.DownTo`, `migrate.Version`, `migrate.BaselineVersion`, `columnExists`, `testdb.TableExists`.
- Produces: `tenant_isolation` RLS policy (ENABLE + FORCE) on the three new tables — direct-tenant for `sprite_sheet_contract`/`identity_cost_ledger`, parent-join for `sprite_sheet_slice`. A reversible `Down` removing all three.

- [ ] **Step 1: Write the failing tests + helpers**

Add to `internal/migrate/migrate_integration_test.go`:

```go
// rlsForced reports whether a table has ROW LEVEL SECURITY both enabled and forced.
func rlsForced(t *testing.T, db *sql.DB, table string) bool {
	t.Helper()
	var forced bool
	if err := db.QueryRow(
		`SELECT relrowsecurity AND relforcerowsecurity FROM pg_class WHERE relname=$1`,
		table).Scan(&forced); err != nil {
		t.Fatalf("rlsForced(%s): %v", table, err)
	}
	return forced
}

// policyExists reports whether a named policy exists on a table.
func policyExists(t *testing.T, db *sql.DB, table, policy string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(
		`SELECT count(*) FROM pg_policies WHERE tablename=$1 AND policyname=$2`,
		table, policy).Scan(&n); err != nil {
		t.Fatalf("policyExists(%s,%s): %v", table, policy, err)
	}
	return n > 0
}

// TestMigration0017NewTableRLS proves RLS is enabled+forced with a tenant_isolation
// policy on each of the three new tables.
func TestMigration0017NewTableRLS(t *testing.T) {
	db, _ := testdb.New(t)
	if err := migrate.Up(db); err != nil {
		t.Fatalf("up: %v", err)
	}
	for _, tbl := range []string{"sprite_sheet_contract", "sprite_sheet_slice", "identity_cost_ledger"} {
		if !rlsForced(t, db, tbl) {
			t.Fatalf("%s is not RLS enabled+forced", tbl)
		}
		if !policyExists(t, db, tbl, "tenant_isolation") {
			t.Fatalf("%s missing tenant_isolation policy", tbl)
		}
	}
}

// TestChunk1RoundTrip proves the whole post-baseline stack reverses: up applies
// every Chunk 1 object, down-to 11 removes them and returns to the baseline, and
// up restores them. This is the harness's first real reversibility proof.
func TestChunk1RoundTrip(t *testing.T) {
	db, _ := testdb.New(t)
	if err := migrate.Up(db); err != nil {
		t.Fatalf("up: %v", err)
	}
	head, err := migrate.Version(db)
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if head <= migrate.BaselineVersion {
		t.Fatalf("head %d should be above baseline %d", head, migrate.BaselineVersion)
	}

	cols := []struct{ table, col string }{
		{"generation_jobs", "governance_envelope"},
		{"generation_jobs", "intent"},
		{"generation_jobs", "anchor_asset_id"},
		{"visual_assets", "anchor_asset_id"},
		{"visual_assets", "parent_asset_id"},
		{"visual_assets", "crop_box"},
	}
	tables := []string{"sprite_sheet_contract", "sprite_sheet_slice", "identity_cost_ledger"}
	assertPresent := func(want bool) {
		for _, c := range cols {
			if got := columnExists(t, db, c.table, c.col); got != want {
				t.Fatalf("%s.%s exists=%v, want %v", c.table, c.col, got, want)
			}
		}
		for _, tbl := range tables {
			if got := testdb.TableExists(t, db, tbl); got != want {
				t.Fatalf("table %s exists=%v, want %v", tbl, got, want)
			}
		}
	}

	assertPresent(true)

	if err := migrate.DownTo(db, migrate.BaselineVersion); err != nil {
		t.Fatalf("down-to baseline: %v", err)
	}
	if v, _ := migrate.Version(db); v != migrate.BaselineVersion {
		t.Fatalf("after down-to: version %d, want %d", v, migrate.BaselineVersion)
	}
	assertPresent(false)

	if err := migrate.Up(db); err != nil {
		t.Fatalf("re-up: %v", err)
	}
	if v, _ := migrate.Version(db); v != head {
		t.Fatalf("after re-up: version %d, want %d", v, head)
	}
	assertPresent(true)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run:
```bash
go test -tags=integration ./internal/migrate/ -run 'TestMigration0017NewTableRLS|TestChunk1RoundTrip' -v
```
Expected: both FAIL — `sprite_sheet_contract is not RLS enabled+forced` (0017 missing); the round-trip's `assertPresent(false)` fails because, without 0017, `down-to 11` cannot run (the new tables exist but no migration removes their RLS)… actually it fails at the RLS assertion first. Either way: RED.

- [ ] **Step 3: Create the migration**

Create `migrations/0017_new_table_rls.sql`:

```sql
-- +goose Up
-- Chunk 1: RLS for the three new tenant-scoped tables, matching the platform
-- pattern established in 0009. Kept in its own migration and intentionally
-- EXCLUDED from sqlc.yaml (sqlc does not need policy DDL — same reason 0009 is
-- excluded). Unlike the irreversible baseline RLS migration, this one is fully
-- reversible (real Down below).
--
-- Direct tenant tables (own tenant_id): sprite_sheet_contract, identity_cost_ledger.
-- Child table (ownership via parent FK): sprite_sheet_slice -> sprite_sheet_contract.

ALTER TABLE sprite_sheet_contract ENABLE ROW LEVEL SECURITY;
ALTER TABLE sprite_sheet_contract FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON sprite_sheet_contract
    USING (tenant_id = NULLIF(current_setting('app.current_tenant', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.current_tenant', true), ''));

ALTER TABLE identity_cost_ledger ENABLE ROW LEVEL SECURITY;
ALTER TABLE identity_cost_ledger FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON identity_cost_ledger
    USING (tenant_id = NULLIF(current_setting('app.current_tenant', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.current_tenant', true), ''));

ALTER TABLE sprite_sheet_slice ENABLE ROW LEVEL SECURITY;
ALTER TABLE sprite_sheet_slice FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON sprite_sheet_slice
    USING (EXISTS (
        SELECT 1 FROM sprite_sheet_contract p
        WHERE p.id = sprite_sheet_slice.sprite_sheet_contract_id
          AND p.tenant_id = NULLIF(current_setting('app.current_tenant', true), '')))
    WITH CHECK (EXISTS (
        SELECT 1 FROM sprite_sheet_contract p
        WHERE p.id = sprite_sheet_slice.sprite_sheet_contract_id
          AND p.tenant_id = NULLIF(current_setting('app.current_tenant', true), '')));

-- +goose Down
DROP POLICY IF EXISTS tenant_isolation ON sprite_sheet_slice;
ALTER TABLE sprite_sheet_slice NO FORCE ROW LEVEL SECURITY;
ALTER TABLE sprite_sheet_slice DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation ON identity_cost_ledger;
ALTER TABLE identity_cost_ledger NO FORCE ROW LEVEL SECURITY;
ALTER TABLE identity_cost_ledger DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation ON sprite_sheet_contract;
ALTER TABLE sprite_sheet_contract NO FORCE ROW LEVEL SECURITY;
ALTER TABLE sprite_sheet_contract DISABLE ROW LEVEL SECURITY;
```

(Every statement is a single SQL statement with no embedded `;`, so no `-- +goose StatementBegin/End` annotations are needed — unlike `0009`'s `DO $$` blocks.)

- [ ] **Step 4: Confirm sqlc is untouched, then run the tests**

Confirm `0017` is NOT in `sqlc.yaml`:
```bash
grep -c 0017 sqlc.yaml   # expected: 0
```

Run:
```bash
go test -tags=integration ./internal/migrate/ -run 'TestMigration0017NewTableRLS|TestChunk1RoundTrip' -v
```
Expected: both PASS.

- [ ] **Step 5: Run the full migrate suite + confirm no codegen drift**

Run:
```bash
go test -tags=integration ./internal/migrate/ ./cmd/migrate/ -v
make generate
git status --short   # expected: only migrations/0017_new_table_rls.sql + test file; NO dbgen changes
```
Expected: all migrate/CLI tests PASS; `make generate` produces no new diff (0017 is not a sqlc input).

- [ ] **Step 6: Commit**

```bash
git add migrations/0017_new_table_rls.sql internal/migrate/migrate_integration_test.go
git commit -m "feat(schema): 0017 reversible RLS for sprite-sheet + cost-ledger tables"
```

---

### Task 8: CI round-trip gate, table-count/version updates, RLS assertions

Spec §6. Update the `migrations` job in `.github/workflows/ci.yml`. The new head is **17** and the post-Chunk-1 table count is **24** (21 baseline incl. `goose_db_version` + 3 new tables).

**Files:**
- Modify: `.github/workflows/ci.yml` (the `migrations` job)

**Interfaces:**
- Consumes: `go run ./cmd/migrate up|down-to`, the guard from Task 1, all migrations from Tasks 2–7.
- Produces: CI proof of `up → down-to 11 → up`, the down-guard negative check, updated counts/version, and RLS assertions for the new tables.

- [ ] **Step 1: Update the head-version and table-count assertions**

In `.github/workflows/ci.yml`, in the step **"assert all expected tables exist"**, replace the `test "$count" = "21"` line (and its comment) with:

```bash
          # 21 baseline objects (20 tables + goose_db_version) + 3 Chunk 1 tables
          # (sprite_sheet_contract, sprite_sheet_slice, identity_cost_ledger) = 24.
          test "$count" = "24"
```

In the step **"assert goose version is the baseline head"**, change the comparison from `test "$v" = "11"` to:

```bash
          # Chunk 1 added migrations 0012-0017; head is now 17.
          test "$v" = "17"
```

(Rename the step label to `assert goose version is the head` if desired — optional.)

- [ ] **Step 2: Add the round-trip + down-guard steps**

In `.github/workflows/ci.yml`, immediately AFTER the **"assert goose version is the baseline head"** step, insert:

```yaml
      - name: assert migrations round-trip above baseline (up -> down-to 11 -> up)
        env:
          POSTGRES_DSN: postgres://image_platform:image_platform@localhost:5432/image_platform?sslmode=disable
          PGPASSWORD: image_platform
        run: |
          set -euo pipefail
          go run ./cmd/migrate down-to 11
          v=$(psql -h localhost -U image_platform -d image_platform -tAc \
            "SELECT max(version_id) FROM goose_db_version WHERE is_applied")
          test "$v" = "11" || { echo "ERROR: after down-to, version=$v (want 11)"; exit 1; }
          count=$(psql -h localhost -U image_platform -d image_platform -tAc \
            "SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_type='BASE TABLE'")
          test "$count" = "21" || { echo "ERROR: after down-to, table count=$count (want 21)"; exit 1; }
          for tbl in sprite_sheet_contract sprite_sheet_slice identity_cost_ledger; do
            n=$(psql -h localhost -U image_platform -d image_platform -tAc \
              "SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_name='$tbl'")
            test "$n" = "0" || { echo "ERROR: $tbl still present after down-to 11"; exit 1; }
          done
          go run ./cmd/migrate up
          v=$(psql -h localhost -U image_platform -d image_platform -tAc \
            "SELECT max(version_id) FROM goose_db_version WHERE is_applied")
          test "$v" = "17" || { echo "ERROR: after re-up, version=$v (want 17)"; exit 1; }
          count=$(psql -h localhost -U image_platform -d image_platform -tAc \
            "SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_type='BASE TABLE'")
          test "$count" = "24" || { echo "ERROR: after re-up, table count=$count (want 24)"; exit 1; }

      - name: assert down-guard refuses crossing below baseline
        env:
          POSTGRES_DSN: postgres://image_platform:image_platform@localhost:5432/image_platform?sslmode=disable
        run: |
          if go run ./cmd/migrate down-to 10; then
            echo "ERROR: down-to 10 was allowed (should be refused below baseline)"; exit 1
          fi
          echo "down-to 10 correctly refused"

      - name: assert RLS enabled+forced on Chunk 1 tables
        env:
          PGPASSWORD: image_platform
        run: |
          for tbl in sprite_sheet_contract sprite_sheet_slice identity_cost_ledger; do
            state=$(psql -h localhost -U image_platform -d image_platform -tAc \
              "SELECT (relrowsecurity AND relforcerowsecurity)::int FROM pg_class WHERE relname = '$tbl'")
            test "$state" = "1" || { echo "ERROR: $tbl not RLS enabled+forced (state=$state)"; exit 1; }
            pol=$(psql -h localhost -U image_platform -d image_platform -tAc \
              "SELECT count(*) FROM pg_policies WHERE tablename = '$tbl' AND policyname = 'tenant_isolation'")
            test "$pol" = "1" || { echo "ERROR: $tbl missing tenant_isolation policy"; exit 1; }
          done
```

- [ ] **Step 3: Validate the CLI flow locally (CI parity)**

Against the local Postgres (fresh DB recommended), confirm the exact commands CI runs:
```bash
go run ./cmd/migrate up
go run ./cmd/migrate down-to 11
go run ./cmd/migrate up
if go run ./cmd/migrate down-to 10; then echo "BUG: should have failed"; else echo "guard ok"; fi
```
Expected: the three real commands succeed; `down-to 10` exits non-zero ("guard ok"). (Use a throwaway DB or your dev DB; this mutates schema.)

- [ ] **Step 4: Sanity-check the YAML**

Run:
```bash
python3 -c "import yaml,sys; yaml.safe_load(open('.github/workflows/ci.yml')); print('yaml ok')"
```
Expected: `yaml ok`.

- [ ] **Step 5: Commit**

```bash
git add .github/workflows/ci.yml
git commit -m "ci(migrations): round-trip up->down-to 11->up, down-guard, RLS asserts (count 21->24, head 17)"
```

---

### Task 9: Correct ADR-P001 (D-9)

Spec §7 / §8. ADR-P001 currently claims reversibility is *not* yet CI-gated and that no post-baseline migration exists. Chunk 1 makes both false; correct them and cite the proving code.

**Files:**
- Modify: `docs/adr/ADR-P001-migration-tooling.md`

**Interfaces:**
- Consumes: nothing (doc-only).
- Produces: an accurate reversibility statement citing `internal/migrate/migrate.go`, migrations `0012`–`0017`, and the CI round-trip step.

- [ ] **Step 1: Update the reversibility bullet**

In `docs/adr/ADR-P001-migration-tooling.md`, under **## Policy for future schema changes**, replace this bullet:

```markdown
- **Reversibility:** every new migration added from Chunk 1 onward MUST ship a
  real, tested `Down`. Once the first post-baseline migration lands, CI will gate
  the round-trip `up → down-to 11 → up` on everything above the baseline. (No
  such migration exists yet, so the harness's reversibility is currently proven
  by the `TestGooseRoundTrip` canary, not by a CI step over the real
  migrations.)
```

with:

```markdown
- **Reversibility:** every new migration added from Chunk 1 onward MUST ship a
  real, tested `Down`. As of Chunk 1 this is enforced end-to-end: the
  `down`/`down-to` floor guard (`internal/migrate/migrate.go`) refuses any target
  below version 11, and CI gates the round-trip `up → down-to 11 → up` over every
  post-baseline migration (`.github/workflows/ci.yml`, the `migrations` job).
  Migrations `0012`–`0017` (governance + cost schema) are the first to exercise
  this, with reversibility also covered by `TestChunk1RoundTrip` in
  `internal/migrate/migrate_integration_test.go`. The `TestGooseRoundTrip` canary
  remains as a harness-level smoke test.
```

- [ ] **Step 2: Note the post-Chunk-1 table count in Consequences**

In the **## Consequences** section, after the sentence ending `the base table count is 20 + goose_db_version = 21.`, append:

```markdown
 Chunk 1 adds three tables (`sprite_sheet_contract`, `sprite_sheet_slice`,
`identity_cost_ledger`), so CI asserts 24 post-Chunk-1 and 21 again after
`down-to 11`.
```

- [ ] **Step 3: Verify the cited code exists**

Run:
```bash
grep -n "func DownTo" internal/migrate/migrate.go
grep -n "func TestChunk1RoundTrip" internal/migrate/migrate_integration_test.go
ls migrations/0012_governance_envelope.sql migrations/0017_new_table_rls.sql
```
Expected: each cited symbol/file is found (the ADR cites only real, present code — D-9).

- [ ] **Step 4: Commit**

```bash
git add docs/adr/ADR-P001-migration-tooling.md
git commit -m "docs(adr): ADR-P001 reversibility now CI-enforced over migrations 0012-0017 [D-9]"
```

---

## Final verification (before opening the PR)

- [ ] **Run the full integration suite locally** (with `POSTGRES_DSN` set):

```bash
go test -tags=integration ./... 2>&1 | tail -30
go vet ./...
go build ./...
make generate && git diff --exit-code   # zero codegen drift
sqlc vet
```
Expected: all green; `git diff --exit-code` returns 0 (everything committed).

- [ ] **Push the branch** (`chunk1-governance-cost-schema`) and open one PR. The PR description must cite **D-4**, **D-9**, and **ADR-P001**, and note this is Chunk 1 of the Combined Governance Envelope + Cost-Optimization program (schema only, expand-only, reversible).
- [ ] Confirm CI is green — especially the `migrations` job's round-trip, down-guard, and RLS steps, and the `go`/`sqlc` jobs' zero-drift checks.

## Definition of done (from spec §9)

- Down-guard added with tests proving `down-to <11` and single-step `down` at the floor error, and `down-to 11` succeeds.
- Migrations `0012`–`0017` land, each with a real `-- +goose Down`.
- sqlc regenerated; `git diff --exit-code` clean in CI.
- CI proves `up → down-to 11 → up`, asserts head 17 / 24 tables and floor 11 / 21 tables, and proves `down-to 10` is refused.
- The three new tables are RLS-enabled+forced with `tenant_isolation` policies (direct for `sprite_sheet_contract` / `identity_cost_ledger`, parent-join for `sprite_sheet_slice`).
- ADR-P001 corrected; PR cites rule IDs.
