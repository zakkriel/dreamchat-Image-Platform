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
