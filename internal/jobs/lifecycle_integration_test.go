//go:build integration

package jobs_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zakkriel/drchat-image-platform/internal/assets"
	"github.com/zakkriel/drchat-image-platform/internal/cost"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
	"github.com/zakkriel/drchat-image-platform/internal/providers"
	"github.com/zakkriel/drchat-image-platform/internal/providers/mock"
)

// failingProvider always errors, to drive the terminal-failure path.
type failingProvider struct{}

func (failingProvider) Generate(context.Context, providers.ProviderGenerateRequest) (providers.ProviderGenerateResult, error) {
	return providers.ProviderGenerateResult{}, errors.New("provider unavailable")
}
func (failingProvider) PollStatus(context.Context, string) (providers.ProviderJobStatus, error) {
	return providers.ProviderJobStatus{}, providers.ErrNotApplicable
}
func (failingProvider) Upscale(context.Context, providers.ProviderUpscaleRequest) (providers.ProviderGenerateResult, error) {
	return providers.ProviderGenerateResult{}, providers.ErrNotImplemented
}
func (failingProvider) Capabilities() providers.ProviderCapabilities {
	return providers.ProviderCapabilities{ProviderID: "mock", ModelName: "mock-v1"}
}

// memStorage is an in-process Storage so the worker-driven lifecycle tests
// don't depend on MinIO (the cost lifecycle, not S3, is under test here).
type memStorage struct{}

func (memStorage) Put(_ context.Context, key string, _ []byte, _ string) (string, error) {
	return "s3://test/" + key, nil
}

// To run (requires Postgres + MinIO with migrations 0001–0003 applied):
//   POSTGRES_DSN=... S3_* ... go test -tags=integration ./internal/jobs/... -run Lifecycle

// submitJob runs the pre-flight + enqueue for a baseParams() artifact and
// returns the job id and reservation id. The enqueuer is a no-op recorder so
// the job stays `queued` until the test drives the worker.
func submitJob(t *testing.T, pool *pgxpool.Pool) (jobID, reservationID string) {
	t.Helper()
	enq := newRecordingEnqueuer()
	svc := newCostService(pool, enq)
	res, err := svc.CreateAndEnqueue(context.Background(), baseParams())
	if err != nil {
		t.Fatalf("submit job: %v", err)
	}
	return res.JobID, res.CostReservationID
}

func budgetAmounts(t *testing.T, pool *pgxpool.Pool, id string) (reserved, spent string) {
	t.Helper()
	if err := pool.QueryRow(context.Background(),
		`SELECT reserved_amount::text, spent_amount::text FROM cost_budgets WHERE id = $1`, id,
	).Scan(&reserved, &spent); err != nil {
		t.Fatalf("read budget amounts: %v", err)
	}
	return reserved, spent
}

func TestLifecycleWorkerSuccessCommitsReservation(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	seedBudget(t, pool, "bud_life_ok", "tenant", itTenant, "active", "1.0000")

	jobID, reservationID := submitJob(t, pool)

	// Before the worker runs: reserved, budget reserved increased.
	if got := scalar(t, pool, `SELECT status FROM cost_reservations WHERE id = $1`, reservationID); got != "reserved" {
		t.Fatalf("pre-worker reservation status: expected reserved, got %s", got)
	}
	if reserved, spent := budgetAmounts(t, pool, "bud_life_ok"); reserved != "0.0100" || spent != "0.0000" {
		t.Fatalf("pre-worker budget: expected reserved 0.0100 / spent 0, got %s / %s", reserved, spent)
	}

	w := &jobs.Worker{
		Jobs:      jobs.NewRepository(pool),
		Assets:    assets.NewRepository(pool),
		Storage:   memStorage{},
		Provider:  mock.New(),
		Finalizer: cost.NewLifecycle(pool, nil),
	}
	if err := w.Process(context.Background(), jobID, 0); err != nil {
		t.Fatalf("worker process: %v", err)
	}

	// reserved -> committed; reserved decremented, spent incremented.
	rStatus, rActual := reservationStatusActual(t, pool, reservationID)
	if rStatus != "committed" || rActual != "0.0100" {
		t.Fatalf("reservation: expected committed/0.0100, got %s/%s", rStatus, rActual)
	}
	if reserved, spent := budgetAmounts(t, pool, "bud_life_ok"); reserved != "0.0000" || spent != "0.0100" {
		t.Fatalf("post-commit budget: expected reserved 0 / spent 0.0100, got %s / %s", reserved, spent)
	}
	if got := scalar(t, pool, `SELECT actual_cost_usd::text FROM generation_jobs WHERE id = $1`, jobID); got != "0.0100" {
		t.Fatalf("job actual_cost_usd: expected 0.0100, got %s", got)
	}
	if got := scalar(t, pool, `SELECT actual_cost_usd::text FROM generation_cost_events WHERE job_id = $1 AND actual_cost_usd IS NOT NULL`, jobID); got != "0.0100" {
		t.Fatalf("cost event actual_cost_usd: expected 0.0100, got %s", got)
	}
	if got := scalar(t, pool, `SELECT status FROM cost_reservation_budget_holds WHERE cost_reservation_id = $1`, reservationID); got != "committed" {
		t.Fatalf("budget hold status: expected committed, got %s", got)
	}
}

