package jobs

import (
	"context"
	"testing"

	"github.com/zakkriel/drchat-image-platform/internal/providers"
	"github.com/zakkriel/drchat-image-platform/internal/providers/mock"
)

// multiRegistry registers several adapters by provider id so the fallback walk
// (Phase 7C-4) can be exercised with a primary + fallbacks across distinct
// providers — unlike testRegistry, which registers a single "mock" adapter.
func multiRegistry(adapters map[string]providers.ImageProvider) *providers.Registry {
	reg := providers.NewRegistry()
	for id, p := range adapters {
		reg.Register(id, p)
	}
	return reg
}

// fallbackPayload builds a job input payload whose primary route is the seeded
// mock route (withDefaultResolvedRoute fills it) and whose fallback_routes carry
// the supplied alternates in order, in the same []any-of-map[string]any shape the
// jobs service persists.
func fallbackPayload(description string, fallbacks ...map[string]any) map[string]any {
	raw := make([]any, 0, len(fallbacks))
	for _, fb := range fallbacks {
		raw = append(raw, fb)
	}
	return map[string]any{
		"world_id":        "w1",
		"description":     description,
		"fallback_routes": raw,
	}
}

// TestFallbackRoutesFromPayloadRoundTripAndTolerance: withFallbackRoutesPayload
// (service side) and fallbackRoutesFromPayload (worker side) round-trip the
// alternates, and the worker reader tolerates missing/invalid shapes by skipping
// rather than failing.
func TestFallbackRoutesFromPayloadRoundTripAndTolerance(t *testing.T) {
	// Round-trip: the service stamps []map[string]any; the worker reads them back
	// after a JSON-style decode where the slice is []any of map[string]any.
	stamped := withFallbackRoutesPayload(map[string]any{}, []map[string]any{
		{"provider_id": "bfl", "model_id": "pm_bfl_v1", "provider_route_id": "route_bfl"},
	})
	raw, ok := stamped["fallback_routes"].([]map[string]any)
	if !ok || len(raw) != 1 {
		t.Fatalf("expected one stamped fallback, got %v", stamped["fallback_routes"])
	}
	// The worker reads the payload after JSON decode, so model the []any shape.
	decoded := map[string]any{"fallback_routes": []any{
		map[string]any{"provider_id": "bfl", "model_id": "pm_bfl_v1", "provider_route_id": "route_bfl"},
	}}
	got := fallbackRoutesFromPayload(decoded)
	if len(got) != 1 || got[0].providerID != "bfl" || got[0].modelID != "pm_bfl_v1" || got[0].routeID != "route_bfl" {
		t.Fatalf("round-trip mismatch, got %+v", got)
	}

	// Tolerance: missing key, wrong type, and entries missing provider/model are
	// all skipped (no panic, no error).
	if r := fallbackRoutesFromPayload(map[string]any{}); r != nil {
		t.Fatalf("missing key must yield nil, got %+v", r)
	}
	if r := fallbackRoutesFromPayload(map[string]any{"fallback_routes": "nope"}); r != nil {
		t.Fatalf("wrong type must yield nil, got %+v", r)
	}
	mixed := map[string]any{"fallback_routes": []any{
		"not-a-map",
		map[string]any{"provider_id": "bfl"}, // missing model_id → skipped
		map[string]any{"provider_id": "bfl", "model_id": "pm_bfl_v1"},
	}}
	if r := fallbackRoutesFromPayload(mixed); len(r) != 1 || r[0].providerID != "bfl" {
		t.Fatalf("expected only the complete entry kept, got %+v", r)
	}
}

