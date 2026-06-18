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
