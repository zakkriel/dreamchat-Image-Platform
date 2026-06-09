// Package cost implements the pre-flight cost-estimation pipeline described
// in docs/architecture/cost-control.md §3 (steps 4–7): load the active
// price, estimate the cost of the request, and atomically hold that estimate
// against every applicable budget before the job is enqueued.
//
// Phase 4 scope: price lookup, estimation, and budget reservation. The
// reservation lifecycle's terminal steps (commit on success / release on
// failure — §3 steps 9–10) are intentionally deferred; see
// frustration_log.md (Phase 4).
package cost

import (
	"context"
	"errors"
	"log/slog"
	"math/big"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/zakkriel/drchat-image-platform/internal/db/dbgen"
	"github.com/zakkriel/drchat-image-platform/internal/ids"
)

// Sentinel errors the jobs layer maps to 422 responses. They are the public
// contract of a failed pre-flight; the handler keys its status code off them.
var (
	ErrNoPriceEntry   = errors.New("cost: no active price entry")
	ErrBudgetExceeded = errors.New("cost: budget exceeded")
)

const (
	ReasonNoPriceEntry   = "no_price_entry"
	ReasonBudgetExceeded = "budget_exceeded"

	statusReserved = "reserved"
	statusFailed   = "failed"

	// supportedUnitType is the only price unit Phase 4 can turn into an
	// estimate. Any other unit is treated as unusable → no_price_entry.
	supportedUnitType = "image"

	defaultCurrency = "USD"
)

// ReserveInput is everything the pipeline needs to price and reserve a job.
type ReserveInput struct {
	JobID         string
	TenantID      string
	TokenID       string
	WorldID       string
	UserID        string
	ProviderID    string
	ModelID       string
	OperationType string
	Units         int32
}

// Reservation is the outcome of a pre-flight. On success Status is
// "reserved"; on a denied request Status is "failed" with FailureReason set
// to one of the Reason* constants.
type Reservation struct {
	ID              string
	Status          string
	FailureReason   string
	EstimatedAmount pgtype.Numeric
	ReservedAmount  pgtype.Numeric
	Currency        string
	// EstimateUSD is the textual form of EstimatedAmount for the API
	// response (e.g. "0.0100"). Empty when no price was found.
	EstimateUSD string
}

// Failed reports whether the reservation denied the request.
func (r Reservation) Failed() bool { return r.Status == statusFailed }

// Reserver is the jobs-facing interface. It runs inside the caller's
// transaction so the reservation row, the budget increments, and the job row
// commit (or roll back) together.
type Reserver interface {
	Reserve(ctx context.Context, tx pgx.Tx, in ReserveInput) (Reservation, error)
}

// Service is the default Reserver backed by Postgres.
type Service struct {
	logger *slog.Logger
}

func NewService(logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{logger: logger}
}

// Reserve loads the price, computes the estimate, holds it against every
// applicable budget, and inserts the cost_reservations row. The job row this
// reservation references must already exist in tx (FK).
func (s *Service) Reserve(ctx context.Context, tx pgx.Tx, in ReserveInput) (Reservation, error) {
	q := dbgen.New(tx)
	reservationID := ids.NewCostReservationID()

	est, err := q.EstimateOperationCost(ctx, dbgen.EstimateOperationCostParams{
		Units:         in.Units,
		ProviderID:    in.ProviderID,
		ModelID:       in.ModelID,
		OperationType: in.OperationType,
	})
	noPrice := false
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		noPrice = true
	case err != nil:
		return Reservation{}, err
	case est.UnitType != supportedUnitType:
		// Correction 6: an unsupported unit is unusable, not a 501. Log it
		// and fail closed as no_price_entry.
		s.logger.LogAttrs(ctx, slog.LevelWarn, "cost_unsupported_unit_type",
			slog.String("provider_id", in.ProviderID),
			slog.String("model_id", in.ModelID),
			slog.String("operation_type", in.OperationType),
			slog.String("unit_type", est.UnitType),
		)
		noPrice = true
	}

	if noPrice {
		return s.insertFailed(ctx, q, in, reservationID, ReasonNoPriceEntry, zeroNumeric(), defaultCurrency, "")
	}

	held, err := s.reserveBudgets(ctx, tx, in, est.EstimatedAmount)
	if err != nil {
		return Reservation{}, err
	}
	if !held {
		return s.insertFailed(ctx, q, in, reservationID, ReasonBudgetExceeded, est.EstimatedAmount, est.Currency, est.EstimatedText)
	}

	row, err := q.InsertCostReservation(ctx, dbgen.InsertCostReservationParams{
		ID:              reservationID,
		GenerationJobID: in.JobID,
		TenantID:        in.TenantID,
		EstimatedAmount: est.EstimatedAmount,
		ReservedAmount:  est.EstimatedAmount,
		Currency:        est.Currency,
		Status:          statusReserved,
	})
	if err != nil {
		return Reservation{}, err
	}
	return Reservation{
		ID:              row.ID,
		Status:          statusReserved,
		EstimatedAmount: est.EstimatedAmount,
		ReservedAmount:  est.EstimatedAmount,
		Currency:        est.Currency,
		EstimateUSD:     est.EstimatedText,
	}, nil
}

