package jobs

import (
	"context"
	"errors"
	"testing"

	"github.com/zakkriel/drchat-image-platform/internal/identities"
	"github.com/zakkriel/drchat-image-platform/internal/webhooks"
)

// fakeWebhookEmitter records every emitted event in order. err, when set, is
// returned from Emit so a test can prove emission failures never change the
// worker's control flow.
type fakeWebhookEmitter struct {
	events []webhooks.EmitInput
	err    error
}

func (f *fakeWebhookEmitter) Emit(_ context.Context, in webhooks.EmitInput) error {
	f.events = append(f.events, in)
	return f.err
}

// only asserts exactly one event was emitted and returns it.
func (f *fakeWebhookEmitter) only(t *testing.T) webhooks.EmitInput {
	t.Helper()
	if len(f.events) != 1 {
		t.Fatalf("expected exactly 1 emitted event, got %d (%+v)", len(f.events), f.events)
	}
	return f.events[0]
}

func (f *fakeWebhookEmitter) assertEvent(t *testing.T, in webhooks.EmitInput, wantEvent, wantJobID string) {
	t.Helper()
	if in.EventType != wantEvent {
		t.Fatalf("expected event %q, got %q", wantEvent, in.EventType)
	}
	if in.JobID != wantJobID {
		t.Fatalf("expected job_id %q, got %q", wantJobID, in.JobID)
	}
	if in.TenantID != "tenant_a" {
		t.Fatalf("expected tenant_id %q, got %q", "tenant_a", in.TenantID)
	}
}

func errorCodeFromData(t *testing.T, in webhooks.EmitInput) string {
	t.Helper()
	code, ok := in.Data["error_code"].(string)
	if !ok {
		t.Fatalf("expected string error_code in data, got %+v", in.Data)
	}
	return code
}

// TestPackCompletedEmitsWebhookEvent proves a pack job — not just a single
// image — emits generation_job.completed carrying every delivered asset. Before
// this, the whole pack fan-out path was silent, so a consumer waiting on a
// character pack was never notified at all.
func TestPackCompletedEmitsWebhookEvent(t *testing.T) {
	repo := newFakeJobsRepo()
	emitter := &fakeWebhookEmitter{}
	variants := []string{"neutral_front_portrait", "neutral_three_quarter_portrait"}
	seedPackJob(repo, "job_pack_emit", "pack_emit", JobTypeCharacterPack, variants)

	w := newPackWorker(repo, &fakeAssetsRepo{}, &selectiveProvider{}, &fakeFinalizer{})
	w.Webhooks = emitter

	if err := w.ProcessPack(context.Background(), "job_pack_emit"); err != nil {
		t.Fatalf("ProcessPack: %v", err)
	}

	ev := emitter.only(t)
	emitter.assertEvent(t, ev, webhooks.EventCompleted, "job_pack_emit")

	ids, ok := ev.Data["final_asset_ids"].([]string)
	if !ok {
		t.Fatalf("expected []string final_asset_ids, got %+v", ev.Data)
	}
	job := repo.jobs["job_pack_emit"]
	if len(ids) != len(variants) {
		t.Fatalf("expected %d delivered asset ids, got %d (%v)", len(variants), len(ids), ids)
	}
	// The event must carry exactly what the durable job carries — the read-back
	// contract depends on the event and GET /v1/jobs/{id} agreeing.
	if len(job.FinalAssetIds) != len(ids) {
		t.Fatalf("event ids %v disagree with job.FinalAssetIds %v", ids, job.FinalAssetIds)
	}
	for i := range ids {
		if ids[i] != job.FinalAssetIds[i] {
			t.Fatalf("event ids %v disagree with job.FinalAssetIds %v", ids, job.FinalAssetIds)
		}
	}
}

