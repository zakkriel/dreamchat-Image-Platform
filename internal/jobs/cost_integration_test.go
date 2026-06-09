//go:build integration

package jobs_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zakkriel/drchat-image-platform/internal/cost"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
)

// To run (requires Postgres with migrations 0001 + 0002 applied):
//   POSTGRES_DSN=postgres://image_platform:image_platform@localhost:5432/image_platform?sslmode=disable \
//   go test -tags=integration ./internal/jobs/... -run Preflight

// baseParams builds a valid artifact CreateAndEnqueueParams pointing at the
// seeded mock price (provider=mock, model=pm_mock_v1, text_to_image, 1 image
// → 0.0100 USD). Callers override fields per scenario.
func baseParams() jobs.CreateAndEnqueueParams {
	return jobs.CreateAndEnqueueParams{
		TenantID:           itTenant,
		RequestedByTokenID: itTokenID,
		JobType:            "artifact",
		WorldID:            "w1",
		InputPayload:       map[string]any{"description": "preflight test"},
		FallbackPolicy:     "compatible_only",
		CacheResult:        "generated_required",
		ProviderID:         "mock",
		ModelID:            "pm_mock_v1",
		OperationType:      "text_to_image",
		Units:              1,
	}
}

func newCostService(pool *pgxpool.Pool, enq *recordingEnqueuer) *jobs.Service {
	return jobs.NewService(pool, enq, cost.NewService(nil))
}

