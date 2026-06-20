//go:build integration

// WARNING: Do NOT add t.Parallel() to any test in this package.
// The goose wrappers (goose.SetBaseFS, goose.SetDialect) mutate process-global
// state; parallel tests would race on that shared state and produce
// non-deterministic failures.

package migrate_test

import (
	"database/sql"
	"embed"
	"io/fs"
	"sort"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"

	"github.com/zakkriel/drchat-image-platform/internal/migrate"
	"github.com/zakkriel/drchat-image-platform/internal/testdb"
	"github.com/zakkriel/drchat-image-platform/migrations"
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
	if v < migrate.BaselineVersion {
		t.Fatalf("version = %d, want >= baseline %d", v, migrate.BaselineVersion)
	}
}

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
		// rawApplyAll simulates "an existing prod DB at version 11": apply
		// only baseline migrations (0001–0011). Post-baseline migrations
		// (0012+) include reversible Down sections whose raw execution would
		// leave schema objects in an inconsistent state (e.g. chunk-1 tables
		// created then immediately dropped). Bootstrap stamps v11 and then Up
		// re-applies post-baseline migrations via goose, so skipping them here
		// is correct. Files are sorted, so the first name > "0011" is the
		// stop marker.
		if n >= "0012" {
			break
		}
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
	if v < migrate.BaselineVersion {
		t.Fatalf("version = %d, want >= baseline %d", v, migrate.BaselineVersion)
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
	if v < migrate.BaselineVersion {
		t.Fatalf("version = %d, want >= baseline %d", v, migrate.BaselineVersion)
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
