//go:build integration

package jobs_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zakkriel/drchat-image-platform/internal/auth"
	"github.com/zakkriel/drchat-image-platform/internal/cost"
	appdb "github.com/zakkriel/drchat-image-platform/internal/db"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
)

// Phase 7C-3 RLS enforcement tests. These connect as the non-superuser,
// RLS-enforced API role (openAPITestPool / POSTGRES_API_DSN) — the ONLY way to
// prove the policies are enforced, since the system/owner role used by the rest
// of the suite bypasses RLS even under FORCE. Fixtures are seeded and torn down
// via the system/bypass pool (openTestPool) so setup is not itself subject to
// RLS (§22.6a two-pool split).

const (
	rlsTenantA = "tenant_rls_a"
	rlsTenantB = "tenant_rls_b"
	rlsTokenA  = "tok_rls_a"
	rlsTokenB  = "tok_rls_b"
	rlsStyleA  = "sty_rls_a"
	rlsStyleB  = "sty_rls_b"
	rlsJobA    = "job_rls_a"
	rlsJobB    = "job_rls_b"
)

func seedRLSFixtures(t *testing.T, sys *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	rlsCleanup(t, sys)
	exec := func(sql string, args ...any) {
		if _, err := sys.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed %q: %v", sql, err)
		}
	}
	for _, tt := range []struct{ tenant, tok, sty, job string }{
		{rlsTenantA, rlsTokenA, rlsStyleA, rlsJobA},
		{rlsTenantB, rlsTokenB, rlsStyleB, rlsJobB},
	} {
		exec(`INSERT INTO api_tokens (id, tenant_id, token_prefix, token_hash, name, owner_type, scopes, environment, status)
		      VALUES ($1, $2, $3, 'h', 't', 'tenant', ARRAY['images:write','jobs:read'], 'dev', 'active')`,
			tt.tok, tt.tenant, "pfx_"+tt.tok)
		exec(`INSERT INTO style_profiles (id, tenant_id, name, style_mode, positive_prompt, default_quality_tier, status)
		      VALUES ($1, $2, 'n', 'open_prompt', 'p', 'standard', 'active')`, tt.sty, tt.tenant)
		exec(`INSERT INTO generation_jobs (id, tenant_id, job_type, status) VALUES ($1, $2, 'artifact', 'queued')`, tt.job, tt.tenant)
		exec(`INSERT INTO idempotency_keys (id, token_id, key, endpoint, request_hash, expires_at)
		      VALUES ($1, $2, 'k', '/e', 'h', now() + interval '1 day')`, "idk_"+tt.tok, tt.tok)
	}
}

func rlsCleanup(t *testing.T, sys *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	for _, tenant := range []string{rlsTenantA, rlsTenantB} {
		stmts := []string{
			`DELETE FROM idempotency_keys WHERE token_id IN (SELECT id FROM api_tokens WHERE tenant_id = $1)`,
			`DELETE FROM generation_cost_events WHERE tenant_id = $1`,
			`DELETE FROM cost_reservation_budget_holds WHERE cost_reservation_id IN (SELECT id FROM cost_reservations WHERE tenant_id = $1)`,
			`UPDATE generation_jobs SET cost_reservation_id = NULL WHERE tenant_id = $1`,
			`DELETE FROM cost_reservations WHERE tenant_id = $1`,
			`DELETE FROM generation_jobs WHERE tenant_id = $1`,
			`DELETE FROM cost_budgets WHERE tenant_id = $1`,
			`DELETE FROM style_profiles WHERE tenant_id = $1`,
			`DELETE FROM api_tokens WHERE tenant_id = $1`,
		}
		for _, s := range stmts {
			if _, err := sys.Exec(ctx, s, tenant); err != nil {
				t.Logf("rls cleanup %q: %v", s, err)
			}
		}
	}
}

// withGUC runs fn in a transaction on pool with app.current_tenant set to
// tenant (or unset when tenant == "").
func withGUC(t *testing.T, pool *pgxpool.Pool, tenant string, fn func(tx pgx.Tx)) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if tenant != "" {
		if _, err := tx.Exec(ctx, "SELECT set_config('app.current_tenant', $1, true)", tenant); err != nil {
			t.Fatalf("set guc: %v", err)
		}
	}
	fn(tx)
}

func countTx(t *testing.T, tx pgx.Tx, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := tx.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", sql, err)
	}
	return n
}

