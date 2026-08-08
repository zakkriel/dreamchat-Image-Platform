package jobs

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/zakkriel/drchat-image-platform/internal/db/dbgen"
)

// JobFromGenerationRow (rowToJob) is a pure column mapping — no DB needed —
// so the visual_identity_id / cost_estimate_usd / actual_cost_usd mapping is
// unit-testable directly against a hand-built dbgen row.
func TestJobFromGenerationRowMapsIdentityAndCost(t *testing.T) {
	identityID := "vi_hero"
	var estimate, actual pgtype.Numeric
	if err := estimate.Scan("0.0500"); err != nil {
		t.Fatal(err)
	}
	if err := actual.Scan("0.0430"); err != nil {
		t.Fatal(err)
	}
	row := dbgen.GenerationJob{
		ID:               "job_1",
		TenantID:         "tenant_a",
		JobType:          "generation",
		Status:           "completed",
		VisualIdentityID: &identityID,
		CostEstimateUsd:  estimate,
		ActualCostUsd:    actual,
	}
	job := JobFromGenerationRow(row)
	if job.VisualIdentityID == nil || *job.VisualIdentityID != identityID {
		t.Fatalf("expected VisualIdentityID=%q, got %v", identityID, job.VisualIdentityID)
	}
	if job.CostEstimateUSD == nil || *job.CostEstimateUSD != "0.0500" {
		t.Fatalf("expected CostEstimateUSD=0.0500, got %v", job.CostEstimateUSD)
	}
	if job.ActualCostUSD == nil || *job.ActualCostUSD != "0.0430" {
		t.Fatalf("expected ActualCostUSD=0.0430, got %v", job.ActualCostUSD)
	}
}

// A job that hasn't been estimated/committed (e.g. a fresh queued row before
// reservation, or a failed-preflight row) must surface nil costs, not "0" —
// callers must not read an absent estimate as a free job.
func TestJobFromGenerationRowNilCostsWhenUnset(t *testing.T) {
	row := dbgen.GenerationJob{
		ID:       "job_2",
		TenantID: "tenant_a",
		JobType:  "generation",
		Status:   "queued",
	}
	job := JobFromGenerationRow(row)
	if job.VisualIdentityID != nil {
		t.Fatalf("expected nil VisualIdentityID, got %v", *job.VisualIdentityID)
	}
	if job.CostEstimateUSD != nil {
		t.Fatalf("expected nil CostEstimateUSD, got %v", *job.CostEstimateUSD)
	}
	if job.ActualCostUSD != nil {
		t.Fatalf("expected nil ActualCostUSD, got %v", *job.ActualCostUSD)
	}
}
