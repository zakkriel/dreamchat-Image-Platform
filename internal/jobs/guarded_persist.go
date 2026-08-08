package jobs

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/zakkriel/drchat-image-platform/internal/assets"
	"github.com/zakkriel/drchat-image-platform/internal/db/dbgen"
)

// statusCancelled is the terminal status an admin cancel moves a job to
// (Phase 7C-1a). The worker treats it as terminal and never records output.
const statusCancelled = "cancelled"

// PersistOutcome is the result of a guarded worker output write (Phase 7C-1a).
// It tells the worker whether the asset + job transition committed, was
// skipped because the job was cancelled before persistence, or was skipped
// because a concurrent attempt already completed the job first.
type PersistOutcome int

const (
	// PersistPersisted means the asset was inserted and the job transitioned.
	PersistPersisted PersistOutcome = iota
	// PersistSkippedCancelled means the job was cancelled before the guarded
	// write, so no asset was inserted and the job was left cancelled.
	PersistSkippedCancelled
	// PersistAlreadyCompleted means a concurrent attempt already completed the
	// job. Nothing was inserted; the caller must not repeat success bookkeeping
	// (provider-attempt success, cost event, Finalizer.Commit, telemetry).
	PersistAlreadyCompleted
	// PersistAlreadyPreviewReady means another attempt already committed the
	// preview. The caller may continue into final generation for a non-lazy job,
	// but must not duplicate preview bookkeeping or the preview asset.
	PersistAlreadyPreviewReady
	// PersistAlreadyTerminal means another attempt already failed the job.
	// Nothing was inserted and the losing worker must stop without changing the
	// terminal state.
	PersistAlreadyTerminal
)

// InsertFinalAssetAndCompleteJobIfNotCancelled inserts a final visual_asset and
// marks the job completed in ONE transaction, guarded by the job row lock
// (Phase 7C-1a). It first locks the generation_jobs row and re-reads its
// status; if the job is `cancelled` it inserts nothing and transitions nothing,
// returning PersistSkippedCancelled. Otherwise it inserts the asset (forced
// jobs supersede their slot) and completes the job atomically. This closes the
// race where a provider returns and the asset is inserted just as a cancel
// lands: admin cancel and this write both take the same row lock, so a
// cancelled job can never end up with a final output attached.
func (r *pgRepository) InsertFinalAssetAndCompleteJobIfNotCancelled(ctx context.Context, jobID, tenantID string, params assets.InsertParams, forced bool, slot assets.ArtifactSlot) (assets.VisualAsset, PersistOutcome, error) {
	return r.insertFinalAssetAndCompleteJob(ctx, jobID, tenantID, params, forced, slot, nil, nil)
}

func (r *pgRepository) InsertFinalAssetAndCompleteJobWithSuccess(ctx context.Context, jobID, tenantID string, params assets.InsertParams, forced bool, slot assets.ArtifactSlot, success PersistSuccessParams) (assets.VisualAsset, PersistOutcome, error) {
	return r.insertFinalAssetAndCompleteJob(ctx, jobID, tenantID, params, forced, slot, &success, nil)
}

func (r *pgRepository) InsertFinalAssetAndCompleteJobWithSuccessForReservation(ctx context.Context, jobID, tenantID, reservationID string, params assets.InsertParams, forced bool, slot assets.ArtifactSlot, success PersistSuccessParams) (assets.VisualAsset, PersistOutcome, error) {
	return r.insertFinalAssetAndCompleteJob(ctx, jobID, tenantID, params, forced, slot, &success, &reservationID)
}

