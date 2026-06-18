# Chunk 0 — Migration Tooling (goose) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the bare `psql` migration loop and the no-version-tracking `cmd/migrate` runner with a goose-backed harness that tracks versions, runs each migration in its own transaction, proves reversibility of new migrations in CI, and lets already-migrated databases converge non-destructively.

**Architecture:** A new `internal/migrate` package wraps goose's legacy global API over the embedded `migrations.FS`, exposing `Up/Down/DownTo/Status/Version/Bootstrap`. goose drives schema via `database/sql` using the pgx stdlib adapter. The existing 11 migrations are mechanically reformatted to goose single-file format and frozen as an **irreversible baseline floor** (versions 1–11); real reversibility is required only from Chunk 1 onward. `cmd/migrate` becomes a thin CLI over the package, keeping the single embedded binary intact for Railway.

**Tech Stack:** Go 1.25, `github.com/pressly/goose/v3` (library, not CLI), `github.com/jackc/pgx/v5/stdlib` (database/sql driver), Postgres 15, existing `sqlc` + `oapi-codegen` codegen.

## Global Constraints

- **No schema changes in this chunk.** Tooling only. The existing migration SQL is reformatted, never semantically altered. (Spec §1, §9.)
- **Tool = goose**, used as a Go **library** (no goose CLI dependency). (Spec §3.)
- **Baseline floor:** migrations 1–11 are frozen and irreversible; their goose `Down` is a guarded no-op. Every migration from Chunk 1 onward MUST ship a real, tested `Down`. (Spec §5.)
- **`BaselineVersion = 11` is a fixed constant — never increment it.** `bootstrap` stamps exactly versions 1–11; later migrations are applied by `Up`, never stamped. (Spec §4.)
- **Version table is goose-native `goose_db_version`** — do not alias to `schema_migrations`. (Spec §3, flagged deviation, accepted.)
- **Bootstrap detection is a full-footprint, database-local check** (20 baseline tables + the `0011` fal seed sentinel). Zero tables → fresh; all present → stamp; anything in between → **refuse loudly, stamp nothing**. Roles are deliberately excluded from the discriminator (they are cluster-global and would misclassify a fresh DB). (Spec §4.)
- **TDD iron law:** failing test first, every task. Integration tests are `//go:build integration`, package `*_test`, skip when `POSTGRES_DSN` is unset, and run via `go test -tags=integration ./...`.
- **Rules:** D-5 (new ADR numbered `ADR-P001`), D-6 (docs under `/docs`), D-9 (doc/comment edits cite the proving file/line). No conflict with D-3/E-1/D-4/D-8 (tooling-only).
- Module path: `github.com/zakkriel/drchat-image-platform`.

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/migrate/migrate.go` | goose wrappers over `migrations.FS`: `Up/Down/DownTo/Status/Version` + `BaselineVersion`. |
| `internal/migrate/bootstrap.go` | `Bootstrap`, footprint detection, baseline stamping. |
| `internal/testdb/testdb.go` | integration-tagged test support: throwaway-database creation + `TableExists`. Shared by `internal/migrate` and `cmd/migrate` tests. |
| `internal/migrate/testdata/canary/00001_canary.sql` | disposable reversible migration for the harness round-trip test. |
| `internal/migrate/migrate_integration_test.go` | `TestGooseRoundTrip`, `TestFreshUp`, `TestFreshBootstrap`, `TestBaselineConvergence`, `TestBootstrapRefusesPartial`. |
| `cmd/migrate/main.go` | thin CLI: `up/down/down-to/status/version/bootstrap` over `internal/migrate`. |
| `cmd/migrate/main_integration_test.go` | `TestCLIUpAndStatus`. |
| `migrations/NNNN_*.sql` (×11) | reformatted goose migrations (renamed from `*.up.sql`). |
| `migrations/embed.go` | embed glob `0*.up.sql` → `0*.sql`. |
| `sqlc.yaml` | schema list entries renamed `.up.sql` → `.sql`. |
| `Makefile` | `migrate`/`migrate-down`/`migrate-status` via the runner; `psql` loop deleted. |
| `.github/workflows/ci.yml` | apply via `go run ./cmd/migrate up`; table-count 18→21; fal seed assertion. |
| `docs/adr/ADR-P001-migration-tooling.md` | the ADR. |

---

## Task 1: goose harness + test support + canary round-trip

**Files:**
- Modify: `go.mod`, `go.sum` (add goose)
- Create: `internal/migrate/migrate.go`
- Create: `internal/testdb/testdb.go`
- Create: `internal/migrate/testdata/canary/00001_canary.sql`
- Test: `internal/migrate/migrate_integration_test.go`

**Interfaces:**
- Produces: `migrate.Up(db *sql.DB) error`, `migrate.Down(db *sql.DB) error`, `migrate.DownTo(db *sql.DB, version int64) error`, `migrate.Status(db *sql.DB) error`, `migrate.Version(db *sql.DB) (int64, error)`, `migrate.BaselineVersion` (untyped const `11`).
- Produces: `testdb.New(t *testing.T) (db *sql.DB, dsn string)` — creates a uniquely-named throwaway database, registers `t.Cleanup` to drop it, skips the test if `POSTGRES_DSN` is unset; `testdb.TableExists(t *testing.T, db *sql.DB, name string) bool`.

- [ ] **Step 1: Add the goose dependency**

```bash
cd /Users/pelao/REPOS/dreamchat/dreamchat-Image-Platform
go get github.com/pressly/goose/v3@latest
go mod tidy
```

Expected: `github.com/pressly/goose/v3` appears under `require` in `go.mod`.

- [ ] **Step 2: Create the throwaway-database test helper**

Create `internal/testdb/testdb.go`:

```go
//go:build integration

// Package testdb provides throwaway-database helpers for integration tests
// that must apply and roll back schema without polluting a shared database.
// It is build-tagged `integration`, so it is never compiled into production
// binaries.
package testdb

