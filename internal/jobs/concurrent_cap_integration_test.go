//go:build integration

package jobs_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zakkriel/drchat-image-platform/internal/cost"
	"github.com/zakkriel/drchat-image-platform/internal/ids"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
)

// These tests exercise the Phase 7C-2 hard concurrent-job cap directly against
// jobs.Service.CreateAndEnqueue. They never run the worker, so no MinIO/S3 is
// required: the cap denial happens inside the create transaction, before any
// reservation / job insert / idempotency insert / enqueue.

// seedJobStatus inserts a generation_jobs row owned by itTokenID at the given
// status, so a test can stage a token's live (or terminal) job count.
func seedJobStatus(t *testing.T, pool *pgxpool.Pool, status string) string {
	t.Helper()
	id := ids.NewGenerationJobID()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO generation_jobs (id, tenant_id, world_id, job_type, status, requested_by_token_id, input_payload)
		 VALUES ($1, $2, 'w1', 'artifact', $3, $4, '{}')`,
		id, itTenant, status, itTokenID,
	); err != nil {
		t.Fatalf("seed job status=%s: %v", status, err)
	}
	return id
}

// capParams builds a create request priced against the seeded mock provider so
// the cost pre-flight passes; max is the per-token concurrent cap. When key is
// non-empty the request is idempotent.
func capParams(key string, max int) jobs.CreateAndEnqueueParams {
	p := jobs.CreateAndEnqueueParams{
		TenantID:           itTenant,
		RequestedByTokenID: itTokenID,
		JobType:            "artifact",
		WorldID:            "w1",
		InputPayload:       map[string]any{"description": "concurrent cap test"},
		FallbackPolicy:     "none",
		CacheResult:        "generated_required",
		ProviderID:         "mock",
		ModelID:            "pm_mock_v1",
		OperationType:      "text_to_image",
		Units:              1,
		MaxConcurrentJobs:  max,
	}
	if key != "" {
		p.IdempotencyKey = key
		p.Endpoint = "POST /v1/artifacts/art/generate"
		p.RequestHash = "hash-" + key
	}
	return p
}

func liveJobCount(t *testing.T, pool *pgxpool.Pool) int {
	return countScalar(t, pool,
		`SELECT count(*) FROM generation_jobs WHERE requested_by_token_id = $1 AND status IN ('queued','running','preview_ready')`,
		itTokenID)
}

func TestConcurrentCapBelowCapProceeds(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)

	enq := newRecordingEnqueuer()
	svc := jobs.NewService(pool, enq, cost.NewService(nil))

	res, err := svc.CreateAndEnqueue(context.Background(), capParams("", 5))
	if err != nil {
		t.Fatalf("below cap must proceed, got error: %v", err)
	}
	if res.Status != "queued" {
		t.Fatalf("expected status=queued, got %q", res.Status)
	}
	if res.ConcurrentJobsLimit != 5 || res.ConcurrentJobsUsed != 1 {
		t.Fatalf("expected limit=5 used=1, got limit=%d used=%d", res.ConcurrentJobsLimit, res.ConcurrentJobsUsed)
	}
	if got := len(enq.snapshot()); got != 1 {
		t.Fatalf("expected one enqueue, got %d", got)
	}
}

func TestConcurrentCapDeniesAtCap(t *testing.T) {
	cases := []string{"queued", "running", "preview_ready"}
	for _, status := range cases {
		t.Run(status, func(t *testing.T) {
			pool := openTestPool(t)
			defer pool.Close()
			cleanup(t, pool)
			defer cleanup(t, pool)
			seedFixtures(t, pool)

			for i := 0; i < 3; i++ {
				seedJobStatus(t, pool, status)
			}
			jobsBefore := countScalar(t, pool, `SELECT count(*) FROM generation_jobs WHERE tenant_id = $1`, itTenant)

			enq := newRecordingEnqueuer()
			svc := jobs.NewService(pool, enq, cost.NewService(nil))

			_, err := svc.CreateAndEnqueue(context.Background(), capParams("", 3))
			if !errors.Is(err, jobs.ErrConcurrentJobsExceeded) {
				t.Fatalf("status=%s at cap: expected ErrConcurrentJobsExceeded, got %v", status, err)
			}

			// No side effects: no new job, no reservation, no idempotency row, no enqueue.
			if got := countScalar(t, pool, `SELECT count(*) FROM generation_jobs WHERE tenant_id = $1`, itTenant); got != jobsBefore {
				t.Fatalf("denial must not create a job: had %d, now %d", jobsBefore, got)
			}
			if got := countScalar(t, pool, `SELECT count(*) FROM cost_reservations WHERE tenant_id = $1`, itTenant); got != 0 {
				t.Fatalf("denial must not create a reservation, got %d", got)
			}
			if got := countScalar(t, pool, `SELECT count(*) FROM idempotency_keys WHERE token_id = $1`, itTokenID); got != 0 {
				t.Fatalf("denial must not create an idempotency row, got %d", got)
			}
			if got := len(enq.snapshot()); got != 0 {
				t.Fatalf("denial must not enqueue, got %d", got)
			}
		})
	}
}

func TestConcurrentCapTerminalJobsDoNotCount(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)

	// Five of each terminal status; none occupy a live slot.
	for _, status := range []string{"completed", "failed", "cancelled"} {
		for i := 0; i < 5; i++ {
			seedJobStatus(t, pool, status)
		}
	}

	enq := newRecordingEnqueuer()
	svc := jobs.NewService(pool, enq, cost.NewService(nil))

	// Cap of 1 with zero live jobs must still proceed.
	res, err := svc.CreateAndEnqueue(context.Background(), capParams("", 1))
	if err != nil {
		t.Fatalf("terminal jobs must not consume slots, got error: %v", err)
	}
	if res.Status != "queued" {
		t.Fatalf("expected status=queued, got %q", res.Status)
	}
}

func TestConcurrentCapCancelFreesSlot(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)

	stuck := seedJobStatus(t, pool, "queued")

	enq := newRecordingEnqueuer()
	svc := jobs.NewService(pool, enq, cost.NewService(nil))

	// At cap (1 live, cap 1) → denied.
	if _, err := svc.CreateAndEnqueue(context.Background(), capParams("", 1)); !errors.Is(err, jobs.ErrConcurrentJobsExceeded) {
		t.Fatalf("expected denial at cap, got %v", err)
	}

	// Cancelling the live job frees the slot (cancelled is terminal).
	if _, err := pool.Exec(context.Background(),
		`UPDATE generation_jobs SET status = 'cancelled' WHERE id = $1`, stuck); err != nil {
		t.Fatalf("cancel job: %v", err)
	}

	if _, err := svc.CreateAndEnqueue(context.Background(), capParams("", 1)); err != nil {
		t.Fatalf("after cancel, create must proceed, got %v", err)
	}
}

func TestConcurrentCapParallelCreatesCannotExceed(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)

	enq := newRecordingEnqueuer()
	svc := jobs.NewService(pool, enq, cost.NewService(nil))

	const cap = 3
	const N = 10
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = svc.CreateAndEnqueue(context.Background(), capParams("", cap))
		}(i)
	}
	close(start)
	wg.Wait()

	ok, denied := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, jobs.ErrConcurrentJobsExceeded):
			denied++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != cap {
		t.Fatalf("advisory lock must hold the cap: expected exactly %d successes, got %d (denied %d)", cap, ok, denied)
	}
	if live := liveJobCount(t, pool); live != cap {
		t.Fatalf("expected exactly %d live jobs, got %d", cap, live)
	}
}

func TestConcurrentCapReplayPreCheckAtCapReturnsExisting(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)

	enq := newRecordingEnqueuer()
	svc := jobs.NewService(pool, enq, cost.NewService(nil))

	const key = "replay-precheck-key"
	first, err := svc.CreateAndEnqueue(context.Background(), capParams(key, 5))
	if err != nil {
		t.Fatalf("first create: %v", err)
	}

	// Fill the cap with extra live jobs so a fresh create would be denied.
	for i := 0; i < 5; i++ {
		seedJobStatus(t, pool, "queued")
	}

	// The handler pre-check (LookupReplay) never consults the cap.
	res, found, err := svc.LookupReplay(context.Background(), jobs.ReplayLookup{
		TokenID:     itTokenID,
		Key:         key,
		Endpoint:    "POST /v1/artifacts/art/generate",
		RequestHash: "hash-" + key,
	})
	if err != nil {
		t.Fatalf("LookupReplay at cap must not error, got %v", err)
	}
	if !found || res.JobID != first.JobID {
		t.Fatalf("pre-check replay must return the existing job %q, got found=%v id=%q", first.JobID, found, res.JobID)
	}
}

func TestConcurrentCapInTransactionReplayAtCapReturnsExisting(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)

	enq := newRecordingEnqueuer()
	svc := jobs.NewService(pool, enq, cost.NewService(nil))

	const key = "replay-intx-key"
	first, err := svc.CreateAndEnqueue(context.Background(), capParams(key, 5))
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	jobsAfterFirst := countScalar(t, pool, `SELECT count(*) FROM generation_jobs WHERE tenant_id = $1`, itTenant)

	// Same-key create while the token is at the cap (cap 1, the first job is
	// live) must resolve to an in-transaction replay, NOT a concurrent denial.
	res, err := svc.CreateAndEnqueue(context.Background(), capParams(key, 1))
	if err != nil {
		t.Fatalf("same-key create at cap must replay, not deny, got %v", err)
	}
	if !res.Replayed || res.JobID != first.JobID {
		t.Fatalf("expected replay of %q, got replayed=%v id=%q", first.JobID, res.Replayed, res.JobID)
	}
	if got := countScalar(t, pool, `SELECT count(*) FROM generation_jobs WHERE tenant_id = $1`, itTenant); got != jobsAfterFirst {
		t.Fatalf("replay must not create a new job: had %d, now %d", jobsAfterFirst, got)
	}
}

func TestConcurrentCapConcurrentSameKeyAtCapReturnsExisting(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)

	enq := newRecordingEnqueuer()
	svc := jobs.NewService(pool, enq, cost.NewService(nil))

	const key = "concurrent-samekey-cap1"
	const N = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make([]jobs.CreateResult, N)
	errs := make([]error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = svc.CreateAndEnqueue(context.Background(), capParams(key, 1))
		}(i)
	}
	close(start)
	wg.Wait()

	jobIDs := map[string]struct{}{}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: same-key duplicate at cap must never be denied, got %v", i, err)
		}
		jobIDs[results[i].JobID] = struct{}{}
	}
	if len(jobIDs) != 1 {
		t.Fatalf("all same-key requests must converge on one job, got %d distinct ids", len(jobIDs))
	}
	if got := countScalar(t, pool, `SELECT count(*) FROM generation_jobs WHERE tenant_id = $1`, itTenant); got != 1 {
		t.Fatalf("expected exactly one job row, got %d", got)
	}
	if got := len(enq.snapshot()); got != 1 {
		t.Fatalf("expected exactly one enqueue, got %d", got)
	}
}

func TestConcurrentCapCacheHitExempt(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)

	// Stage live jobs that would exceed any small cap.
	for i := 0; i < 5; i++ {
		seedJobStatus(t, pool, "queued")
	}
	liveBefore := liveJobCount(t, pool)

	enq := newRecordingEnqueuer()
	svc := jobs.NewService(pool, enq, cost.NewService(nil))

	// A cache-hit completion takes no MaxConcurrentJobs and must never be
	// cap-checked: it lands a completed job and consumes no live slot.
	res, err := svc.CreateCompletedCacheHitJob(context.Background(), jobs.CreateCacheHitParams{
		TenantID:           itTenant,
		RequestedByTokenID: itTokenID,
		JobType:            "artifact",
		WorldID:            "w1",
		InputPayload:       map[string]any{"description": "cache hit"},
		FallbackPolicy:     "none",
		FinalAssetID:       "asset_cachehit_fake",
	})
	if err != nil {
		t.Fatalf("cache-hit completion must be exempt from the cap, got %v", err)
	}
	if res.Status != "completed" {
		t.Fatalf("expected completed cache-hit, got %q", res.Status)
	}
	if got := liveJobCount(t, pool); got != liveBefore {
		t.Fatalf("cache-hit must consume no live slot: live was %d, now %d", liveBefore, got)
	}
	if got := len(enq.snapshot()); got != 0 {
		t.Fatalf("cache-hit must not enqueue, got %d", got)
	}
}