// TestWorkerFallbackPrimaryFailsFallbackSucceeds: the primary provider fails and
// the next same-price fallback succeeds, so the job completes with the asset's
// provenance set to the FALLBACK route, two provider_attempts are recorded (one
// failed for the primary, one succeeded for the fallback), and the single cost
// reservation is committed exactly once.
func TestWorkerFallbackPrimaryFailsFallbackSucceeds(t *testing.T) {
	jobsRepo := newFakeJobsRepo()
	assetsRepo := &fakeAssetsRepo{}
	jobsRepo.assets = assetsRepo
	storage := &fakeStorage{}
	fin := &fakeFinalizer{}

	worldID := "w1"
	tokenID := "tok_test"
	_, _ = jobsRepo.Insert(context.Background(), InsertParams{
		ID:                 "job_fb1",
		TenantID:           "tenant_a",
		WorldID:            &worldID,
		JobType:            "artifact",
		RequestedByTokenID: &tokenID,
		// Primary is the seeded mock route; the single fallback is the bfl route.
		InputPayload: fallbackPayload("bronze key", map[string]any{
			"provider_id":       "bfl",
			"model_id":          "pm_bfl_v1",
			"provider_route_id": "route_bfl_text_to_image_standard",
		}),
	})

	w := &Worker{
		Jobs:    jobsRepo,
		Assets:  assetsRepo,
		Storage: storage,
		// Primary "mock" always errors; the fallback "bfl" succeeds.
		Providers: multiRegistry(map[string]providers.ImageProvider{
			"mock": errorProvider{},
			"bfl":  mock.New(),
		}),
		Finalizer: fin,
	}
	if err := w.Process(context.Background(), "job_fb1", 0); err != nil {
		t.Fatalf("Process: %v", err)
	}

	// Job completed.
	if len(jobsRepo.markCompleted) != 1 || jobsRepo.markCompleted[0] != "job_fb1" {
		t.Fatalf("expected job_fb1 completed, got %+v", jobsRepo.markCompleted)
	}
	// Asset provenance reflects the WINNER (the bfl fallback), not the primary.
	if len(assetsRepo.stored) != 1 {
		t.Fatalf("expected one asset stored, got %d", len(assetsRepo.stored))
	}
	asset := assetsRepo.stored[0]
	if asset.ProviderID == nil || *asset.ProviderID != "bfl" {
		t.Fatalf("expected asset provenance provider_id=bfl (the winning fallback), got %v", asset.ProviderID)
	}
	if asset.ModelID == nil || *asset.ModelID != "pm_bfl_v1" {
		t.Fatalf("expected asset provenance model_id=pm_bfl_v1, got %v", asset.ModelID)
	}
	// Two attempts: primary (mock) failed, fallback (bfl) succeeded.
	if len(jobsRepo.attempts) != 2 {
		t.Fatalf("expected two provider attempts, got %+v", jobsRepo.attempts)
	}
	if jobsRepo.attempts[0].ProviderID != "mock" || jobsRepo.attempts[1].ProviderID != "bfl" {
		t.Fatalf("expected attempts [mock, bfl] in order, got %+v", jobsRepo.attempts)
	}
	// Cost events: one failed (primary) + one completed (winner).
	var failed, completed int
	for _, ce := range jobsRepo.costEvents {
		switch ce.Status {
		case "failed":
			failed++
		case "completed":
			completed++
		}
	}
	if failed != 1 || completed != 1 {
		t.Fatalf("expected 1 failed + 1 completed cost event, got failed=%d completed=%d (%+v)", failed, completed, jobsRepo.costEvents)
	}
	// Reservation committed exactly once, never released.
	if len(fin.committed) != 1 || fin.committed[0] != "job_fb1" {
		t.Fatalf("expected one commit, got %+v", fin.committed)
	}
	if len(fin.released) != 0 {
		t.Fatalf("expected no release on success, got %+v", fin.released)
	}
}

// TestWorkerFallbackAllRoutesFailOnFinalAttempt: every route in the chain fails
// on the final asynq attempt, so the job is marked failed + the reservation is
// released, with exactly one provider_attempt recorded per route.
func TestWorkerFallbackAllRoutesFailOnFinalAttempt(t *testing.T) {
	jobsRepo := newFakeJobsRepo()
	assetsRepo := &fakeAssetsRepo{}
	jobsRepo.assets = assetsRepo
	fin := &fakeFinalizer{}

	worldID := "w1"
	_, _ = jobsRepo.Insert(context.Background(), InsertParams{
		ID:       "job_fb2",
		TenantID: "tenant_a",
		WorldID:  &worldID,
		JobType:  "artifact",
		InputPayload: fallbackPayload("fail", map[string]any{
			"provider_id":       "bfl",
			"model_id":          "pm_bfl_v1",
			"provider_route_id": "route_bfl_text_to_image_standard",
		}),
	})

	w := &Worker{
		Jobs:    jobsRepo,
		Assets:  assetsRepo,
		Storage: &fakeStorage{},
		// Both routes' adapters error.
		Providers: multiRegistry(map[string]providers.ImageProvider{
			"mock": errorProvider{},
			"bfl":  errorProvider{},
		}),
		Finalizer: fin,
	}
	if err := w.Process(context.Background(), "job_fb2", int32(MaxAttempts-1)); err == nil {
		t.Fatalf("expected error when every route fails on the final attempt")
	}

	// One attempt per route (both failed), in order.
	if len(jobsRepo.attempts) != 2 {
		t.Fatalf("expected two provider attempts (one per route), got %+v", jobsRepo.attempts)
	}
	if jobsRepo.attempts[0].ProviderID != "mock" || jobsRepo.attempts[1].ProviderID != "bfl" {
		t.Fatalf("expected attempts [mock, bfl] in order, got %+v", jobsRepo.attempts)
	}
	// Job marked failed + reservation released on the terminal attempt.
	if len(jobsRepo.markFailed) != 1 || jobsRepo.markFailed[0] != "job_fb2" {
		t.Fatalf("expected job_fb2 marked failed, got %+v", jobsRepo.markFailed)
	}
	if len(fin.released) != 1 || fin.released[0] != "job_fb2" {
		t.Fatalf("expected reservation released, got %+v", fin.released)
	}
	if len(fin.committed) != 0 {
		t.Fatalf("expected no commit on total failure, got %+v", fin.committed)
	}
	// Both cost events are failures; none completed.
	for _, ce := range jobsRepo.costEvents {
		if ce.Status != "failed" {
			t.Fatalf("expected only failed cost events, got %+v", jobsRepo.costEvents)
		}
	}
	if len(jobsRepo.costEvents) != 2 {
		t.Fatalf("expected two failed cost events (one per route), got %+v", jobsRepo.costEvents)
	}
}

