package jobs

import "testing"

// A true-preview job makes two provider calls in one run, so its hold covers
// both phases.
func TestWorstCaseBillableUnitsCoversPreviewAndFinal(t *testing.T) {
	units := worstCaseBillableUnits(CreateAndEnqueueParams{
		InputPayload: map[string]any{
			"delivery_mode":      "preview_first",
			"preview_capability": "true_preview",
		},
		Units: 1,
	})
	if units != 2 {
		t.Fatalf("preview-first units = %d, want 2", units)
	}
}

// Retries and same-price fallback routes are failure paths: they are billed
// through reservation-scoped cost events, never pre-charged onto the hold.
func TestWorstCaseBillableUnitsExcludesRetriesAndFallbacks(t *testing.T) {
	units := worstCaseBillableUnits(CreateAndEnqueueParams{Units: 1})
	if units != 1 {
		t.Fatalf("single-image units = %d, want 1 (retry cap must not inflate the hold)", units)
	}
}

// A pack reserves one operation per cell it actually intends to generate, so a
// partial pack never holds budget for roles that are already satisfied.
func TestWorstCaseBillableUnitsPricesOnlyMissingPackCells(t *testing.T) {
	units := worstCaseBillableUnits(CreateAndEnqueueParams{
		Units: 10,
		AssetPack: &AssetPackSpec{
			MissingRoles: []string{"neutral", "warm", "angry"},
		},
	})
	if units != 3 {
		t.Fatalf("pack units = %d, want 3", units)
	}
}

func TestWorstCaseBillableUnitsSaturatesInt32(t *testing.T) {
	maxInt32 := int32(^uint32(0) >> 1)
	units := worstCaseBillableUnits(CreateAndEnqueueParams{
		InputPayload: map[string]any{
			"delivery_mode":      "preview_first",
			"preview_capability": "true_preview",
		},
		Units: maxInt32,
	})
	if units != maxInt32 {
		t.Fatalf("worst-case units wrapped: %d", units)
	}
}