import (
	"database/sql"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

var nonIdent = regexp.MustCompile(`[^a-z0-9_]`)

// New creates a fresh, uniquely-named database derived from POSTGRES_DSN and
// returns a *sql.DB connected to it plus that database's DSN. The database is
// dropped via t.Cleanup. The test is skipped when POSTGRES_DSN is unset.
func New(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_DSN not set")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open admin: %v", err)
	}
	name := "chunk0_" + nonIdent.ReplaceAllString(strings.ToLower(t.Name()), "_")
	// WITH (FORCE) terminates stragglers; requires Postgres 13+ (CI is 15).
	if _, err := admin.Exec("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)"); err != nil {
		t.Fatalf("drop pre-existing %s: %v", name, err)
	}
	if _, err := admin.Exec("CREATE DATABASE " + name); err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	u.Path = "/" + name
	newDSN := u.String()
	db, err := sql.Open("pgx", newDSN)
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("ping %s: %v", name, err)
	}
	t.Cleanup(func() {
		_ = db.Close()
		_, _ = admin.Exec("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)")
		_ = admin.Close()
	})
	return db, newDSN
}

// TableExists reports whether a base table of the given name exists in the
// public schema of db.
func TableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	err := db.QueryRow(
		`SELECT count(*) FROM information_schema.tables
		 WHERE table_schema='public' AND table_name=$1`, name).Scan(&n)
	if err != nil {
		t.Fatalf("TableExists(%s): %v", name, err)
	}
	return n > 0
}
```

- [ ] **Step 3: Create the canary migration**

Create `internal/migrate/testdata/canary/00001_canary.sql`:

```sql
-- +goose Up
CREATE TABLE goose_canary (id integer PRIMARY KEY);

-- +goose Down
DROP TABLE goose_canary;
```

- [ ] **Step 4: Write the failing round-trip test**

Create `internal/migrate/migrate_integration_test.go`:

```go
//go:build integration

package migrate_test

import (
	"embed"
	"testing"

	"github.com/pressly/goose/v3"

	"github.com/zakkriel/drchat-image-platform/internal/testdb"
)

//go:embed testdata/canary/*.sql
var canaryFS embed.FS

// TestGooseRoundTrip proves the harness (goose + pgx stdlib + embedded FS)
// genuinely reverses a migration, independent of the irreversible baseline.
func TestGooseRoundTrip(t *testing.T) {
	db, _ := testdb.New(t)

	goose.SetBaseFS(canaryFS)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("dialect: %v", err)
	}
	const dir = "testdata/canary"

	if err := goose.Up(db, dir); err != nil {
		t.Fatalf("up: %v", err)
	}
	if !testdb.TableExists(t, db, "goose_canary") {
		t.Fatal("canary table missing after up")
	}
	if err := goose.Down(db, dir); err != nil {
		t.Fatalf("down: %v", err)
	}
	if testdb.TableExists(t, db, "goose_canary") {
		t.Fatal("canary table present after down")
	}
	if err := goose.Up(db, dir); err != nil {
		t.Fatalf("re-up: %v", err)
	}
	if !testdb.TableExists(t, db, "goose_canary") {
		t.Fatal("canary table missing after re-up")
	}
}
```

- [ ] **Step 5: Run the test to verify it fails**

```bash
POSTGRES_DSN=postgres://image_platform:image_platform@localhost:5432/image_platform?sslmode=disable \
  go test -tags=integration ./internal/migrate/... -run TestGooseRoundTrip -v
```

Expected: FAIL (compile error — `internal/migrate` has no non-test Go file yet, and/or goose import unused until the package exists). If you have no local Postgres, run `make up` first (`docker compose up -d`).

- [ ] **Step 6: Create the migrate package wrappers**

Create `internal/migrate/migrate.go`:

```go
// Package migrate drives schema migrations with goose over the embedded
// migrations FS. It is the single code path for local dev, CI, and Railway
// deploy-from-image. goose runs over database/sql via the pgx stdlib adapter.
package migrate

import (
	"database/sql"

	"github.com/pressly/goose/v3"

	"github.com/zakkriel/drchat-image-platform/migrations"
)

// BaselineVersion is the frozen irreversible-baseline floor. Migrations 1..11
// predate goose adoption and have no real Down; bootstrap stamps exactly these
// versions on already-migrated databases. NEVER increment this constant —
// migrations added after goose adoption (Chunk 1+) are applied by Up, not
// stamped. See docs/adr/ADR-P001-migration-tooling.md.
const BaselineVersion = 11

// gooseInit points goose at the embedded migrations and the Postgres dialect.
// Called per operation so callers never depend on init order.
func gooseInit() error {
	goose.SetBaseFS(migrations.FS)
	return goose.SetDialect("postgres")
}

// Up applies all pending migrations.
func Up(db *sql.DB) error {
	if err := gooseInit(); err != nil {
		return err
	}
	return goose.Up(db, ".")
}

// Down rolls back the most recently applied migration.
func Down(db *sql.DB) error {
	if err := gooseInit(); err != nil {
		return err
	}
	return goose.Down(db, ".")
}

// DownTo rolls back to (and including) the given target version.
func DownTo(db *sql.DB, version int64) error {
	if err := gooseInit(); err != nil {
		return err
	}
	return goose.DownTo(db, ".", version)
}

// Status prints applied/pending state to stdout.
func Status(db *sql.DB) error {
	if err := gooseInit(); err != nil {
		return err
	}
	return goose.Status(db, ".")
}

// Version returns the current applied schema version (0 on a fresh DB).
func Version(db *sql.DB) (int64, error) {
	if err := gooseInit(); err != nil {
		return 0, err
	}
	return goose.GetDBVersion(db)
}
```

- [ ] **Step 7: Run the test to verify it passes**

```bash
POSTGRES_DSN=postgres://image_platform:image_platform@localhost:5432/image_platform?sslmode=disable \
  go test -tags=integration ./internal/migrate/... -run TestGooseRoundTrip -v
```

Expected: PASS. Also confirm the production build is unaffected:

```bash
go build ./...
```

Expected: builds clean (the `testdb` package is excluded without the `integration` tag).

- [ ] **Step 8: Commit**

```bash
git add go.mod go.sum internal/migrate/migrate.go internal/testdb/testdb.go \
  internal/migrate/testdata/canary/00001_canary.sql \
  internal/migrate/migrate_integration_test.go