// TestRLSEnabledForcedAndPolicies proves the configuration itself: representative
// protected tables have relrowsecurity AND relforcerowsecurity, the global
// reference tables do not, and the tenant_isolation policy exists in pg_policies.
func TestRLSEnabledForcedAndPolicies(t *testing.T) {
	sys := openTestPool(t)
	defer sys.Close()
	ctx := context.Background()

	protected := []string{"generation_jobs", "visual_assets", "api_tokens", "cost_reservations", "idempotency_keys", "asset_pack_items"}
	for _, tbl := range protected {
		var enabled, forced bool
		if err := sys.QueryRow(ctx,
			`SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE relname = $1`, tbl).Scan(&enabled, &forced); err != nil {
			t.Fatalf("pg_class %s: %v", tbl, err)
		}
		if !enabled || !forced {
			t.Fatalf("%s: expected RLS enabled+forced, got enabled=%v forced=%v", tbl, enabled, forced)
		}
		var policies int
		if err := sys.QueryRow(ctx,
			`SELECT count(*) FROM pg_policies WHERE tablename = $1 AND policyname = 'tenant_isolation'`, tbl).Scan(&policies); err != nil {
			t.Fatalf("pg_policies %s: %v", tbl, err)
		}
		if policies != 1 {
			t.Fatalf("%s: expected one tenant_isolation policy, got %d", tbl, policies)
		}
	}

	for _, tbl := range []string{"provider_models", "provider_routes", "provider_model_prices"} {
		var enabled bool
		if err := sys.QueryRow(ctx, `SELECT relrowsecurity FROM pg_class WHERE relname = $1`, tbl).Scan(&enabled); err != nil {
			t.Fatalf("pg_class %s: %v", tbl, err)
		}
		if enabled {
			t.Fatalf("%s is a global reference table; RLS must NOT be enabled", tbl)
		}
	}
}

// TestRLSTenantVisibilityAndDenyByDefault is the core acceptance proof under the
// non-superuser API role: tenant A sees only A, tenant B sees only B, and an
// unset GUC sees zero protected rows (deny-by-default). It also proves a wrong
// GUC hides a row even when the query omits the tenant predicate.
func TestRLSTenantVisibilityAndDenyByDefault(t *testing.T) {
	sys := openTestPool(t)
	defer sys.Close()
	api := openAPITestPool(t)
	defer api.Close()
	seedRLSFixtures(t, sys)
	defer rlsCleanup(t, sys)

	withGUC(t, api, rlsTenantA, func(tx pgx.Tx) {
		if got := countTx(t, tx, `SELECT count(*) FROM generation_jobs WHERE id = $1`, rlsJobA); got != 1 {
			t.Fatalf("tenant A must see its own job, got %d", got)
		}
		if got := countTx(t, tx, `SELECT count(*) FROM generation_jobs WHERE id = $1`, rlsJobB); got != 0 {
			t.Fatalf("tenant A must NOT see tenant B's job, got %d", got)
		}
		// Wrong/own GUC hides the other tenant's row even with NO tenant predicate.
		if got := countTx(t, tx, `SELECT count(*) FROM generation_jobs`); got != 1 {
			t.Fatalf("tenant A must see exactly its own jobs with no predicate, got %d", got)
		}
	})

	withGUC(t, api, rlsTenantB, func(tx pgx.Tx) {
		if got := countTx(t, tx, `SELECT count(*) FROM generation_jobs WHERE id = $1`, rlsJobB); got != 1 {
			t.Fatalf("tenant B must see its own job, got %d", got)
		}
		if got := countTx(t, tx, `SELECT count(*) FROM generation_jobs WHERE id = $1`, rlsJobA); got != 0 {
			t.Fatalf("tenant B must NOT see tenant A's job, got %d", got)
		}
	})

	// Unset GUC: zero protected rows across direct + child tables.
	withGUC(t, api, "", func(tx pgx.Tx) {
		for _, tbl := range []string{"generation_jobs", "api_tokens", "style_profiles", "idempotency_keys"} {
			if got := countTx(t, tx, `SELECT count(*) FROM `+tbl); got != 0 {
				t.Fatalf("unset GUC must see zero %s rows, got %d", tbl, got)
			}
		}
		// Global reference tables remain readable with no GUC.
		if got := countTx(t, tx, `SELECT count(*) FROM provider_models`); got == 0 {
			t.Fatalf("global provider_models must remain readable with no GUC")
		}
	})
}

