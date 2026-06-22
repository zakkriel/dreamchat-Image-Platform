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

// TestCLIDownGuard proves the CLI refuses down-to below the baseline floor.
func TestCLIDownGuard(t *testing.T) {
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
	if err := run([]string{"down-to", "10"}, getenv); err == nil {
		t.Fatal("expected down-to 10 to be refused below baseline")
	}
}