git commit -m "feat(migrate): goose harness + canary round-trip test"
```

---

## Task 2: Reformat the 11 baseline migrations to goose format

**Files:**
- Rename + modify: `migrations/0001_initial.up.sql` … `migrations/0011_fal_provider_seed.up.sql` (→ `*.sql`)
- Modify: `migrations/embed.go`
- Modify: `sqlc.yaml`
- Test: `internal/migrate/migrate_integration_test.go` (add `TestFreshUp`)

**Interfaces:**
- Consumes: `migrate.Up`, `testdb.New`, `testdb.TableExists` (Task 1).
- Produces: a goose-valid `migrations.FS` containing 11 single-file migrations named `0001_initial.sql` … `0011_fal_provider_seed.sql`.

> Goose reads **single-file** migrations with `-- +goose Up` / `-- +goose Down`
> sections — not `.up.sql`/`.down.sql` pairs. This task is a pure reformat: no
> SQL semantics change. `TestFreshUp` + the codegen-diff gate prove faithfulness.

- [ ] **Step 1: Write the failing `TestFreshUp` test**

Append to `internal/migrate/migrate_integration_test.go`:

```go
import (
	// add to the existing import block:
	"github.com/zakkriel/drchat-image-platform/internal/migrate"
)

// baselineTables is the full database-local footprint created by migrations
// 1..11 (17 from 0001, cost_reservation_budget_holds from 0003, the two
// webhook tables from 0010).
var baselineTables = []string{
	"api_tokens", "asset_pack_items", "asset_packs", "audit_events",
	"cost_budgets", "cost_reservation_budget_holds", "cost_reservations",
	"generation_cost_events", "generation_jobs", "idempotency_keys",
	"provider_attempts", "provider_model_prices", "provider_models",
	"provider_routes", "style_profiles", "visual_assets", "visual_identities",
	"visual_identity_versions", "webhook_deliveries", "webhook_endpoints",
}

