package db

import (
	"github.com/jackc/pgx/v5/pgxpool"
)

// SystemDB is the explicit system / RLS-bypass database executor. It is a
// distinct named type (not a bare *pgxpool.Pool) so that the system path is
// reachable only where it is deliberately wired — a normal tenant request
// handler holds the tenant pool and cannot accidentally obtain a SystemDB.
//
// It is backed by the BYPASSRLS image_platform_system role (see
// migrations/0009_rls_tenant_isolation.sql) and is used ONLY for legitimate
// cross-tenant or pre-tenant operations:
//
//   - auth token lookup before a principal (and therefore a tenant) exists
//   - the async TouchAPITokenLastUsed after auth
//   - the worker loading a job by id before it knows the tenant
//   - the system cost lifecycle running from the worker
//   - migrations / seed scripts
//   - admin handlers explicitly classified as cross-tenant, after an admin:*
//     scope check
//
// Because the underlying role bypasses RLS, callers must not route normal
// tenant create/write paths through SystemDB — that would defeat the isolation
// this PR adds.
type SystemDB struct {
	pool *pgxpool.Pool
}

// NewSystemDB wraps the system (BYPASSRLS) pool. Construct exactly one of these
// per process from the POSTGRES_SYSTEM_DSN pool and hand it only to the system
// paths listed above.
func NewSystemDB(pool *pgxpool.Pool) *SystemDB {
	return &SystemDB{pool: pool}
}

// Pool returns the underlying system pool for the system code paths that need a
// raw *pgxpool.Pool (the worker, auth repository, admin-cross-tenant services).
// It is intentionally a method on the named type rather than an exported field
// so obtaining the bypass pool is always an explicit, greppable call.
func (s *SystemDB) Pool() *pgxpool.Pool {
	if s == nil {
		return nil
	}
	return s.pool
}
