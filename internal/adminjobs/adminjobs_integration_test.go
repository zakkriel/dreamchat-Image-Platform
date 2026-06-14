//go:build integration

package adminjobs_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zakkriel/drchat-image-platform/internal/adminjobs"
	"github.com/zakkriel/drchat-image-platform/internal/assets"
	"github.com/zakkriel/drchat-image-platform/internal/cost"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
	"github.com/zakkriel/drchat-image-platform/internal/providers"
	"github.com/zakkriel/drchat-image-platform/internal/providers/mock"
)

// To run (requires Postgres with migrations 0001–0007 applied):
//   POSTGRES_DSN=postgres://image_platform:image_platform@localhost:5432/image_platform?sslmode=disable \
//   go test -tags=integration ./internal/adminjobs/...

const (
	itTenant  = "tenant_it_adminjobs"
	itTokenID = "tok_it_adminjobs"
	itWorld   = "w_it_adminjobs"
)

func openTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_DSN not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("pool.Ping: %v", err)
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
	exec(`DELETE FROM generation_cost_events WHERE tenant_id = $1`, itTenant)
	exec(`DELETE FROM provider_attempts WHERE generation_job_id IN (SELECT id FROM generation_jobs WHERE tenant_id = $1)`, itTenant)
	exec(`DELETE FROM visual_assets WHERE tenant_id = $1`, itTenant)
	exec(`DELETE FROM cost_reservation_budget_holds WHERE cost_reservation_id IN (SELECT id FROM cost_reservations WHERE tenant_id = $1)`, itTenant)
	exec(`UPDATE generation_jobs SET cost_reservation_id = NULL WHERE tenant_id = $1`, itTenant)
	exec(`DELETE FROM cost_reservations WHERE tenant_id = $1`, itTenant)
	exec(`DELETE FROM generation_jobs WHERE tenant_id = $1`, itTenant)
	exec(`DELETE FROM cost_budgets WHERE tenant_id = $1`, itTenant)
	exec(`DELETE FROM api_tokens WHERE id = $1`, itTokenID)
}

func seedToken(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO api_tokens (id, tenant_id, token_prefix, token_hash, name, owner_type, scopes, environment, status)
		 VALUES ($1, $2, 'dci_it_aj', 'h', 't', 'tenant', ARRAY['images:write'], 'dev', 'active')`,
		itTokenID, itTenant,
	); err != nil {
		t.Fatalf("seed token: %v", err)
	}
}

func seedBudget(t *testing.T, pool *pgxpool.Pool, id, status, limit string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO cost_budgets (id, tenant_id, scope_type, scope_id, period, limit_amount, status)
		 VALUES ($1, $2, 'tenant', $2, 'daily', $3, $4)`,
		id, itTenant, limit, status,
	); err != nil {
		t.Fatalf("seed budget %s: %v", id, err)
	}
}

// recordingEnqueuer counts enqueues and can be made to fail.
type recordingEnqueuer struct {
	mu     sync.Mutex
	jobIDs []string
	fail   bool
}

func (e *recordingEnqueuer) EnqueueGenerateArtifact(_ context.Context, jobID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.fail {
		return errors.New("forced enqueue failure")
	}
	e.jobIDs = append(e.jobIDs, jobID)
	return nil
}
func (e *recordingEnqueuer) EnqueueGeneratePack(_ context.Context, jobID string) error {
	return e.EnqueueGenerateArtifact(context.Background(), jobID)
}
func (e *recordingEnqueuer) Close() error { return nil }
func (e *recordingEnqueuer) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.jobIDs)
}

