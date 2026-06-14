package db

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// tenantGUC is the session/transaction setting the RLS policies in
// migrations/0009_rls_tenant_isolation.up.sql read. Every tenant-scoped DB
// statement on the request path must run with this set, or RLS denies by
// default (an unset GUC becomes NULL and matches no tenant-owned rows).
const tenantGUC = "app.current_tenant"

// ErrNoTenant is returned by SetTenantLocal / WithTenant when called with an
// empty tenant id. A tenant-scoped statement with no tenant is an internal bug
// (it would silently see zero rows under RLS), so we surface it loudly instead.
var ErrNoTenant = errors.New("db: empty tenant id for tenant-scoped execution")

// SetTenantLocal sets app.current_tenant for the duration of tx using
// set_config(..., is_local => true). It is the building block for services
// that already own their transaction (jobs.Service.CreateAndEnqueue,
// adminjobs cancel/retry, the cost reserve inside create, …): call it once
// right after BeginTx, before any tenant-owned query, so the rows the
// transaction touches are scoped to tenantID under RLS. The setting is
// transaction-local and is discarded when the transaction ends, so it never
// leaks onto the next checkout of a pooled connection.
func SetTenantLocal(ctx context.Context, tx pgx.Tx, tenantID string) error {
	if tenantID == "" {
		return ErrNoTenant
	}
	// Third argument true => the setting is local to the current transaction.
	_, err := tx.Exec(ctx, "SELECT set_config($1, $2, true)", tenantGUC, tenantID)
	return err
}

// WithTenant runs fn inside a transaction on pool with app.current_tenant set to
// tenantID (transaction-local). It begins the transaction, sets the tenant GUC,
// invokes fn with the transaction, and commits on success or rolls back on
// error. Because the GUC is transaction-local it ends with the transaction and
// cannot leak across pooled connections.
//
// This is the default tenant executor for request-path DB work that is not
// already inside a service-owned transaction. Read-only queries also need the
// GUC, so reads go through here too.
func WithTenant(ctx context.Context, pool *pgxpool.Pool, tenantID string, fn func(ctx context.Context, tx pgx.Tx) error) error {
	if tenantID == "" {
		return ErrNoTenant
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	if err := SetTenantLocal(ctx, tx, tenantID); err != nil {
		return err
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	committed = true
	return nil
}
