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
