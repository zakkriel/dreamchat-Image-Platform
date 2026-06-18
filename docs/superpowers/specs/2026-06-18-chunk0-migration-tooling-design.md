# Chunk 0 — Migration Tooling (goose, irreversible baseline floor)

> **Status:** approved design, pre-implementation.
> **Date:** 2026-06-18
> **Scope:** Chunk 0 of the Combined Governance Envelope + Cost-Optimization Contract program.
> This is the **prerequisite gate**: no other chunk may add schema until this lands.
> One chunk = one worktree = one plan = one PR.

## 1. Goal & boundaries

Replace the two ad-hoc migration mechanisms with a single **goose**-backed harness that:

- tracks applied migration versions in a version table,
- runs each migration in its own transaction,
- proves reversibility of **new** migrations in CI (up → down → up),
- lets already-migrated staging/prod databases converge onto the version table
  **without re-applying** the baseline destructively.

**This chunk is tooling only — it ships no schema changes.** It is the hard gate
the rest of the program builds on.

### What exists today (the starting point)

- `migrations/0001…0011 *.up.sql` — up-only; **no down files anywhere**. Zero-padded
  numeric prefixes define apply order.
- Several files self-wrap in `BEGIN;/COMMIT;` (`0002, 0003, 0006, 0009, 0011`).
- DML-only seed migrations (`0002, 0006, 0011`); `0009` creates RLS roles/policies;
  `0009`/`0010` contain `DO $$ … $$` blocks.
- **Two runners:** the `make migrate` Docker `psql` loop, and
  `cmd/migrate/main.go` — an `embed.FS` + pgx runner with **no version tracking**
  (fails on re-run by design), used for Railway deploy-from-binary.
- **CI `migrations` job** applies a hardcoded list of files via `psql -f` then runs
  ~15 hand-written table/column/index/RLS assertions. No up→down→up cycle today.
- **sqlc.yaml** points its schema at a hand-picked subset of `.up.sql` files
  (includes `0001,0003,0004,0005,0007,0008,0010`; excludes the seeds and the RLS
  migration `0009`).
- **Tool deps** use Go 1.25's `tool` directive (`go tool oapi-codegen`). No migration
  library present yet.

### Decisions locked during brainstorming

- **Tool:** goose (chosen over golang-migrate). Rationale: per-migration transactions
  by default, single-file up/down, first-class `embed.FS`, optional Go migrations later.
- **Baseline:** irreversible baseline floor. The existing 11 migrations become an
  immutable baseline; true reversibility is required only for migrations added from
  Chunk 1 onward.

## 2. File-format conversion (mechanical, behavior-preserving)

Goose SQL migrations are **single-file with `-- +goose Up` / `-- +goose Down`
sections** — goose does **not** read `.up.sql`/`.down.sql` pairs. Therefore:

- Rename `migrations/NNNN_name.up.sql` → `migrations/NNNN_name.sql`. The existing
  zero-padded prefixes map directly to goose sequential versions `1…11`.
- Move the existing body under `-- +goose Up`.
- Add a `-- +goose Down` section that is an **explicit guarded no-op**, e.g.:
  ```sql
  -- +goose Down
  -- Baseline migration: irreversible. Roll back by restoring from backup.
  SELECT 'baseline migration NNNN is irreversible' WHERE false;
  ```
- **Strip the self-managed `BEGIN;`/`COMMIT;`** from `0002, 0003, 0006, 0009, 0011`.
  Goose wraps each migration in a transaction by default; a nested `COMMIT` would
  close goose's transaction early and break atomicity.
- Wrap the `DO $$ … $$` blocks with `-- +goose StatementBegin` /
  `-- +goose StatementEnd` so goose's naive `;` splitter does not break on
  semicolons inside the dollar-quoted bodies. Affected: **`0009`** (5 blocks),
  **`0010`** (1 block).
