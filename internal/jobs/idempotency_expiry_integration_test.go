//go:build integration

package jobs_test

import (
	"context"
	"testing"

	"github.com/zakkriel/drchat-image-platform/internal/jobs"
)

// Idempotency expiry enforcement: an expired idempotency record must behave
// as absent — LookupReplay reports not-found and a new create with the SAME
// key (even a different body) takes the key over instead of replaying the
// stale job or conflicting forever. Before this fix GetIdempotencyKey ignored
// expires_at, so a denied/stale record poisoned its key indefinitely.
func TestIdempotencyExpiredKeyIsReusable(t *testing.T) {
	pool := openTestPool(t)
	defer pool.Close()
	cleanup(t, pool)
	defer cleanup(t, pool)
	seedFixtures(t, pool)

	enq := &recordingEnqueuer{}
	svc := newCostService(pool, enq)
	const key = "expiry-key-1"
	p1 := baseParams()
	p1.IdempotencyKey = key
	p1.Endpoint = "POST /v1/artifacts/art_1/generate"
	p1.RequestHash = "hash-body-1"

	first, err := svc.CreateAndEnqueue(context.Background(), p1)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}

	// Live record: same key + DIFFERENT body must conflict (the unchanged
	// pre-expiry contract).
	p2 := p1
	p2.RequestHash = "hash-body-2"
	if _, err := svc.CreateAndEnqueue(context.Background(), p2); err != jobs.ErrIdempotencyConflict {
		t.Fatalf("live key with different body: expected ErrIdempotencyConflict, got %v", err)
	}

	// Expire the record in place.
	if _, err := pool.Exec(context.Background(),
		`UPDATE idempotency_keys SET expires_at = now() - interval '1 hour' WHERE key = $1`, key); err != nil {
		t.Fatalf("expire record: %v", err)
	}

	// Expired ⇒ LookupReplay treats the key as new.
	_, found, err := svc.LookupReplay(context.Background(), jobs.ReplayLookup{
		TenantID:    itTenant,
		TokenID:     itTokenID,
		Key:         key,
		Endpoint:    p1.Endpoint,
		RequestHash: p1.RequestHash,
	})
	if err != nil {
		t.Fatalf("LookupReplay: %v", err)
	}
	if found {
		t.Fatal("expired idempotency record must be treated as not found")
	}

	// Expired ⇒ a new create with the same key (different body) takes the key
	// over and creates a FRESH job — no conflict, no stale replay.
	second, err := svc.CreateAndEnqueue(context.Background(), p2)
	if err != nil {
		t.Fatalf("create over expired key: %v", err)
	}
	if second.Replayed {
		t.Fatal("create over expired key must not be a replay")
	}
	if second.JobID == first.JobID {
		t.Fatal("create over expired key must create a fresh job")
	}

	// The taken-over record is live again: an identical retry now replays the
	// SECOND job.
	third, err := svc.CreateAndEnqueue(context.Background(), p2)
	if err != nil {
		t.Fatalf("replay after takeover: %v", err)
	}
	if !third.Replayed || third.JobID != second.JobID {
		t.Fatalf("expected replay of the takeover job %s, got %+v", second.JobID, third)
	}
}
