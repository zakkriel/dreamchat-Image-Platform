// Package jobs owns generation_jobs lifecycle, provider_attempts, and the
// asynq enqueue/handler wiring. Handlers go through this package; sqlc
// types stay inside it.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zakkriel/drchat-image-platform/internal/assets"
	appdb "github.com/zakkriel/drchat-image-platform/internal/db"
	"github.com/zakkriel/drchat-image-platform/internal/db/dbgen"
)

var (
	ErrNotFound = errors.New("jobs: generation job not found")
)

// Job is the domain view of generation_jobs used by handlers, the worker,
// and the API response mapping.
type Job struct {
	ID                 string
	TenantID           string
	WorldID            *string
	JobType            string
	Status             string
	RequestedByTokenID *string
	// VisualIdentityID is the first-class generation_jobs.visual_identity_id
	// column (distinct from InputPayload["identity_id"]/["visual_identity_id"],
	// which is the payload-carried value older/ad-hoc jobs rely on).
	VisualIdentityID *string
	AssetPackID      *string
	InputPayload     map[string]any
	FallbackPolicy   *string
	CacheResult      *string
	PreviewAssetIds  []string
	FinalAssetIds    []string
	ErrorCode        *string
	ErrorMessage     *string
	Retryable        *bool
	// CostReservationID identifies the exact reservation held for this job.
	// Workers retain it across provider work so a stale task cannot finalize a
	// later admin-retry reservation attached to the same reusable job id.
	CostReservationID *string
	// CostEstimateUSD is the pre-flight estimate stamped at reservation time
	// (generation_jobs.cost_estimate_usd). Nil when never estimated (e.g. an
	// unpriced test route).
	CostEstimateUSD *string
	// ActualCostUSD is the reconciled actual cost stamped by
	// cost.Lifecycle.Commit on job success (generation_jobs.actual_cost_usd).
	// Nil until the job commits.
	ActualCostUSD *string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	StartedAt     *time.Time
	CompletedAt   *time.Time
}

// InsertParams captures everything Phase 3 writes when accepting a job.
type InsertParams struct {
	ID                 string
	TenantID           string
	WorldID            *string
	JobType            string
	RequestedByTokenID *string
	InputPayload       map[string]any
	FallbackPolicy     *string
	CacheResult        *string
}

// ProviderAttemptInsertParams captures per-call attempt rows.
type ProviderAttemptInsertParams struct {
	ID              string
	GenerationJobID string
	ProviderID      string
	ModelID         *string
	ProviderRouteID *string
	AttemptNumber   int32
}

// ProviderAttempt is the domain view of a single provider call.
type ProviderAttempt struct {
	ID              string
	GenerationJobID string
	ProviderID      string
	ModelID         *string
	ProviderRouteID *string
	AttemptNumber   int32
	Status          string
}

// AssetPackItemInsertParams captures one delivered pack variant (ADR-008:
// the worker, not the provider adapter, writes asset_pack_items).
type AssetPackItemInsertParams struct {
	ID            string
	AssetPackID   string
	VisualAssetID string
	VariantKey    string
	SortOrder     int32
}

// AssetPackItem is the domain view of an asset_pack_items row.
type AssetPackItem struct {
	ID            string
	AssetPackID   string
	VisualAssetID string
	VariantKey    string
	SortOrder     int32
}

// CostEventInsertParams captures a single cost-event row for telemetry.
type CostEventInsertParams struct {
	ID                string
	TenantID          string
	JobID             *string
	AssetID           *string
	CostReservationID *string
	TokenID           *string
	ProviderID        *string
	ModelID           *string
	ProviderAttemptID *string
	Operation         string
	EstimatedCostUSD  *string
	ActualCostUSD     *string
	DurationMs        *int32
	Status            string
	Metadata          []byte
}

// PersistSuccessParams is the provider-attempt and cost-event bookkeeping that
// belongs in the same transaction as a guarded output/status write. The
// optional production seam closes the crash window between terminal output
// persistence and the separate success bookkeeping calls.
type PersistSuccessParams struct {
	AttemptID string
	LatencyMs int32
	CostEvent CostEventInsertParams
}