func baseParams() jobs.CreateAndEnqueueParams {
	return jobs.CreateAndEnqueueParams{
		TenantID:           itTenant,
		RequestedByTokenID: itTokenID,
		JobType:            "artifact",
		WorldID:            itWorld,
		InputPayload:       map[string]any{"description": "admin jobs test"},
		FallbackPolicy:     "compatible_only",
		CacheResult:        "generated_required",
		ProviderID:         "mock",
		ModelID:            "pm_mock_v1",
		OperationType:      "text_to_image",
		Units:              1,
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

func newServices(pool *pgxpool.Pool, enq *recordingEnqueuer) (*jobs.Service, *adminjobs.Service) {
	lifecycle := cost.NewLifecycle(pool, nil)
	jobSvc := jobs.NewService(pool, enq, cost.NewService(nil)).WithFinalizer(lifecycle)
	adminSvc := adminjobs.NewService(pool, cost.NewService(nil), lifecycle, enq, nil)
	return jobSvc, adminSvc
}

// createQueuedJob runs the real create+reserve path so a cancel/retry has a
// genuine job + reservation to act on.
func createQueuedJob(t *testing.T, jobSvc *jobs.Service) string {
	t.Helper()
	res, err := jobSvc.CreateAndEnqueue(context.Background(), baseParams())
	if err != nil {
		t.Fatalf("CreateAndEnqueue: %v", err)
	}
	if res.Status != "queued" {
		t.Fatalf("expected queued, got %s", res.Status)
	}
	return res.JobID
}

func TestCancelQueuedReleasesReservation(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedToken(t, pool)
	seedBudget(t, pool, "bud_aj_cancel", "active", "1.0000")

	enq := &recordingEnqueuer{}
	jobSvc, adminSvc := newServices(pool, enq)
	jobID := createQueuedJob(t, jobSvc)

	if held := scalar(t, pool, `SELECT reserved_amount::text FROM cost_budgets WHERE id='bud_aj_cancel'`); held != "0.0100" {
		t.Fatalf("precondition: budget should hold 0.0100, got %s", held)
	}

	job, err := adminSvc.CancelJob(context.Background(), itTenant, jobID)
	if err != nil {
		t.Fatalf("CancelJob: %v", err)
	}
	if job.Status != "cancelled" {
		t.Fatalf("expected cancelled, got %s", job.Status)
	}
	if job.ErrorCode == nil || *job.ErrorCode != "cancelled" || job.Retryable == nil || *job.Retryable || job.CompletedAt == nil {
		t.Fatalf("cancel must set error_code=cancelled, retryable=false, completed_at; got %+v", job)
	}
	if s := scalar(t, pool, `SELECT status FROM cost_reservations WHERE generation_job_id=$1`, jobID); s != "released" {
		t.Fatalf("reservation must be released, got %s", s)
	}
	if held := scalar(t, pool, `SELECT reserved_amount::text FROM cost_budgets WHERE id='bud_aj_cancel'`); held != "0.0000" {
		t.Fatalf("budget hold must be reclaimed to 0, got %s", held)
	}
}

func TestCancelIdempotentDoesNotDoubleRelease(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedToken(t, pool)
	seedBudget(t, pool, "bud_aj_idem", "active", "1.0000")

	enq := &recordingEnqueuer{}
	jobSvc, adminSvc := newServices(pool, enq)
	jobID := createQueuedJob(t, jobSvc)

	if _, err := adminSvc.CancelJob(context.Background(), itTenant, jobID); err != nil {
		t.Fatalf("first cancel: %v", err)
	}
	job, err := adminSvc.CancelJob(context.Background(), itTenant, jobID)
	if err != nil {
		t.Fatalf("second cancel must be idempotent, got %v", err)
	}
	if job.Status != "cancelled" {
		t.Fatalf("expected cancelled, got %s", job.Status)
	}
	// reserved must not go negative or change on the repeated release.
	if held := scalar(t, pool, `SELECT reserved_amount::text FROM cost_budgets WHERE id='bud_aj_idem'`); held != "0.0000" {
		t.Fatalf("repeated cancel must not double-release, got %s", held)
	}
}

func TestCancelRunningAndPreviewReady(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedToken(t, pool)
	seedBudget(t, pool, "bud_aj_states", "active", "1.0000")

	enq := &recordingEnqueuer{}
	jobSvc, adminSvc := newServices(pool, enq)

	for _, st := range []string{"running", "preview_ready"} {
		jobID := createQueuedJob(t, jobSvc)
		if _, err := pool.Exec(context.Background(), `UPDATE generation_jobs SET status=$2 WHERE id=$1`, jobID, st); err != nil {
			t.Fatalf("force status %s: %v", st, err)
		}
		job, err := adminSvc.CancelJob(context.Background(), itTenant, jobID)
		if err != nil {
			t.Fatalf("cancel from %s: %v", st, err)
		}
		if job.Status != "cancelled" {
			t.Fatalf("cancel from %s must yield cancelled, got %s", st, job.Status)
		}
		if s := scalar(t, pool, `SELECT status FROM cost_reservations WHERE generation_job_id=$1`, jobID); s != "released" {
			t.Fatalf("cancel from %s must release reservation, got %s", st, s)
		}
	}
}

func TestCancelTerminalStatesRejected(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedToken(t, pool)
	seedBudget(t, pool, "bud_aj_term", "active", "1.0000")

	enq := &recordingEnqueuer{}
	jobSvc, adminSvc := newServices(pool, enq)

	for _, st := range []string{"completed", "failed"} {
		jobID := createQueuedJob(t, jobSvc)
		if _, err := pool.Exec(context.Background(), `UPDATE generation_jobs SET status=$2 WHERE id=$1`, jobID, st); err != nil {
			t.Fatalf("force status %s: %v", st, err)
		}
		_, err := adminSvc.CancelJob(context.Background(), itTenant, jobID)
		if !errors.Is(err, adminjobs.ErrInvalidState) {
			t.Fatalf("cancel from %s must be invalid_state, got %v", st, err)
		}
	}
}

func TestCancelMissingOrCrossTenant(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedToken(t, pool)
	seedBudget(t, pool, "bud_aj_404", "active", "1.0000")

	enq := &recordingEnqueuer{}
	jobSvc, adminSvc := newServices(pool, enq)
	jobID := createQueuedJob(t, jobSvc)

	if _, err := adminSvc.CancelJob(context.Background(), itTenant, "job_does_not_exist"); !errors.Is(err, adminjobs.ErrNotFound) {
		t.Fatalf("missing job must be not_found, got %v", err)
	}
	if _, err := adminSvc.CancelJob(context.Background(), "tenant_other", jobID); !errors.Is(err, adminjobs.ErrNotFound) {
		t.Fatalf("cross-tenant job must be not_found, got %v", err)
	}
}

// insertFailedJob inserts a terminally-failed job with a persisted resolved
// route + cost context on its payload, so retry can re-reserve without
// re-resolving. cost_reservation_id stays NULL (the prior reservation was
// released on failure).
func insertFailedJob(t *testing.T, pool *pgxpool.Pool, id, modelID string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{
		"description":       "retry test",
		"provider_id":       "mock",
		"model_id":          modelID,
		"provider_route_id": "route_mock_text_to_image_standard",
		"operation_type":    "text_to_image",
		"units":             1,
	})
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO generation_jobs (id, tenant_id, world_id, job_type, status, requested_by_token_id, input_payload, error_code, error_message, retryable, completed_at)
		 VALUES ($1,$2,$3,'artifact','failed',$4,$5,'provider_failure','boom',true, now())`,
		id, itTenant, itWorld, itTokenID, payload,
	); err != nil {
		t.Fatalf("insert failed job: %v", err)
	}
}

func TestRetryFailedJobReReservesAndEnqueues(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedToken(t, pool)
	seedBudget(t, pool, "bud_aj_retry", "active", "1.0000")

	enq := &recordingEnqueuer{}
	_, adminSvc := newServices(pool, enq)
	insertFailedJob(t, pool, "job_aj_retry", "pm_mock_v1")

	job, err := adminSvc.RetryJob(context.Background(), itTenant, "job_aj_retry")
	if err != nil {
		t.Fatalf("RetryJob: %v", err)
	}
	if job.Status != "queued" {
		t.Fatalf("expected queued, got %s", job.Status)
	}
	if job.ErrorCode != nil || job.ErrorMessage != nil || job.Retryable != nil {
		t.Fatalf("retry must clear failure fields, got %+v", job)
	}
	if len(job.FinalAssetIds) != 0 {
		t.Fatalf("retry must clear final_asset_ids, got %+v", job.FinalAssetIds)
	}
	// Exactly one fresh reserved reservation, linked to the same job, priced
	// against the persisted model (0.0100 from the mock price).
	cnt := scalar(t, pool, `SELECT count(*) FROM cost_reservations WHERE generation_job_id=$1 AND status='reserved'`, "job_aj_retry")
	if cnt != "1" {
		t.Fatalf("expected exactly one fresh reserved reservation, got %s", cnt)
	}
	est := scalar(t, pool, `SELECT estimated_amount::text FROM cost_reservations WHERE generation_job_id=$1 AND status='reserved'`, "job_aj_retry")
	if est != "0.0100" {
		t.Fatalf("retry must re-reserve against the persisted model price (0.0100), got %s", est)
	}
	linked := scalar(t, pool, `SELECT cost_reservation_id FROM generation_jobs WHERE id=$1`, "job_aj_retry")
	if linked == "" {
		t.Fatalf("retry must link the fresh reservation to the job")
	}
	if enq.count() != 1 {
		t.Fatalf("retry must enqueue exactly once, got %d", enq.count())
	}
}

func TestRetryNonFailedRejected(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedToken(t, pool)
	seedBudget(t, pool, "bud_aj_retry_bad", "active", "1.0000")

	enq := &recordingEnqueuer{}
	jobSvc, adminSvc := newServices(pool, enq)
	jobID := createQueuedJob(t, jobSvc) // status queued

	if _, err := adminSvc.RetryJob(context.Background(), itTenant, jobID); !errors.Is(err, adminjobs.ErrInvalidState) {
		t.Fatalf("retry of a queued job must be invalid_state, got %v", err)
	}
}

func TestRetryDeniedByNoPriceLeavesJobFailed(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedToken(t, pool)
	seedBudget(t, pool, "bud_aj_np", "active", "1.0000")

	enq := &recordingEnqueuer{}
	_, adminSvc := newServices(pool, enq)
	insertFailedJob(t, pool, "job_aj_np", "pm_no_price_model")

	_, err := adminSvc.RetryJob(context.Background(), itTenant, "job_aj_np")
	if !errors.Is(err, cost.ErrNoPriceEntry) {
		t.Fatalf("expected no_price_entry, got %v", err)
	}
	if st := scalar(t, pool, `SELECT status FROM generation_jobs WHERE id=$1`, "job_aj_np"); st != "failed" {
		t.Fatalf("job must remain failed, got %s", st)
	}
	if ec := scalar(t, pool, `SELECT error_code FROM generation_jobs WHERE id=$1`, "job_aj_np"); ec != "provider_failure" {
		t.Fatalf("failure fields must be untouched, got %s", ec)
	}
	cnt := scalar(t, pool, `SELECT count(*) FROM cost_reservations WHERE generation_job_id=$1`, "job_aj_np")
	if cnt != "0" {
		t.Fatalf("denied retry must leave no reservation row, got %s", cnt)
	}
	if enq.count() != 0 {
		t.Fatalf("denied retry must not enqueue, got %d", enq.count())
	}
}

func TestRetryDeniedByBudgetLeavesJobFailed(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedToken(t, pool)
	// Budget too tight for a 0.0100 reservation.
	seedBudget(t, pool, "bud_aj_tight", "active", "0.0050")

	enq := &recordingEnqueuer{}
	_, adminSvc := newServices(pool, enq)
	insertFailedJob(t, pool, "job_aj_budget", "pm_mock_v1")

	_, err := adminSvc.RetryJob(context.Background(), itTenant, "job_aj_budget")
	if !errors.Is(err, cost.ErrBudgetExceeded) {
		t.Fatalf("expected budget_exceeded, got %v", err)
	}
	if st := scalar(t, pool, `SELECT status FROM generation_jobs WHERE id=$1`, "job_aj_budget"); st != "failed" {
		t.Fatalf("job must remain failed, got %s", st)
	}
	cnt := scalar(t, pool, `SELECT count(*) FROM cost_reservations WHERE generation_job_id=$1 AND status='reserved'`, "job_aj_budget")
	if cnt != "0" {
		t.Fatalf("denied retry must leave no live reservation, got %s", cnt)
	}
	if held := scalar(t, pool, `SELECT reserved_amount::text FROM cost_budgets WHERE id='bud_aj_tight'`); held != "0.0000" {
		t.Fatalf("denied retry must not hold budget, got %s", held)
	}
	if enq.count() != 0 {
		t.Fatalf("denied retry must not enqueue, got %d", enq.count())
	}
}

// Budget reset across a forced window boundary: a previously exceeded budget
// admits a retry in a fresh period, with spent reset and the window advanced.
func TestRetryAcrossBudgetWindowBoundary(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedToken(t, pool)
	// Exceeded last period with stale spend; window two days behind.
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO cost_budgets (id, tenant_id, scope_type, scope_id, period, limit_amount, spent_amount, status, period_start)
		 VALUES ('bud_aj_window', $1, 'tenant', $1, 'daily', '1.0000', '0.9900', 'exceeded',
		         date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' - interval '2 days')`,
		itTenant,
	); err != nil {
		t.Fatalf("seed exceeded budget: %v", err)
	}

	enq := &recordingEnqueuer{}
	_, adminSvc := newServices(pool, enq)
	insertFailedJob(t, pool, "job_aj_window", "pm_mock_v1")

	job, err := adminSvc.RetryJob(context.Background(), itTenant, "job_aj_window")
	if err != nil {
		t.Fatalf("retry in a fresh window must succeed, got %v", err)
	}
	if job.Status != "queued" {
		t.Fatalf("expected queued, got %s", job.Status)
	}
	status := scalar(t, pool, `SELECT status FROM cost_budgets WHERE id='bud_aj_window'`)
	spent := scalar(t, pool, `SELECT spent_amount::text FROM cost_budgets WHERE id='bud_aj_window'`)
	reserved := scalar(t, pool, `SELECT reserved_amount::text FROM cost_budgets WHERE id='bud_aj_window'`)
	if status != "active" || spent != "0.0000" || reserved != "0.0100" {
		t.Fatalf("reset on retry: expected active/0/0.0100, got %s/%s/%s", status, spent, reserved)
	}
}