// TestPackAllItemsFailedEmitsWebhookEvent proves a pack whose every item failed
// emits generation_job.failed with the pack error code, instead of failing
// silently.
func TestPackAllItemsFailedEmitsWebhookEvent(t *testing.T) {
	repo := newFakeJobsRepo()
	emitter := &fakeWebhookEmitter{}
	seedPackJob(repo, "job_pack_allfail", "pack_allfail", JobTypeCharacterPack, []string{"neutral_front_portrait"})

	// failOn matches every prompt, so no item can be delivered.
	provider := &selectiveProvider{failOn: []string{"Captain Mira"}}
	w := newPackWorker(repo, &fakeAssetsRepo{}, provider, &fakeFinalizer{})
	w.Webhooks = emitter

	if err := w.ProcessPack(context.Background(), "job_pack_allfail"); err != nil {
		t.Fatalf("ProcessPack returned infra error: %v", err)
	}
	if job := repo.jobs["job_pack_allfail"]; job.Status != "failed" {
		t.Fatalf("expected failed job, got %q", job.Status)
	}

	ev := emitter.only(t)
	emitter.assertEvent(t, ev, webhooks.EventFailed, "job_pack_allfail")
	if got := errorCodeFromData(t, ev); got != errorCodePackAllFailed {
		t.Fatalf("expected error_code %q, got %q", errorCodePackAllFailed, got)
	}
}