// GuardedSuccessPersister is an optional production extension. Lightweight
// repository fakes keep the original guarded methods; the worker uses this
// interface when available so output, terminal status, attempt success, and
// the success cost event commit atomically.
type GuardedSuccessPersister interface {
	InsertFinalAssetAndCompleteJobWithSuccess(ctx context.Context, jobID, tenantID string, params assets.InsertParams, forced bool, slot assets.ArtifactSlot, success PersistSuccessParams) (assets.VisualAsset, PersistOutcome, error)
	InsertPreviewAssetAndMarkPreviewReadyWithSuccess(ctx context.Context, jobID, tenantID string, params assets.InsertParams, success PersistSuccessParams) (assets.VisualAsset, PersistOutcome, error)
}

// ReservationBoundStateUpdater is the production CAS extension used by queue
// tasks. It keeps the reservation/run token in every mutable job transition so
// a delayed task cannot claim or terminalize a later admin retry. Lightweight
// repositories may continue to implement the legacy methods only.
type ReservationBoundStateUpdater interface {
	MarkRunningForReservation(ctx context.Context, id, tenantID, reservationID string) (Job, error)
	ClaimPreviewFinalizationForReservation(ctx context.Context, id, tenantID, reservationID string) (Job, error)
	MarkPreviewReadyForReservation(ctx context.Context, id, tenantID string, previewAssetIDs []string, reservationID string) (Job, error)
	MarkCompletedForReservation(ctx context.Context, id, tenantID string, finalAssetIDs []string, reservationID string) (Job, error)
	MarkFailedForReservation(ctx context.Context, id, tenantID, errorCode, errorMessage string, retryable bool, reservationID string) (Job, error)
}

// ReservationBoundSuccessPersister is the reservation-aware counterpart to
// GuardedSuccessPersister. The job row lock and output/status write use the
// same captured reservation token.
type ReservationBoundSuccessPersister interface {
	InsertFinalAssetAndCompleteJobWithSuccessForReservation(ctx context.Context, jobID, tenantID, reservationID string, params assets.InsertParams, forced bool, slot assets.ArtifactSlot, success PersistSuccessParams) (assets.VisualAsset, PersistOutcome, error)
	InsertPreviewAssetAndMarkPreviewReadyWithSuccessForReservation(ctx context.Context, jobID, tenantID, reservationID string, params assets.InsertParams, success PersistSuccessParams) (assets.VisualAsset, PersistOutcome, error)
}

// PackSuccessPersister atomically couples a generated pack item with its
// provider-attempt success row and per-cell cost event.
type PackSuccessPersister interface {
	InsertPackItemWithAssetAndSuccess(ctx context.Context, asset assets.InsertParams, item AssetPackItemInsertParams, success PersistSuccessParams) error
	InsertPackItemWithAssetSupersedingAndSuccess(ctx context.Context, asset assets.InsertParams, item AssetPackItemInsertParams, slot assets.VariantSlot, success PersistSuccessParams) error
}

// ProviderAttemptCostUpdater is an optional extension implemented by the
// Postgres repository. It keeps provider billing metadata out of the broad
// Repository test seam while allowing production workers to reconcile it.
type ProviderAttemptCostUpdater interface {
	UpdateProviderAttemptCost(ctx context.Context, id, providerRequestID string, actualCostUSD *string, currency string) error
}

