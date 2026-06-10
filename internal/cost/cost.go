// Package cost implements the cost-control pipeline described in
// docs/architecture/cost-control.md §3.
//
//   - Pre-flight (steps 4–7): load the active price, estimate the cost, and
//     atomically hold that estimate against every applicable budget before the
//     job is enqueued. See Service.Reserve.
//   - Terminal lifecycle (steps 9–10): commit the hold to spend on job
//     success, or release it back on terminal failure. See Lifecycle.
//
// Every budget increment made at reserve time is recorded in
// cost_reservation_budget_holds so the terminal transition reverses exactly
// the rows that were credited — never a broad update by tenant/scope.
package cost

import (
	"context"
	"errors"
	"log/slog"
	"math/big"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

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

	statusReserved  = "reserved"
	statusCommitted = "committed"
	statusReleased  = "released"
	statusFailed    = "failed"

	// supportedUnitType is the only price unit Phase 4 can turn into an
	// estimate. Any other unit is treated as unusable → no_price_entry.
	supportedUnitType = "image"

	defaultCurrency = "USD"

	// Cost-event statuses written by the terminal lifecycle.
	costEventSucceeded = "succeeded"
	costEventFailed    = "failed"

	operationTextToImage = "text_to_image"
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
// applicable budget, and records the cost_reservations row plus a hold row per
// budget credited. The job row this reservation references must already exist
// in tx (FK).
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

	// Insert the reservation as `reserved` first so the budget holds can FK to
	// it. If the budget hold is denied we flip it to `failed` (the savepoint
	// rolls back the holds + increments it made).
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

	held, err := s.reserveBudgets(ctx, tx, in, est.EstimatedAmount, reservationID)
	if err != nil {
		return Reservation{}, err
	}
	if !held {
		reason := ReasonBudgetExceeded
		if err := q.MarkReservationBudgetExceeded(ctx, dbgen.MarkReservationBudgetExceededParams{
			ID:            reservationID,
			FailureReason: &reason,
		}); err != nil {
			return Reservation{}, err
		}
		return Reservation{
			ID:              reservationID,
			Status:          statusFailed,
			FailureReason:   reason,
			EstimatedAmount: est.EstimatedAmount,
			ReservedAmount:  zeroNumeric(),
			Currency:        est.Currency,
			EstimateUSD:     est.EstimatedText,
		}, nil
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
// narrowest applicable scope, all-or-nothing, and records a hold row per
// budget credited (so commit/release can reverse exactly these). It runs in a
// savepoint so a denial rolls back any partial increments and holds while the
// outer transaction still commits the failed job + reservation for
// auditability.
//
// Returns (true, nil) when every applicable budget permitted the hold,
// (false, nil) when a budget denied it (budget_exceeded), and a non-nil
// error only on an infrastructure failure.
func (s *Service) reserveBudgets(ctx context.Context, tx pgx.Tx, in ReserveInput, amount pgtype.Numeric, reservationID string) (bool, error) {
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
		// Record the hold so the terminal transition reverses exactly this row.
		if err := spq.InsertBudgetHold(ctx, dbgen.InsertBudgetHoldParams{
			ID:                ids.NewBudgetHoldID(),
			CostReservationID: reservationID,
			CostBudgetID:      b.ID,
			ReservedAmount:    amount,
		}); err != nil {
			return false, err
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

// nullNumeric is an explicit SQL NULL for a numeric column.
func nullNumeric() pgtype.Numeric { return pgtype.Numeric{Valid: false} }

// ---------------------------------------------------------------------------
// Terminal lifecycle (docs/architecture/cost-control.md §3 steps 9–10)
// ---------------------------------------------------------------------------

// Finalizer transitions a job's reservation to its terminal state. Both
// methods are idempotent: the reservation status guards the budget movement so
// a retry after a partial failure never double-moves an amount.
type Finalizer interface {
	// Commit moves the held estimate from reserved → spent (job succeeded).
	Commit(ctx context.Context, jobID string) error
	// Release returns the held estimate to reserved → available (job failed).
	Release(ctx context.Context, jobID string) error
}

// Lifecycle is the Postgres-backed Finalizer. It owns its own pool because the
// worker runs outside any request transaction.
type Lifecycle struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

func NewLifecycle(pool *pgxpool.Pool, logger *slog.Logger) *Lifecycle {
	if logger == nil {
		logger = slog.Default()
	}
	return &Lifecycle{pool: pool, logger: logger}
}

// Commit transitions reserved → committed for the job's reservation, moves
// each held amount from reserved to spent on its budget, stamps the job's
// actual_cost_usd, and finalizes the cost event. A no-op when the reservation
// is not in `reserved` (already committed/released/failed-preflight).
func (l *Lifecycle) Commit(ctx context.Context, jobID string) error {
	return l.finalize(ctx, jobID, statusCommitted)
}

// Release transitions reserved → released for the job's reservation and
// returns each held amount to its budget's reserved pool (spent untouched).
// A no-op when the reservation is not in `reserved`.
func (l *Lifecycle) Release(ctx context.Context, jobID string) error {
	return l.finalize(ctx, jobID, statusReleased)
}

func (l *Lifecycle) finalize(ctx context.Context, jobID, target string) error {
	tx, err := l.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	q := dbgen.New(tx)

	var (
		reservationID string
		estimated     pgtype.Numeric
		tenantID      string
	)
	noop := false
	switch target {
	case statusCommitted:
		row, err := q.CommitReservationForJob(ctx, jobID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			noop = true // not reserved → idempotent no-op
		case err != nil:
			return err
		default:
			reservationID, estimated, tenantID = row.ID, row.EstimatedAmount, row.TenantID
		}
	case statusReleased:
		row, err := q.ReleaseReservationForJob(ctx, jobID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			noop = true
		case err != nil:
			return err
		default:
			reservationID, estimated, tenantID = row.ID, row.EstimatedAmount, row.TenantID
		}
	default:
		return errors.New("cost: invalid finalize target " + target)
	}

	if noop {
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		committed = true
		return nil
	}

	holds, err := q.ListReservedBudgetHolds(ctx, reservationID)
	if err != nil {
		return err
	}
	for _, h := range holds {
		if target == statusCommitted {
			if err := q.CommitBudgetHold(ctx, dbgen.CommitBudgetHoldParams{Amount: h.ReservedAmount, ID: h.CostBudgetID}); err != nil {
				return err
			}
		} else {
			if err := q.ReleaseBudgetHold(ctx, dbgen.ReleaseBudgetHoldParams{Amount: h.ReservedAmount, ID: h.CostBudgetID}); err != nil {
				return err
			}
		}
		if err := q.MarkBudgetHoldStatus(ctx, dbgen.MarkBudgetHoldStatusParams{Status: target, ID: h.ID}); err != nil {
			return err
		}
	}

	// Cost-event + job actual: on commit, actual = estimate; on release, none.
	if target == statusCommitted {
		if err := q.SetGenerationJobActualCost(ctx, dbgen.SetGenerationJobActualCostParams{ActualCostUsd: estimated, ID: jobID}); err != nil {
			return err
		}
		if err := l.finalizeCostEvent(ctx, q, jobID, tenantID, estimated, estimated, costEventSucceeded); err != nil {
			return err
		}
	} else {
		if err := l.finalizeCostEvent(ctx, q, jobID, tenantID, estimated, nullNumeric(), costEventFailed); err != nil {
			return err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}
	committed = true
	return nil
}

// finalizeCostEvent stamps estimated/actual/status onto the job's latest cost
// event (the one the worker wrote for the terminal attempt). If none exists it
// writes a fallback row so the cost ledger is never silently missing.
func (l *Lifecycle) finalizeCostEvent(ctx context.Context, q *dbgen.Queries, jobID, tenantID string, estimated, actual pgtype.Numeric, status string) error {
	job := jobID
	n, err := q.UpdateLatestJobCostEvent(ctx, dbgen.UpdateLatestJobCostEventParams{
		EstimatedCostUsd: estimated,
		ActualCostUsd:    actual,
		Status:           status,
		JobID:            &job,
	})
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	return q.InsertFinalizerCostEvent(ctx, dbgen.InsertFinalizerCostEventParams{
		ID:               ids.NewCostEventID(),
		TenantID:         tenantID,
		JobID:            &job,
		Operation:        operationTextToImage,
		EstimatedCostUsd: estimated,
		ActualCostUsd:    actual,
		Status:           status,
	})
}
