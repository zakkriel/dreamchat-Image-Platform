//go:build integration

package migrate_test

import (
	"embed"
	"testing"

	"github.com/pressly/goose/v3"

	"github.com/zakkriel/drchat-image-platform/internal/migrate"
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