type Repository interface {
	Insert(ctx context.Context, params InsertParams) (Job, error)
	GetByIDForTenant(ctx context.Context, id, tenantID string) (Job, error)
	GetByID(ctx context.Context, id string) (Job, error)
	MarkRunning(ctx context.Context, id, tenantID string) (Job, error)
	// MarkPreviewReady is the Phase 7B two-phase intermediate transition: flip
	// the job to preview_ready and record preview_asset_ids. The worker commits
	// it BEFORE final generation begins (a separate DB transaction from final
	// persistence) so the preview state is externally observable before the
	// final asset exists.
	MarkPreviewReady(ctx context.Context, id, tenantID string, previewAssetIDs []string) (Job, error)
	MarkCompleted(ctx context.Context, id, tenantID string, finalAssetIDs []string) (Job, error)
	MarkFailed(ctx context.Context, id, tenantID, errorCode, errorMessage string, retryable bool) (Job, error)

	// InsertFinalAssetAndCompleteJobIfNotCancelled and
	// InsertPreviewAssetAndMarkPreviewReadyIfNotCancelled are the Phase 7C-1a
	// guarded worker output writes: each locks the generation_jobs row, skips
	// the write if the job is cancelled, and otherwise inserts the asset and
	// transitions the job in one transaction. They close the in-flight cancel
	// race so a cancelled job can never have an output asset attached.
	InsertFinalAssetAndCompleteJobIfNotCancelled(ctx context.Context, jobID, tenantID string, params assets.InsertParams, forced bool, slot assets.ArtifactSlot) (assets.VisualAsset, PersistOutcome, error)
	InsertPreviewAssetAndMarkPreviewReadyIfNotCancelled(ctx context.Context, jobID, tenantID string, params assets.InsertParams) (assets.VisualAsset, PersistOutcome, error)
	InsertProviderAttempt(ctx context.Context, params ProviderAttemptInsertParams) (ProviderAttempt, error)
	MarkProviderAttemptSucceeded(ctx context.Context, id string, latencyMs int32) error
	MarkProviderAttemptFailed(ctx context.Context, id, errorCode, errorMessage string, latencyMs int32) error
	CountProviderAttempts(ctx context.Context, jobID string) (int32, error)
	InsertCostEvent(ctx context.Context, params CostEventInsertParams) error

	// Pack fan-out (Phase 5A). The pack row itself is created in the jobs
	// service's create transaction; the worker only moves its status and
	// appends items. InsertPackItemWithAsset writes the visual_assets row
	// and its asset_pack_items row in one transaction so a delivered variant
	// is observable atomically — a failed item insert rolls the asset back
	// instead of leaving an orphan the retry path can't see.
	UpdateAssetPackStatus(ctx context.Context, packID, status string) error
	// UpdateAssetPackCompleteness records the pack's final delivered-vs-missing
	// required roles (Phase 6A3) so a consumer can read completeness directly off
	// asset_packs. The worker calls it at the terminal step.
	UpdateAssetPackCompleteness(ctx context.Context, packID string, delivered, missing []string) error
	InsertPackItemWithAsset(ctx context.Context, asset assets.InsertParams, item AssetPackItemInsertParams) error
	// InsertPackItemWithAssetSuperseding is the Phase 6A4 forced-regeneration
	// pack-item write: same atomic asset + asset_pack_items transaction as
	// InsertPackItemWithAsset, but it first archives the prior ready asset of the
	// role's exact slot and versions the new one (prior_max + 1), all under a slot
	// advisory lock. A forced pack has no reused items, so there is no skip logic
	// here — every role takes this path.
	InsertPackItemWithAssetSuperseding(ctx context.Context, asset assets.InsertParams, item AssetPackItemInsertParams, slot assets.VariantSlot) error
	InsertAssetPackItem(ctx context.Context, params AssetPackItemInsertParams) error
	ListAssetPackItems(ctx context.Context, packID string) ([]AssetPackItem, error)
	// ListAssetPackItemsForTenant is the request-path pack-items read: it runs
	// inside the tenant executor so the read is scoped by app.current_tenant
	// under RLS (asset_pack_items has no tenant_id column of its own; its
	// policy joins to the parent asset_pack, so the query relies on the GUC).
	// Without it this read returns ZERO rows under the RLS-enforced API role
	// even for the owning tenant. The worker keeps ListAssetPackItems on its
	// BYPASSRLS system pool, where the GUC is irrelevant. Ported from the
	// stranded fix on claude/zealous-ritchie-84qd37 (e14153f).
	ListAssetPackItemsForTenant(ctx context.Context, packID, tenantID string) ([]AssetPackItem, error)
}

// errPackJobNotActive is returned by the atomic pack-item transaction when
// cancellation or another terminal worker won the generation-job row lock.
// The worker stops fan-out rather than treating that state as an ordinary item
// failure and later overwriting pack completeness/status.
var errPackJobNotActive = errors.New("jobs: pack job is no longer active")

type pgRepository struct {
	q    *dbgen.Queries
	pool *pgxpool.Pool
}

var _ GuardedSuccessPersister = (*pgRepository)(nil)
var _ PackSuccessPersister = (*pgRepository)(nil)

func NewRepository(pool *pgxpool.Pool) Repository {
	return &pgRepository{q: dbgen.New(pool), pool: pool}
}