- **NO TRANSACTION audit (completed for the baseline).** Every migration goose runs
  inside its own transaction, so any statement that cannot run in a transaction
  (`CREATE INDEX CONCURRENTLY`, `DROP INDEX CONCURRENTLY`, `ALTER TYPE … ADD VALUE`,
  `CREATE/DROP DATABASE`, `VACUUM`, `REINDEX`, `ALTER SYSTEM`, …) must be split into
  its own migration marked `-- +goose NO TRANSACTION`. A grep across all 11 baseline
  files (`migrations/*.up.sql`) found **none** of these — so **no baseline file needs
  `-- +goose NO TRANSACTION`.** Every migration added from Chunk 1 onward must be
  re-audited the same way; this is recorded as policy in ADR-P001 (§8).

No SQL **semantics** change in this step — it is a pure reformat. The proof that
the reformat is faithful is that a fresh `up` still produces the identical schema
(see §7 `TestFreshUp` and the CI table/column assertions).

## 3. The runner — extend `cmd/migrate` to use goose as a library

Keep the single embedded binary so the Railway deploy-from-image story is preserved
and there is **one code path** for local, CI, and production. No goose **CLI**
dependency — goose is added as a Go library (`github.com/pressly/goose/v3`) and
driven through its Go API over the embedded filesystem:

```go
goose.SetBaseFS(migrations.FS)
goose.SetDialect("postgres")
```

`cmd/migrate` gains subcommands:

| Command                  | Behavior                                                        |
|--------------------------|----------------------------------------------------------------|
| `up`                     | apply all pending migrations                                   |
| `down`                   | roll back the most recent migration                            |
| `down-to <version>`      | roll back to a target version (used by CI reversibility proof) |
| `status`                 | print applied/pending state                                    |
| `version`                | print current DB version                                       |
| `bootstrap`              | converge an existing DB onto the version table (see §4)        |

- `migrations/embed.go`: change the embed glob `0*.up.sql` → `0*.sql`.

**Version table:** goose-native `goose_db_version` (default).

> ⚠️ **Flagged deviation.** The source prompt's literal wording asked for "a
> `schema_migrations` version table" — that is golang-migrate's table name and
> shape. Because goose was chosen, we keep goose's native `goose_db_version` table
> (columns `id, version_id, is_applied, tstamp`) rather than disguising goose under
> the `schema_migrations` name. This was raised and accepted during brainstorming.
> goose's table name *can* be aliased via `goose.SetTableName("schema_migrations")`
> if a future requirement demands the literal name; not done now (least surprise).

## 4. Baseline convergence — `cmd/migrate bootstrap`

Existing staging/prod databases already have `0001…0011` applied (via the old
embedded `cmd/migrate`, which globs the full `0*.up.sql` set) with **no version
tracking**. Running goose against them naively would find no `goose_db_version`
table and try to apply everything from scratch, failing on already-existing objects.
`bootstrap` resolves this idempotently using a **full-footprint check** — never a
single-object probe:

- **Fresh database** — **zero** baseline tables present → apply versions `1…11`
  normally. This is the fresh branch and is tested directly (`TestFreshBootstrap`,
  §7), not just via a raw `up`.
- **Already-migrated database** — the **complete** baseline footprint is present
  (all **20** baseline tables, the two RLS roles `image_platform_api` /
  `image_platform_system` created in `0009`, and the `0011` fal seed sentinel row) →
  create the version table and **stamp versions `1…11` as applied without running
  them**, so a subsequent `up` is a clean no-op.
- **Present but incomplete** — *some but not all* of the footprint present →
  **refuse loudly, exit non-zero, and stamp nothing.** A partially-migrated database
  is an unknown state a human must resolve; bootstrap must never guess.
- Safe to re-run: if `goose_db_version` already exists, `bootstrap` is a no-op that
  delegates to `up`.

