// Command migrate applies the embedded up-migrations to a Postgres database in
// filename order. It is a deliberately minimal runner for staging / Railway
// smoke testing — NOT a full migration framework:
//
//   - reads POSTGRES_DSN
//   - applies migrations/0*.up.sql (embedded) in filename order
//   - prints each migration filename before applying it
//   - fails fast on the first migration error and exits non-zero
//   - needs no Docker Compose and no local psql
//
// It does not track applied migrations, so it is intended for a fresh database
// (the first deploy of a staging environment). Re-running against an already
// migrated database will fail on the first CREATE that already exists — that is
// the intended fail-fast behavior, not a bug.
package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/zakkriel/drchat-image-platform/migrations"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "migrate: "+err.Error())
		os.Exit(1)
	}
}

func run() error {
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		return fmt.Errorf("POSTGRES_DSN is required")
	}

	names, err := fs.Glob(migrations.FS, "0*.up.sql")
	if err != nil {
		return fmt.Errorf("listing migrations: %w", err)
	}
	if len(names) == 0 {
		return fmt.Errorf("no migrations found (expected migrations/0*.up.sql)")
	}
	sort.Strings(names) // filename order == apply order (zero-padded prefixes).

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connecting to Postgres: %w", err)
	}
	defer func() { _ = conn.Close(context.Background()) }()

	for _, name := range names {
		sqlBytes, readErr := migrations.FS.ReadFile(name)
		if readErr != nil {
			return fmt.Errorf("reading %s: %w", name, readErr)
		}
		fmt.Println("applying " + name)
		// No arguments -> pgx uses the simple protocol, which executes the whole
		// multi-statement file in one implicit transaction. A file that defines
		// its own BEGIN/COMMIT (e.g. 0009) is honored as-is.
		if _, execErr := conn.Exec(ctx, string(sqlBytes)); execErr != nil {
			return fmt.Errorf("applying %s: %w", name, execErr)
		}
	}

	fmt.Printf("migrate: applied %d migration(s)\n", len(names))
	return nil
}