// --- end-to-end worker tests ------------------------------------------------

type memStorage struct{}

func (memStorage) Put(_ context.Context, key string, _ []byte, _ string) (string, error) {
	return "s3://test/" + key, nil
}
func (memStorage) Presign(_ context.Context, key string, _ time.Duration) (string, error) {
	return "https://test.local/" + key, nil
}

func newWorker(pool *pgxpool.Pool) *jobs.Worker {
	reg := providers.NewRegistry()
	reg.Register("mock", mock.New())
	return &jobs.Worker{
		Jobs:      jobs.NewRepository(pool),
		Assets:    assets.NewRepository(pool),
		Storage:   memStorage{},
		Providers: reg,
		Finalizer: cost.NewLifecycle(pool, nil),
	}
}

// End-to-end: cancel a queued job, then run the worker on it. The worker must
// treat cancelled as terminal — produce no asset, not resurrect the job, and
// keep the reservation released.
func TestEndToEndCancelQueuedThenWorker(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedToken(t, pool)
	seedBudget(t, pool, "bud_aj_e2e_cancel", "active", "1.0000")

	enq := &recordingEnqueuer{}
	jobSvc, adminSvc := newServices(pool, enq)
	jobID := createQueuedJob(t, jobSvc)

	if _, err := adminSvc.CancelJob(context.Background(), itTenant, jobID); err != nil {
		t.Fatalf("CancelJob: %v", err)
	}
	// The worker picks the task up after the cancel landed.
	if err := newWorker(pool).Process(context.Background(), jobID, 0); err != nil {
		t.Fatalf("worker Process on cancelled job: %v", err)
	}
	if st := scalar(t, pool, `SELECT status FROM generation_jobs WHERE id=$1`, jobID); st != "cancelled" {
		t.Fatalf("worker must not resurrect a cancelled job, status=%s", st)
	}
	cnt := scalar(t, pool, `SELECT count(*) FROM visual_assets WHERE generation_job_id=$1`, jobID)
	if cnt != "0" {
		t.Fatalf("worker must produce no asset for a cancelled job, got %s", cnt)
	}
	if s := scalar(t, pool, `SELECT status FROM cost_reservations WHERE generation_job_id=$1`, jobID); s != "released" {
		t.Fatalf("reservation must stay released, got %s", s)
	}
	if held := scalar(t, pool, `SELECT reserved_amount::text FROM cost_budgets WHERE id='bud_aj_e2e_cancel'`); held != "0.0000" {
		t.Fatalf("budget must stay reclaimed, got %s", held)
	}
}

