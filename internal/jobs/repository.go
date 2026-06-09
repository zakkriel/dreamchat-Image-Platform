// Package jobs owns generation_jobs lifecycle, provider_attempts, and the
// asynq enqueue/handler wiring. Handlers go through this package; sqlc
// types stay inside it.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

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
	InputPayload       map[string]any
	FallbackPolicy     *string
	CacheResult        *string
	PreviewAssetIds    []string
	FinalAssetIds      []string
	ErrorCode          *string
	ErrorMessage       *string
	Retryable          *bool
	CreatedAt          time.Time
	UpdatedAt          time.Time
	StartedAt          *time.Time
	CompletedAt        *time.Time
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
	AttemptNumber   int32
}

// ProviderAttempt is the domain view of a single provider call.
type ProviderAttempt struct {
	ID              string
	GenerationJobID string
	ProviderID      string
	AttemptNumber   int32
	Status          string
}

// CostEventInsertParams captures a single cost-event row for telemetry.
type CostEventInsertParams struct {
	ID                string
	TenantID          string
	JobID             *string
	AssetID           *string
	TokenID           *string
	ProviderID        *string
	ProviderAttemptID *string
	Operation         string
	DurationMs        *int32
	Status            string
}

type Repository interface {
	Insert(ctx context.Context, params InsertParams) (Job, error)
	GetByIDForTenant(ctx context.Context, id, tenantID string) (Job, error)
	GetByID(ctx context.Context, id string) (Job, error)
	MarkRunning(ctx context.Context, id, tenantID string) (Job, error)
	MarkCompleted(ctx context.Context, id, tenantID string, finalAssetIDs []string) (Job, error)
	MarkFailed(ctx context.Context, id, tenantID, errorCode, errorMessage string, retryable bool) (Job, error)
	InsertProviderAttempt(ctx context.Context, params ProviderAttemptInsertParams) (ProviderAttempt, error)
	MarkProviderAttemptSucceeded(ctx context.Context, id string, latencyMs int32) error
	MarkProviderAttemptFailed(ctx context.Context, id, errorCode, errorMessage string, latencyMs int32) error
	CountProviderAttempts(ctx context.Context, jobID string) (int32, error)
	InsertCostEvent(ctx context.Context, params CostEventInsertParams) error
}

type pgRepository struct {
	q *dbgen.Queries
}

func NewRepository(pool *pgxpool.Pool) Repository {
	return &pgRepository{q: dbgen.New(pool)}
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
	})
	if err != nil {
		return Job{}, err
	}
	return rowToJob(row), nil
}

func (r *pgRepository) GetByIDForTenant(ctx context.Context, id, tenantID string) (Job, error) {
	row, err := r.q.GetGenerationJobByID(ctx, dbgen.GetGenerationJobByIDParams{
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

func (r *pgRepository) InsertProviderAttempt(ctx context.Context, params ProviderAttemptInsertParams) (ProviderAttempt, error) {
	row, err := r.q.InsertProviderAttempt(ctx, dbgen.InsertProviderAttemptParams{
		ID:              params.ID,
		GenerationJobID: params.GenerationJobID,
		ProviderID:      params.ProviderID,
		AttemptNumber:   params.AttemptNumber,
	})
	if err != nil {
		return ProviderAttempt{}, err
	}
	return ProviderAttempt{
		ID:              row.ID,
		GenerationJobID: row.GenerationJobID,
		ProviderID:      row.ProviderID,
		AttemptNumber:   row.AttemptNumber,
		Status:          row.Status,
	}, nil
}

func (r *pgRepository) MarkProviderAttemptSucceeded(ctx context.Context, id string, latencyMs int32) error {
	lm := latencyMs
	return r.q.MarkProviderAttemptSucceeded(ctx, dbgen.MarkProviderAttemptSucceededParams{
		ID:        id,
		LatencyMs: &lm,
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

func (r *pgRepository) InsertCostEvent(ctx context.Context, params CostEventInsertParams) error {
	return r.q.InsertGenerationCostEvent(ctx, dbgen.InsertGenerationCostEventParams{
		ID:                params.ID,
		TenantID:          params.TenantID,
		JobID:             params.JobID,
		AssetID:           params.AssetID,
		TokenID:           params.TokenID,
		ProviderID:        params.ProviderID,
		ProviderAttemptID: params.ProviderAttemptID,
		Operation:         params.Operation,
		DurationMs:        params.DurationMs,
		Status:            params.Status,
	})
}

func marshalPayload(payload map[string]any) ([]byte, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	return json.Marshal(payload)
}

func rowToJob(row dbgen.GenerationJob) Job {
	job := Job{
		ID:                 row.ID,
		TenantID:           row.TenantID,
		WorldID:            row.WorldID,
		JobType:            row.JobType,
		Status:             row.Status,
		RequestedByTokenID: row.RequestedByTokenID,
		FallbackPolicy:     row.FallbackPolicy,
		CacheResult:        row.CacheResult,
		PreviewAssetIds:    row.PreviewAssetIds,
		FinalAssetIds:      row.FinalAssetIds,
		ErrorCode:          row.ErrorCode,
		ErrorMessage:       row.ErrorMessage,
		Retryable:          row.Retryable,
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

func unwrapTimestamp(t pgtype.Timestamptz) time.Time {
	if !t.Valid {
		return time.Time{}
	}
	return t.Time
}