// TestRLSWithCheckBlocksCrossTenantWrites proves WITH CHECK rejects both
// inserting a row for another tenant and updating a row to another tenant.
func TestRLSWithCheckBlocksCrossTenantWrites(t *testing.T) {
	sys := openTestPool(t)
	defer sys.Close()
	api := openAPITestPool(t)
	defer api.Close()
	seedRLSFixtures(t, sys)
	defer rlsCleanup(t, sys)

	ctx := context.Background()
	withGUC(t, api, rlsTenantA, func(tx pgx.Tx) {
		if _, err := tx.Exec(ctx,
			`INSERT INTO generation_jobs (id, tenant_id, job_type, status) VALUES ('job_rls_x', $1, 'artifact', 'queued')`, rlsTenantB); err == nil {
			t.Fatalf("WITH CHECK must reject inserting a tenant_B row under GUC=tenant_A")
		}
	})
	// Update moving A's own row to tenant B must also be rejected (and find the
	// row only because GUC=A makes it visible).
	withGUC(t, api, rlsTenantA, func(tx pgx.Tx) {
		if _, err := tx.Exec(ctx,
			`UPDATE generation_jobs SET tenant_id = $1 WHERE id = $2`, rlsTenantB, rlsJobA); err == nil {
			t.Fatalf("WITH CHECK must reject moving a row to another tenant")
		}
	})
}

// TestRLSChildTableIsolation proves a child table (idempotency_keys via
// api_tokens) is isolated by the parent-join policy.
func TestRLSChildTableIsolation(t *testing.T) {
	sys := openTestPool(t)
	defer sys.Close()
	api := openAPITestPool(t)
	defer api.Close()
	seedRLSFixtures(t, sys)
	defer rlsCleanup(t, sys)

	withGUC(t, api, rlsTenantA, func(tx pgx.Tx) {
		if got := countTx(t, tx, `SELECT count(*) FROM idempotency_keys WHERE token_id = $1`, rlsTokenA); got != 1 {
			t.Fatalf("tenant A must see its own idempotency_keys, got %d", got)
		}
		if got := countTx(t, tx, `SELECT count(*) FROM idempotency_keys WHERE token_id = $1`, rlsTokenB); got != 0 {
			t.Fatalf("tenant A must NOT see tenant B's idempotency_keys, got %d", got)
		}
	})
	withGUC(t, api, "", func(tx pgx.Tx) {
		if got := countTx(t, tx, `SELECT count(*) FROM idempotency_keys`); got != 0 {
			t.Fatalf("unset GUC must see zero idempotency_keys, got %d", got)
		}
	})
}

// TestRLSAdminSystemCrossTenant proves the system/BYPASSRLS role can perform the
// legitimate cross-tenant read admin endpoints rely on.
func TestRLSAdminSystemCrossTenant(t *testing.T) {
	sys := openTestPool(t)
	defer sys.Close()
	seedRLSFixtures(t, sys)
	defer rlsCleanup(t, sys)

	apiDSN := openAPITestPool(t) // skips if POSTGRES_API_DSN unset
	apiDSN.Close()

	ctx := context.Background()
	var n int
	if err := sys.QueryRow(ctx,
		`SELECT count(*) FROM generation_jobs WHERE id IN ($1, $2)`, rlsJobA, rlsJobB).Scan(&n); err != nil {
		t.Fatalf("system cross-tenant read: %v", err)
	}
	if n != 2 {
		t.Fatalf("system role must read both tenants' jobs, got %d", n)
	}
}