func (r *pgRepository) Insert(ctx context.Context, params InsertParams) (Job, error) {
	payload, err := marshalPayload(params.InputPayload)
	if err != nil {
		return Job{}, err
	}
	row, err := r.q.InsertGenerationJob(ctx, dbgen.InsertGenerationJobParams{
		ID:                 params.ID,
		TenantID:           params.TenantID,
		WorldID:            params.WorldID,
		JobType:            params.JobType,
		RequestedByTokenID: params.RequestedByTokenID,
		InputPayload:       payload,
		FallbackPolicy:     params.FallbackPolicy,
		CacheResult:        params.CacheResult,
		// Governance/render/subject columns - not set on the repository path;
		// nil/zero persists NULL (existing behavior unchanged).
		GovernanceEnvelope:   nil,
		ClassificationID:     nil,
		Visibility:           nil,
		ContentClass:         nil,
		AuthorizedBy:         nil,
		GovernanceVerifiedAt: pgtype.Timestamptz{},
		Intent:               nil,
		TransformOnly:        nil,
		Transform:            nil,
		MaxMegapixels:        pgtype.Numeric{},
		Lazy:                 nil,
		AnchorAssetID:        nil,
		DeriveFrom:           nil,
	})
	if err != nil {
		return Job{}, err
	}
	return rowToJob(row), nil
}

// GetByIDForTenant is the request-path job read. It runs inside a tenant
// executor (Phase 7C-3) so the read is scoped by app.current_tenant under RLS:
// a job belonging to another tenant is invisible at the DB layer and surfaces
// as ErrNotFound (→ 404), independent of the app-level tenant predicate that
// also remains. On a BYPASSRLS/superuser pool the GUC is harmless and the
// predicate alone still scopes the read.
func (r *pgRepository) GetByIDForTenant(ctx context.Context, id, tenantID string) (Job, error) {
	var job Job
	err := appdb.WithTenant(ctx, r.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		row, err := dbgen.New(tx).GetGenerationJobByID(ctx, dbgen.GetGenerationJobByIDParams{
			ID:       id,
			TenantID: tenantID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		job = rowToJob(row)
		return nil
	})
	if err != nil {
		return Job{}, err
	}
	return job, nil
}

func (r *pgRepository) GetByID(ctx context.Context, id string) (Job, error) {
	row, err := r.q.GetGenerationJobByIDUnchecked(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Job{}, ErrNotFound
		}
		return Job{}, err
	}
	return rowToJob(row), nil
}

func (r *pgRepository) MarkRunning(ctx context.Context, id, tenantID string) (Job, error) {
	row, err := r.q.MarkGenerationJobRunning(ctx, dbgen.MarkGenerationJobRunningParams{
		ID:       id,
		TenantID: tenantID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Job{}, ErrNotFound
		}
		return Job{}, err
	}
	return rowToJob(row), nil
}

func (r *pgRepository) MarkRunningForReservation(ctx context.Context, id, tenantID, reservationID string) (Job, error) {
	row, err := r.q.MarkGenerationJobRunning(ctx, dbgen.MarkGenerationJobRunningParams{
		ID:                id,
		TenantID:          tenantID,
		CostReservationID: &reservationID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Job{}, ErrNotFound
		}
		return Job{}, err
	}
	return rowToJob(row), nil
}

// ClaimPreviewFinalization atomically marks the non-lazy preview final phase
// as claimed. A missing row means another first delivery owns the final call or
// the job became terminal.
func (r *pgRepository) ClaimPreviewFinalization(ctx context.Context, id, tenantID string) (Job, error) {
	row, err := r.q.ClaimPreviewFinalization(ctx, dbgen.ClaimPreviewFinalizationParams{
		ID:       id,
		TenantID: tenantID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Job{}, ErrNotFound
		}
		return Job{}, err
	}
	return rowToJob(row), nil
}

func (r *pgRepository) ClaimPreviewFinalizationForReservation(ctx context.Context, id, tenantID, reservationID string) (Job, error) {
	row, err := r.q.ClaimPreviewFinalization(ctx, dbgen.ClaimPreviewFinalizationParams{
		ID:                id,
		TenantID:          tenantID,
		CostReservationID: &reservationID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Job{}, ErrNotFound
		}
		return Job{}, err
	}
	return rowToJob(row), nil
}

