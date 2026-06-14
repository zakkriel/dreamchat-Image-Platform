// Package admincost implements the minimum admin cost surface
// (docs/architecture/admin-control-surface.md §"Cost controls"): the price
// book, cost budgets, and a read-only cost-reservation list. Every
// state-changing call writes an audit_events row in the same transaction as
// the mutation — if the audit insert fails, the mutation fails.
//
// Money is carried as decimal strings end-to-end (NUMERIC in Postgres) so the
// surface never rounds through a float.
package admincost

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zakkriel/drchat-image-platform/internal/db/dbgen"
	"github.com/zakkriel/drchat-image-platform/internal/ids"
)

// Sentinel errors mapped to HTTP status by the handler.
var (
	ErrNotFound = errors.New("admincost: resource not found")
	ErrInvalid  = errors.New("admincost: invalid request")
)

const (
	defaultCurrency        = "USD"
	defaultBudgetStatus    = "active"
	defaultReservationRows = 100
	maxReservationRows     = 1000

	resourcePrice  = "provider_model_price"
	resourceBudget = "cost_budget"

	eventPriceCreated  = "admin.price_book.created"
	eventPriceUpdated  = "admin.price_book.updated"
	eventBudgetCreated = "admin.cost_budget.created"
	eventBudgetUpdated = "admin.cost_budget.updated"
)

var (
	validOperationTypes   = set("text_to_image", "image_to_image", "upscale", "variant_pack", "edit")
	validUnitTypes        = set("image", "megapixel", "second", "credit", "request")
	validScopeTypes       = set("tenant", "token", "world", "user")
	validBudgetPeriods    = set("daily", "monthly")
	validBudgetStatuses   = set("active", "paused", "exceeded")
	validReservationStati = set("reserved", "committed", "released", "failed")
)

// Actor identifies the admin token performing a mutation, for the audit row.
type Actor struct {
	TokenID   string
	TenantID  string
	RequestID string
}

// Service is the Postgres-backed admin cost surface.
type Service struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
	now    func() time.Time
}

func NewService(pool *pgxpool.Pool, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{pool: pool, logger: logger, now: time.Now}
}

// ---------------------------------------------------------------------------
// Domain views (money as decimal strings)
// ---------------------------------------------------------------------------