// TestWithTenantExecutorSetsGUCAndNoLeak proves the db.WithTenant executor: the
// GUC is set inside the transaction, the tenant-scoped read is isolated, and the
// setting does not leak after commit or rollback (next checkout has no GUC).
func TestWithTenantExecutorSetsGUCAndNoLeak(t *testing.T) {
	sys := openTestPool(t)
	defer sys.Close()
	api := openAPITestPool(t)
	defer api.Close()
	seedRLSFixtures(t, sys)
	defer rlsCleanup(t, sys)

	ctx := context.Background()

	// GUC is set inside, and the scoped read sees only tenant A.
	if err := appdb.WithTenant(ctx, api, rlsTenantA, func(ctx context.Context, tx pgx.Tx) error {
		var cur string
		if err := tx.QueryRow(ctx, `SELECT current_setting('app.current_tenant', true)`).Scan(&cur); err != nil {
			return err
		}
		if cur != rlsTenantA {
			t.Fatalf("WithTenant must set GUC to %q, got %q", rlsTenantA, cur)
		}
		if got := countTx(t, tx, `SELECT count(*) FROM generation_jobs`); got != 1 {
			t.Fatalf("tenant executor read must see only tenant A's job, got %d", got)
		}
		return nil
	}); err != nil {
		t.Fatalf("WithTenant: %v", err)
	}

	// No leak after commit: drain the pool and confirm a fresh acquire has no GUC.
	assertNoLeak(t, api)

	// No leak after rollback (fn returns an error).
	wantErr := errors.New("boom")
	if err := appdb.WithTenant(ctx, api, rlsTenantA, func(ctx context.Context, tx pgx.Tx) error {
		return wantErr
	}); !errors.Is(err, wantErr) {
		t.Fatalf("WithTenant should surface fn error, got %v", err)
	}
	assertNoLeak(t, api)

	// A bare read outside the tenant executor (no GUC) sees zero protected rows.
	var n int
	if err := api.QueryRow(ctx, `SELECT count(*) FROM generation_jobs`).Scan(&n); err != nil {
		t.Fatalf("bare read: %v", err)
	}
	if n != 0 {
		t.Fatalf("read outside tenant executor must see zero rows, got %d", n)
	}
}

// assertNoLeak checks that newly-acquired connections carry no app.current_tenant
// (the transaction-local GUC must not survive onto a pooled connection).
func assertNoLeak(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		var cur string
		if err := pool.QueryRow(ctx, `SELECT current_setting('app.current_tenant', true)`).Scan(&cur); err != nil {
			t.Fatalf("leak check: %v", err)
		}
		if cur != "" {
			t.Fatalf("tenant GUC leaked onto a pooled connection: %q", cur)
		}
	}
}

// TestServiceOwnedTransactionUnderAPIRole proves jobs.Service.CreateAndEnqueue
// sets the tenant GUC inside its own transaction: the whole create lands under
// the non-superuser API role (every WITH CHECK passes because the GUC matches),
// and the job is created for the right tenant. If the service did NOT set the
// GUC, the api_tokens FK / generation_jobs WITH CHECK would reject the insert.
func TestServiceOwnedTransactionUnderAPIRole(t *testing.T) {
	sys := openTestPool(t)
	defer sys.Close()
	api := openAPITestPool(t)
	defer api.Close()
	seedRLSFixtures(t, sys)
	defer rlsCleanup(t, sys)

	ctx := context.Background()
	// A tenant budget so the cost reserve exercises the budget-hold write path
	// (cost_budgets UPDATE + cost_reservation_budget_holds INSERT) under RLS too.
	if _, err := sys.Exec(ctx,
		`INSERT INTO cost_budgets (id, tenant_id, scope_type, scope_id, period, limit_amount, status)
		 VALUES ('bud_rls_a', $1, 'tenant', $1, 'daily', '5.0000', 'active')`, rlsTenantA); err != nil {
		t.Fatalf("seed budget: %v", err)
	}

	enq := newRecordingEnqueuer()
	svc := jobs.NewService(api, enq, cost.NewService(nil)).WithFinalizer(cost.NewLifecycle(api, nil))

	res, err := svc.CreateAndEnqueue(ctx, jobs.CreateAndEnqueueParams{
		TenantID:           rlsTenantA,
		RequestedByTokenID: rlsTokenA,
		JobType:            "artifact",
		WorldID:            "w1",
		InputPayload:       map[string]any{"style_profile_id": rlsStyleA, "description": "rls"},
		CacheResult:        "generated_required",
		FallbackPolicy:     "none",
		ProviderID:         "mock",
		ModelID:            "pm_mock_v1",
		OperationType:      "text_to_image",
		Units:              1,
	})
	if err != nil {
		t.Fatalf("CreateAndEnqueue under API role: %v", err)
	}
	if res.Status != "queued" {
		t.Fatalf("expected queued, got %q", res.Status)
	}

	// Verify via the system pool that the job exists for tenant A and that the
	// tenant-owned reservation + budget hold landed (proving the GUC was set
	// inside the service transaction: every WITH CHECK passed under the API role).
	var tenant string
	if err := sys.QueryRow(ctx, `SELECT tenant_id FROM generation_jobs WHERE id = $1`, res.JobID).Scan(&tenant); err != nil {
		t.Fatalf("read created job: %v", err)
	}
	if tenant != rlsTenantA {
		t.Fatalf("job created for wrong tenant: %q", tenant)
	}
	var holds int
	if err := sys.QueryRow(ctx,
		`SELECT count(*) FROM cost_reservation_budget_holds h
		 JOIN cost_reservations r ON r.id = h.cost_reservation_id
		 WHERE r.generation_job_id = $1`, res.JobID).Scan(&holds); err != nil {
		t.Fatalf("read budget holds: %v", err)
	}
	if holds != 1 {
		t.Fatalf("expected one budget hold under RLS, got %d", holds)
	}

	// Clean the job + budget this test created (outside the seeded ids).
	_, _ = sys.Exec(ctx, `UPDATE generation_jobs SET cost_reservation_id = NULL WHERE id = $1`, res.JobID)
	_, _ = sys.Exec(ctx, `DELETE FROM cost_reservation_budget_holds WHERE cost_reservation_id IN (SELECT id FROM cost_reservations WHERE generation_job_id = $1)`, res.JobID)
	_, _ = sys.Exec(ctx, `DELETE FROM cost_reservations WHERE generation_job_id = $1`, res.JobID)
	_, _ = sys.Exec(ctx, `DELETE FROM generation_jobs WHERE id = $1`, res.JobID)
	_, _ = sys.Exec(ctx, `DELETE FROM cost_budgets WHERE id = 'bud_rls_a'`)
}

