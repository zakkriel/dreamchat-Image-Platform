//go:build integration

package admincost_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zakkriel/drchat-image-platform/internal/admincost"
	"github.com/zakkriel/drchat-image-platform/internal/cost"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
)

// To run (requires Postgres with migrations 0001–0003 applied):
//   POSTGRES_DSN=... go test -tags=integration ./internal/admincost/...

const (
	itTenant = "tenant_it_admincost"
	itToken  = "tok_it_admincost"
)

func openPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_DSN not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
	return pool
}

func cleanup(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Logf("cleanup %q: %v", sql, err)
		}
	}
	exec(`DELETE FROM audit_events WHERE tenant_id = $1`, itTenant)
	exec(`DELETE FROM cost_reservation_budget_holds WHERE cost_reservation_id IN (SELECT id FROM cost_reservations WHERE tenant_id = $1)`, itTenant)
	exec(`UPDATE generation_jobs SET cost_reservation_id = NULL WHERE tenant_id = $1`, itTenant)
	exec(`DELETE FROM cost_reservations WHERE tenant_id = $1`, itTenant)
	exec(`DELETE FROM generation_jobs WHERE tenant_id = $1`, itTenant)
	exec(`DELETE FROM cost_budgets WHERE tenant_id = $1`, itTenant)
	exec(`DELETE FROM provider_model_prices WHERE provider_id = 'admincost_it'`)
	exec(`DELETE FROM style_profiles WHERE tenant_id = $1`, itTenant)
	exec(`DELETE FROM api_tokens WHERE id = $1`, itToken)
}

func seed(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`INSERT INTO api_tokens (id, tenant_id, token_prefix, token_hash, name, owner_type, scopes, environment, status)
		 VALUES ($1, $2, $3, 'h', 't', 'tenant', ARRAY['admin:costs'], 'dev', 'active')`,
		itToken, itTenant, "dci_it_ac"); err != nil {
		t.Fatalf("seed token: %v", err)
	}
}

func actor() admincost.Actor {
	return admincost.Actor{TokenID: itToken, TenantID: itTenant, RequestID: "req_it"}
}

func scalar(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) string {
	t.Helper()
	var out string
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&out); err != nil {
		t.Fatalf("scalar %q: %v", sql, err)
	}
	return out
}

func TestCreatePriceSupersedesPreviousActive(t *testing.T) {
	pool := openPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seed(t, pool)
	svc := admincost.NewService(pool, nil)

	first, err := svc.CreatePrice(context.Background(), actor(), admincost.CreatePriceInput{
		ProviderID: "admincost_it", ModelID: "m1", OperationType: "text_to_image",
		UnitType: "image", PricePerUnit: "0.0100",
	})
	if err != nil {
		t.Fatalf("create first price: %v", err)
	}
	second, err := svc.CreatePrice(context.Background(), actor(), admincost.CreatePriceInput{
		ProviderID: "admincost_it", ModelID: "m1", OperationType: "text_to_image",
		UnitType: "image", PricePerUnit: "0.0200",
	})
	if err != nil {
		t.Fatalf("create second price: %v", err)
	}

	// Exactly one active price for the key, and it's the second.
	activeCount := scalar(t, pool, `SELECT count(*) FROM provider_model_prices WHERE provider_id='admincost_it' AND model_id='m1' AND operation_type='text_to_image' AND is_active=true`)
	if activeCount != "1" {
		t.Fatalf("expected exactly one active price, got %s", activeCount)
	}
	activeID := scalar(t, pool, `SELECT id FROM provider_model_prices WHERE provider_id='admincost_it' AND model_id='m1' AND operation_type='text_to_image' AND is_active=true`)
	if activeID != second.ID {
		t.Fatalf("expected second price active, got %s", activeID)
	}
	firstActive := scalar(t, pool, `SELECT is_active::text FROM provider_model_prices WHERE id=$1`, first.ID)
	if firstActive != "false" {
		t.Fatalf("expected first price superseded (is_active=false), got %s", firstActive)
	}
	firstEffTo := scalar(t, pool, `SELECT (effective_to IS NOT NULL)::text FROM provider_model_prices WHERE id=$1`, first.ID)
	if firstEffTo != "true" {
		t.Fatalf("expected superseded price to have effective_to set")
	}

	// Audit row for the create.
	audit := scalar(t, pool, `SELECT count(*) FROM audit_events WHERE event_type='admin.price_book.created' AND resource_id=$1`, second.ID)
	if audit != "1" {
		t.Fatalf("expected one price_book.created audit row, got %s", audit)
	}
}