func (r *pgRepository) MarkPreviewReady(ctx context.Context, id, tenantID string, previewAssetIDs []string) (Job, error) {
	if previewAssetIDs == nil {
		previewAssetIDs = []string{}
	}
	row, err := r.q.MarkGenerationJobPreviewReady(ctx, dbgen.MarkGenerationJobPreviewReadyParams{
		ID:              id,
		TenantID:        tenantID,
		PreviewAssetIds: previewAssetIDs,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Job{}, ErrNotFound
		}
		return Job{}, err
	}
	return rowToJob(row), nil
}

func (r *pgRepository) MarkPreviewReadyForReservation(ctx context.Context, id, tenantID string, previewAssetIDs []string, reservationID string) (Job, error) {
	if previewAssetIDs == nil {
		previewAssetIDs = []string{}
	}
	row, err := r.q.MarkGenerationJobPreviewReady(ctx, dbgen.MarkGenerationJobPreviewReadyParams{
		ID:                id,
		TenantID:          tenantID,
		PreviewAssetIds:   previewAssetIDs,
		CostReservationID: &reservationID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Job{}, ErrNotFound
		}
		return Job{}, err
	}
	return rowToJob(row), nil
}

func (r *pgRepository) MarkCompleted(ctx context.Context, id, tenantID string, finalAssetIDs []string) (Job, error) {
	row, err := r.q.MarkGenerationJobCompleted(ctx, dbgen.MarkGenerationJobCompletedParams{
		ID:            id,
		TenantID:      tenantID,
		FinalAssetIds: finalAssetIDs,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Job{}, ErrNotFound
		}
		return Job{}, err
	}
	return rowToJob(row), nil
}

func (r *pgRepository) MarkCompletedForReservation(ctx context.Context, id, tenantID string, finalAssetIDs []string, reservationID string) (Job, error) {
	row, err := r.q.MarkGenerationJobCompleted(ctx, dbgen.MarkGenerationJobCompletedParams{
		ID:                id,
		TenantID:          tenantID,
		FinalAssetIds:     finalAssetIDs,
		CostReservationID: &reservationID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Job{}, ErrNotFound
		}
		return Job{}, err
	}
	return rowToJob(row), nil
}

func (r *pgRepository) MarkFailed(ctx context.Context, id, tenantID, errorCode, errorMessage string, retryable bool) (Job, error) {
	ec := errorCode
	em := errorMessage
	rb := retryable
	row, err := r.q.MarkGenerationJobFailed(ctx, dbgen.MarkGenerationJobFailedParams{
		ID:           id,
		TenantID:     tenantID,
		ErrorCode:    &ec,
		ErrorMessage: &em,
		Retryable:    &rb,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Job{}, ErrNotFound
		}
		return Job{}, err
	}
	return rowToJob(row), nil
}

func (r *pgRepository) MarkFailedForReservation(ctx context.Context, id, tenantID, errorCode, errorMessage string, retryable bool, reservationID string) (Job, error) {
	ec := errorCode
	em := errorMessage
	rb := retryable
	row, err := r.q.MarkGenerationJobFailed(ctx, dbgen.MarkGenerationJobFailedParams{
		ID:                id,
		TenantID:          tenantID,
		ErrorCode:         &ec,
		ErrorMessage:      &em,
		Retryable:         &rb,
		CostReservationID: &reservationID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Job{}, ErrNotFound
		}
		return Job{}, err
	}
	return rowToJob(row), nil
}

func (r *pgRepository) InsertProviderAttempt(ctx context.Context, params ProviderAttemptInsertParams) (ProviderAttempt, error) {
	row, err := r.q.InsertProviderAttempt(ctx, dbgen.InsertProviderAttemptParams{
		ID:              params.ID,
		GenerationJobID: params.GenerationJobID,
		ProviderID:      params.ProviderID,
		ModelID:         params.ModelID,
		ProviderRouteID: params.ProviderRouteID,
		AttemptNumber:   params.AttemptNumber,
	})
	if err != nil {
		return ProviderAttempt{}, err
	}
	return ProviderAttempt{
		ID:              row.ID,
		GenerationJobID: row.GenerationJobID,
		ProviderID:      row.ProviderID,
		ModelID:         row.ModelID,
		ProviderRouteID: row.ProviderRouteID,
		AttemptNumber:   row.AttemptNumber,
		Status:          row.Status,
	}, nil
}

func (r *pgRepository) MarkProviderAttemptSucceeded(ctx context.Context, id string, latencyMs int32) error {
	return markProviderAttemptSucceededWithQueries(ctx, r.q, id, latencyMs)
}

