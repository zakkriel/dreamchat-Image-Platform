// Package adminjobs implements the Phase 7C-1 admin job-control surface:
// cancel a non-terminal job (reclaiming its reserved cost) and retry a failed
// job (re-reserving cost against the persisted resolved route, never
// re-resolving). It owns the transactional orchestration; the HTTP handler is
// a thin adapter that maps the package's sentinel errors to status codes.
package adminjobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zakkriel/drchat-image-platform/internal/cost"
	"github.com/zakkriel/drchat-image-platform/internal/db/dbgen"
	"github.com/zakkriel/drchat-image-platform/internal/jobs"
)

// Sentinel errors the handler maps to status codes:
//   - ErrNotFound       → 404 (missing or cross-tenant job)
//   - ErrInvalidState   → 409 invalid_state (cancel/retry from a disallowed status)
//   - cost.ErrNoPriceEntry / cost.ErrBudgetExceeded → 422 (retry reservation denied)
//   - jobs.ErrEnqueueFailed → 500 (enqueue failed after a successful retry commit)
var (
	ErrNotFound     = errors.New("adminjobs: generation job not found")
	ErrInvalidState = errors.New("adminjobs: job not in a valid state for this action")
)

const (
	statusQueued       = "queued"
	statusRunning      = "running"
	statusPreviewReady = "preview_ready"
	statusCompleted    = "completed"
	statusFailed       = "failed"
	statusCancelled    = "cancelled"

	defaultOperationType = "text_to_image"
	defaultUnits         = 1
)

// Releaser is the cost-reservation release surface admin job control needs.
// *cost.Lifecycle satisfies it: ReleaseInTx composes the release into the
// cancel transaction (atomic with the status flip); Release is the standalone
// idempotent release used on a retry enqueue failure. Both are no-ops when the
// reservation is not in `reserved`.
type Releaser interface {
	ReleaseInTx(ctx context.Context, tx pgx.Tx, jobID string) error
	Release(ctx context.Context, jobID string) error
}

// Service implements admin cancel/retry against Postgres + the cost pipeline.
type Service struct {
	pool     *pgxpool.Pool
	reserver cost.Reserver
	releaser Releaser
	enqueuer jobs.Enqueuer
	logger   *slog.Logger
}

func NewService(pool *pgxpool.Pool, reserver cost.Reserver, releaser Releaser, enqueuer jobs.Enqueuer, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{pool: pool, reserver: reserver, releaser: releaser, enqueuer: enqueuer, logger: logger}
}