func TestLifecycleWorkerFailureReleasesReservation(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	seedBudget(t, pool, "bud_life_fail", "tenant", itTenant, "active", "1.0000")

	jobID, reservationID := submitJob(t, pool)

	// errorProvider always fails; run the final attempt so the job is marked
	// failed and the reservation is released.
	w := &jobs.Worker{
		Jobs:      jobs.NewRepository(pool),
		Assets:    assets.NewRepository(pool),
		Storage:   memStorage{},
		Provider:  failingProvider{},
		Finalizer: cost.NewLifecycle(pool, nil),
	}
	if err := w.Process(context.Background(), jobID, int32(jobs.MaxAttempts-1)); err == nil {
		t.Fatalf("expected provider error on final attempt")
	}

	rStatus, rActual := reservationStatusActual(t, pool, reservationID)
	if rStatus != "released" {
		t.Fatalf("reservation: expected released, got %s", rStatus)
	}
	if rActual != "" {
		t.Fatalf("reservation actual_amount: expected NULL, got %s", rActual)
	}
	if reserved, spent := budgetAmounts(t, pool, "bud_life_fail"); reserved != "0.0000" || spent != "0.0000" {
		t.Fatalf("post-release budget: expected reserved 0 / spent 0, got %s / %s", reserved, spent)
	}
	if got := scalar(t, pool, `SELECT status FROM generation_cost_events WHERE job_id = $1 ORDER BY created_at DESC LIMIT 1`, jobID); got != "failed" {
		t.Fatalf("cost event status: expected failed, got %s", got)
	}
}

func TestLifecycleDoubleCommitDoesNotDoubleMove(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	seedBudget(t, pool, "bud_life_dc", "tenant", itTenant, "active", "1.0000")

	jobID, reservationID := submitJob(t, pool)
	fin := cost.NewLifecycle(pool, nil)

	if err := fin.Commit(context.Background(), jobID); err != nil {
		t.Fatalf("first commit: %v", err)
	}
	if err := fin.Commit(context.Background(), jobID); err != nil {
		t.Fatalf("second commit (should be no-op): %v", err)
	}
	if reserved, spent := budgetAmounts(t, pool, "bud_life_dc"); reserved != "0.0000" || spent != "0.0100" {
		t.Fatalf("double commit moved twice: reserved %s / spent %s", reserved, spent)
	}
	if got := reservationStatus(t, pool, reservationID); got != "committed" {
		t.Fatalf("reservation: expected committed, got %s", got)
	}
}

func TestLifecycleDoubleReleaseDoesNotDoubleMove(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	seedBudget(t, pool, "bud_life_dr", "tenant", itTenant, "active", "1.0000")

	jobID, reservationID := submitJob(t, pool)
	fin := cost.NewLifecycle(pool, nil)

	if err := fin.Release(context.Background(), jobID); err != nil {
		t.Fatalf("first release: %v", err)
	}
	if err := fin.Release(context.Background(), jobID); err != nil {
		t.Fatalf("second release (should be no-op): %v", err)
	}
	if reserved, spent := budgetAmounts(t, pool, "bud_life_dr"); reserved != "0.0000" || spent != "0.0000" {
		t.Fatalf("double release corrupted budget: reserved %s / spent %s", reserved, spent)
	}
	if got := reservationStatus(t, pool, reservationID); got != "released" {
		t.Fatalf("reservation: expected released, got %s", got)
	}
}