func markProviderAttemptSucceededWithQueries(ctx context.Context, q *dbgen.Queries, id string, latencyMs int32) error {
	lm := latencyMs
	return q.MarkProviderAttemptSucceeded(ctx, dbgen.MarkProviderAttemptSucceededParams{
		ID:        id,
		LatencyMs: &lm,
	})
}

// UpdateProviderAttemptCost persists provider-side billing metadata when an
// adapter reports it. This is an optional repository extension so lightweight
// worker fakes do not need to implement provider reconciliation to test the
// generation path.
func (r *pgRepository) UpdateProviderAttemptCost(ctx context.Context, id, providerRequestID string, actualCostUSD *string, currency string) error {
	actual := pgtype.Numeric{}
	if actualCostUSD != nil {
		if err := actual.Scan(*actualCostUSD); err != nil {
			return err
		}
	}
	if currency == "" {
		currency = "USD"
	}
	return r.q.UpdateProviderAttemptCost(ctx, dbgen.UpdateProviderAttemptCostParams{
		ID:                id,
		ProviderRequestID: strPtrOrNil(providerRequestID),
		ActualCost:        actual,
		Currency:          currency,
	})
}

func (r *pgRepository) MarkProviderAttemptFailed(ctx context.Context, id, errorCode, errorMessage string, latencyMs int32) error {
	ec := errorCode
	em := errorMessage
	lm := latencyMs
	return r.q.MarkProviderAttemptFailed(ctx, dbgen.MarkProviderAttemptFailedParams{
		ID:           id,
		ErrorCode:    &ec,
		ErrorMessage: &em,
		LatencyMs:    &lm,
	})
}

func (r *pgRepository) CountProviderAttempts(ctx context.Context, jobID string) (int32, error) {
	return r.q.CountProviderAttemptsForJob(ctx, jobID)
}

func (r *pgRepository) UpdateAssetPackStatus(ctx context.Context, packID, status string) error {
	return r.q.UpdateAssetPackStatus(ctx, dbgen.UpdateAssetPackStatusParams{
		ID:     packID,
		Status: status,
	})
}

func (r *pgRepository) UpdateAssetPackStatusForJob(ctx context.Context, packID, jobID, status string) error {
	return r.q.UpdateAssetPackStatusForJob(ctx, dbgen.UpdateAssetPackStatusForJobParams{
		ID:              packID,
		GenerationJobID: &jobID,
		Status:          status,
	})
}

func (r *pgRepository) UpdateAssetPackCompleteness(ctx context.Context, packID string, delivered, missing []string) error {
	if delivered == nil {
		delivered = []string{}
	}
	if missing == nil {
		missing = []string{}
	}
	return r.q.UpdateAssetPackCompleteness(ctx, dbgen.UpdateAssetPackCompletenessParams{
		ID:             packID,
		DeliveredRoles: delivered,
		MissingRoles:   missing,
	})
}

func (r *pgRepository) UpdateAssetPackCompletenessForJob(ctx context.Context, packID, jobID string, delivered, missing []string) error {
	if delivered == nil {
		delivered = []string{}
	}
	if missing == nil {
		missing = []string{}
	}
	return r.q.UpdateAssetPackCompletenessForJob(ctx, dbgen.UpdateAssetPackCompletenessForJobParams{
		ID:              packID,
		GenerationJobID: &jobID,
		DeliveredRoles:  delivered,
		MissingRoles:    missing,
	})
}

func lockActivePackJob(ctx context.Context, q *dbgen.Queries, asset assets.InsertParams) error {
	if asset.GenerationJobID == nil || *asset.GenerationJobID == "" {
		return nil
	}
	status, err := q.LockGenerationJobForUpdate(ctx, dbgen.LockGenerationJobForUpdateParams{
		ID:       *asset.GenerationJobID,
		TenantID: asset.TenantID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errPackJobNotActive
		}
		return err
	}
	if status != "running" {
		return errPackJobNotActive
	}
	return nil
}

func (r *pgRepository) InsertPackItemWithAsset(ctx context.Context, asset assets.InsertParams, item AssetPackItemInsertParams) error {
	return r.insertPackItemWithAsset(ctx, asset, item, nil)
}

func (r *pgRepository) InsertPackItemWithAssetAndSuccess(ctx context.Context, asset assets.InsertParams, item AssetPackItemInsertParams, success PersistSuccessParams) error {
	return r.insertPackItemWithAsset(ctx, asset, item, &success)
}