// TestWorkerFallbackUnavailableAdapterIsSkipped: a fallback whose adapter is not
// registered in this process is skipped (no attempt recorded for it), and the
// next registered fallback produces the asset.
func TestWorkerFallbackUnavailableAdapterIsSkipped(t *testing.T) {
	jobsRepo := newFakeJobsRepo()
	assetsRepo := &fakeAssetsRepo{}
	jobsRepo.assets = assetsRepo
	fin := &fakeFinalizer{}

	worldID := "w1"
	tokenID := "tok_test"
	_, _ = jobsRepo.Insert(context.Background(), InsertParams{
		ID:                 "job_fb3",
		TenantID:           "tenant_a",
		WorldID:            &worldID,
		JobType:            "artifact",
		RequestedByTokenID: &tokenID,
		// Primary mock errors; first fallback "ghost" has no registered adapter and
		// must be skipped; second fallback "bfl" succeeds.
		InputPayload: fallbackPayload("bronze key",
			map[string]any{
				"provider_id":       "ghost",
				"model_id":          "pm_ghost_v1",
				"provider_route_id": "route_ghost",
			},
			map[string]any{
				"provider_id":       "bfl",
				"model_id":          "pm_bfl_v1",
				"provider_route_id": "route_bfl_text_to_image_standard",
			},
		),
	})

	w := &Worker{
		Jobs:    jobsRepo,
		Assets:  assetsRepo,
		Storage: &fakeStorage{},
		Providers: multiRegistry(map[string]providers.ImageProvider{
			"mock": errorProvider{},
			"bfl":  mock.New(),
			// "ghost" deliberately not registered.
		}),
		Finalizer: fin,
	}
	if err := w.Process(context.Background(), "job_fb3", 0); err != nil {
		t.Fatalf("Process: %v", err)
	}

	// Only the registered routes get attempts: mock (failed) + bfl (succeeded).
	// The unregistered "ghost" route is skipped without an attempt row.
	if len(jobsRepo.attempts) != 2 {
		t.Fatalf("expected two attempts (ghost skipped), got %+v", jobsRepo.attempts)
	}
	for _, a := range jobsRepo.attempts {
		if a.ProviderID == "ghost" {
			t.Fatalf("the unregistered ghost route must not record an attempt, got %+v", jobsRepo.attempts)
		}
	}
	if jobsRepo.attempts[0].ProviderID != "mock" || jobsRepo.attempts[1].ProviderID != "bfl" {
		t.Fatalf("expected attempts [mock, bfl] (ghost skipped), got %+v", jobsRepo.attempts)
	}
	// Asset produced by bfl.
	if len(assetsRepo.stored) != 1 || assetsRepo.stored[0].ProviderID == nil || *assetsRepo.stored[0].ProviderID != "bfl" {
		t.Fatalf("expected asset provenance provider_id=bfl, got %+v", assetsRepo.stored)
	}
	if len(jobsRepo.markCompleted) != 1 {
		t.Fatalf("expected job completed via the bfl fallback, got %+v", jobsRepo.markCompleted)
	}
}
