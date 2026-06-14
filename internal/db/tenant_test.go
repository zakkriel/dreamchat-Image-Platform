package db

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

// An empty tenant id is an internal bug (a tenant-scoped statement with no
// tenant would silently see zero rows under RLS), so both executors must reject
// it loudly before touching the database — i.e. without dereferencing the pool
// or tx, which lets us assert the guard with nil arguments.

func TestSetTenantLocalRejectsEmptyTenant(t *testing.T) {
	if err := SetTenantLocal(context.Background(), nil, ""); !errors.Is(err, ErrNoTenant) {
		t.Fatalf("expected ErrNoTenant, got %v", err)
	}
}

func TestWithTenantRejectsEmptyTenant(t *testing.T) {
	called := false
	err := WithTenant(context.Background(), nil, "", func(context.Context, pgx.Tx) error {
		called = true
		return nil
	})
	if !errors.Is(err, ErrNoTenant) {
		t.Fatalf("expected ErrNoTenant, got %v", err)
	}
	if called {
		t.Fatalf("fn must not run when the tenant id is empty")
	}
}