func (r *pgRepository) insertPackItemWithAsset(ctx context.Context, asset assets.InsertParams, item AssetPackItemInsertParams, success *PersistSuccessParams) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
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
	if err := lockActivePackJob(ctx, q, asset); err != nil {
		return err
	}
	if _, err := assets.InsertWithQueries(ctx, q, asset); err != nil {
		return err
	}
	if err := q.InsertAssetPackItem(ctx, dbgen.InsertAssetPackItemParams{
		ID:            item.ID,
		AssetPackID:   item.AssetPackID,
		VisualAssetID: item.VisualAssetID,
		VariantKey:    item.VariantKey,
		SortOrder:     item.SortOrder,
	}); err != nil {
		return err
	}
	if success != nil {
		if err := persistSuccessWithQueries(ctx, q, *success, asset.ID); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	committed = true
	return nil
}

func (r *pgRepository) InsertPackItemWithAssetSuperseding(ctx context.Context, asset assets.InsertParams, item AssetPackItemInsertParams, slot assets.VariantSlot) error {
	return r.insertPackItemWithAssetSuperseding(ctx, asset, item, slot, nil)
}

func (r *pgRepository) InsertPackItemWithAssetSupersedingAndSuccess(ctx context.Context, asset assets.InsertParams, item AssetPackItemInsertParams, slot assets.VariantSlot, success PersistSuccessParams) error {
	return r.insertPackItemWithAssetSuperseding(ctx, asset, item, slot, &success)
}

func (r *pgRepository) insertPackItemWithAssetSuperseding(ctx context.Context, asset assets.InsertParams, item AssetPackItemInsertParams, slot assets.VariantSlot, success *PersistSuccessParams) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
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
	if err := lockActivePackJob(ctx, q, asset); err != nil {
		return err
	}
	// Supersede + insert the new ready asset (versioned, prior ready rows
	// archived) under the slot lock, then append the pack item — all in one
	// transaction so a delivered regenerated variant is observable atomically.
	if _, err := assets.SupersedeVariantSlotWithQueries(ctx, q, asset, slot); err != nil {
		return err
	}
	if err := q.InsertAssetPackItem(ctx, dbgen.InsertAssetPackItemParams{
		ID:            item.ID,
		AssetPackID:   item.AssetPackID,
		VisualAssetID: item.VisualAssetID,
		VariantKey:    item.VariantKey,
		SortOrder:     item.SortOrder,
	}); err != nil {
		return err
	}
	if success != nil {
		if err := persistSuccessWithQueries(ctx, q, *success, asset.ID); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	committed = true
	return nil
}

func (r *pgRepository) InsertAssetPackItem(ctx context.Context, params AssetPackItemInsertParams) error {
	return r.q.InsertAssetPackItem(ctx, dbgen.InsertAssetPackItemParams{
		ID:            params.ID,
		AssetPackID:   params.AssetPackID,
		VisualAssetID: params.VisualAssetID,
		VariantKey:    params.VariantKey,
		SortOrder:     params.SortOrder,
	})
}

func (r *pgRepository) ListAssetPackItems(ctx context.Context, packID string) ([]AssetPackItem, error) {
	rows, err := r.q.ListAssetPackItems(ctx, packID)
	if err != nil {
		return nil, err
	}
	return rowsToAssetPackItems(rows), nil
}