// End-to-end: retry a failed job, then run the worker. The same job completes
// and its fresh reservation is committed exactly once.
func TestEndToEndRetryThenWorkerCompletes(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedToken(t, pool)
	seedBudget(t, pool, "bud_aj_e2e_retry", "active", "1.0000")

	enq := &recordingEnqueuer{}
	_, adminSvc := newServices(pool, enq)
	insertFailedJob(t, pool, "job_aj_e2e_retry", "pm_mock_v1")

	if _, err := adminSvc.RetryJob(context.Background(), itTenant, "job_aj_e2e_retry"); err != nil {
		t.Fatalf("RetryJob: %v", err)
	}
	if err := newWorker(pool).Process(context.Background(), "job_aj_e2e_retry", 0); err != nil {
		t.Fatalf("worker Process on retried job: %v", err)
	}
	if st := scalar(t, pool, `SELECT status FROM generation_jobs WHERE id=$1`, "job_aj_e2e_retry"); st != "completed" {
		t.Fatalf("retried job must complete, got %s", st)
	}
	cnt := scalar(t, pool, `SELECT count(*) FROM visual_assets WHERE generation_job_id=$1`, "job_aj_e2e_retry")
	if cnt != "1" {
		t.Fatalf("retried job must produce exactly one asset, got %s", cnt)
	}
	if s := scalar(t, pool, `SELECT status FROM cost_reservations WHERE generation_job_id=$1`, "job_aj_e2e_retry"); s != "committed" {
		t.Fatalf("reservation must be committed once, got %s", s)
	}
	spent := scalar(t, pool, `SELECT spent_amount::text FROM cost_budgets WHERE id='bud_aj_e2e_retry'`)
	reserved := scalar(t, pool, `SELECT reserved_amount::text FROM cost_budgets WHERE id='bud_aj_e2e_retry'`)
	if spent != "0.0100" || reserved != "0.0000" {
		t.Fatalf("on commit, budget should be spent=0.0100 reserved=0, got spent=%s reserved=%s", spent, reserved)
	}
}
