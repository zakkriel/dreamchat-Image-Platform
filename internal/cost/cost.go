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
	"fmt"
	"log/slog"
	"math/big"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zakkriel/drchat-image-platform/internal/db/dbgen"
	"github.com/zakkriel/drchat-image-platform/internal/ids"
	"github.com/zakkriel/drchat-image-platform/internal/telemetry"
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
		// Phase 7C-1c: lazy budget period reset. Before enforcing the limit,
		// roll this budget over to its current UTC window if the window has
		// elapsed — advancing period_start, zeroing spent_amount, and clearing
		// an `exceeded` status back to `active`. This runs inside the savepoint
		// (and thus the reservation transaction), so the reset and the hold
		// commit or roll back together. The query is idempotent under
		// concurrency: the UPDATE takes a row lock held until the outer
		// transaction commits, and its `period_start < window` guard means only
		// the first concurrent reserver actually resets. A budget that was
		// `exceeded` last period is therefore `active` again here, so it falls
		// through to the active branch and can admit the reservation.
		if _, err := spq.ResetBudgetPeriodIfElapsed(ctx, b.ID); err != nil {
			return false, err
		}
		// The budget list was read before the savepoint acquired the row lock.
		// Admin can therefore pause or resume it in between. Try the state that
		// was observed first, then the other state on ErrNoRows so a paused →
		// active transition cannot silently bypass the cap (and active → paused
		// does not produce a spurious denial).
		reservePaused := func() error {
			_, err := spq.ReservePausedBudget(ctx, dbgen.ReservePausedBudgetParams{Amount: amount, ID: b.ID})
			return err
		}
		reserveActive := func() error {
			_, err := spq.ReserveActiveBudget(ctx, dbgen.ReserveActiveBudgetParams{Amount: amount, ID: b.ID})
			return err
		}
		var reserveErr error
		if b.Status == "paused" {
			reserveErr = reservePaused()
			if errors.Is(reserveErr, pgx.ErrNoRows) {
				reserveErr = reserveActive()
			}
		} else {
			reserveErr = reserveActive()
			if errors.Is(reserveErr, pgx.ErrNoRows) {
				reserveErr = reservePaused()
			}
		}
		if reserveErr != nil {
			if errors.Is(reserveErr, pgx.ErrNoRows) {
				return false, nil // current status is exceeded or the active cap would be exceeded
			}
			return false, reserveErr
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

func numericText(n pgtype.Numeric) string {
	if !n.Valid {
		return ""
	}
	value, err := n.Value()
	if err != nil || value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func zeroNumeric() pgtype.Numeric {
	return pgtype.Numeric{Int: big.NewInt(0), Exp: 0, Valid: true}
}

// nullNumeric is an explicit SQL NULL for a numeric column.
func nullNumeric() pgtype.Numeric { return pgtype.Numeric{Valid: false} }

// numericIsReported reports whether n is a real value the SQL layer
// returned (SUM(...) over at least one matching row), as opposed to SQL
// NULL (no provider ever reported an actual on this job). Valid alone is
// the correct signal: a provider that genuinely billed $0.00 still
// produces a valid, zero-valued numeric from SUM, and that must be
// distinguished from "nothing was ever billed" (NULL) rather than folded
// into it by also requiring a positive sign.
func numericIsReported(n pgtype.Numeric) bool {
	return n.Valid
}

func numericIsNonZero(n pgtype.Numeric) bool {
	return n.Valid && n.Int != nil && n.Int.Sign() != 0
}

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

// ReservationFinalizer is the stale-task-safe extension implemented by the
// Postgres lifecycle. Workers use the reservation captured with their job
// snapshot; admin/request paths continue using the broad Finalizer methods.
type ReservationFinalizer interface {
	CommitForReservation(ctx context.Context, jobID, reservationID string) error
	ReleaseForReservation(ctx context.Context, jobID, reservationID string) error
}

// ReservationReconciler folds provider-reported events that arrive after a
// reservation was already committed or released. It is separate from the
// finalizer interface so lightweight test finalizers do not need a database
// reconciliation implementation.
type ReservationReconciler interface {
	ReconcileForReservation(ctx context.Context, jobID, reservationID string) error
}

// Lifecycle is the Postgres-backed Finalizer. It is dual-context and
// executor-agnostic (Phase 7C-3):
//
//   - From the worker (system / BYPASSRLS) the standalone Commit/Release open a
//     transaction on the pool the Lifecycle was constructed with — that pool is
//     the system pool, so RLS is bypassed and the worker (which knows only a
//     job id) can finalize without a tenant GUC.
//   - From the request-path admin cancel/retry it is invoked via CommitInTx /
//     ReleaseInTx on the caller's transaction, which is tenant-local: the admin
//     service set app.current_tenant inside that transaction, so the same code
//     runs correctly under RLS without choosing its own pool or hardcoding the
//     system executor.
//
// It must never select a pool for itself beyond the one it was handed at
// construction, and the in-tx methods must operate purely on the caller's tx.
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

// CommitForReservation finalizes the exact reservation captured by a worker
// task, preventing a stale task from committing a later admin-retry hold.
func (l *Lifecycle) CommitForReservation(ctx context.Context, jobID, reservationID string) error {
	return l.finalize(ctx, jobID, statusCommitted, reservationID)
}

// Release transitions reserved → released for the job's reservation and
// returns each held amount to its budget's reserved pool (spent untouched).
// A no-op when the reservation is not in `reserved`.
func (l *Lifecycle) Release(ctx context.Context, jobID string) error {
	return l.finalize(ctx, jobID, statusReleased)
}

// ReleaseForReservation releases the exact reservation captured by a worker
// task, preventing a stale task from releasing a later admin-retry hold.
func (l *Lifecycle) ReleaseForReservation(ctx context.Context, jobID, reservationID string) error {
	return l.finalize(ctx, jobID, statusReleased, reservationID)
}

// ReconcileForReservation is safe to call after every discarded provider
// success. It locks the terminal reservation, sums only provider-reported
// actuals, and applies the signed delta exactly once to each budget and the
// identity ledger. This closes the race where cancellation or a duplicate
// winner finalizes before the losing provider call returns.
func (l *Lifecycle) ReconcileForReservation(ctx context.Context, jobID, reservationID string) error {
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
	reservation, err := q.GetTerminalReservationForReconciliation(ctx, dbgen.GetTerminalReservationForReconciliationParams{
		ReservationID:   reservationID,
		GenerationJobID: jobID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	reservationRef := reservationID
	jobRef := jobID
	reported, err := q.SumReportedReservationActual(ctx, dbgen.SumReportedReservationActualParams{
		ReservationID:   &reservationRef,
		GenerationJobID: &jobRef,
	})
	if err != nil {
		return err
	}
	if !numericIsReported(reported) {
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		committed = true
		return nil
	}
	delta, err := q.ReconcileReservationActual(ctx, dbgen.ReconcileReservationActualParams{
		ReservationID:   reservationID,
		GenerationJobID: jobID,
		ActualAmount:    reported,
		PreviousActual:  reservation.ActualAmount,
	})
	if err != nil {
		return err
	}
	if err := q.SetGenerationJobActualCostForReservation(ctx, dbgen.SetGenerationJobActualCostForReservationParams{
		ActualCostUsd:     reported,
		ID:                jobID,
		CostReservationID: &reservationRef,
	}); err != nil {
		return err
	}
	if numericIsNonZero(delta) {
		holds, err := q.ListFinalizedBudgetHolds(ctx, reservationID)
		if err != nil {
			return err
		}
		for _, hold := range holds {
			if err := q.AdjustBudgetSpent(ctx, dbgen.AdjustBudgetSpentParams{
				DeltaAmount: delta,
				ID:          hold.CostBudgetID,
			}); err != nil {
				return err
			}
		}
		if err := q.UpsertIdentityCostLedgerActualForJob(ctx, dbgen.UpsertIdentityCostLedgerActualForJobParams{
			LedgerID:        ids.NewIdentityCostLedgerID(),
			DeltaAmount:     delta,
			Currency:        reservation.Currency,
			GenerationJobID: jobID,
		}); err != nil {
			return err
		}
		// Terminal finalization already recorded the reservation estimate and
		// actual. Reconciliation contributes only the signed delta; recording
		// the updated totals again would double-count late events.
		telemetry.DefaultMetrics().RecordActualDelta(numericText(delta))
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	committed = true
	return nil
}

func (l *Lifecycle) finalize(ctx context.Context, jobID, target string, reservationIDs ...string) error {
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
	reservationID := ""
	if len(reservationIDs) > 0 {
		reservationID = reservationIDs[0]
	}
	if err := l.finalizeInTxForReservation(ctx, tx, jobID, target, reservationID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	committed = true
	return nil
}

// CommitInTx commits a job's reservation within the caller's transaction
// (reserved → committed, moving each held amount from reserved to spent and
// stamping the job's actual cost), without committing the transaction. It is
// the executor-agnostic counterpart to ReleaseInTx: the caller owns the tx and
// is responsible for the tenant GUC (a request-path admin tx that set
// app.current_tenant, or a system/bypass tx). Like Commit it is a no-op when
// the reservation is not in `reserved`.
func (l *Lifecycle) CommitInTx(ctx context.Context, tx pgx.Tx, jobID string) error {
	return l.finalizeInTx(ctx, tx, jobID, statusCommitted)
}

// ReleaseInTx releases a job's reservation within the caller's transaction
// (reserved → released, returning each held amount to its budget's reserved
// pool; spent untouched). It is the building block admin cancel uses to set a
// job `cancelled` and release its reservation atomically in one transaction.
// Like Release it is a no-op when the reservation is not in `reserved`.
func (l *Lifecycle) ReleaseInTx(ctx context.Context, tx pgx.Tx, jobID string) error {
	return l.finalizeInTx(ctx, tx, jobID, statusReleased)
}

// finalizeInTx performs the terminal transition against the caller's
// transaction without committing it, so it can be composed into a larger
// transaction (admin cancel) or wrapped by finalize for the worker path.
func (l *Lifecycle) finalizeInTx(ctx context.Context, tx pgx.Tx, jobID, target string) error {
	return l.finalizeInTxForReservation(ctx, tx, jobID, target, "")
}

func (l *Lifecycle) finalizeInTxForReservation(ctx context.Context, tx pgx.Tx, jobID, target, reservationID string) error {
	q := dbgen.New(tx)

	var (
		finalReservationID string
		estimated          pgtype.Numeric
		actual             pgtype.Numeric
		tenantID           string
		currency           string
	)
	noop := false
	switch target {
	case statusCommitted:
		var err error
		if reservationID != "" {
			row, queryErr := q.CommitReservationByID(ctx, dbgen.CommitReservationByIDParams{GenerationJobID: jobID, ReservationID: reservationID})
			err = queryErr
			if err == nil {
				finalReservationID, estimated, actual, tenantID, currency = row.ID, row.EstimatedAmount, row.ActualAmount, row.TenantID, row.Currency
			}
		} else {
			row, queryErr := q.CommitReservationForJob(ctx, jobID)
			err = queryErr
			if err == nil {
				finalReservationID, estimated, actual, tenantID, currency = row.ID, row.EstimatedAmount, row.ActualAmount, row.TenantID, row.Currency
			}
		}
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			noop = true // not reserved → idempotent no-op
		case err != nil:
			return err
		}
	case statusReleased:
		var err error
		if reservationID != "" {
			row, queryErr := q.ReleaseReservationByID(ctx, dbgen.ReleaseReservationByIDParams{GenerationJobID: jobID, ReservationID: reservationID})
			err = queryErr
			if err == nil {
				// actual carries a provider-reported partial charge when one
				// exists (cost-control.md §3 step 10: "preview succeeded, final
				// failed"), including a genuine $0.00 report; NULL (row.ActualAmount
				// invalid) means no provider ever billed anything on this job.
				// currency is needed below for the identity-ledger write on a
				// partial charge.
				finalReservationID, estimated, actual, tenantID, currency = row.ID, row.EstimatedAmount, row.ActualAmount, row.TenantID, row.Currency
			}
		} else {
			row, queryErr := q.ReleaseReservationForJob(ctx, jobID)
			err = queryErr
			if err == nil {
				// actual carries a provider-reported partial charge when one
				// exists (cost-control.md §3 step 10: "preview succeeded, final
				// failed"), including a genuine $0.00 report; NULL (row.ActualAmount
				// invalid) means no provider ever billed anything on this job.
				// currency is needed below for the identity-ledger write on a
				// partial charge.
				finalReservationID, estimated, actual, tenantID, currency = row.ID, row.EstimatedAmount, row.ActualAmount, row.TenantID, row.Currency
			}
		}
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			noop = true
		case err != nil:
			return err
		}
	default:
		return errors.New("cost: invalid finalize target " + target)
	}

	if noop {
		return nil
	}

	// partialCharge is cost-control.md §3 step 10: a released (failed/
	// cancelled) job where a provider nonetheless reported and billed a real
	// call (e.g. preview succeeded, final failed) — including an explicit
	// $0.00 report, which is still "billed" (a known outcome), just for zero
	// money. It is charged exactly like a commit — moved from reserved to
	// spent, stamped on the job, folded into the identity ledger — while the
	// reservation itself still ends in `released` (the job did not succeed)
	// and the unbilled remainder of the hold is simply dropped from
	// `reserved_amount` (never added to spent).
	partialCharge := target == statusReleased && numericIsReported(actual)
	chargedActual := target == statusCommitted || partialCharge

	holds, err := q.ListReservedBudgetHolds(ctx, finalReservationID)
	if err != nil {
		return err
	}
	for _, h := range holds {
		if chargedActual {
			if err := q.CommitBudgetHold(ctx, dbgen.CommitBudgetHoldParams{ReservedAmount: h.ReservedAmount, ActualAmount: actual, ID: h.CostBudgetID}); err != nil {
				return err
			}
		} else {
			if err := q.ReleaseBudgetHold(ctx, dbgen.ReleaseBudgetHoldParams{Amount: h.ReservedAmount, ID: h.CostBudgetID}); err != nil {
				return err
			}
		}
		// The hold's own status always follows the reservation's terminal
		// status (released here, even for a partially-charged release) — it
		// records what happened to the RESERVATION, not whether this specific
		// hold moved money via Commit vs Release above.
		if err := q.MarkBudgetHoldStatus(ctx, dbgen.MarkBudgetHoldStatusParams{Status: target, ID: h.ID}); err != nil {
			return err
		}
	}

	// Cost-event + job actual: on commit, or a release with a provider-
	// reported partial charge, provider-reported actuals win (estimated cost
	// is the explicit fallback only when commit never got a provider actual
	// at all — see CommitReservationForJob's COALESCE). A plain release (no
	// provider ever charged anything) leaves the job's actual cost untouched
	// (nil) and no identity ledger row is written.
	costEventStatus := costEventFailed
	costEventActual := nullNumeric()
	if target == statusCommitted {
		costEventStatus = costEventSucceeded
	}
	if chargedActual {
		costEventActual = actual
		var err error
		if finalReservationID != "" {
			err = q.SetGenerationJobActualCostForReservation(ctx, dbgen.SetGenerationJobActualCostForReservationParams{
				ActualCostUsd:     actual,
				ID:                jobID,
				CostReservationID: &finalReservationID,
			})
		} else {
			err = q.SetGenerationJobActualCost(ctx, dbgen.SetGenerationJobActualCostParams{ActualCostUsd: actual, ID: jobID})
		}
		if err != nil {
			return err
		}
		if err := q.UpsertIdentityCostLedgerForJob(ctx, dbgen.UpsertIdentityCostLedgerForJobParams{
			LedgerID:        ids.NewIdentityCostLedgerID(),
			EstimatedAmount: estimated,
			ActualAmount:    actual,
			Currency:        currency,
			GenerationJobID: jobID,
		}); err != nil {
			return err
		}
		telemetry.DefaultMetrics().RecordCost(numericText(estimated), numericText(actual))
	}
	if err := l.finalizeCostEvent(ctx, q, jobID, finalReservationID, tenantID, estimated, costEventActual, costEventStatus); err != nil {
		return err
	}

	return nil
}

// finalizeCostEvent stamps estimated/actual/status onto the job's latest cost
// event (the one the worker wrote for the terminal attempt). If none exists it
// writes a fallback row so the cost ledger is never silently missing.
func (l *Lifecycle) finalizeCostEvent(ctx context.Context, q *dbgen.Queries, jobID, reservationID, tenantID string, estimated, actual pgtype.Numeric, status string) error {
	job := jobID
	var reservationIDPtr *string
	if reservationID != "" {
		reservationIDPtr = &reservationID
	}
	n, err := q.UpdateLatestJobCostEvent(ctx, dbgen.UpdateLatestJobCostEventParams{
		EstimatedCostUsd:  estimated,
		ActualCostUsd:     actual,
		Status:            status,
		JobID:             &job,
		CostReservationID: reservationIDPtr,
	})
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	return q.InsertFinalizerCostEvent(ctx, dbgen.InsertFinalizerCostEventParams{
		ID:                ids.NewCostEventID(),
		TenantID:          tenantID,
		JobID:             &job,
		CostReservationID: reservationIDPtr,
		Operation:         operationTextToImage,
		EstimatedCostUsd:  estimated,
		ActualCostUsd:     actual,
		Status:            status,
	})
}