func (r *pgRepository) insertFinalAssetAndCompleteJob(ctx context.Context, jobID, tenantID string, params assets.InsertParams, forced bool, slot assets.ArtifactSlot, success *PersistSuccessParams, reservationID *string) (assets.VisualAsset, PersistOutcome, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return assets.VisualAsset{}, PersistPersisted, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	q := dbgen.New(tx)

	locked, err := q.LockGenerationJobRowForUpdate(ctx, dbgen.LockGenerationJobRowForUpdateParams{ID: jobID, TenantID: tenantID, CostReservationID: reservationID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if reservationID != nil {
				if commitErr := tx.Commit(ctx); commitErr != nil {
					return assets.VisualAsset{}, PersistPersisted, commitErr
				}
				committed = true
				return assets.VisualAsset{}, PersistAlreadyTerminal, nil
			}
			return assets.VisualAsset{}, PersistPersisted, ErrNotFound
		}
		return assets.VisualAsset{}, PersistPersisted, err
	}
	status := locked.Status
	if status == statusCancelled {
		// Cancelled before persistence: record no output, transition nothing.
		// Commit to release the row lock so the cancel side isn't blocked.
		if err := tx.Commit(ctx); err != nil {
			return assets.VisualAsset{}, PersistPersisted, err
		}
		committed = true
		return assets.VisualAsset{}, PersistSkippedCancelled, nil
	}
	if status == "completed" {
		if err := tx.Commit(ctx); err != nil {
			return assets.VisualAsset{}, PersistPersisted, err
		}
		committed = true
		return assets.VisualAsset{}, PersistAlreadyCompleted, nil
	}
	if status == "failed" {
		if err := tx.Commit(ctx); err != nil {
			return assets.VisualAsset{}, PersistPersisted, err
		}
		committed = true
		return assets.VisualAsset{}, PersistAlreadyTerminal, nil
	}
	if status != "running" && status != "preview_ready" {
		return assets.VisualAsset{}, PersistPersisted, fmt.Errorf("jobs: final asset persistence requires an active job, got status %q", status)
	}

	var asset assets.VisualAsset
	if forced {
		asset, err = assets.SupersedeArtifactSlotWithQueries(ctx, q, params, slot)
	} else {
		asset, err = assets.InsertWithQueries(ctx, q, params)
	}
	if err != nil {
		return assets.VisualAsset{}, PersistPersisted, err
	}
	if _, err := q.MarkGenerationJobCompleted(ctx, dbgen.MarkGenerationJobCompletedParams{
		ID:                jobID,
		TenantID:          tenantID,
		FinalAssetIds:     []string{asset.ID},
		CostReservationID: reservationID,
	}); err != nil {
		return assets.VisualAsset{}, PersistPersisted, err
	}
	if success != nil {
		if success.AttemptID == "" {
			return assets.VisualAsset{}, PersistPersisted, errors.New("jobs: guarded success bookkeeping missing provider attempt")
		}
		if err := markProviderAttemptSucceededWithQueries(ctx, q, success.AttemptID, success.LatencyMs); err != nil {
			return assets.VisualAsset{}, PersistPersisted, fmt.Errorf("mark guarded attempt succeeded: %w", err)
		}
		event := success.CostEvent
		event.AssetID = &asset.ID
		event.ProviderAttemptID = &success.AttemptID
		if err := insertCostEventWithQueries(ctx, q, event); err != nil {
			return assets.VisualAsset{}, PersistPersisted, fmt.Errorf("insert guarded success cost event: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return assets.VisualAsset{}, PersistPersisted, err
	}
	committed = true
	return asset, PersistPersisted, nil
}

// InsertPreviewAssetAndMarkPreviewReadyIfNotCancelled inserts a preview
// visual_asset and marks the job preview_ready in ONE transaction, guarded by
// the job row lock (Phase 7C-1a). Same cancel guard as the final write: if the
// job is `cancelled` it inserts nothing and returns PersistSkippedCancelled, so
// a cancelled preview-first job never gets a preview output recorded.
func (r *pgRepository) InsertPreviewAssetAndMarkPreviewReadyIfNotCancelled(ctx context.Context, jobID, tenantID string, params assets.InsertParams) (assets.VisualAsset, PersistOutcome, error) {
	return r.insertPreviewAssetAndMarkPreviewReady(ctx, jobID, tenantID, params, nil, nil)
}

func (r *pgRepository) InsertPreviewAssetAndMarkPreviewReadyWithSuccess(ctx context.Context, jobID, tenantID string, params assets.InsertParams, success PersistSuccessParams) (assets.VisualAsset, PersistOutcome, error) {
	return r.insertPreviewAssetAndMarkPreviewReady(ctx, jobID, tenantID, params, &success, nil)
}

func (r *pgRepository) InsertPreviewAssetAndMarkPreviewReadyWithSuccessForReservation(ctx context.Context, jobID, tenantID, reservationID string, params assets.InsertParams, success PersistSuccessParams) (assets.VisualAsset, PersistOutcome, error) {
	return r.insertPreviewAssetAndMarkPreviewReady(ctx, jobID, tenantID, params, &success, &reservationID)
}

func (r *pgRepository) insertPreviewAssetAndMarkPreviewReady(ctx context.Context, jobID, tenantID string, params assets.InsertParams, success *PersistSuccessParams, reservationID *string) (assets.VisualAsset, PersistOutcome, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return assets.VisualAsset{}, PersistPersisted, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	q := dbgen.New(tx)

	locked, err := q.LockGenerationJobRowForUpdate(ctx, dbgen.LockGenerationJobRowForUpdateParams{ID: jobID, TenantID: tenantID, CostReservationID: reservationID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if reservationID != nil {
				if commitErr := tx.Commit(ctx); commitErr != nil {
					return assets.VisualAsset{}, PersistPersisted, commitErr
				}
				committed = true
				return assets.VisualAsset{}, PersistAlreadyTerminal, nil
			}
			return assets.VisualAsset{}, PersistPersisted, ErrNotFound
		}
		return assets.VisualAsset{}, PersistPersisted, err
	}
	status := locked.Status
	if status == statusCancelled {
		if err := tx.Commit(ctx); err != nil {
			return assets.VisualAsset{}, PersistPersisted, err
		}
		committed = true
		return assets.VisualAsset{}, PersistSkippedCancelled, nil
	}
	if status == "preview_ready" {
		if err := tx.Commit(ctx); err != nil {
			return assets.VisualAsset{}, PersistPersisted, err
		}
		committed = true
		return assets.VisualAsset{}, PersistAlreadyPreviewReady, nil
	}
	if status == "completed" {
		if err := tx.Commit(ctx); err != nil {
			return assets.VisualAsset{}, PersistPersisted, err
		}
		committed = true
		return assets.VisualAsset{}, PersistAlreadyCompleted, nil
	}
	if status == "failed" {
		if err := tx.Commit(ctx); err != nil {
			return assets.VisualAsset{}, PersistPersisted, err
		}
		committed = true
		return assets.VisualAsset{}, PersistAlreadyTerminal, nil
	}
	if status != "running" {
		return assets.VisualAsset{}, PersistPersisted, fmt.Errorf("jobs: preview persistence requires a running job, got status %q", status)
	}

	asset, err := assets.InsertPreviewWithQueries(ctx, q, params)
	if err != nil {
		return assets.VisualAsset{}, PersistPersisted, err
	}
	if _, err := q.MarkGenerationJobPreviewReady(ctx, dbgen.MarkGenerationJobPreviewReadyParams{
		ID:                jobID,
		TenantID:          tenantID,
		PreviewAssetIds:   []string{asset.ID},
		CostReservationID: reservationID,
	}); err != nil {
		return assets.VisualAsset{}, PersistPersisted, err
	}
	if success != nil {
		if success.AttemptID == "" {
			return assets.VisualAsset{}, PersistPersisted, errors.New("jobs: guarded preview success bookkeeping missing provider attempt")
		}
		if err := markProviderAttemptSucceededWithQueries(ctx, q, success.AttemptID, success.LatencyMs); err != nil {
			return assets.VisualAsset{}, PersistPersisted, fmt.Errorf("mark guarded preview attempt succeeded: %w", err)
		}
		event := success.CostEvent
		event.AssetID = &asset.ID
		event.ProviderAttemptID = &success.AttemptID
		if err := insertCostEventWithQueries(ctx, q, event); err != nil {
			return assets.VisualAsset{}, PersistPersisted, fmt.Errorf("insert guarded preview success cost event: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return assets.VisualAsset{}, PersistPersisted, err
	}
	committed = true
	return asset, PersistPersisted, nil
}

// persistSuccessWithQueries records the provider attempt and its cost event
// inside an existing transaction used by pack persistence.
func persistSuccessWithQueries(ctx context.Context, q *dbgen.Queries, success PersistSuccessParams, assetID string) error {
	if success.AttemptID == "" {
		return errors.New("jobs: success bookkeeping missing provider attempt")
	}
	if err := markProviderAttemptSucceededWithQueries(ctx, q, success.AttemptID, success.LatencyMs); err != nil {
		return fmt.Errorf("mark success attempt: %w", err)
	}
	event := success.CostEvent
	event.AssetID = &assetID
	event.ProviderAttemptID = &success.AttemptID
	if err := insertCostEventWithQueries(ctx, q, event); err != nil {
		return fmt.Errorf("insert success cost event: %w", err)
	}
	return nil
}