func (s *Service) insertFailed(ctx context.Context, q *dbgen.Queries, in ReserveInput, reservationID, reason string, estimated pgtype.Numeric, currency, estimateText string) (Reservation, error) {
	r := reason
	row, err := q.InsertCostReservation(ctx, dbgen.InsertCostReservationParams{
		ID:              reservationID,
		GenerationJobID: in.JobID,
		TenantID:        in.TenantID,
		EstimatedAmount: estimated,
		ReservedAmount:  zeroNumeric(),
		Currency:        currency,
		Status:          statusFailed,
		FailureReason:   &r,
	})
	if err != nil {
		return Reservation{}, err
	}
	return Reservation{
		ID:              row.ID,
		Status:          statusFailed,
		FailureReason:   reason,
		EstimatedAmount: estimated,
		ReservedAmount:  zeroNumeric(),
		Currency:        currency,
		EstimateUSD:     estimateText,
	}, nil
}

// reserveBudgets holds `amount` against the tenant budget(s) plus the
// narrowest applicable scope, all-or-nothing. It runs in a savepoint so a
// denial rolls back any partial increments while the outer transaction still
// commits the failed job + reservation for auditability.
//
// Returns (true, nil) when every applicable budget permitted the hold,
// (false, nil) when a budget denied it (budget_exceeded), and a non-nil
// error only on an infrastructure failure.
func (s *Service) reserveBudgets(ctx context.Context, tx pgx.Tx, in ReserveInput, amount pgtype.Numeric) (bool, error) {
	q := dbgen.New(tx)
	all, err := q.ListBudgetsForReservation(ctx, dbgen.ListBudgetsForReservationParams{
		TenantID: in.TenantID,
		TokenID:  in.TokenID,
		WorldID:  in.WorldID,
		UserID:   in.UserID,
	})
	if err != nil {
		return false, err
	}
	toEnforce := selectBudgets(all)
	if len(toEnforce) == 0 {
		return true, nil
	}

	sp, err := tx.Begin(ctx)
	if err != nil {
		return false, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = sp.Rollback(ctx)
		}
	}()
	spq := dbgen.New(sp)

	for _, b := range toEnforce {
		switch b.Status {
		case "exceeded":
			return false, nil
		case "paused":
			// Recording only: hold against it but never deny.
			if _, err := spq.ReservePausedBudget(ctx, dbgen.ReservePausedBudgetParams{Amount: amount, ID: b.ID}); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					continue // status raced away from paused; don't deny
				}
				return false, err
			}
		default: // active
			if _, err := spq.ReserveActiveBudget(ctx, dbgen.ReserveActiveBudgetParams{Amount: amount, ID: b.ID}); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return false, nil // would exceed the limit
				}
				return false, err
			}
		}
	}

	if err := sp.Commit(ctx); err != nil {
		return false, err
	}
	committed = true
	return true, nil
}

// selectBudgets returns the tenant-scope budgets plus the narrowest
// applicable narrower scope (token, then world, then user). Both the tenant
// budget and the chosen narrower budget must permit a reservation.
func selectBudgets(all []dbgen.ListBudgetsForReservationRow) []dbgen.ListBudgetsForReservationRow {
	var tenant, token, world, user []dbgen.ListBudgetsForReservationRow
	for _, b := range all {
		switch b.ScopeType {
		case "tenant":
			tenant = append(tenant, b)
		case "token":
			token = append(token, b)
		case "world":
			world = append(world, b)
		case "user":
			user = append(user, b)
		}
	}
	out := append([]dbgen.ListBudgetsForReservationRow(nil), tenant...)
	switch {
	case len(token) > 0:
		out = append(out, token...)
	case len(world) > 0:
		out = append(out, world...)
	case len(user) > 0:
		out = append(out, user...)
	}
	return out
}

func zeroNumeric() pgtype.Numeric {
	return pgtype.Numeric{Int: big.NewInt(0), Exp: 0, Valid: true}
}