**Why a full-footprint check and not a single early table — do not "simplify" this
back to one table.** A single early sentinel such as `generation_jobs` only proves
that *some* migration ran, not that the database is fully converged at baseline 11.
A DB stuck partway (say at version 7) still has `generation_jobs`; a single-object
probe would classify it "already-migrated", stamp `1…11`, and **silently skip
`8…11` forever** — the exact silent mis-stamp this guard exists to prevent. (Keying
off `generation_jobs` would additionally violate locked hardening #1.) A single
early object structurally *cannot* distinguish "fully at 11" from "partway", so
detection must verify the whole footprint, and any state between empty and complete
is **refused, not guessed** (locked hardening #2, extended to partial schema).
Because `0011` is seed-only (no DDL), the footprint also verifies its seed sentinel
so a DDL-complete-but-seed-missing database is treated as incomplete and refused
rather than stamped.

The stamping inserts the version rows goose expects (`version_id` `0` plus `1…11`,
`is_applied = true`) so goose's own bookkeeping is consistent afterward. The exact
enumerated object list (20 table names + 2 role names + the fal seed sentinel) is
fixed in the implementation plan.

## 5. Reversibility policy — baseline floor

- Baseline migrations `1…11` have **no-op `Down` sections** — production rollback is
  "restore from backup", never "roll the schema to empty".
- **Every migration from Chunk 1 onward MUST ship a real, tested `Down`.**
- CI proves the round-trip on everything **above** the baseline:
  `up (to head) → down-to 11 → up (to head)`. In Chunk 0 there are no
  above-baseline migrations yet, so the *harness's* reversibility is proven instead
  by the `TestGooseRoundTrip` canary test (§7).
- The **expand → backfill → contract** policy governs all future schema changes and
  is recorded in ADR-P001 (§8): additive "expand" changes ship first; data backfills
  are separate migrations; destructive "contract" changes land last, only after the
  expand deploy has settled.

## 6. Codegen, Makefile, CI

### sqlc
- Keep the **same explicit ordered schema list** in `sqlc.yaml`, with each entry
  renamed `.up.sql` → `.sql`. This preserves the current include/exclude set —
  importantly it still **excludes `0009`** so sqlc's Postgres parser never encounters
  `CREATE POLICY` / `CREATE ROLE`.
- sqlc is goose-annotation-aware and parses only the `-- +goose Up` section, ignoring
  `Down`. Correctness is guaranteed by CI's existing "generated files are committed"
  gate (`git diff --exit-code` after `make generate`) — codegen output must not drift.

### Makefile
- `make migrate` → `go run ./cmd/migrate up`.
- Add `make migrate-down` → `go run ./cmd/migrate down`, `make migrate-status` →
  `go run ./cmd/migrate status`.
- Delete the bare `psql` apply loop. Update the `help` target text.

### CI `migrations` job
- Replace the hardcoded `psql -f …` apply block with `go run ./cmd/migrate up`.
- **Keep** the existing table / column / index / RLS-enforcement assertions — they are
  valuable smoke tests of the baseline schema.
- **Update the table-count assertion — and note the coverage change.** The current
  job applies only `0001…0009` via `psql` and asserts `18` tables; its hardcoded list
  never applied `0010` (webhooks) or `0011` (fal seed). Switching to
  `go run ./cmd/migrate up` applies the **full** set `0001…0011`, which additionally
  creates the two `0010` webhook tables and runs the `0011` fal seed. Post-`up` count
  is therefore **20 baseline tables + `goose_db_version` = 21** (not 19). The plan
  must confirm `0010`/`0011` apply cleanly in sequence under goose and add a
  fal-provider-seed presence assertion alongside the existing mock/bfl ones.
- **Add** assertions:
  - the version table reports head version `11` after `up`;
  - convergence path: raw-apply the baseline → `bootstrap` → assert a following `up`
    is a no-op;
  - reversibility self-test (the `TestGooseRoundTrip` integration test runs under the
    existing `go test -tags=integration ./...` step against the CI Postgres).

## 7. Tests (TDD — failing test first, integration-tagged, real Postgres)

Written before implementation; each runs under `-tags=integration` against the CI
Postgres service.

1. **`TestGooseRoundTrip`** *(the first failing test)* — apply a disposable **canary**
   reversible migration from a `testdata/` directory (not the real `migrations/` dir)
   via goose → assert its object exists → `down` → assert gone → `up` → assert back.
   Proves the harness genuinely reverses, independent of the irreversible baseline.
2. **`TestBaselineConvergence`** — raw-apply the 11 baseline files to a clean DB
   (simulating an existing prod DB with the full footprint) → run `bootstrap` →
   assert `goose_db_version` head = `11` → assert a following `up` is a no-op
   (no error, no schema change).
3. **`TestFreshBootstrap`** — empty DB → `bootstrap` → assert it takes the **fresh
   branch** and applies versions `1…11` (head = `11`, all baseline objects present).
   This tests the fresh path of `bootstrap` *directly*, distinct from `TestFreshUp`'s
   plain `up`.
4. **`TestBootstrapRefusesPartial`** — apply only a *partial* baseline (e.g. through
   `0007`, so the footprint is present-but-incomplete) → run `bootstrap` → assert it
   **refuses loudly**: exits non-zero, creates **no** `goose_db_version` table, and
   stamps nothing. Locks the refuse-on-partial branch from §4.
5. **`TestFreshUp`** — empty DB → `up` → assert all **20** baseline tables plus the
   goose version table exist, and that the `0009`/`0010` `DO`-blocks parsed and
   applied (RLS roles/policies and webhook objects present).

## 8. Docs (D-9 evidence-backed; scoped to what this chunk makes false)

- **New:** `docs/adr/ADR-P001-migration-tooling.md` (rule **D-5**, `ADR-P###`
  numbering). Records: the goose choice and rationale; the irreversible-baseline-floor
  decision; the `bootstrap`/stamp convergence mechanism **and the full-footprint
  detection rationale** (why never a single early sentinel; refuse-on-partial); the
  **expand → backfill → contract** policy for all future schema; the **NO-TRANSACTION
  audit requirement** for every future migration; the reversibility requirement from
  Chunk 1 onward; the flagged `goose_db_version` vs `schema_migrations` deviation.
  Cites the proving files/lines (runner subcommands, CI step, `bootstrap`
  implementation).
- **Update only what Chunk 0 falsifies:**
  - README + Makefile `help` lines describing `make migrate` applying `…up.sql` via
    `psql`.
  - The DECISIONS.md line stating `make migrate` applies `docs/db/initial_schema.sql`.
  - Stale `migrations/000N_….up.sql` filename references in code comments:
    `internal/providers/bfl/bfl.go`, `internal/providers/fal/fal.go`,
    `internal/db/system.go`, `internal/db/tenant.go`.
- **Explicitly deferred to the later Docs chunk** (not this PR): the broad README
  "Phase 0 / v0.5.0" cleanup, adding fal to DECISIONS.md, the OpenAPI version
  correction, and "PLANNED" cost-API relabeling.

## 9. Out of scope for Chunk 0

- No new schema; no DTO / OpenAPI / governance / cost-routing work (Chunks 1–9).
- No goose CLI dependency (library only).
- No rewrite of existing migration SQL semantics — mechanical reformat only.
- No change to the worker RLS posture beyond what the reformat strictly requires.

## 10. Acceptance criteria

- `make migrate` applies and `make migrate-down` rolls back via goose; no bare `psql`
  loop remains in the Makefile or CI.
- CI proves the harness round-trips (canary up → down → up); that an already-migrated
  DB converges via `bootstrap` without destructive re-apply; that a fresh DB
  bootstraps to `1…11`; and that a **partially-migrated DB is refused, not stamped**.
- `sqlc` regenerated with zero diff; existing tests green; CI table/column/RLS
  assertions still pass, with the table-count assertion updated to **21**
  (20 baseline tables + `goose_db_version`) now that `up` applies the full
  `0001…0011` set.
- `cmd/migrate` remains a single embedded binary (Railway deploy story intact).
- ADR-P001 written with cited evidence; falsified docs/comments corrected.

## 11. Rule compliance

| Rule | How honored |
|---|---|
| **D-5** | New ADR numbered `ADR-P001` (platform `ADR-P###` convention). |
| **D-6** | New docs written under `/docs`. |
| **D-9** | Doc/comment edits scoped to claims this chunk makes false, each cited. |
| **Process law / TDD** | Failing integration test (`TestGooseRoundTrip`) written first. |
| **D-3 / E-1 / D-4 / D-8** | No conflict — tooling-only chunk; no content policy, JSONB, or sync paths touched. |

**Flagged deviation (not a rule conflict):** version-table name is goose-native
`goose_db_version`, not the prompt's literally-worded `schema_migrations` (§3).