func seedBudget(t *testing.T, pool *pgxpool.Pool, id, scopeType, scopeID, status, limit string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO cost_budgets (id, tenant_id, scope_type, scope_id, period, limit_amount, status)
		 VALUES ($1, $2, $3, $4, 'daily', $5, $6)`,
		id, itTenant, scopeType, scopeID, limit, status,
	); err != nil {
		t.Fatalf("seed budget %s: %v", id, err)
	}
}

func scalar(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) string {
	t.Helper()
	var out string
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&out); err != nil {
		t.Fatalf("scalar %q: %v", sql, err)
	}
	return out
}

func reservationStatusForJob(t *testing.T, pool *pgxpool.Pool, jobID string) (status, reason, estimated, reserved string) {
	t.Helper()
	var reasonPtr *string
	if err := pool.QueryRow(context.Background(),
		`SELECT status, failure_reason, estimated_amount::text, reserved_amount::text
		 FROM cost_reservations WHERE generation_job_id = $1`, jobID,
	).Scan(&status, &reasonPtr, &estimated, &reserved); err != nil {
		t.Fatalf("read reservation: %v", err)
	}
	if reasonPtr != nil {
		reason = *reasonPtr
	}
	return status, reason, estimated, reserved
}

func TestPreflightNoPriceEntryReturns422AndReplays(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)

	enq := newRecordingEnqueuer()
	svc := newCostService(pool, enq)

	p := baseParams()
	p.ModelID = "pm_missing" // no active price for this model
	p.IdempotencyKey = "preflight-np-1"
	p.Endpoint = "POST /v1/artifacts/art/generate"
	p.RequestHash = jobs.HashRequestBody([]byte(`{"description":"x"}`))

	res, err := svc.CreateAndEnqueue(context.Background(), p)
	if !errors.Is(err, cost.ErrNoPriceEntry) {
		t.Fatalf("expected ErrNoPriceEntry, got %v", err)
	}
	if res.JobID == "" || res.Status != "failed" {
		t.Fatalf("expected failed job result, got %+v", res)
	}

	status := scalar(t, pool, `SELECT status FROM generation_jobs WHERE id = $1`, res.JobID)
	errCode := scalar(t, pool, `SELECT error_code FROM generation_jobs WHERE id = $1`, res.JobID)
	if status != "failed" || errCode != "no_price_entry" {
		t.Fatalf("job: expected failed/no_price_entry, got %s/%s", status, errCode)
	}
	rStatus, rReason, rEst, rReserved := reservationStatusForJob(t, pool, res.JobID)
	if rStatus != "failed" || rReason != "no_price_entry" {
		t.Fatalf("reservation: expected failed/no_price_entry, got %s/%s", rStatus, rReason)
	}
	if rEst != "0.0000" || rReserved != "0.0000" {
		t.Fatalf("reservation amounts: expected 0/0, got est=%s reserved=%s", rEst, rReserved)
	}
	if got := enq.snapshot(); len(got) != 0 {
		t.Fatalf("expected no enqueue for failed preflight, got %v", got)
	}

	// Replay must return the same 422 and the same job, no new rows.
	res2, err2 := svc.CreateAndEnqueue(context.Background(), p)
	if !errors.Is(err2, cost.ErrNoPriceEntry) {
		t.Fatalf("replay: expected ErrNoPriceEntry, got %v", err2)
	}
	if res2.JobID != res.JobID || !res2.Replayed {
		t.Fatalf("replay: expected same job replayed, got %+v", res2)
	}
	jobCount := scalar(t, pool, `SELECT count(*) FROM generation_jobs WHERE tenant_id = $1`, itTenant)
	if jobCount != "1" {
		t.Fatalf("expected exactly one job after replay, got %s", jobCount)
	}
	if got := enq.snapshot(); len(got) != 0 {
		t.Fatalf("expected still no enqueue after replay, got %v", got)
	}
}

func TestPreflightBudgetExceededReturns422AndReplays(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	seedBudget(t, pool, "bud_tenant_tight", "tenant", itTenant, "active", "0.0050")

	enq := newRecordingEnqueuer()
	svc := newCostService(pool, enq)

	p := baseParams()
	p.IdempotencyKey = "preflight-be-1"
	p.Endpoint = "POST /v1/artifacts/art/generate"
	p.RequestHash = jobs.HashRequestBody([]byte(`{"description":"x"}`))

	res, err := svc.CreateAndEnqueue(context.Background(), p)
	if !errors.Is(err, cost.ErrBudgetExceeded) {
		t.Fatalf("expected ErrBudgetExceeded, got %v", err)
	}

	status := scalar(t, pool, `SELECT status FROM generation_jobs WHERE id = $1`, res.JobID)
	errCode := scalar(t, pool, `SELECT error_code FROM generation_jobs WHERE id = $1`, res.JobID)
	if status != "failed" || errCode != "budget_exceeded" {
		t.Fatalf("job: expected failed/budget_exceeded, got %s/%s", status, errCode)
	}
	rStatus, rReason, rEst, rReserved := reservationStatusForJob(t, pool, res.JobID)
	if rStatus != "failed" || rReason != "budget_exceeded" {
		t.Fatalf("reservation: expected failed/budget_exceeded, got %s/%s", rStatus, rReason)
	}
	// estimate is computed even when denied; nothing is held.
	if rEst != "0.0100" || rReserved != "0.0000" {
		t.Fatalf("reservation amounts: expected est=0.0100 reserved=0, got est=%s reserved=%s", rEst, rReserved)
	}
	held := scalar(t, pool, `SELECT reserved_amount::text FROM cost_budgets WHERE id = 'bud_tenant_tight'`)
	if held != "0.0000" {
		t.Fatalf("budget reserved_amount should be untouched, got %s", held)
	}
	if got := enq.snapshot(); len(got) != 0 {
		t.Fatalf("expected no enqueue for budget-denied request, got %v", got)
	}

	res2, err2 := svc.CreateAndEnqueue(context.Background(), p)
	if !errors.Is(err2, cost.ErrBudgetExceeded) {
		t.Fatalf("replay: expected ErrBudgetExceeded, got %v", err2)
	}
	if res2.JobID != res.JobID {
		t.Fatalf("replay: expected same job, got %s vs %s", res2.JobID, res.JobID)
	}
}

func TestPreflightHappyPathReservesBudget(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	seedBudget(t, pool, "bud_tenant_ok", "tenant", itTenant, "active", "1.0000")

	enq := newRecordingEnqueuer()
	svc := newCostService(pool, enq)

	res, err := svc.CreateAndEnqueue(context.Background(), baseParams())
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if res.Status != "queued" || res.EstimatedCostUSD != "0.0100" || res.Currency != "USD" || res.CostReservationID == "" {
		t.Fatalf("unexpected result: %+v", res)
	}
	rStatus, _, rEst, rReserved := reservationStatusForJob(t, pool, res.JobID)
	if rStatus != "reserved" || rEst != "0.0100" || rReserved != "0.0100" {
		t.Fatalf("reservation: expected reserved/0.0100/0.0100, got %s/%s/%s", rStatus, rEst, rReserved)
	}
	held := scalar(t, pool, `SELECT reserved_amount::text FROM cost_budgets WHERE id = 'bud_tenant_ok'`)
	if held != "0.0100" {
		t.Fatalf("budget reserved_amount: expected 0.0100, got %s", held)
	}
	estOnJob := scalar(t, pool, `SELECT cost_estimate_usd::text FROM generation_jobs WHERE id = $1`, res.JobID)
	if estOnJob != "0.0100" {
		t.Fatalf("job cost_estimate_usd: expected 0.0100, got %s", estOnJob)
	}
	if got := enq.snapshot(); len(got) != 1 {
		t.Fatalf("expected exactly one enqueue, got %v", got)
	}
}

func TestPreflightConcurrentTightBudgetExactlyOneSucceeds(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	// Budget fits exactly one 0.0100 reservation.
	seedBudget(t, pool, "bud_tenant_exact", "tenant", itTenant, "active", "0.0100")

	enq := newRecordingEnqueuer()
	svc := newCostService(pool, enq)

	const N = 8
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

	success, denied := 0, 0
	for i, err := range errs {
		switch {
		case err == nil:
			success++
		case errors.Is(err, cost.ErrBudgetExceeded):
			denied++
		default:
			t.Fatalf("request %d: unexpected error %v", i, err)
		}
	}
	if success != 1 || denied != N-1 {
		t.Fatalf("expected exactly 1 success and %d budget_exceeded, got success=%d denied=%d", N-1, success, denied)
	}
	reserved := scalar(t, pool, `SELECT count(*) FROM cost_reservations WHERE tenant_id = $1 AND status = 'reserved'`, itTenant)
	failed := scalar(t, pool, `SELECT count(*) FROM cost_reservations WHERE tenant_id = $1 AND status = 'failed'`, itTenant)
	if reserved != "1" {
		t.Fatalf("expected exactly one reserved reservation, got %s", reserved)
	}
	if failed != "7" {
		t.Fatalf("expected seven failed reservations, got %s", failed)
	}
	held := scalar(t, pool, `SELECT reserved_amount::text FROM cost_budgets WHERE id = 'bud_tenant_exact'`)
	if held != "0.0100" {
		t.Fatalf("budget reserved_amount: expected 0.0100, got %s", held)
	}
	if got := enq.snapshot(); len(got) != 1 {
		t.Fatalf("expected exactly one enqueue across concurrent requests, got %v", got)
	}
}

func TestPreflightPausedBudgetRecordsButNeverDenies(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	// Limit far below the estimate, but paused → record without denying.
	seedBudget(t, pool, "bud_tenant_paused", "tenant", itTenant, "paused", "0.0000")

	enq := newRecordingEnqueuer()
	svc := newCostService(pool, enq)

	res, err := svc.CreateAndEnqueue(context.Background(), baseParams())
	if err != nil {
		t.Fatalf("paused budget must not deny, got %v", err)
	}
	if res.Status != "queued" {
		t.Fatalf("expected queued, got %s", res.Status)
	}
	held := scalar(t, pool, `SELECT reserved_amount::text FROM cost_budgets WHERE id = 'bud_tenant_paused'`)
	if held != "0.0100" {
		t.Fatalf("paused budget should still record the hold, got %s", held)
	}
}

func TestPreflightExceededBudgetDenies(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	seedBudget(t, pool, "bud_tenant_exc", "tenant", itTenant, "exceeded", "100.0000")

	enq := newRecordingEnqueuer()
	svc := newCostService(pool, enq)

	_, err := svc.CreateAndEnqueue(context.Background(), baseParams())
	if !errors.Is(err, cost.ErrBudgetExceeded) {
		t.Fatalf("exceeded budget must deny, got %v", err)
	}
}

func TestPreflightNarrowerScopeMustAlsoPermit(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	// Tenant permits, token (narrower) denies → request denied.
	seedBudget(t, pool, "bud_tenant_wide", "tenant", itTenant, "active", "1.0000")
	seedBudget(t, pool, "bud_token_tight", "token", itTokenID, "active", "0.0050")

	enq := newRecordingEnqueuer()
	svc := newCostService(pool, enq)

	_, err := svc.CreateAndEnqueue(context.Background(), baseParams())
	if !errors.Is(err, cost.ErrBudgetExceeded) {
		t.Fatalf("narrower token budget must deny, got %v", err)
	}
	// Tenant budget must not have been left holding the estimate (savepoint
	// rollback on the narrower denial).
	tenantHeld := scalar(t, pool, `SELECT reserved_amount::text FROM cost_budgets WHERE id = 'bud_tenant_wide'`)
	if tenantHeld != "0.0000" {
		t.Fatalf("tenant budget hold should have rolled back, got %s", tenantHeld)
	}
}

func TestPreflightTenantAndNarrowerBothPermit(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	seedBudget(t, pool, "bud_tenant_both", "tenant", itTenant, "active", "1.0000")
	seedBudget(t, pool, "bud_token_both", "token", itTokenID, "active", "1.0000")

	enq := newRecordingEnqueuer()
	svc := newCostService(pool, enq)

	res, err := svc.CreateAndEnqueue(context.Background(), baseParams())
	if err != nil {
		t.Fatalf("both budgets permit, expected success, got %v", err)
	}
	if res.Status != "queued" {
		t.Fatalf("expected queued, got %s", res.Status)
	}
	tenantHeld := scalar(t, pool, `SELECT reserved_amount::text FROM cost_budgets WHERE id = 'bud_tenant_both'`)
	tokenHeld := scalar(t, pool, `SELECT reserved_amount::text FROM cost_budgets WHERE id = 'bud_token_both'`)
	if tenantHeld != "0.0100" || tokenHeld != "0.0100" {
		t.Fatalf("both budgets should hold 0.0100, got tenant=%s token=%s", tenantHeld, tokenHeld)
	}
}
