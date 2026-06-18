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
- **Reversibility:** every new migration added from Chunk 1 onward MUST ship a
  real, tested `Down`. Once the first post-baseline migration lands, CI will gate
  the round-trip `up → down-to 11 → up` on everything above the baseline. (No
  such migration exists yet, so the harness's reversibility is currently proven
  by the `TestGooseRoundTrip` canary, not by a CI step over the real
  migrations.)
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

Goose pulls `modernc.org/sqlite` as a transitive dependency (verifiable in
`go.sum`) for a Postgres-only service; this is acceptable bloat — the sqlite
driver is not used at runtime.
