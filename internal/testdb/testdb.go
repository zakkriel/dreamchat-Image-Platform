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