// ListAssetPackItemsForTenant runs the pack-items read inside a tenant
// executor (WithTenant → set_config app.current_tenant), mirroring
// GetByIDForTenant, so the asset_pack_items parent-join RLS policy admits the
// owning tenant's rows on the RLS-enforced API pool.
func (r *pgRepository) ListAssetPackItemsForTenant(ctx context.Context, packID, tenantID string) ([]AssetPackItem, error) {
	var out []AssetPackItem
	err := appdb.WithTenant(ctx, r.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := dbgen.New(tx).ListAssetPackItems(ctx, packID)
		if err != nil {
			return err
		}
		out = rowsToAssetPackItems(rows)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func rowsToAssetPackItems(rows []dbgen.AssetPackItem) []AssetPackItem {
	out := make([]AssetPackItem, 0, len(rows))
	for _, row := range rows {
		out = append(out, AssetPackItem{
			ID:            row.ID,
			AssetPackID:   row.AssetPackID,
			VisualAssetID: row.VisualAssetID,
			VariantKey:    row.VariantKey,
			SortOrder:     row.SortOrder,
		})
	}
	return out
}

func (r *pgRepository) InsertCostEvent(ctx context.Context, params CostEventInsertParams) error {
	return insertCostEventWithQueries(ctx, r.q, params)
}

func insertCostEventWithQueries(ctx context.Context, q *dbgen.Queries, params CostEventInsertParams) error {
	estimated := pgtype.Numeric{}
	if params.EstimatedCostUSD != nil {
		if err := estimated.Scan(*params.EstimatedCostUSD); err != nil {
			return err
		}
	}
	actual := pgtype.Numeric{}
	if params.ActualCostUSD != nil {
		if err := actual.Scan(*params.ActualCostUSD); err != nil {
			return err
		}
	}
	metadata := params.Metadata
	if len(metadata) == 0 {
		metadata = []byte(`{}`)
	}
	return q.InsertGenerationCostEvent(ctx, dbgen.InsertGenerationCostEventParams{
		ID:                params.ID,
		TenantID:          params.TenantID,
		JobID:             params.JobID,
		AssetID:           params.AssetID,
		CostReservationID: params.CostReservationID,
		TokenID:           params.TokenID,
		ProviderID:        params.ProviderID,
		ModelID:           params.ModelID,
		ProviderAttemptID: params.ProviderAttemptID,
		Operation:         params.Operation,
		EstimatedCostUsd:  estimated,
		ActualCostUsd:     actual,
		DurationMs:        params.DurationMs,
		Status:            params.Status,
		Metadata:          metadata,
	})
}

func strPtrOrNil(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func marshalPayload(payload map[string]any) ([]byte, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	return json.Marshal(payload)
}

// JobFromGenerationRow converts a dbgen generation_jobs row into the domain Job
// view. Exposed so the admin job-control package (Phase 7C-1), which runs its
// own cancel/retry transactions over generation_jobs, can return the same Job
// shape the repository does without duplicating the column mapping.
func JobFromGenerationRow(row dbgen.GenerationJob) Job {
	return rowToJob(row)
}

func rowToJob(row dbgen.GenerationJob) Job {
	job := Job{
		ID:                 row.ID,
		TenantID:           row.TenantID,
		WorldID:            row.WorldID,
		JobType:            row.JobType,
		Status:             row.Status,
		RequestedByTokenID: row.RequestedByTokenID,
		VisualIdentityID:   row.VisualIdentityID,
		AssetPackID:        row.AssetPackID,
		FallbackPolicy:     row.FallbackPolicy,
		CacheResult:        row.CacheResult,
		PreviewAssetIds:    row.PreviewAssetIds,
		FinalAssetIds:      row.FinalAssetIds,
		ErrorCode:          row.ErrorCode,
		ErrorMessage:       row.ErrorMessage,
		Retryable:          row.Retryable,
		CostReservationID:  row.CostReservationID,
		CostEstimateUSD:    numericPtr(row.CostEstimateUsd),
		ActualCostUSD:      numericPtr(row.ActualCostUsd),
		CreatedAt:          unwrapTimestamp(row.CreatedAt),
		UpdatedAt:          unwrapTimestamp(row.UpdatedAt),
	}
	if len(row.InputPayload) > 0 {
		_ = json.Unmarshal(row.InputPayload, &job.InputPayload)
	}
	if row.StartedAt.Valid {
		t := row.StartedAt.Time
		job.StartedAt = &t
	}
	if row.CompletedAt.Valid {
		t := row.CompletedAt.Time
		job.CompletedAt = &t
	}
	return job
}

// numericPtr renders a nullable pgtype.Numeric column as its decimal text
// form for the API response, matching cost.numericText's convention. Nil for
// SQL NULL (a job that hasn't been estimated/committed yet) rather than "0" —
// callers must not read a nil cost as free.
func numericPtr(n pgtype.Numeric) *string {
	if !n.Valid {
		return nil
	}
	value, err := n.Value()
	if err != nil || value == nil {
		return nil
	}
	s := fmt.Sprint(value)
	return &s
}

func unwrapTimestamp(t pgtype.Timestamptz) time.Time {
	if !t.Valid {
		return time.Time{}
	}
	return t.Time
}
