//go:build integration

package jobs_test

import (
	"context"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// To run (requires Postgres with migrations 0001–0007 applied):
//   POSTGRES_DSN=... go test -tags=integration ./internal/jobs/... -run BudgetReset

// seedBudgetFull inserts a budget with explicit period, status, limit, spent,
// reserved, and period_start so a test can pin a window boundary directly in
// the DB (the production admin surface never exposes spent/reserved or
// period_start mutation — Phase 7C-1c §"Budget admin surface").
func seedBudgetFull(t *testing.T, pool *pgxpool.Pool, id, period, status, limit, spent, reserved, periodStartSQL string) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO cost_budgets (id, tenant_id, scope_type, scope_id, period, limit_amount, spent_amount, reserved_amount, status, period_start)
		 VALUES ($1, $2, 'tenant', $2, $3, $4, $5, $6, $7, `+periodStartSQL+`)`,
		id, itTenant, period, limit, spent, reserved, status,
	)
	if err != nil {
		t.Fatalf("seed budget %s: %v", id, err)
	}
}

func budgetRow(t *testing.T, pool *pgxpool.Pool, id string) (status, spent, reserved string, periodStartEpoch float64) {
	t.Helper()
	if err := pool.QueryRow(context.Background(),
		`SELECT status, spent_amount::text, reserved_amount::text, extract(epoch from period_start)
		 FROM cost_budgets WHERE id = $1`, id,
	).Scan(&status, &spent, &reserved, &periodStartEpoch); err != nil {
		t.Fatalf("read budget %s: %v", id, err)
	}
	return
}

func currentDayFloorEpoch(t *testing.T, pool *pgxpool.Pool) float64 {
	t.Helper()
	var e float64
	if err := pool.QueryRow(context.Background(),
		`SELECT extract(epoch from (date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'))`,
	).Scan(&e); err != nil {
		t.Fatalf("day floor: %v", err)
	}
	return e
}

// Lazy daily reset: a daily budget whose window elapsed and is `exceeded` rolls
// over at reservation time — spent zeroes, status returns to active, the window
// advances, and a pre-existing live hold in reserved_amount is preserved.
func TestBudgetResetDailyAdvancesWindowAndAdmits(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	// Exceeded last period, window two days behind, with a live 0.0050 hold and
	// a large stale spend. limit is generous so the only blocker is the stale
	// exceeded state — which the reset must clear.
	seedBudgetFull(t, pool, "bud_daily_reset", "daily", "exceeded", "1.0000", "0.9000", "0.0050",
		`date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' - interval '2 days'`)

	enq := newRecordingEnqueuer()
	svc := newCostService(pool, enq)

	res, err := svc.CreateAndEnqueue(context.Background(), baseParams())
	if err != nil {
		t.Fatalf("reservation in a fresh window must succeed, got %v", err)
	}
	if res.Status != "queued" {
		t.Fatalf("expected queued, got %s", res.Status)
	}

	status, spent, reserved, ps := budgetRow(t, pool, "bud_daily_reset")
	if status != "active" {
		t.Fatalf("exceeded budget must reset to active, got %s", status)
	}
	if spent != "0.0000" {
		t.Fatalf("spent must reset to 0, got %s", spent)
	}
	// reserved = preserved live hold (0.0050) + the new hold (0.0100).
	if reserved != "0.0150" {
		t.Fatalf("reserved must keep the live hold and add the new one (0.0150), got %s", reserved)
	}
	if ps != currentDayFloorEpoch(t, pool) {
		t.Fatalf("period_start must advance to the current UTC day floor")
	}
}

// Lazy monthly reset: same semantics on a monthly window.
func TestBudgetResetMonthlyAdvancesWindowAndAdmits(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	seedBudgetFull(t, pool, "bud_monthly_reset", "monthly", "exceeded", "1.0000", "0.7000", "0.0000",
		`date_trunc('month', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' - interval '1 month'`)

	enq := newRecordingEnqueuer()
	svc := newCostService(pool, enq)

	if _, err := svc.CreateAndEnqueue(context.Background(), baseParams()); err != nil {
		t.Fatalf("reservation in a fresh month must succeed, got %v", err)
	}
	status, spent, reserved, ps := budgetRow(t, pool, "bud_monthly_reset")
	if status != "active" || spent != "0.0000" || reserved != "0.0100" {
		t.Fatalf("monthly reset: expected active/0/0.0100, got %s/%s/%s", status, spent, reserved)
	}
	var monthFloor float64
	if err := pool.QueryRow(context.Background(),
		`SELECT extract(epoch from (date_trunc('month', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'))`,
	).Scan(&monthFloor); err != nil {
		t.Fatalf("month floor: %v", err)
	}
	if ps != monthFloor {
		t.Fatalf("period_start must advance to the current UTC month floor")
	}
}

// A paused budget rolls its window and zeroes spent but stays paused.
func TestBudgetResetPausedStaysPaused(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	seedBudgetFull(t, pool, "bud_paused_reset", "daily", "paused", "0.0000", "0.5000", "0.0000",
		`date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' - interval '3 days'`)

	enq := newRecordingEnqueuer()
	svc := newCostService(pool, enq)

	// Paused records but never denies, so the reservation still succeeds.
	if _, err := svc.CreateAndEnqueue(context.Background(), baseParams()); err != nil {
		t.Fatalf("paused budget must not deny, got %v", err)
	}
	status, spent, _, ps := budgetRow(t, pool, "bud_paused_reset")
	if status != "paused" {
		t.Fatalf("paused budget must stay paused after reset, got %s", status)
	}
	if spent != "0.0000" {
		t.Fatalf("paused budget spent must reset to 0, got %s", spent)
	}
	if ps != currentDayFloorEpoch(t, pool) {
		t.Fatalf("paused budget period_start must still advance")
	}
}

// The reset is idempotent under concurrency: two reservations racing on an
// elapsed exceeded budget reset the window exactly once (no double-spend, no
// lost hold) and both succeed.
func TestBudgetResetConcurrentDoesNotDoubleSpend(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	seedBudgetFull(t, pool, "bud_race_reset", "daily", "exceeded", "1.0000", "0.9500", "0.0000",
		`date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' - interval '1 day'`)

	enq := newRecordingEnqueuer()
	svc := newCostService(pool, enq)

	const N = 2
	errs := make([]error, N)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = svc.CreateAndEnqueue(context.Background(), baseParams())
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("request %d must succeed in the fresh window, got %v", i, err)
		}
	}
	status, spent, reserved, ps := budgetRow(t, pool, "bud_race_reset")
	if status != "active" {
		t.Fatalf("expected active after reset, got %s", status)
	}
	// spent reset exactly once (not below 0, not left stale); both holds counted.
	if spent != "0.0000" {
		t.Fatalf("spent must be reset to exactly 0, got %s", spent)
	}
	if reserved != "0.0200" {
		t.Fatalf("both concurrent holds must be counted (0.0200), got %s", reserved)
	}
	if ps != currentDayFloorEpoch(t, pool) {
		t.Fatalf("period_start must advance to the current day floor")
	}
}
