package jobs

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/zakkriel/drchat-image-platform/internal/assets"
	"github.com/zakkriel/drchat-image-platform/internal/db/dbgen"
)

// statusCancelled is the terminal status an admin cancel moves a job to
// (Phase 7C-1a). The worker treats it as terminal and never records output.
const statusCancelled = "cancelled"

// PersistOutcome is the result of a guarded worker output write (Phase 7C-1a).
// It tells the worker whether the asset + job transition committed or was
// skipped because the job was cancelled before persistence.
type PersistOutcome int

const (
	// PersistPersisted means the asset was inserted and the job transitioned.
	PersistPersisted PersistOutcome = iota
	// PersistSkippedCancelled means the job was cancelled before the guarded
	// write, so no asset was inserted and the job was left cancelled.
	PersistSkippedCancelled
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

	status, err := q.LockGenerationJobForUpdate(ctx, dbgen.LockGenerationJobForUpdateParams{ID: jobID, TenantID: tenantID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return assets.VisualAsset{}, PersistPersisted, ErrNotFound
		}
		return assets.VisualAsset{}, PersistPersisted, err
	}
	if status == statusCancelled {
		// Cancelled before persistence: record no output, transition nothing.
		// Commit to release the row lock so the cancel side isn't blocked.
		if err := tx.Commit(ctx); err != nil {
			return assets.VisualAsset{}, PersistPersisted, err
		}
		committed = true
		return assets.VisualAsset{}, PersistSkippedCancelled, nil
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
		ID:            jobID,
		TenantID:      tenantID,
		FinalAssetIds: []string{asset.ID},
	}); err != nil {
		return assets.VisualAsset{}, PersistPersisted, err
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

	status, err := q.LockGenerationJobForUpdate(ctx, dbgen.LockGenerationJobForUpdateParams{ID: jobID, TenantID: tenantID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return assets.VisualAsset{}, PersistPersisted, ErrNotFound
		}
		return assets.VisualAsset{}, PersistPersisted, err
	}
	if status == statusCancelled {
		if err := tx.Commit(ctx); err != nil {
			return assets.VisualAsset{}, PersistPersisted, err
		}
		committed = true
		return assets.VisualAsset{}, PersistSkippedCancelled, nil
	}

	asset, err := assets.InsertPreviewWithQueries(ctx, q, params)
	if err != nil {
		return assets.VisualAsset{}, PersistPersisted, err
	}
	if _, err := q.MarkGenerationJobPreviewReady(ctx, dbgen.MarkGenerationJobPreviewReadyParams{
		ID:              jobID,
		TenantID:        tenantID,
		PreviewAssetIds: []string{asset.ID},
	}); err != nil {
		return assets.VisualAsset{}, PersistPersisted, err
	}
	if err := tx.Commit(ctx); err != nil {
		return assets.VisualAsset{}, PersistPersisted, err
	}
	committed = true
	return asset, PersistPersisted, nil
}
