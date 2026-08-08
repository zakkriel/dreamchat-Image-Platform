package jobs

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	"github.com/zakkriel/drchat-image-platform/internal/providers"
)

func TestReportedProviderCostNormalizesExplicitAndMetadataValues(t *testing.T) {
	explicit := " 0.0125 "
	actual, currency, ok := reportedProviderCost(providers.ProviderGenerateResult{
		ActualCostUSD: &explicit,
		CostCurrency:  "USD",
	})
	if !ok || actual != "0.0125" || currency != "USD" {
		t.Fatalf("explicit cost = %q/%q/%v", actual, currency, ok)
	}
	fallback := providers.ProviderGenerateResult{
		Metadata: map[string]any{"actual_cost_usd": json.Number("0.004")},
	}
	actual, currency, ok = reportedProviderCost(fallback)
	if !ok || actual != "0.004" || currency != "USD" {
		t.Fatalf("metadata cost = %q/%q/%v", actual, currency, ok)
	}
	invalid := "NaN"
	if _, _, ok := reportedProviderCost(providers.ProviderGenerateResult{ActualCostUSD: &invalid}); ok {
		t.Fatal("NaN must not be treated as provider cost")
	}
	tooLarge := "10000000000"
	if _, _, ok := reportedProviderCost(providers.ProviderGenerateResult{ActualCostUSD: &tooLarge}); ok {
		t.Fatal("NUMERIC(14,4)-overflowing cost must be ignored")
	}
	if _, currency, ok := reportedProviderCost(providers.ProviderGenerateResult{ActualCostUSD: &explicit, CostCurrency: " usd "}); !ok || currency != "USD" {
		t.Fatalf("currency normalization = %q/%v", currency, ok)
	}
	euro := "0.01"
	if reportedProviderCostPtr(providers.ProviderGenerateResult{ActualCostUSD: &euro, CostCurrency: "EUR"}) != nil {
		t.Fatal("non-USD actual must not enter actual_cost_usd")
	}
}

func TestProviderImageDimensionsUsesDecodedBytes(t *testing.T) {
	width, height, err := providerImageDimensions(providers.ProviderImage{
		Bytes:  tinyPNGBytes(),
		Width:  1,
		Height: 1,
	})
	if err != nil {
		t.Fatalf("decode dimensions: %v", err)
	}
	if width != 8 || height != 8 {
		t.Fatalf("decoded dimensions = %dx%d, want 8x8 despite stale metadata", width, height)
	}
	if _, _, err := providerImageDimensions(providers.ProviderImage{Bytes: []byte("not an image"), Width: 100, Height: 100}); err == nil {
		t.Fatal("undecodable bytes must not be accepted from claimed metadata")
	}
}

func TestMaxMegapixelsForWorkerRejectsMalformedPayload(t *testing.T) {
	if value, err := maxMegapixelsForWorker(Job{InputPayload: map[string]any{"max_megapixels": "1.5"}}); err != nil || math.Abs(value-1.5) > 1e-9 {
		t.Fatalf("valid string max_megapixels = %v/%v", value, err)
	}
	if _, err := maxMegapixelsForWorker(Job{InputPayload: map[string]any{"max_megapixels": "not-a-number"}}); err == nil {
		t.Fatal("malformed max_megapixels must fail closed")
	}
	if _, err := maxMegapixelsForWorker(Job{InputPayload: map[string]any{"max_megapixels": 4.01}}); err == nil {
		t.Fatal("max_megapixels above the platform ceiling must fail closed")
	}
}

func TestBillableMetadataIsStructuredAndExtensible(t *testing.T) {
	metadata := billableMetadata("pack_cell", map[string]any{"variant_key": "angry"})
	var decoded map[string]any
	if err := json.Unmarshal(metadata, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["billable_operation"] != "pack_cell" || decoded["variant_key"] != "angry" {
		t.Fatalf("metadata = %#v", decoded)
	}
}
func TestWorkerIgnoresFirstDeliveryOfAlreadyRunningJob(t *testing.T) {
	repo := newFakeJobsRepo()
	if _, err := repo.Insert(context.Background(), InsertParams{
		ID: "job_duplicate", TenantID: "tenant_a", JobType: "artifact",
		InputPayload: map[string]any{"description": "duplicate"},
	}); err != nil {
		t.Fatal(err)
	}
	repo.mu.Lock()
	job := repo.jobs["job_duplicate"]
	job.Status = "running"
	repo.jobs[job.ID] = job
	repo.mu.Unlock()

	provider := &countingProvider{}
	worker := &Worker{
		Jobs: repo, Assets: &fakeAssetsRepo{}, Storage: &fakeStorage{},
		Providers: testRegistry(provider),
	}
	if err := worker.Process(context.Background(), job.ID, 0); err != nil {
		t.Fatalf("duplicate delivery: %v", err)
	}
	if got := provider.callCount(); got != 0 {
		t.Fatalf("first delivery of a running job must not call provider, got %d calls", got)
	}
	if repo.markRunningCalls != 0 {
		t.Fatalf("running duplicate must not attempt a new claim, got %d claims", repo.markRunningCalls)
	}
}
func TestWorkerDoesNotReleaseWhenTerminalMarkFails(t *testing.T) {
	repo := newFakeJobsRepo()
	if _, err := repo.Insert(context.Background(), InsertParams{
		ID: "job_mark_failed_error", TenantID: "tenant_a", JobType: "artifact",
		InputPayload: map[string]any{"description": "failure"},
	}); err != nil {
		t.Fatal(err)
	}
	repo.failNextMarkFailed = true
	fin := &fakeFinalizer{}
	worker := &Worker{
		Jobs: repo, Assets: &fakeAssetsRepo{}, Storage: &fakeStorage{},
		Providers: testRegistry(errorProvider{}), Finalizer: fin,
	}
	if err := worker.Process(context.Background(), "job_mark_failed_error", int32(MaxAttempts-1)); err == nil {
		t.Fatal("expected provider error on final attempt")
	}
	if len(fin.released) != 0 {
		t.Fatalf("reservation must not release when MarkFailed fails, got %+v", fin.released)
	}
	if repo.jobs["job_mark_failed_error"].Status != "running" {
		t.Fatalf("failed terminal CAS must leave active job unchanged, got %q", repo.jobs["job_mark_failed_error"].Status)
	}
}

func TestWorkerCostEventsCarryReservationID(t *testing.T) {
	repo := newFakeJobsRepo()
	if _, err := repo.Insert(context.Background(), InsertParams{
		ID: "job_wave3_reservation", TenantID: "tenant_a", JobType: "artifact",
		InputPayload: map[string]any{"description": "reservation scoped"},
	}); err != nil {
		t.Fatal(err)
	}
	reservationID := "resv_wave3"
	repo.mu.Lock()
	job := repo.jobs["job_wave3_reservation"]
	job.CostReservationID = &reservationID
	repo.jobs[job.ID] = job
	repo.mu.Unlock()

	assetsRepo := &fakeAssetsRepo{}
	repo.assets = assetsRepo
	worker := &Worker{
		Jobs: repo, Assets: assetsRepo, Storage: &fakeStorage{},
		Providers: testRegistry(&countingProvider{}),
	}
	if err := worker.Process(context.Background(), job.ID, 0); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(repo.costEvents) != 1 || repo.costEvents[0].CostReservationID == nil || *repo.costEvents[0].CostReservationID != reservationID {
		t.Fatalf("cost event reservation attribution = %+v", repo.costEvents)
	}
}