func TestLifecycleCommitAfterReleaseIsNoop(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	seedBudget(t, pool, "bud_life_car", "tenant", itTenant, "active", "1.0000")

	jobID, reservationID := submitJob(t, pool)
	fin := cost.NewLifecycle(pool, nil)

	if err := fin.Release(context.Background(), jobID); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := fin.Commit(context.Background(), jobID); err != nil {
		t.Fatalf("commit after release (should be no-op): %v", err)
	}
	// Released wins; spent never moved, reservation stays released.
	if reserved, spent := budgetAmounts(t, pool, "bud_life_car"); reserved != "0.0000" || spent != "0.0000" {
		t.Fatalf("commit-after-release corrupted budget: reserved %s / spent %s", reserved, spent)
	}
	if got := reservationStatus(t, pool, reservationID); got != "released" {
		t.Fatalf("reservation: expected released, got %s", got)
	}
}

func TestLifecycleReleaseAfterCommitIsNoop(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	seedBudget(t, pool, "bud_life_rac", "tenant", itTenant, "active", "1.0000")

	jobID, reservationID := submitJob(t, pool)
	fin := cost.NewLifecycle(pool, nil)

	if err := fin.Commit(context.Background(), jobID); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := fin.Release(context.Background(), jobID); err != nil {
		t.Fatalf("release after commit (should be no-op): %v", err)
	}
	// Committed wins; spent stays moved, reservation stays committed.
	if reserved, spent := budgetAmounts(t, pool, "bud_life_rac"); reserved != "0.0000" || spent != "0.0100" {
		t.Fatalf("release-after-commit corrupted budget: reserved %s / spent %s", reserved, spent)
	}
	if got := reservationStatus(t, pool, reservationID); got != "committed" {
		t.Fatalf("reservation: expected committed, got %s", got)
	}
}

func TestLifecycleFailedPreflightReservationIsNoop(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)
	// Tight budget so the reservation fails pre-flight (status=failed).
	seedBudget(t, pool, "bud_life_fp", "tenant", itTenant, "active", "0.0050")

	enq := newRecordingEnqueuer()
	svc := newCostService(pool, enq)
	res, _ := svc.CreateAndEnqueue(context.Background(), baseParams())
	jobID := res.JobID

	fin := cost.NewLifecycle(pool, nil)
	// Commit/Release on a failed-preflight reservation must be no-ops.
	if err := fin.Commit(context.Background(), jobID); err != nil {
		t.Fatalf("commit on failed preflight: %v", err)
	}
	if err := fin.Release(context.Background(), jobID); err != nil {
		t.Fatalf("release on failed preflight: %v", err)
	}
	if got := scalar(t, pool, `SELECT status FROM cost_reservations WHERE generation_job_id = $1`, jobID); got != "failed" {
		t.Fatalf("reservation: expected failed (unchanged), got %s", got)
	}
	if reserved, spent := budgetAmounts(t, pool, "bud_life_fp"); reserved != "0.0000" || spent != "0.0000" {
		t.Fatalf("failed-preflight finalize touched budget: reserved %s / spent %s", reserved, spent)
	}
}

// reservationStatus reads only the status of a reservation by id.
func reservationStatus(t *testing.T, pool *pgxpool.Pool, reservationID string) string {
	t.Helper()
	return scalar(t, pool, `SELECT status FROM cost_reservations WHERE id = $1`, reservationID)
}

// reservationStatusActual reads status + actual_amount (empty string for NULL).
func reservationStatusActual(t *testing.T, pool *pgxpool.Pool, reservationID string) (status, actual string) {
	t.Helper()
	var actualPtr *string
	if err := pool.QueryRow(context.Background(),
		`SELECT status, actual_amount::text FROM cost_reservations WHERE id = $1`, reservationID,
	).Scan(&status, &actualPtr); err != nil {
		t.Fatalf("read reservation: %v", err)
	}
	if actualPtr != nil {
		actual = *actualPtr
	}
	return status, actual
}