// TestPackInvalidJobEmitsWebhookEvent proves an unrunnable pack job (no pack
// link / no variant keys on the payload) still notifies the consumer.
func TestPackInvalidJobEmitsWebhookEvent(t *testing.T) {
	repo := newFakeJobsRepo()
	emitter := &fakeWebhookEmitter{}
	tokenID := "tok_test"
	if _, err := repo.Insert(context.Background(), InsertParams{
		ID:                 "job_pack_invalid",
		TenantID:           "tenant_a",
		JobType:            JobTypeCharacterPack,
		RequestedByTokenID: &tokenID,
		InputPayload:       map[string]any{},
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	w := newPackWorker(repo, &fakeAssetsRepo{}, &selectiveProvider{}, &fakeFinalizer{})
	w.Webhooks = emitter

	if err := w.ProcessPack(context.Background(), "job_pack_invalid"); err != nil {
		t.Fatalf("ProcessPack returned infra error: %v", err)
	}
	if job := repo.jobs["job_pack_invalid"]; job.Status != "failed" {
		t.Fatalf("expected failed job, got %q", job.Status)
	}

	ev := emitter.only(t)
	emitter.assertEvent(t, ev, webhooks.EventFailed, "job_pack_invalid")
	if got := errorCodeFromData(t, ev); got != errorCodePackInvalidJob {
		t.Fatalf("expected error_code %q, got %q", errorCodePackInvalidJob, got)
	}
}

// TestPackFailTerminalEmitsWebhookEvent covers the failPackTerminal branch: a
// pack routed to a reference-conditioned provider with no usable anchors fails
// closed, and that terminal failure is now reported.
func TestPackFailTerminalEmitsWebhookEvent(t *testing.T) {
	repo := newFakeJobsRepo()
	repo.assets = &fakeAssetsRepo{}
	emitter := &fakeWebhookEmitter{}
	seedFalPackJob(repo, "job_pack_noref", "pack_noref", []string{"neutral_front_portrait"})

	provider := &referenceProvider{}
	w := newPackWorker(repo, &fakeAssetsRepo{}, provider, &fakeFinalizer{})
	w.Providers = newFalRegistry(provider)
	w.Identities = &fakeIdentityReader{identity: identities.VisualIdentity{
		ID:             "vi_test",
		TenantID:       "tenant_a",
		AnchorAssetIds: nil,
	}}
	w.Webhooks = emitter

	if err := w.ProcessPack(context.Background(), "job_pack_noref"); err != nil {
		t.Fatalf("ProcessPack returned infra error: %v", err)
	}
	if n := len(provider.calls()); n != 0 {
		t.Fatalf("provider must not be called without references; got %d calls", n)
	}

	ev := emitter.only(t)
	emitter.assertEvent(t, ev, webhooks.EventFailed, "job_pack_noref")
	if got := errorCodeFromData(t, ev); got != errorCodeMissingReference {
		t.Fatalf("expected error_code %q, got %q", errorCodeMissingReference, got)
	}
}

// TestSingleImageFailTerminalEmitsWebhookEvent covers the failTerminal branch on
// the single-image path. These unrunnable-job codes never reach
// failJobOnFinalAttempt, so before this the job went terminally failed with no
// event at all.
func TestSingleImageFailTerminalEmitsWebhookEvent(t *testing.T) {
	repo := newFakeJobsRepo()
	repo.assets = &fakeAssetsRepo{}
	emitter := &fakeWebhookEmitter{}
	provider := &referenceProvider{}
	seedFalGenerationJob(repo, "job_gen_noref")

	w := &Worker{
		Jobs:      repo,
		Assets:    &fakeAssetsRepo{},
		Storage:   &fakeStorage{},
		Providers: newFalRegistry(provider),
		Identities: &fakeIdentityReader{identity: identities.VisualIdentity{
			ID:             "vi_test",
			TenantID:       "tenant_a",
			AnchorAssetIds: nil,
		}},
		Webhooks: emitter,
	}

	if err := w.Process(context.Background(), "job_gen_noref", 0); err != nil {
		t.Fatalf("Process returned infra error: %v", err)
	}
	if job := repo.jobs["job_gen_noref"]; job.Status != "failed" {
		t.Fatalf("expected failed job, got %q", job.Status)
	}

	ev := emitter.only(t)
	emitter.assertEvent(t, ev, webhooks.EventFailed, "job_gen_noref")
	if got := errorCodeFromData(t, ev); got != errorCodeMissingReference {
		t.Fatalf("expected error_code %q, got %q", errorCodeMissingReference, got)
	}
}

// TestCompletedEmittedWhenCostCommitFails pins the emit-ordering rule: every
// event is emitted immediately after its durable status transition, BEFORE cost
// finalization. A commitReservation failure returns an error so asynq retries,
// but that retry short-circuits on the terminal status and only re-runs
// finalization — so an emit placed after the commit would be lost forever.
func TestCompletedEmittedWhenCostCommitFails(t *testing.T) {
	repo := newFakeJobsRepo()
	emitter := &fakeWebhookEmitter{}
	fin := &fakeFinalizer{failNextCommit: true}
	seedPackJob(repo, "job_pack_commitfail", "pack_commitfail", JobTypeCharacterPack, []string{"neutral_front_portrait"})

	w := newPackWorker(repo, &fakeAssetsRepo{}, &selectiveProvider{}, fin)
	w.Webhooks = emitter

	if err := w.ProcessPack(context.Background(), "job_pack_commitfail"); err == nil {
		t.Fatal("expected ProcessPack to return the commit error so asynq retries")
	}
	if job := repo.jobs["job_pack_commitfail"]; job.Status != "completed" {
		t.Fatalf("expected the job to be durably completed, got %q", job.Status)
	}

	ev := emitter.only(t)
	emitter.assertEvent(t, ev, webhooks.EventCompleted, "job_pack_commitfail")
}

// TestTerminalPackRetryDoesNotReEmit proves the terminal short-circuit keeps
// emission at-most-once-per-transition: a redelivered task for an already
// terminal pack job re-runs only the idempotent cost finalization and emits
// nothing further.
func TestTerminalPackRetryDoesNotReEmit(t *testing.T) {
	repo := newFakeJobsRepo()
	emitter := &fakeWebhookEmitter{}
	seedPackJob(repo, "job_pack_retry", "pack_retry", JobTypeCharacterPack, []string{"neutral_front_portrait"})

	w := newPackWorker(repo, &fakeAssetsRepo{}, &selectiveProvider{}, &fakeFinalizer{})
	w.Webhooks = emitter

	for i := 0; i < 3; i++ {
		if err := w.ProcessPack(context.Background(), "job_pack_retry"); err != nil {
			t.Fatalf("ProcessPack attempt %d: %v", i+1, err)
		}
	}

	ev := emitter.only(t)
	emitter.assertEvent(t, ev, webhooks.EventCompleted, "job_pack_retry")
}

// TestEmitterErrorNeverFailsPackJob proves emission is strictly best-effort: a
// broken webhook subsystem must never turn a delivered pack into a failed job.
func TestEmitterErrorNeverFailsPackJob(t *testing.T) {
	repo := newFakeJobsRepo()
	emitter := &fakeWebhookEmitter{err: errors.New("webhook subsystem down")}
	seedPackJob(repo, "job_pack_emiterr", "pack_emiterr", JobTypeCharacterPack, []string{"neutral_front_portrait"})

	w := newPackWorker(repo, &fakeAssetsRepo{}, &selectiveProvider{}, &fakeFinalizer{})
	w.Webhooks = emitter

	if err := w.ProcessPack(context.Background(), "job_pack_emiterr"); err != nil {
		t.Fatalf("ProcessPack must not fail on an emitter error: %v", err)
	}
	if job := repo.jobs["job_pack_emiterr"]; job.Status != "completed" {
		t.Fatalf("expected completed job, got %q", job.Status)
	}
	if len(emitter.events) != 1 {
		t.Fatalf("expected the emit to be attempted once, got %d", len(emitter.events))
	}
}