type PriceEntry struct {
	ID            string     `json:"id"`
	ProviderID    string     `json:"provider_id"`
	ModelID       string     `json:"model_id"`
	OperationType string     `json:"operation_type"`
	UnitType      string     `json:"unit_type"`
	PricePerUnit  string     `json:"price_per_unit"`
	Currency      string     `json:"currency"`
	EffectiveFrom time.Time  `json:"effective_from"`
	EffectiveTo   *time.Time `json:"effective_to"`
	IsActive      bool       `json:"is_active"`
	Source        *string    `json:"source"`
	Notes         *string    `json:"notes"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type Budget struct {
	ID             string `json:"id"`
	TenantID       string `json:"tenant_id"`
	ScopeType      string `json:"scope_type"`
	ScopeID        string `json:"scope_id"`
	Period         string `json:"period"`
	LimitAmount    string `json:"limit_amount"`
	ReservedAmount string `json:"reserved_amount"`
	SpentAmount    string `json:"spent_amount"`
	Currency       string `json:"currency"`
	Status         string `json:"status"`
	// PeriodStart is the start of the budget's current UTC window (Phase 7C-1c).
	// The reservation path lazily advances it (zeroing spent_amount) when the
	// window has elapsed; it is otherwise read-only over the admin surface.
	PeriodStart time.Time `json:"period_start"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type ReservationRow struct {
	ID              string    `json:"id"`
	GenerationJobID string    `json:"generation_job_id"`
	TenantID        string    `json:"tenant_id"`
	EstimatedAmount string    `json:"estimated_amount"`
	ReservedAmount  string    `json:"reserved_amount"`
	ActualAmount    *string   `json:"actual_amount"`
	Currency        string    `json:"currency"`
	Status          string    `json:"status"`
	FailureReason   *string   `json:"failure_reason"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// ---------------------------------------------------------------------------
// Price book
// ---------------------------------------------------------------------------

type CreatePriceInput struct {
	ProviderID    string
	ModelID       string
	OperationType string
	UnitType      string
	PricePerUnit  string
	Currency      string
	Source        *string
	Notes         *string
}

// CreatePrice inserts a new active price entry, superseding the previous active
// entry for the same (provider × model × operation_type) in one transaction.
func (s *Service) CreatePrice(ctx context.Context, actor Actor, in CreatePriceInput) (PriceEntry, error) {
	if in.ProviderID == "" || in.ModelID == "" || in.OperationType == "" || in.UnitType == "" || in.PricePerUnit == "" {
		return PriceEntry{}, fieldErr("provider_id, model_id, operation_type, unit_type, and price_per_unit are required")
	}
	if !validOperationTypes[in.OperationType] {
		return PriceEntry{}, fieldErr("operation_type is not a valid value")
	}
	if !validUnitTypes[in.UnitType] {
		return PriceEntry{}, fieldErr("unit_type is not a valid value")
	}
	price, err := numericFromString(in.PricePerUnit)
	if err != nil {
		return PriceEntry{}, fieldErr("price_per_unit must be a decimal value")
	}
	currency := in.Currency
	if currency == "" {
		currency = defaultCurrency
	}

	var out PriceEntry
	err = s.inTx(ctx, func(q *dbgen.Queries) error {
		superseded, err := q.SupersedePreviousActivePrice(ctx, dbgen.SupersedePreviousActivePriceParams{
			ProviderID:    in.ProviderID,
			ModelID:       in.ModelID,
			OperationType: in.OperationType,
		})
		if err != nil {
			return err
		}
		row, err := q.InsertProviderModelPrice(ctx, dbgen.InsertProviderModelPriceParams{
			ID:            ids.NewProviderPriceID(),
			ProviderID:    in.ProviderID,
			ModelID:       in.ModelID,
			OperationType: in.OperationType,
			UnitType:      in.UnitType,
			PricePerUnit:  price,
			Currency:      currency,
			Source:        in.Source,
			Notes:         in.Notes,
		})
		if err != nil {
			return err
		}
		out = priceFromInsert(row)
		return s.writeAudit(ctx, q, actor, eventPriceCreated, resourcePrice, out.ID, actor.TenantID, map[string]any{
			"provider_id":     in.ProviderID,
			"model_id":        in.ModelID,
			"operation_type":  in.OperationType,
			"unit_type":       in.UnitType,
			"price_per_unit":  out.PricePerUnit,
			"currency":        currency,
			"superseded_rows": superseded,
		})
	})
	if err != nil {
		return PriceEntry{}, err
	}
	return out, nil
}

func (s *Service) ListPrices(ctx context.Context) ([]PriceEntry, error) {
	rows, err := dbgen.New(s.pool).ListProviderModelPrices(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]PriceEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, priceFromList(r))
	}
	return out, nil
}

func (s *Service) GetPrice(ctx context.Context, id string) (PriceEntry, error) {
	row, err := dbgen.New(s.pool).GetProviderModelPrice(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PriceEntry{}, ErrNotFound
		}
		return PriceEntry{}, err
	}
	return priceFromGet(row), nil
}

type UpdatePriceInput struct {
	SetEffectiveTo bool
	EffectiveTo    *time.Time
	IsActive       *bool
	Notes          *string
}

// UpdatePrice mutates only effective_to, is_active, and notes.
func (s *Service) UpdatePrice(ctx context.Context, actor Actor, id string, in UpdatePriceInput) (PriceEntry, error) {
	if _, err := s.GetPrice(ctx, id); err != nil {
		return PriceEntry{}, err
	}

	var effTo pgtype.Timestamptz
	if in.SetEffectiveTo && in.EffectiveTo != nil {
		effTo = pgtype.Timestamptz{Time: *in.EffectiveTo, Valid: true}
	}

	changed := map[string]any{}
	if in.SetEffectiveTo {
		changed["effective_to"] = in.EffectiveTo
	}
	if in.IsActive != nil {
		changed["is_active"] = *in.IsActive
	}
	if in.Notes != nil {
		changed["notes"] = *in.Notes
	}

	var out PriceEntry
	err := s.inTx(ctx, func(q *dbgen.Queries) error {
		row, err := q.UpdateProviderModelPrice(ctx, dbgen.UpdateProviderModelPriceParams{
			SetEffectiveTo: in.SetEffectiveTo,
			EffectiveTo:    effTo,
			IsActive:       in.IsActive,
			Notes:          in.Notes,
			ID:             id,
		})
		if err != nil {
			return err
		}
		out = priceFromUpdate(row)
		return s.writeAudit(ctx, q, actor, eventPriceUpdated, resourcePrice, out.ID, actor.TenantID, map[string]any{
			"changed": changed,
		})
	})
	if err != nil {
		return PriceEntry{}, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Cost budgets
// ---------------------------------------------------------------------------

type CreateBudgetInput struct {
	TenantID    string
	ScopeType   string
	ScopeID     string
	Period      string
	LimitAmount string
	Currency    string
	Status      string
	// PeriodStart optionally pins the start of the budget's first window. When
	// nil it defaults to the current UTC window start for the period (Phase
	// 7C-1c). reserved_amount and spent_amount remain platform-owned.
	PeriodStart *time.Time
}

// CreateBudget inserts a budget. reserved_amount and spent_amount are
// platform-owned and start at 0.
func (s *Service) CreateBudget(ctx context.Context, actor Actor, in CreateBudgetInput) (Budget, error) {
	if in.TenantID == "" || in.ScopeType == "" || in.ScopeID == "" || in.Period == "" || in.LimitAmount == "" {
		return Budget{}, fieldErr("tenant_id, scope_type, scope_id, period, and limit_amount are required")
	}
	if !validScopeTypes[in.ScopeType] {
		return Budget{}, fieldErr("scope_type must be one of tenant, token, world, user")
	}
	if !validBudgetPeriods[in.Period] {
		return Budget{}, fieldErr("period must be one of daily, monthly")
	}
	if in.ScopeType == "tenant" && in.ScopeID != in.TenantID {
		return Budget{}, fieldErr("scope_id must equal tenant_id for scope_type=tenant")
	}
	status := in.Status
	if status == "" {
		status = defaultBudgetStatus
	}
	if !validBudgetStatuses[status] {
		return Budget{}, fieldErr("status must be one of active, paused, exceeded")
	}
	limit, err := numericFromString(in.LimitAmount)
	if err != nil {
		return Budget{}, fieldErr("limit_amount must be a decimal value")
	}
	currency := in.Currency
	if currency == "" {
		currency = defaultCurrency
	}
	// period_start defaults to the current UTC window start for the period
	// (matching the lazy reset's window semantics) unless the caller pins it.
	periodStart := currentWindowStart(in.Period, s.now())
	if in.PeriodStart != nil {
		periodStart = in.PeriodStart.UTC()
	}

	var out Budget
	err = s.inTx(ctx, func(q *dbgen.Queries) error {
		row, err := q.InsertCostBudget(ctx, dbgen.InsertCostBudgetParams{
			ID:          ids.NewCostBudgetID(),
			TenantID:    in.TenantID,
			ScopeType:   in.ScopeType,
			ScopeID:     in.ScopeID,
			Period:      in.Period,
			LimitAmount: limit,
			Currency:    currency,
			Status:      status,
			PeriodStart: pgtype.Timestamptz{Time: periodStart, Valid: true},
		})
		if err != nil {
			return err
		}
		out = budgetFromInsert(row)
		return s.writeAudit(ctx, q, actor, eventBudgetCreated, resourceBudget, out.ID, out.TenantID, map[string]any{
			"scope_type":   in.ScopeType,
			"scope_id":     in.ScopeID,
			"period":       in.Period,
			"limit_amount": out.LimitAmount,
			"currency":     currency,
			"status":       status,
		})
	})
	if err != nil {
		return Budget{}, err
	}
	return out, nil
}

func (s *Service) ListBudgets(ctx context.Context) ([]Budget, error) {
	rows, err := dbgen.New(s.pool).ListCostBudgets(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Budget, 0, len(rows))
	for _, r := range rows {
		out = append(out, budgetFromList(r))
	}
	return out, nil
}

type UpdateBudgetInput struct {
	LimitAmount *string
	Status      *string
}

// UpdateBudget mutates only limit_amount and status.
func (s *Service) UpdateBudget(ctx context.Context, actor Actor, id string, in UpdateBudgetInput) (Budget, error) {
	if _, err := dbgen.New(s.pool).GetCostBudget(ctx, id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Budget{}, ErrNotFound
		}
		return Budget{}, err
	}

	var limit pgtype.Numeric
	if in.LimitAmount != nil {
		n, err := numericFromString(*in.LimitAmount)
		if err != nil {
			return Budget{}, fieldErr("limit_amount must be a decimal value")
		}
		limit = n
	}
	if in.Status != nil && !validBudgetStatuses[*in.Status] {
		return Budget{}, fieldErr("status must be one of active, paused, exceeded")
	}

	changed := map[string]any{}
	if in.LimitAmount != nil {
		changed["limit_amount"] = *in.LimitAmount
	}
	if in.Status != nil {
		changed["status"] = *in.Status
	}

	var out Budget
	err := s.inTx(ctx, func(q *dbgen.Queries) error {
		row, err := q.UpdateCostBudget(ctx, dbgen.UpdateCostBudgetParams{
			LimitAmount: limit,
			Status:      in.Status,
			ID:          id,
		})
		if err != nil {
			return err
		}
		out = budgetFromUpdate(row)
		return s.writeAudit(ctx, q, actor, eventBudgetUpdated, resourceBudget, out.ID, out.TenantID, map[string]any{
			"changed": changed,
		})
	})
	if err != nil {
		return Budget{}, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Cost reservations (read-only)
// ---------------------------------------------------------------------------

type ReservationFilter struct {
	TenantID      *string
	Status        *string
	CreatedAfter  *time.Time
	CreatedBefore *time.Time
	Limit         int
}

func (s *Service) ListReservations(ctx context.Context, f ReservationFilter) ([]ReservationRow, error) {
	if f.Status != nil && !validReservationStati[*f.Status] {
		return nil, fieldErr("status must be one of reserved, committed, released, failed")
	}
	limit := f.Limit
	if limit <= 0 {
		limit = defaultReservationRows
	}
	if limit > maxReservationRows {
		limit = maxReservationRows
	}
	params := dbgen.ListCostReservationsAdminParams{
		TenantID: f.TenantID,
		Status:   f.Status,
		RowLimit: int32(limit),
	}
	if f.CreatedAfter != nil {
		params.CreatedAfter = pgtype.Timestamptz{Time: *f.CreatedAfter, Valid: true}
	}
	if f.CreatedBefore != nil {
		params.CreatedBefore = pgtype.Timestamptz{Time: *f.CreatedBefore, Valid: true}
	}
	rows, err := dbgen.New(s.pool).ListCostReservationsAdmin(ctx, params)
	if err != nil {
		return nil, err
	}
	out := make([]ReservationRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, reservationFromRow(r))
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Internals
// ---------------------------------------------------------------------------

// inTx runs fn inside a single transaction so the mutation and its audit row
// commit (or roll back) together.
func (s *Service) inTx(ctx context.Context, fn func(q *dbgen.Queries) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	if err := fn(dbgen.New(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	committed = true
	return nil
}

func (s *Service) writeAudit(ctx context.Context, q *dbgen.Queries, actor Actor, eventType, resourceType, resourceID, tenantID string, metadata map[string]any) error {
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["request_id"] = actor.RequestID
	metadata["actor_token_id"] = actor.TokenID
	if tenantID != "" {
		metadata["tenant_id"] = tenantID
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	var tenantPtr *string
	if tenantID != "" {
		t := tenantID
		tenantPtr = &t
	}
	var actorPtr *string
	if actor.TokenID != "" {
		a := actor.TokenID
		actorPtr = &a
	}
	rt := resourceType
	rid := resourceID
	return q.InsertAuditEvent(ctx, dbgen.InsertAuditEventParams{
		ID:           ids.NewAuditEventID(),
		TenantID:     tenantPtr,
		EventType:    eventType,
		ActorTokenID: actorPtr,
		ResourceType: &rt,
		ResourceID:   &rid,
		Metadata:     raw,
	})
}

func fieldErr(msg string) error { return &fieldError{msg: msg} }

type fieldError struct{ msg string }

func (e *fieldError) Error() string { return "admincost: " + e.msg }
func (e *fieldError) Unwrap() error { return ErrInvalid }

func set(vals ...string) map[string]bool {
	m := make(map[string]bool, len(vals))
	for _, v := range vals {
		m[v] = true
	}
	return m
}

func numericFromString(s string) (pgtype.Numeric, error) {
	var n pgtype.Numeric
	if err := n.Scan(s); err != nil {
		return pgtype.Numeric{}, err
	}
	return n, nil
}

// currentWindowStart returns the start of the UTC budget window for a period
// (Phase 7C-1c). It mirrors the SQL window math used by the lazy reset so a
// freshly created budget and one that has rolled over share the same anchor:
// daily → UTC date floor, monthly → first of the current UTC month.
func currentWindowStart(period string, now time.Time) time.Time {
	u := now.UTC()
	if period == "monthly" {
		return time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}