func TestUpdatePriceMutableFields(t *testing.T) {
	pool := openPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seed(t, pool)
	svc := admincost.NewService(pool, nil)

	p, err := svc.CreatePrice(context.Background(), actor(), admincost.CreatePriceInput{
		ProviderID: "admincost_it", ModelID: "m2", OperationType: "text_to_image",
		UnitType: "image", PricePerUnit: "0.0100",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	notes := "deprecated"
	inactive := false
	updated, err := svc.UpdatePrice(context.Background(), actor(), p.ID, admincost.UpdatePriceInput{
		IsActive: &inactive,
		Notes:    &notes,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.IsActive {
		t.Fatalf("expected is_active=false after update")
	}
	if updated.Notes == nil || *updated.Notes != "deprecated" {
		t.Fatalf("expected notes updated, got %v", updated.Notes)
	}
	audit := scalar(t, pool, `SELECT count(*) FROM audit_events WHERE event_type='admin.price_book.updated' AND resource_id=$1`, p.ID)
	if audit != "1" {
		t.Fatalf("expected one price_book.updated audit row, got %s", audit)
	}
}

func TestCreateAndUpdateBudgetWithAudit(t *testing.T) {
	pool := openPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seed(t, pool)
	svc := admincost.NewService(pool, nil)

	b, err := svc.CreateBudget(context.Background(), actor(), admincost.CreateBudgetInput{
		TenantID: itTenant, ScopeType: "tenant", ScopeID: itTenant, Period: "daily", LimitAmount: "5.0000",
	})
	if err != nil {
		t.Fatalf("create budget: %v", err)
	}
	if b.ReservedAmount != "0.0000" || b.SpentAmount != "0.0000" {
		t.Fatalf("expected zero reserved/spent on new budget, got %s/%s", b.ReservedAmount, b.SpentAmount)
	}
	if c := scalar(t, pool, `SELECT count(*) FROM audit_events WHERE event_type='admin.cost_budget.created' AND resource_id=$1`, b.ID); c != "1" {
		t.Fatalf("expected one cost_budget.created audit row, got %s", c)
	}

	paused := "paused"
	limit := "9.0000"
	upd, err := svc.UpdateBudget(context.Background(), actor(), b.ID, admincost.UpdateBudgetInput{
		LimitAmount: &limit, Status: &paused,
	})
	if err != nil {
		t.Fatalf("update budget: %v", err)
	}
	if upd.LimitAmount != "9.0000" || upd.Status != "paused" {
		t.Fatalf("expected limit 9.0000 / status paused, got %s / %s", upd.LimitAmount, upd.Status)
	}
	if c := scalar(t, pool, `SELECT count(*) FROM audit_events WHERE event_type='admin.cost_budget.updated' AND resource_id=$1`, b.ID); c != "1" {
		t.Fatalf("expected one cost_budget.updated audit row, got %s", c)
	}
}

func TestListReservationsFiltersByTenantAndStatus(t *testing.T) {
	pool := openPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seed(t, pool)
	ctx := context.Background()

	// A job + reserved reservation for our tenant.
	if _, err := pool.Exec(ctx,
		`INSERT INTO generation_jobs (id, tenant_id, job_type, status) VALUES ('job_ac1', $1, 'artifact', 'queued')`, itTenant); err != nil {
		t.Fatalf("seed job: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO cost_reservations (id, generation_job_id, tenant_id, estimated_amount, reserved_amount, status)
		 VALUES ('resv_ac1', 'job_ac1', $1, 0.0100, 0.0100, 'reserved')`, itTenant); err != nil {
		t.Fatalf("seed reservation: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO generation_jobs (id, tenant_id, job_type, status) VALUES ('job_ac2', $1, 'artifact', 'failed')`, itTenant); err != nil {
		t.Fatalf("seed job2: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO cost_reservations (id, generation_job_id, tenant_id, estimated_amount, reserved_amount, status, failure_reason)
		 VALUES ('resv_ac2', 'job_ac2', $1, 0.0100, 0.0000, 'failed', 'budget_exceeded')`, itTenant); err != nil {
		t.Fatalf("seed reservation2: %v", err)
	}

	svc := admincost.NewService(pool, nil)
	tenant := itTenant
	all, err := svc.ListReservations(ctx, admincost.ReservationFilter{TenantID: &tenant})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 reservations for tenant, got %d", len(all))
	}

	reserved := "reserved"
	onlyReserved, err := svc.ListReservations(ctx, admincost.ReservationFilter{TenantID: &tenant, Status: &reserved})
	if err != nil {
		t.Fatalf("list reserved: %v", err)
	}
	if len(onlyReserved) != 1 || onlyReserved[0].ID != "resv_ac1" {
		t.Fatalf("expected one reserved reservation resv_ac1, got %+v", onlyReserved)
	}

	other := "tenant_other"
	none, err := svc.ListReservations(ctx, admincost.ReservationFilter{TenantID: &other})
	if err != nil {
		t.Fatalf("list other: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("expected no reservations for other tenant, got %d", len(none))
	}
}

func TestAdminBudgetCreateAffectsNextGeneration(t *testing.T) {
	pool := openPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seed(t, pool)
	ctx := context.Background()

	// An active price for a dedicated provider/model so we don't collide with
	// the seeded mock price.
	svc := admincost.NewService(pool, nil)
	if _, err := svc.CreatePrice(ctx, actor(), admincost.CreatePriceInput{
		ProviderID: "admincost_it", ModelID: "mgen", OperationType: "text_to_image",
		UnitType: "image", PricePerUnit: "0.0100",
	}); err != nil {
		t.Fatalf("create price: %v", err)
	}
	// A tight tenant budget that admits less than one 0.0100 generation.
	if _, err := svc.CreateBudget(ctx, actor(), admincost.CreateBudgetInput{
		TenantID: itTenant, ScopeType: "tenant", ScopeID: itTenant, Period: "daily", LimitAmount: "0.0050",
	}); err != nil {
		t.Fatalf("create budget: %v", err)
	}

	jobsSvc := jobs.NewService(pool, noopEnqueuer{}, cost.NewService(nil))
	_, err := jobsSvc.CreateAndEnqueue(ctx, jobs.CreateAndEnqueueParams{
		TenantID: itTenant, RequestedByTokenID: itToken, JobType: "artifact", WorldID: "w1",
		InputPayload:   map[string]any{"description": "x"},
		FallbackPolicy: "compatible_only", CacheResult: "generated_required",
		ProviderID: "admincost_it", ModelID: "mgen", OperationType: "text_to_image", Units: 1,
	})
	if !errors.Is(err, cost.ErrBudgetExceeded) {
		t.Fatalf("expected budget_exceeded from the admin-created budget, got %v", err)
	}
}

type noopEnqueuer struct{}

func (noopEnqueuer) EnqueueGenerateArtifact(context.Context, string) error { return nil }
func (noopEnqueuer) EnqueueGeneratePack(context.Context, string) error     { return nil }
func (noopEnqueuer) Close() error                                          { return nil }