// TestFreshUp proves migrate.Up applies the full reformatted baseline to an
// empty database: all 20 baseline tables plus goose_db_version, version == 11.
func TestFreshUp(t *testing.T) {
	db, _ := testdb.New(t)

	if err := migrate.Up(db); err != nil {
		t.Fatalf("up: %v", err)
	}
	for _, tbl := range baselineTables {
		if !testdb.TableExists(t, db, tbl) {
			t.Fatalf("baseline table %q missing after up", tbl)
		}
	}
	if !testdb.TableExists(t, db, "goose_db_version") {
		t.Fatal("goose_db_version missing after up")
	}
	v, err := migrate.Version(db)
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if v != migrate.BaselineVersion {
		t.Fatalf("version = %d, want %d", v, migrate.BaselineVersion)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

```bash
POSTGRES_DSN=postgres://image_platform:image_platform@localhost:5432/image_platform?sslmode=disable \
  go test -tags=integration ./internal/migrate/... -run TestFreshUp -v
```

Expected: FAIL — `migrate.Up` errors because `migrations.FS` still holds `*.up.sql` files with no goose annotations (goose reports a missing-annotation / parse error).

- [ ] **Step 3: Rename all 11 migration files**

```bash
cd /Users/pelao/REPOS/dreamchat/dreamchat-Image-Platform
for f in migrations/0*.up.sql; do git mv "$f" "${f%.up.sql}.sql"; done
ls migrations/0*.sql
```

Expected: 11 files now named `0001_initial.sql` … `0011_fal_provider_seed.sql`.

- [ ] **Step 4: Add goose annotations to every file (uniform edit)**

For **each** of the 11 `migrations/0*.sql` files:

1. Insert `-- +goose Up` as the **very first line** of the file.
2. Append to the **end** of the file (replace `NNNN` with that file's number):

```sql

-- +goose Down
-- Baseline migration: irreversible floor. Roll back by restoring from backup.
SELECT 'baseline migration NNNN is irreversible' WHERE false;
```

- [ ] **Step 5: Strip self-managed transactions (5 files)**

Goose wraps each migration in its own transaction; a nested `COMMIT` would close it early. In each of these files delete the standalone `BEGIN;` line and the standalone `COMMIT;` line (verified locations):

- `migrations/0002_seed_mock_provider.sql` — `BEGIN;` (was line 18), `COMMIT;` (was line 61)
- `migrations/0003_cost_lifecycle.sql` — `BEGIN;` (17), `COMMIT;` (37)
- `migrations/0006_bfl_provider_seed.sql` — `BEGIN;` (28), `COMMIT;` (110)
- `migrations/0009_rls_tenant_isolation.sql` — `BEGIN;` (47), `COMMIT;` (213)
- `migrations/0011_fal_provider_seed.sql` — `BEGIN;` (34), `COMMIT;` (79)

(Line numbers are pre-edit references; match the literal `BEGIN;` / `COMMIT;` statements.)

- [ ] **Step 6: Wrap dollar-quoted blocks (2 files)**

Goose's statement splitter breaks on `;` inside `DO $$ … $$` bodies. For **every** `DO $$ … END $$;` block, insert `-- +goose StatementBegin` on the line immediately **before** `DO $$`, and `-- +goose StatementEnd` on the line immediately **after** `END $$;`:

- `migrations/0009_rls_tenant_isolation.sql` — **5** `DO` blocks.
- `migrations/0010_webhooks.sql` — **1** `DO` block.

Example shape:

```sql
-- +goose StatementBegin
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'image_platform_api') THEN
    CREATE ROLE image_platform_api LOGIN PASSWORD 'image_platform_api';
  END IF;
END $$;
-- +goose StatementEnd
```

- [ ] **Step 7: Update the embed glob**

In `migrations/embed.go`, change the embed directive and its comment:

```go
// Only the *.sql files are embedded. They are goose single-file migrations
// applied in filename order, so the zero-padded numeric prefixes (0001_, 0002_,
// ...) define apply order.
package migrations

import "embed"

// FS holds every migration in filename order.
//
//go:embed 0*.sql
var FS embed.FS
```

- [ ] **Step 8: Update the sqlc schema list**

In `sqlc.yaml`, rename each schema entry's extension `.up.sql` → `.sql` (the include set is unchanged — `0009` stays excluded so sqlc never parses `CREATE POLICY/ROLE`):

```yaml
    schema:
      - "migrations/0001_initial.sql"
      - "migrations/0003_cost_lifecycle.sql"
      - "migrations/0004_pack_completeness.sql"
      - "migrations/0005_supersede_on_regenerate.sql"
      - "migrations/0007_budget_period_reset.sql"
      - "migrations/0008_rate_limits.sql"
      - "migrations/0010_webhooks.sql"
```

- [ ] **Step 9: Regenerate codegen and assert zero drift**

```bash
make generate
git diff --exit-code internal/db/dbgen
```

Expected: `make generate` succeeds and `git diff --exit-code` reports **no changes** (sqlc is goose-annotation aware and applies only the `Up` sections; the baseline `Down` no-ops are `SELECT`s and cannot alter the catalog regardless).

- [ ] **Step 10: Run `TestFreshUp` (and the round-trip) to verify they pass**

```bash
POSTGRES_DSN=postgres://image_platform:image_platform@localhost:5432/image_platform?sslmode=disable \
  go test -tags=integration ./internal/migrate/... -v
```

Expected: PASS (`TestFreshUp` + `TestGooseRoundTrip`). A failure here means a `DO` block wasn't wrapped or a `BEGIN/COMMIT` wasn't stripped — fix that file and re-run.

> Note: `cmd/migrate` still globs `0*.up.sql` and is therefore non-functional at
> runtime until Task 3 rewires it. `go build ./...` stays green; this is the only
> task boundary where `make migrate` is temporarily broken.

- [ ] **Step 11: Commit**

```bash
git add migrations/ sqlc.yaml internal/migrate/migrate_integration_test.go
git commit -m "refactor(migrations): reformat 0001-0011 to goose single-file format"
```

---

## Task 3: Rewire `cmd/migrate` into a goose CLI + update the Makefile

**Files:**
- Modify (replace): `cmd/migrate/main.go`
- Modify: `Makefile`
- Test: `cmd/migrate/main_integration_test.go`

**Interfaces:**
- Consumes: `migrate.Up/Down/DownTo/Status/Version` (Task 1).
- Produces: `run(args []string, getenv func(string) string) error` in `package main` — testable CLI dispatch.

- [ ] **Step 1: Write the failing CLI test**

Create `cmd/migrate/main_integration_test.go`:

```go
//go:build integration

package main

import (
	"testing"

	"github.com/zakkriel/drchat-image-platform/internal/testdb"
)

// TestCLIUpAndStatus drives the CLI dispatch end-to-end against a throwaway DB.
func TestCLIUpAndStatus(t *testing.T) {
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
	if err := run([]string{"status"}, getenv); err != nil {
		t.Fatalf("run status: %v", err)
	}
	if err := run([]string{"version"}, getenv); err != nil {
		t.Fatalf("run version: %v", err)
	}
	if err := run([]string{"bogus"}, getenv); err == nil {
		t.Fatal("run bogus: expected error for unknown command")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

```bash
POSTGRES_DSN=postgres://image_platform:image_platform@localhost:5432/image_platform?sslmode=disable \
  go test -tags=integration ./cmd/migrate/... -run TestCLIUpAndStatus -v
```

Expected: FAIL — the current `main.go` has no `run([]string, func(string) string)` function (compile error).

- [ ] **Step 3: Replace `cmd/migrate/main.go`**

Overwrite `cmd/migrate/main.go` with:

```go
// Command migrate is the single migration runner for local dev, CI, and Railway
// deploy-from-image. It drives goose over the embedded migrations FS via
// internal/migrate. Subcommands: up, down, down-to <version>, status, version,
// bootstrap. See docs/adr/ADR-P001-migration-tooling.md.
package main

import (
	"database/sql"
	"fmt"
	"os"
	"strconv"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/zakkriel/drchat-image-platform/internal/migrate"
)

func main() {
	if err := run(os.Args[1:], os.Getenv); err != nil {
		fmt.Fprintln(os.Stderr, "migrate: "+err.Error())
		os.Exit(1)
	}
}

func run(args []string, getenv func(string) string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: migrate <up|down|down-to|status|version|bootstrap> [version]")
	}
	dsn := getenv("POSTGRES_DSN")
	if dsn == "" {
		return fmt.Errorf("POSTGRES_DSN is required")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer func() { _ = db.Close() }()

	switch args[0] {
	case "up":
		return migrate.Up(db)
	case "down":
		return migrate.Down(db)
	case "down-to":
		if len(args) < 2 {
			return fmt.Errorf("down-to requires a target version")
		}
		v, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			return fmt.Errorf("invalid version %q: %w", args[1], err)
		}
		return migrate.DownTo(db, v)
	case "status":
		return migrate.Status(db)
	case "version":
		v, err := migrate.Version(db)
		if err != nil {
			return err
		}
		fmt.Printf("current version: %d\n", v)
		return nil
	case "bootstrap":
		return migrate.Bootstrap(db)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}
```

> `migrate.Bootstrap` does not exist until Task 4. To keep this task's commit
> compiling, Task 4 is sequenced immediately after; if you implement strictly
> one task per build-green commit, move the `case "bootstrap"` line into Task 4.
> Recommended: implement Task 3 and Task 4 back-to-back. (If you need Task 3 to
> stand alone green, temporarily replace the bootstrap case body with
> `return fmt.Errorf("bootstrap not yet implemented")` and restore it in Task 4.)

- [ ] **Step 4: Rewrite the Makefile migrate targets**

In `Makefile`: update `.PHONY`, the `help` text, and replace the `migrate` recipe; add `migrate-down` and `migrate-status`. The runner needs a DSN reachable from the host — for the docker-compose stack that is `localhost:5432`.

Replace the `help` migrate line and the whole `migrate:` recipe with:

```makefile
help:
	@echo "Targets:"
	@echo "  make up            - docker compose up -d"
	@echo "  make down          - docker compose down -v"
	@echo "  make migrate       - apply migrations via goose (cmd/migrate up)"
	@echo "  make migrate-down  - roll back the latest migration (cmd/migrate down)"
	@echo "  make migrate-status- show goose migration status"
	@echo "  make seed          - insert one dev API token (raw value printed once)"
	@echo "  make seed-admin    - insert one dev admin token (admin:costs only)"
	@echo "  make dev           - up + wait-for-ready + migrate + seed"
	@echo "  make test          - go test ./..."
	@echo "  make build         - go build ./..."
	@echo "  make generate      - run oapi-codegen + sqlc generate"
	@echo "  make fmt           - gofmt -w ."
	@echo "  make vet           - go vet ./..."
	@echo "  make lint          - golangci-lint run"

migrate:
	POSTGRES_DSN=$(POSTGRES_DSN) go run ./cmd/migrate up

migrate-down:
	POSTGRES_DSN=$(POSTGRES_DSN) go run ./cmd/migrate down

migrate-status:
	POSTGRES_DSN=$(POSTGRES_DSN) go run ./cmd/migrate status
```

Update the `.PHONY` line to include the new targets:

```makefile
.PHONY: help up down dev migrate migrate-down migrate-status seed seed-admin test build generate fmt vet lint wait-ready
```

The top-of-file `POSTGRES_DSN ?= postgres://image_platform:image_platform@localhost:5432/image_platform?sslmode=disable` default already exists and is reused. Confirm no `psql`-loop recipe remains anywhere in the Makefile.

- [ ] **Step 5: Run the CLI test to verify it passes**

```bash
POSTGRES_DSN=postgres://image_platform:image_platform@localhost:5432/image_platform?sslmode=disable \
  go test -tags=integration ./cmd/migrate/... -run TestCLIUpAndStatus -v
go build ./...
```

Expected: PASS and clean build.

- [ ] **Step 6: Commit**

```bash
git add cmd/migrate/main.go cmd/migrate/main_integration_test.go Makefile
git commit -m "feat(migrate): goose-backed cmd/migrate CLI; drop psql loop from Makefile"
```

---

## Task 4: Bootstrap — full-footprint detection + baseline stamping

**Files:**
- Create: `internal/migrate/bootstrap.go`
- Test: `internal/migrate/migrate_integration_test.go` (add 3 tests + raw-apply helper)

**Interfaces:**
- Consumes: `migrate.Up`, `migrate.Version`, `migrate.BaselineVersion`, `migrations.FS`, `testdb.New`.
- Produces: `migrate.Bootstrap(db *sql.DB) error`.

- [ ] **Step 1: Write the failing bootstrap tests**

Append to `internal/migrate/migrate_integration_test.go` (add `io/fs`, `sort`, `database/sql` to the import block as needed):

```go
// rawApplyFile executes a migration file's full text directly (no goose version
// tracking) to simulate a database migrated by the old psql loop. The baseline
// Down sections are no-op SELECTs, so executing them is harmless.
func rawApplyFile(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	b, err := migrations.FS.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	if _, err := db.Exec(string(b)); err != nil {
		t.Fatalf("raw apply %s: %v", name, err)
	}
}

func rawApplyAll(t *testing.T, db *sql.DB) {
	t.Helper()
	names, err := fs.Glob(migrations.FS, "0*.sql")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	sort.Strings(names)
	for _, n := range names {
		rawApplyFile(t, db, n)
	}
}

func gooseTrackingExists(t *testing.T, db *sql.DB) bool {
	t.Helper()
	return testdb.TableExists(t, db, "goose_db_version")
}

// TestFreshBootstrap exercises bootstrap's FRESH branch directly: an empty DB
// is migrated to the full baseline.
func TestFreshBootstrap(t *testing.T) {
	db, _ := testdb.New(t)

	if err := migrate.Bootstrap(db); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	v, err := migrate.Version(db)
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if v != migrate.BaselineVersion {
		t.Fatalf("version = %d, want %d", v, migrate.BaselineVersion)
	}
	for _, tbl := range baselineTables {
		if !testdb.TableExists(t, db, tbl) {
			t.Fatalf("baseline table %q missing after fresh bootstrap", tbl)
		}
	}
}

// TestBaselineConvergence exercises the ALREADY-MIGRATED branch: a DB with the
// full footprint but no version table is stamped, not re-applied.
func TestBaselineConvergence(t *testing.T) {
	db, _ := testdb.New(t)
	rawApplyAll(t, db) // simulate an existing prod DB at version 11
	if gooseTrackingExists(t, db) {
		t.Fatal("precondition: goose_db_version should not exist after raw apply")
	}

	if err := migrate.Bootstrap(db); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	v, err := migrate.Version(db)
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if v != migrate.BaselineVersion {
		t.Fatalf("version = %d, want %d", v, migrate.BaselineVersion)
	}
	// A following Up must be a clean no-op.
	if err := migrate.Up(db); err != nil {
		t.Fatalf("up after bootstrap: %v", err)
	}
}

// TestBootstrapRefusesPartial exercises the REFUSE branch: a present-but-
// incomplete footprint (only 0001 applied) must be refused and stamp nothing.
func TestBootstrapRefusesPartial(t *testing.T) {
	db, _ := testdb.New(t)
	rawApplyFile(t, db, "0001_initial.sql") // 17 of 20 tables, no fal seed

	err := migrate.Bootstrap(db)
	if err == nil {
		t.Fatal("expected bootstrap to refuse a partially-migrated database")
	}
	if !strings.Contains(err.Error(), "refused") {
		t.Fatalf("error %q should mention 'refused'", err.Error())
	}
	if gooseTrackingExists(t, db) {
		t.Fatal("bootstrap must not create goose_db_version when refusing")
	}
}
```

(Ensure `strings` is imported in the test file.)

- [ ] **Step 2: Run them to verify they fail**

```bash
POSTGRES_DSN=postgres://image_platform:image_platform@localhost:5432/image_platform?sslmode=disable \
  go test -tags=integration ./internal/migrate/... -run 'Bootstrap|Convergence' -v
```

Expected: FAIL — `migrate.Bootstrap` is undefined (compile error).

- [ ] **Step 3: Implement bootstrap**

Create `internal/migrate/bootstrap.go`:

```go
package migrate

import (
	"database/sql"
	"fmt"
	"strings"
)

// baselineTables are the 20 tables created by baseline migrations 1..11. Their
// presence count (all database-local) is the footprint signal: 0 → fresh DB,
// all → fully migrated, in-between → partial/unknown. Roles created by 0009 are
// intentionally excluded — they are cluster-global and would misclassify a
// fresh database on a cluster where the roles already exist.
var baselineTables = []string{
	"api_tokens", "asset_pack_items", "asset_packs", "audit_events",
	"cost_budgets", "cost_reservation_budget_holds", "cost_reservations",
	"generation_cost_events", "generation_jobs", "idempotency_keys",
	"provider_attempts", "provider_model_prices", "provider_models",
	"provider_routes", "style_profiles", "visual_assets", "visual_identities",
	"visual_identity_versions", "webhook_deliveries", "webhook_endpoints",
}

// Bootstrap converges any database onto the goose version table without
// destructive re-application. See docs/adr/ADR-P001-migration-tooling.md and the
// design spec §4.
//
//   - goose_db_version already present  -> delegate to Up (apply pending).
//   - zero baseline tables              -> fresh DB, apply everything via Up.
//   - full footprint (20 tables + 0011  -> stamp versions 1..BaselineVersion as
//     fal seed)                            applied without running them, then Up.
//   - anything in between               -> REFUSE; stamp nothing.
func Bootstrap(db *sql.DB) error {
	tracked, err := tableExists(db, "goose_db_version")
	if err != nil {
		return fmt.Errorf("probe version table: %w", err)
	}
	if tracked {
		return Up(db)
	}

	present, err := countBaselineTables(db)
	if err != nil {
		return fmt.Errorf("probe baseline footprint: %w", err)
	}

	switch {
	case present == 0:
		// Fresh database: apply everything normally.
		return Up(db)
	case present == len(baselineTables):
		seed, err := falSeedPresent(db)
		if err != nil {
			return fmt.Errorf("probe fal seed: %w", err)
		}
		if !seed {
			return fmt.Errorf(
				"bootstrap refused: all %d baseline tables present but the 0011 fal seed is missing — "+
					"database is at an incomplete/unknown state; a human must resolve it (nothing stamped)",
				len(baselineTables))
		}
		if err := stampBaseline(db); err != nil {
			return err
		}
		// Apply any post-baseline (Chunk 1+) migrations.
		return Up(db)
	default:
		return fmt.Errorf(
			"bootstrap refused: %d of %d baseline tables present — database is partially migrated to an "+
				"unknown state; a human must resolve it (nothing stamped)",
			present, len(baselineTables))
	}
}

// stampBaseline marks versions 0..BaselineVersion applied WITHOUT running them,
// so a subsequent Up is a no-op for the baseline. It writes goose's own version
// table schema explicitly (stable across goose v3), which is deterministic and
// does not depend on goose internals.
func stampBaseline(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS goose_db_version (
		id serial NOT NULL,
		version_id bigint NOT NULL,
		is_applied boolean NOT NULL,
		tstamp timestamp NULL DEFAULT now(),
		PRIMARY KEY(id)
	)`); err != nil {
		return fmt.Errorf("create version table: %w", err)
	}
	for v := int64(0); v <= BaselineVersion; v++ {
		if _, err := db.Exec(
			`INSERT INTO goose_db_version (version_id, is_applied) VALUES ($1, true)`, v,
		); err != nil {
			return fmt.Errorf("stamp version %d: %w", v, err)
		}
	}
	return nil
}

func tableExists(db *sql.DB, name string) (bool, error) {
	var n int
	err := db.QueryRow(
		`SELECT count(*) FROM information_schema.tables
		 WHERE table_schema='public' AND table_name=$1`, name).Scan(&n)
	return n > 0, err
}

func countBaselineTables(db *sql.DB) (int, error) {
	placeholders := make([]string, len(baselineTables))
	args := make([]any, len(baselineTables))
	for i, name := range baselineTables {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = name
	}
	q := `SELECT count(*) FROM information_schema.tables
	      WHERE table_schema='public' AND table_name IN (` + strings.Join(placeholders, ",") + `)`
	var n int
	err := db.QueryRow(q, args...).Scan(&n)
	return n, err
}

// falSeedPresent reports whether migration 0011's seed ran. 0011 adds no table,
// so the table footprint alone cannot distinguish version 10 from 11.
func falSeedPresent(db *sql.DB) (bool, error) {
	var n int
	err := db.QueryRow(
		`SELECT count(*) FROM provider_models WHERE id='pm_fal_flux_kontext_multi'`).Scan(&n)
	return n > 0, err
}
```

> Note: `baselineTables` is also declared in the test file (Task 2 Step 1). Go
> allows the same identifier in `package migrate` and `package migrate_test`
> without conflict — they are different packages. Keep both; the test copy keeps
> the test self-contained.

- [ ] **Step 4: Run the bootstrap tests to verify they pass**

```bash
POSTGRES_DSN=postgres://image_platform:image_platform@localhost:5432/image_platform?sslmode=disable \
  go test -tags=integration ./internal/migrate/... -v
```

Expected: PASS for `TestFreshBootstrap`, `TestBaselineConvergence`, `TestBootstrapRefusesPartial` (plus the earlier two). If you used the temporary bootstrap stub in Task 3 Step 3, restore the real `case "bootstrap": return migrate.Bootstrap(db)` now and rerun `go test -tags=integration ./cmd/migrate/...`.

- [ ] **Step 5: Commit**

```bash
git add internal/migrate/bootstrap.go internal/migrate/migrate_integration_test.go cmd/migrate/main.go
git commit -m "feat(migrate): bootstrap with full-footprint detection and baseline stamping"
```

---

## Task 5: CI — apply via goose, fix counts, assert fal seed

**Files:**
- Modify: `.github/workflows/ci.yml` (the `migrations` job)

**Interfaces:** none (CI only). The new Go integration tests already run in the existing `integration tests` step (`go test -tags=integration ./...`) and cover round-trip / bootstrap / convergence / refuse.

- [ ] **Step 1: Replace the psql apply block with the goose runner**

In `.github/workflows/ci.yml`, in the `migrations` job, replace the entire `- name: apply migrations` step (the `psql -f migrations/0001…0009` block) with:

```yaml
      - name: apply migrations (goose)
        env:
          POSTGRES_DSN: postgres://image_platform:image_platform@localhost:5432/image_platform?sslmode=disable
        run: go run ./cmd/migrate up
```

This now applies the **full** `0001…0011` set (the old hardcoded list stopped at `0009`).

- [ ] **Step 2: Update the table-count assertion 18 → 21**

Replace the `- name: assert all expected tables exist` step's script and comment with:

```yaml
      - name: assert all expected tables exist
        env:
          PGPASSWORD: image_platform
        run: |
          count=$(psql -h localhost -U image_platform -d image_platform -tAc \
            "SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_type='BASE TABLE'")
          echo "table count: $count"
          # 20 baseline tables (17 from 0001 + cost_reservation_budget_holds from
          # 0003 + 2 webhook tables from 0010) + goose_db_version = 21. goose now
          # applies the FULL 0001-0011 set; the old psql list stopped at 0009.
          test "$count" = "21"
```

- [ ] **Step 3: Assert the goose version and the fal seed**

Add two steps after the table-count assertion:

```yaml
      - name: assert goose version is the baseline head
        env:
          PGPASSWORD: image_platform
        run: |
          v=$(psql -h localhost -U image_platform -d image_platform -tAc \
            "SELECT max(version_id) FROM goose_db_version WHERE is_applied")
          echo "goose version: $v"
          test "$v" = "11"

      - name: assert fal provider seed present (migration 0011)
        env:
          PGPASSWORD: image_platform
        run: |
          model=$(psql -h localhost -U image_platform -d image_platform -tAc \
            "SELECT 1 FROM provider_models WHERE id = 'pm_fal_flux_kontext_multi' AND provider_id = 'fal'")
          route=$(psql -h localhost -U image_platform -d image_platform -tAc \
            "SELECT 1 FROM provider_routes WHERE id = 'route_fal_text_to_image_pack' AND required_capability = 'pack_capable'")
          test "$model" = "1"
          test "$route" = "1"
```

Leave every other assertion in the `migrations` job (columns, indexes, RLS enforcement, mock/bfl seeds) unchanged — they remain valid smoke tests.

- [ ] **Step 4: Verify locally against a clean database**

```bash
make up                      # docker compose up -d (postgres on localhost:5432)
make migrate                 # go run ./cmd/migrate up
PGPASSWORD=image_platform psql -h localhost -U image_platform -d image_platform -tAc \
  "SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_type='BASE TABLE'"
# expect: 21
PGPASSWORD=image_platform psql -h localhost -U image_platform -d image_platform -tAc \
  "SELECT max(version_id) FROM goose_db_version WHERE is_applied"
# expect: 11
PGPASSWORD=image_platform psql -h localhost -U image_platform -d image_platform -tAc \
  "SELECT 1 FROM provider_models WHERE id='pm_fal_flux_kontext_multi'"
# expect: 1
```

Expected: counts and version match the assertions. (Tear down with `make down` when finished.)

- [ ] **Step 5: Commit**

```bash
git add .github/workflows/ci.yml
git commit -m "ci(migrations): apply via goose, assert version 11 + fal seed, count 18->21"
```

---

## Task 6: Docs — ADR-P001 + correct falsified docs/comments

**Files:**
- Create: `docs/adr/ADR-P001-migration-tooling.md`
- Modify: `README.md`, `DECISIONS.md`
- Modify (comments): `internal/providers/bfl/bfl.go`, `internal/providers/fal/fal.go`, `internal/db/system.go`, `internal/db/tenant.go`

**Interfaces:** none.

- [ ] **Step 1: Write ADR-P001**

Create `docs/adr/ADR-P001-migration-tooling.md`:

```markdown
# ADR-P001: Migration tooling — goose with an irreversible baseline floor

- **Status:** accepted (2026-06-18)
- **Platform ADR** (numbered `ADR-P###` per rule D-5).

## Context

Migrations were applied two ways: a bare `psql` loop in the Makefile and an
embedded pgx runner (`cmd/migrate`) with no version tracking. Neither tracked
applied versions or supported rollback, and CI applied a hand-maintained file
list (which had drifted — it never applied `0010`/`0011`). We need version
tracking, per-migration transactions, reversibility for new migrations, and a
safe convergence path for already-migrated staging/prod databases.

## Decision

Adopt **goose** (`github.com/pressly/goose/v3`) as a Go **library** driven
through `internal/migrate` over the embedded `migrations.FS`, keeping
`cmd/migrate` a single embedded binary (Railway deploy-from-image preserved).
Evidence: `internal/migrate/migrate.go` (wrappers), `cmd/migrate/main.go`
(subcommands `up/down/down-to/status/version/bootstrap`).

The existing migrations `0001…0011` are reformatted to goose single-file format
and frozen as an **irreversible baseline floor**: their `Down` is a guarded
no-op. **Every migration from Chunk 1 onward MUST ship a real, tested `Down`.**

`bootstrap` converges existing databases via a **full-footprint, database-local
check** (`internal/migrate/bootstrap.go`): zero baseline tables → fresh (apply);
all 20 baseline tables + the `0011` fal seed sentinel → stamp versions 1–11
without running them; anything in between → refuse and stamp nothing. A single
early-table probe is deliberately rejected — it cannot distinguish "fully at 11"
from "partway" and would silently skip the missing migrations.

The version table is goose-native `goose_db_version` (the source prompt's literal
`schema_migrations` named golang-migrate's table; not used).

## Policy for future schema changes

- **expand → backfill → contract.** Additive ("expand") changes ship first;
  data backfills are separate migrations; destructive ("contract") changes land
  last, only after the expand deploy has settled.
- **Reversibility:** every new migration has a real `Down`; CI proves
  `up → down-to 11 → up` on everything above the baseline.
- **NO-TRANSACTION audit:** goose runs each migration in its own transaction.
  Any statement that cannot run in a transaction (`CREATE INDEX CONCURRENTLY`,
  `ALTER TYPE … ADD VALUE`, `CREATE/DROP DATABASE`, `VACUUM`, `REINDEX`, …) must
  be isolated in its own migration marked `-- +goose NO TRANSACTION`. The 11
  baseline files were audited and need none.

## Consequences

CI applies the full set via `go run ./cmd/migrate up`
(`.github/workflows/ci.yml`), closing the prior `0010`/`0011` coverage gap; the
base table count is 20 + `goose_db_version` = 21. Rollback to empty is not
supported for the baseline — production rollback is restore-from-backup.
```

- [ ] **Step 2: Fix the README migrate description**

In `README.md`, the "Dev loop" / docs section claims `make migrate` applies SQL via psql. Update the relevant lines to reflect goose. Replace the CI sentence:

> CI runs `go vet`, `go build`, `go test`, `openapi-spec-validator`, `sqlc vet`,
> and applies the migration to a throwaway Postgres asserting the 17 tables
> exist.

with:

```markdown
CI runs `go vet`, `go build`, `go test`, `openapi-spec-validator`, `sqlc vet`,
and applies migrations to a throwaway Postgres via `go run ./cmd/migrate up`
(goose), asserting the 20 baseline tables + `goose_db_version` exist and the
schema is at version 11.
```

(Evidence for the claim: `.github/workflows/ci.yml` `migrations` job; `cmd/migrate/main.go`.)

- [ ] **Step 3: Fix the DECISIONS.md migrate line**

In `DECISIONS.md` (Local development section), replace:

> `make up` brings the stack up. `make migrate` applies
> `docs/db/initial_schema.sql`. `make dev` is the full bootstrap …

with:

```markdown
`make up` brings the stack up. `make migrate` applies the goose migrations in
`migrations/` via `go run ./cmd/migrate up`. `make dev` is the full bootstrap
(compose + migrate + seed dev token). `make test` runs `go test ./...`.
```

(Evidence: `Makefile` `migrate` target; `cmd/migrate/main.go`.)

- [ ] **Step 4: Fix stale filename references in code comments**

Update these comments to the renamed `.sql` files (search each for the literal `.up.sql`):

- `internal/providers/bfl/bfl.go` — `migrations/0006_bfl_provider_seed.up.sql` → `…0006_bfl_provider_seed.sql`
- `internal/providers/fal/fal.go` — `migrations/0011_fal_provider_seed.up.sql` → `…0011_fal_provider_seed.sql`
- `internal/db/system.go` — `migrations/0009_rls_tenant_isolation.up.sql` → `…0009_rls_tenant_isolation.sql`
- `internal/db/tenant.go` — `migrations/0009_rls_tenant_isolation.up.sql` → `…0009_rls_tenant_isolation.sql`

- [ ] **Step 5: Verify nothing else references the old filenames**

```bash
grep -rn "\.up\.sql" --include="*.go" --include="*.md" --include="*.yaml" --include="*.yml" --include="Makefile" . \
  | grep -v "docs/superpowers/"
```

Expected: no matches outside the `docs/superpowers/` spec/plan (which intentionally describe the rename). If any remain (e.g., the OpenAPI/data-model docs), update them too with an evidence note, or leave broad `Phase 0/v0.5.0` cleanup to the later Docs chunk if unrelated to the rename.

- [ ] **Step 6: Verify build + full integration suite green, then commit**

```bash
go build ./...
POSTGRES_DSN=postgres://image_platform:image_platform@localhost:5432/image_platform?sslmode=disable \
  REDIS_ADDR=localhost:6379 \
  go test -tags=integration ./internal/migrate/... ./cmd/migrate/...
git add docs/adr/ADR-P001-migration-tooling.md README.md DECISIONS.md \
  internal/providers/bfl/bfl.go internal/providers/fal/fal.go \
  internal/db/system.go internal/db/tenant.go
git commit -m "docs: ADR-P001 migration tooling; correct migrate references for goose"
```

---

## Self-Review

**Spec coverage (spec §-by-§):**
- §2 file conversion → Task 2 (rename, Up/Down wrap, BEGIN/COMMIT strip, StatementBegin/End). ✓
- §3 library runner + subcommands + `goose_db_version` → Tasks 1, 3. ✓
- §4 bootstrap full-footprint + refuse-on-partial + stamp → Task 4. ✓
- §5 reversibility floor + NO-TRANSACTION policy → baseline no-op Downs (Task 2), policy in ADR (Task 6). Forward `up→down-to 11→up` proof is documented for Chunk 1+ (no above-baseline migration exists to exercise it in Chunk 0; the canary `TestGooseRoundTrip` proves the harness reverses). ✓
- §6 sqlc list rename + Makefile + CI count/coverage → Tasks 2, 3, 5. ✓
- §7 all five tests (RoundTrip, BaselineConvergence, FreshBootstrap, RefusesPartial, FreshUp) → Tasks 1, 2, 4. ✓
- §8 ADR-P001 + falsified docs/comments → Task 6. ✓
- §10 acceptance (no psql loop, harness round-trips, convergence, count 21, single binary, ADR) → covered across Tasks 3–6. ✓

**Placeholder scan:** no TBD/TODO; every code step shows complete code. ✓

**Type consistency:** `migrate.Up/Down/DownTo(int64)/Status/Version(→int64)/Bootstrap` and `testdb.New(→*sql.DB,string)`/`TableExists(→bool)` are used identically across Tasks 1–4. `run([]string, func(string)string) error` defined and used in Task 3. ✓

**Known sequencing note:** Task 3's `main.go` references `migrate.Bootstrap` (Task 4). Implement Tasks 3 and 4 back-to-back, or use the documented one-line stub to keep Task 3's commit independently green.
