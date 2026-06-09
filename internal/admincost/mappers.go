package admincost

import (
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/zakkriel/drchat-image-platform/internal/db/dbgen"
)

func tsTime(t pgtype.Timestamptz) time.Time {
	if !t.Valid {
		return time.Time{}
	}
	return t.Time
}

func tsPtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	out := t.Time
	return &out
}

// numericPtr formats a nullable NUMERIC as its decimal string, or nil for SQL
// NULL. pgtype.Numeric.Value() returns the canonical text form.
func numericPtr(n pgtype.Numeric) *string {
	if !n.Valid {
		return nil
	}
	v, err := n.Value()
	if err != nil {
		return nil
	}
	if s, ok := v.(string); ok {
		return &s
	}
	return nil
}

func priceFromInsert(r dbgen.InsertProviderModelPriceRow) PriceEntry {
	return PriceEntry{
		ID: r.ID, ProviderID: r.ProviderID, ModelID: r.ModelID,
		OperationType: r.OperationType, UnitType: r.UnitType,
		PricePerUnit: r.PricePerUnit, Currency: r.Currency,
		EffectiveFrom: tsTime(r.EffectiveFrom), EffectiveTo: tsPtr(r.EffectiveTo),
		IsActive: r.IsActive, Source: r.Source, Notes: r.Notes,
		CreatedAt: tsTime(r.CreatedAt), UpdatedAt: tsTime(r.UpdatedAt),
	}
}

func priceFromList(r dbgen.ListProviderModelPricesRow) PriceEntry {
	return PriceEntry{
		ID: r.ID, ProviderID: r.ProviderID, ModelID: r.ModelID,
		OperationType: r.OperationType, UnitType: r.UnitType,
		PricePerUnit: r.PricePerUnit, Currency: r.Currency,
		EffectiveFrom: tsTime(r.EffectiveFrom), EffectiveTo: tsPtr(r.EffectiveTo),
		IsActive: r.IsActive, Source: r.Source, Notes: r.Notes,
		CreatedAt: tsTime(r.CreatedAt), UpdatedAt: tsTime(r.UpdatedAt),
	}
}

func priceFromGet(r dbgen.GetProviderModelPriceRow) PriceEntry {
	return PriceEntry{
		ID: r.ID, ProviderID: r.ProviderID, ModelID: r.ModelID,
		OperationType: r.OperationType, UnitType: r.UnitType,
		PricePerUnit: r.PricePerUnit, Currency: r.Currency,
		EffectiveFrom: tsTime(r.EffectiveFrom), EffectiveTo: tsPtr(r.EffectiveTo),
		IsActive: r.IsActive, Source: r.Source, Notes: r.Notes,
		CreatedAt: tsTime(r.CreatedAt), UpdatedAt: tsTime(r.UpdatedAt),
	}
}

func priceFromUpdate(r dbgen.UpdateProviderModelPriceRow) PriceEntry {
	return PriceEntry{
		ID: r.ID, ProviderID: r.ProviderID, ModelID: r.ModelID,
		OperationType: r.OperationType, UnitType: r.UnitType,
		PricePerUnit: r.PricePerUnit, Currency: r.Currency,
		EffectiveFrom: tsTime(r.EffectiveFrom), EffectiveTo: tsPtr(r.EffectiveTo),
		IsActive: r.IsActive, Source: r.Source, Notes: r.Notes,
		CreatedAt: tsTime(r.CreatedAt), UpdatedAt: tsTime(r.UpdatedAt),
	}
}

func budgetFromInsert(r dbgen.InsertCostBudgetRow) Budget {
	return Budget{
		ID: r.ID, TenantID: r.TenantID, ScopeType: r.ScopeType, ScopeID: r.ScopeID,
		Period: r.Period, LimitAmount: r.LimitAmount, ReservedAmount: r.ReservedAmount,
		SpentAmount: r.SpentAmount, Currency: r.Currency, Status: r.Status,
		CreatedAt: tsTime(r.CreatedAt), UpdatedAt: tsTime(r.UpdatedAt),
	}
}

func budgetFromList(r dbgen.ListCostBudgetsRow) Budget {
	return Budget{
		ID: r.ID, TenantID: r.TenantID, ScopeType: r.ScopeType, ScopeID: r.ScopeID,
		Period: r.Period, LimitAmount: r.LimitAmount, ReservedAmount: r.ReservedAmount,
		SpentAmount: r.SpentAmount, Currency: r.Currency, Status: r.Status,
		CreatedAt: tsTime(r.CreatedAt), UpdatedAt: tsTime(r.UpdatedAt),
	}
}

func budgetFromUpdate(r dbgen.UpdateCostBudgetRow) Budget {
	return Budget{
		ID: r.ID, TenantID: r.TenantID, ScopeType: r.ScopeType, ScopeID: r.ScopeID,
		Period: r.Period, LimitAmount: r.LimitAmount, ReservedAmount: r.ReservedAmount,
		SpentAmount: r.SpentAmount, Currency: r.Currency, Status: r.Status,
		CreatedAt: tsTime(r.CreatedAt), UpdatedAt: tsTime(r.UpdatedAt),
	}
}

func reservationFromRow(r dbgen.ListCostReservationsAdminRow) ReservationRow {
	return ReservationRow{
		ID: r.ID, GenerationJobID: r.GenerationJobID, TenantID: r.TenantID,
		EstimatedAmount: r.EstimatedAmount, ReservedAmount: r.ReservedAmount,
		ActualAmount: numericPtr(r.ActualAmount), Currency: r.Currency,
		Status: r.Status, FailureReason: r.FailureReason,
		CreatedAt: tsTime(r.CreatedAt), UpdatedAt: tsTime(r.UpdatedAt),
	}
}