// TestCostLifecycleDualContext proves cost.Lifecycle is executor-agnostic:
// Release works through the system executor (worker path), and ReleaseInTx works
// composed into a tenant-scoped transaction (admin cancel/retry path), without
// the lifecycle ever choosing its own pool or hardcoding the system executor.
func TestCostLifecycleDualContext(t *testing.T) {
	sys := openTestPool(t)
	defer sys.Close()
	api := openAPITestPool(t)
	defer api.Close()
	seedRLSFixtures(t, sys)
	defer rlsCleanup(t, sys)

	ctx := context.Background()
	enq := newRecordingEnqueuer()
	svc := jobs.NewService(api, enq, cost.NewService(nil))

	create := func() string {
		res, err := svc.CreateAndEnqueue(ctx, jobs.CreateAndEnqueueParams{
			TenantID:           rlsTenantA,
			RequestedByTokenID: rlsTokenA,
			JobType:            "artifact",
			WorldID:            "w1",
			InputPayload:       map[string]any{"style_profile_id": rlsStyleA},
			CacheResult:        "generated_required",
			FallbackPolicy:     "none",
			ProviderID:         "mock",
			ModelID:            "pm_mock_v1",
			OperationType:      "text_to_image",
			Units:              1,
		})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		return res.JobID
	}
	reservationStatus := func(jobID string) string {
		var s string
		if err := sys.QueryRow(ctx, `SELECT status FROM cost_reservations WHERE generation_job_id = $1`, jobID).Scan(&s); err != nil {
			t.Fatalf("read reservation: %v", err)
		}
		return s
	}
	cleanupJob := func(jobID string) {
		_, _ = sys.Exec(ctx, `UPDATE generation_jobs SET cost_reservation_id = NULL WHERE id = $1`, jobID)
		_, _ = sys.Exec(ctx, `DELETE FROM generation_cost_events WHERE job_id = $1`, jobID)
		_, _ = sys.Exec(ctx, `DELETE FROM cost_reservation_budget_holds WHERE cost_reservation_id IN (SELECT id FROM cost_reservations WHERE generation_job_id = $1)`, jobID)
		_, _ = sys.Exec(ctx, `DELETE FROM cost_reservations WHERE generation_job_id = $1`, jobID)
		_, _ = sys.Exec(ctx, `DELETE FROM generation_jobs WHERE id = $1`, jobID)
	}

	// System context: the worker's Lifecycle is constructed with the system pool
	// (BYPASSRLS); standalone Release works with no tenant GUC.
	sysJob := create()
	defer cleanupJob(sysJob)
	sysLifecycle := cost.NewLifecycle(sys, nil)
	if err := sysLifecycle.Release(ctx, sysJob); err != nil {
		t.Fatalf("system-context Release: %v", err)
	}
	if got := reservationStatus(sysJob); got != "released" {
		t.Fatalf("system-context release: expected released, got %q", got)
	}

	// Tenant context: the same Lifecycle invoked via ReleaseInTx inside a
	// tenant-scoped transaction on the API (RLS-enforced) pool. The lifecycle here
	// is constructed with the tenant pool but its pool is irrelevant — it operates
	// purely on the caller's tx.
	tenantJob := create()
	defer cleanupJob(tenantJob)
	tenantLifecycle := cost.NewLifecycle(api, nil)
	if err := appdb.WithTenant(ctx, api, rlsTenantA, func(ctx context.Context, tx pgx.Tx) error {
		return tenantLifecycle.ReleaseInTx(ctx, tx, tenantJob)
	}); err != nil {
		t.Fatalf("tenant-context ReleaseInTx: %v", err)
	}
	if got := reservationStatus(tenantJob); got != "released" {
		t.Fatalf("tenant-context release: expected released, got %q", got)
	}
}