// CancelJob cancels a non-terminal job and releases its reserved cost, both in
// one transaction (Phase 7C-1a). It locks the job row (the same lock the
// worker's guarded persist takes), so the transition and the release are
// atomic and an in-flight worker can never attach output to a cancelled job.
//
// Allowed source states: queued, running, preview_ready. From completed/failed
// it returns ErrInvalidState (→ 409). From cancelled it is idempotent: it
// re-runs the (no-op) release and returns the existing cancelled job.
func (s *Service) CancelJob(ctx context.Context, tenantID, jobID string) (jobs.Job, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return jobs.Job{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	q := dbgen.New(tx)

	status, err := q.LockGenerationJobForUpdate(ctx, dbgen.LockGenerationJobForUpdateParams{ID: jobID, TenantID: tenantID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return jobs.Job{}, ErrNotFound
		}
		return jobs.Job{}, err
	}

	switch status {
	case statusCancelled:
		// Idempotent: ensure the reservation is released (no-op if already) and
		// return the existing cancelled job unchanged.
		if err := s.releaser.ReleaseInTx(ctx, tx, jobID); err != nil {
			return jobs.Job{}, err
		}
		row, err := q.GetGenerationJobByID(ctx, dbgen.GetGenerationJobByIDParams{ID: jobID, TenantID: tenantID})
		if err != nil {
			return jobs.Job{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return jobs.Job{}, err
		}
		committed = true
		return jobs.JobFromGenerationRow(row), nil
	case statusQueued, statusRunning, statusPreviewReady:
		msg := cancelMessage(status)
		row, err := q.CancelGenerationJob(ctx, dbgen.CancelGenerationJobParams{ID: jobID, TenantID: tenantID, ErrorMessage: &msg})
		if err != nil {
			return jobs.Job{}, err
		}
		// Release the reservation in the SAME transaction so the cancel and the
		// budget reclaim commit together — exactly once.
		if err := s.releaser.ReleaseInTx(ctx, tx, jobID); err != nil {
			return jobs.Job{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return jobs.Job{}, err
		}
		committed = true
		return jobs.JobFromGenerationRow(row), nil
	default: // completed, failed
		return jobs.Job{}, ErrInvalidState
	}
}

// RetryJob reopens a failed job (Phase 7C-1b). It validates the job is failed,
// re-reserves cost against the persisted resolved provider/model/operation/
// units (read from input_payload — never re-resolving the route), reopens the
// job to queued (clearing the failure fields and any stale final output, while
// preserving preview_asset_ids), and enqueues it. Reservation + reset + cost
// link happen in one transaction; the enqueue follows the commit and mirrors
// the create path's enqueue-failure cleanup.
//
// A denied reservation (no price / budget exceeded) leaves the job failed, does
// not enqueue, and does not leave a partial reservation — the transaction rolls
// back the speculative failed reservation row too.
func (s *Service) RetryJob(ctx context.Context, tenantID, jobID string) (jobs.Job, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return jobs.Job{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	q := dbgen.New(tx)

	row, err := q.LockGenerationJobRowForUpdate(ctx, dbgen.LockGenerationJobRowForUpdateParams{ID: jobID, TenantID: tenantID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return jobs.Job{}, ErrNotFound
		}
		return jobs.Job{}, err
	}
	if row.Status != statusFailed {
		return jobs.Job{}, ErrInvalidState
	}

	rin, err := reserveInputFromRow(jobID, tenantID, row)
	if err != nil {
		return jobs.Job{}, err
	}

	res, err := s.reserver.Reserve(ctx, tx, rin)
	if err != nil {
		return jobs.Job{}, fmt.Errorf("adminjobs: reserve cost: %w", err)
	}
	if res.Failed() {
		// Denied: leave the job failed untouched, create no live reservation.
		// Returning here rolls the transaction back (the failed reservation row
		// included), so no partial reservation lingers.
		return jobs.Job{}, failureError(res.FailureReason)
	}

	resetRow, err := q.RetryResetGenerationJob(ctx, dbgen.RetryResetGenerationJobParams{
		ID:                jobID,
		TenantID:          tenantID,
		CostReservationID: &res.ID,
		CostEstimateUsd:   res.EstimatedAmount,
	})
	if err != nil {
		return jobs.Job{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return jobs.Job{}, err
	}
	committed = true

	packID := ""
	if resetRow.AssetPackID != nil {
		packID = *resetRow.AssetPackID
	}
	if err := s.enqueueRetry(ctx, jobID, tenantID, packID); err != nil {
		return jobs.Job{}, err
	}
	return jobs.JobFromGenerationRow(resetRow), nil
}

// enqueueRetry places the reopened job on the queue and mirrors the create
// path's enqueue-failure behavior: on failure it marks the job failed, marks a
// pack job's pack failed, and releases the fresh reservation so it does not sit
// reserved forever. It never leaves a queued job with no queued task.
func (s *Service) enqueueRetry(ctx context.Context, jobID, tenantID, packID string) error {
	enqueueFn := s.enqueuer.EnqueueGenerateArtifact
	if packID != "" {
		enqueueFn = s.enqueuer.EnqueueGeneratePack
	}
	if err := enqueueFn(ctx, jobID); err != nil {
		q := dbgen.New(s.pool)
		ec := "enqueue_failed"
		em := err.Error()
		rb := false
		if _, markErr := q.MarkGenerationJobFailed(ctx, dbgen.MarkGenerationJobFailedParams{
			ID:           jobID,
			TenantID:     tenantID,
			ErrorCode:    &ec,
			ErrorMessage: &em,
			Retryable:    &rb,
		}); markErr != nil {
			return fmt.Errorf("%w (also mark-failed: %v): %v", jobs.ErrEnqueueFailed, markErr, err)
		}
		if packID != "" {
			if packErr := q.UpdateAssetPackStatus(ctx, dbgen.UpdateAssetPackStatusParams{ID: packID, Status: statusFailed}); packErr != nil {
				return fmt.Errorf("%w (also mark-pack-failed: %v): %v", jobs.ErrEnqueueFailed, packErr, err)
			}
		}
		if relErr := s.releaser.Release(ctx, jobID); relErr != nil {
			return fmt.Errorf("%w (also release-reservation: %v): %v", jobs.ErrEnqueueFailed, relErr, err)
		}
		return fmt.Errorf("%w: %v", jobs.ErrEnqueueFailed, err)
	}
	return nil
}

// reserveInputFromRow builds the retry cost reservation input from the job's
// persisted payload. provider_id and model_id are required (the resolve-once
// route the handler persisted at creation); operation_type/units fall back to
// the platform defaults for jobs created before they were persisted. The route
// is NEVER re-resolved — this reads exactly what was priced.
func reserveInputFromRow(jobID, tenantID string, row dbgen.GenerationJob) (cost.ReserveInput, error) {
	var payload map[string]any
	if len(row.InputPayload) > 0 {
		_ = json.Unmarshal(row.InputPayload, &payload)
	}
	providerID := payloadString(payload, "provider_id")
	modelID := payloadString(payload, "model_id")
	if providerID == "" || modelID == "" {
		return cost.ReserveInput{}, fmt.Errorf("adminjobs: job %s has no persisted resolved route to retry", jobID)
	}
	operationType := payloadString(payload, "operation_type")
	if operationType == "" {
		operationType = defaultOperationType
	}
	units := payloadInt32(payload, "units")
	if units <= 0 {
		units = defaultUnits
	}
	tokenID := ""
	if row.RequestedByTokenID != nil {
		tokenID = *row.RequestedByTokenID
	}
	worldID := ""
	if row.WorldID != nil {
		worldID = *row.WorldID
	}
	return cost.ReserveInput{
		JobID:         jobID,
		TenantID:      tenantID,
		TokenID:       tokenID,
		WorldID:       worldID,
		UserID:        payloadString(payload, "user_id"),
		ProviderID:    providerID,
		ModelID:       modelID,
		OperationType: operationType,
		Units:         units,
	}, nil
}

func cancelMessage(fromStatus string) string {
	return "job cancelled by admin from status " + fromStatus
}

// failureError maps a reservation failure reason to the cost sentinel the
// handler keys its 422 status code off.
func failureError(reason string) error {
	switch reason {
	case cost.ReasonNoPriceEntry:
		return cost.ErrNoPriceEntry
	case cost.ReasonBudgetExceeded:
		return cost.ErrBudgetExceeded
	default:
		return fmt.Errorf("adminjobs: retry reservation failed: %s", reason)
	}
}

func payloadString(payload map[string]any, key string) string {
	s, _ := payload[key].(string)
	return s
}

// payloadInt32 reads an integer from the payload. JSON numbers decode as
// float64; an absent or non-numeric value yields 0.
func payloadInt32(payload map[string]any, key string) int32 {
	switch v := payload[key].(type) {
	case float64:
		return int32(v)
	case int:
		return int32(v)
	case int32:
		return v
	default:
		return 0
	}
}