// TestAuthPreTenantAndAPIRoleDenied proves the auth ordering: the system
// executor reads api_tokens before a tenant is known and the async last-used
// touch succeeds, while the SAME read through the RLS-enforced API role with no
// GUC sees nothing (the api_tokens policy is not weakened for prefix lookup).
func TestAuthPreTenantAndAPIRoleDenied(t *testing.T) {
	sys := openTestPool(t)
	defer sys.Close()
	api := openAPITestPool(t)
	defer api.Close()
	seedRLSFixtures(t, sys)
	defer rlsCleanup(t, sys)

	ctx := context.Background()
	prefix := "pfx_" + rlsTokenA

	// System executor: pre-tenant lookup + touch succeed.
	sysRepo := auth.NewRepository(sys)
	tok, err := sysRepo.GetActiveAPITokenByPrefix(ctx, prefix)
	if err != nil {
		t.Fatalf("system auth lookup: %v", err)
	}
	if tok.TenantID != rlsTenantA {
		t.Fatalf("expected tenant %q, got %q", rlsTenantA, tok.TenantID)
	}
	if err := sysRepo.TouchAPITokenLastUsed(ctx, tok.ID); err != nil {
		t.Fatalf("TouchAPITokenLastUsed under system executor: %v", err)
	}

	// API role with no GUC: the token lookup finds nothing (deny-by-default).
	apiRepo := auth.NewRepository(api)
	if _, err := apiRepo.GetActiveAPITokenByPrefix(ctx, prefix); !errors.Is(err, auth.ErrTokenNotFound) {
		t.Fatalf("API-role lookup with no GUC must be ErrTokenNotFound, got %v", err)
	}
}

// TestWorkerSystemAndRequestPathReads proves the worker/system read path and the
// request-path read seam. The worker loads a job by id on the system pool
// (BYPASSRLS) — the unchecked GetByID. The request-path GetByIDForTenant on the
// API pool is tenant-scoped: cross-tenant access behaves like not-found, and the
// API role's unchecked GetByID (no GUC) also sees nothing.
func TestWorkerSystemAndRequestPathReads(t *testing.T) {
	sys := openTestPool(t)
	defer sys.Close()
	api := openAPITestPool(t)
	defer api.Close()
	seedRLSFixtures(t, sys)
	defer rlsCleanup(t, sys)

	ctx := context.Background()

	// Worker/system: load job by id with no tenant context.
	if _, err := jobs.NewRepository(sys).GetByID(ctx, rlsJobA); err != nil {
		t.Fatalf("worker GetByID under system executor: %v", err)
	}

	apiRepo := jobs.NewRepository(api)
	// Own-tenant request-path read works.
	if _, err := apiRepo.GetByIDForTenant(ctx, rlsJobA, rlsTenantA); err != nil {
		t.Fatalf("own-tenant GetByIDForTenant: %v", err)
	}
	// Cross-tenant request-path read is not-found.
	if _, err := apiRepo.GetByIDForTenant(ctx, rlsJobB, rlsTenantA); !errors.Is(err, jobs.ErrNotFound) {
		t.Fatalf("cross-tenant GetByIDForTenant must be ErrNotFound, got %v", err)
	}
	// The API role cannot reach another tenant's job via the unchecked read
	// either — with no GUC, RLS hides it (the system executor is not reachable
	// from the tenant pool).
	if _, err := apiRepo.GetByID(ctx, rlsJobB); !errors.Is(err, jobs.ErrNotFound) {
		t.Fatalf("API-role unchecked GetByID must be ErrNotFound under RLS, got %v", err)
	}
}
