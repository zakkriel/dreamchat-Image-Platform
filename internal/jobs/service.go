package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zakkriel/drchat-image-platform/internal/db/dbgen"
	"github.com/zakkriel/drchat-image-platform/internal/ids"
)

// IdempotencyTTL is how long an idempotency_keys row stays useful for
// replay. Matches the 24h figure in docs/api/idempotency.md.
const IdempotencyTTL = 24 * time.Hour

var (
	// ErrIdempotencyConflict is returned when the supplied idempotency key
	// was previously used for a different endpoint or a different request
	// body. The handler maps this to 409 idempotency_conflict.
	ErrIdempotencyConflict = errors.New("jobs: idempotency conflict")

	// ErrEnqueueFailed signals the caller that the generation_jobs row was
	// written and rolled to status=failed because the queue rejected the
	// task. The handler maps this to 500.
	ErrEnqueueFailed = errors.New("jobs: enqueue failed")
)

// CreateAndEnqueueParams carries everything a handler needs to provide to
// the service to land a generation job.
type CreateAndEnqueueParams struct {
	TenantID           string
	RequestedByTokenID string
	JobType            string
	WorldID            string
	InputPayload       map[string]any
	FallbackPolicy     string
	CacheResult        string

	// Idempotency context. When IdempotencyKey is empty the service skips
	// the idempotency table altogether and creates a fresh job.
	IdempotencyKey string
	Endpoint       string
	RequestHash    string
}

// CreateResult is the service's return shape. Replayed is true when the
// idempotency layer found a prior row and the caller should report the
// existing job_id instead of treating the response as a fresh insert.
type CreateResult struct {
	JobID    string
	Replayed bool
}

// Creator is the handler-facing interface. Tests stub this.
type Creator interface {
	CreateAndEnqueue(ctx context.Context, params CreateAndEnqueueParams) (CreateResult, error)
}

// Service implements Creator against Postgres + the asynq Enqueuer.
type Service struct {
	pool     *pgxpool.Pool
	enqueuer Enqueuer
	ttl      time.Duration
	now      func() time.Time
}

func NewService(pool *pgxpool.Pool, enqueuer Enqueuer) *Service {
	return &Service{
		pool:     pool,
		enqueuer: enqueuer,
		ttl:      IdempotencyTTL,
		now:      time.Now,
	}
}

// CreateAndEnqueue is the atomic create + idempotency + enqueue path.
//
// When IdempotencyKey is non-empty the service runs the generation_jobs
// insert and the idempotency_keys insert inside a single transaction.
// ON CONFLICT DO NOTHING on (token_id, key) means the loser of a race rolls
// back its speculative generation_jobs row, then reads the winner's row and
// reports the winner's job_id (or 409 on body/endpoint mismatch). Only the
// winner enqueues a task.
//
// If the enqueue call itself fails *after* a successful commit, the job is
// marked failed (status=failed, retryable=false) so the row doesn't sit at
// status=queued forever. The error is returned to the handler as
// ErrEnqueueFailed so the response is 500.
func (s *Service) CreateAndEnqueue(ctx context.Context, params CreateAndEnqueueParams) (CreateResult, error) {
	payload, err := marshalPayload(params.InputPayload)
	if err != nil {
		return CreateResult{}, err
	}

	if params.IdempotencyKey == "" {
		jobID := ids.NewGenerationJobID()
		if err := s.insertJob(ctx, dbgen.New(s.pool), jobID, params, payload); err != nil {
			return CreateResult{}, fmt.Errorf("insert job: %w", err)
		}
		if err := s.enqueue(ctx, jobID, params.TenantID); err != nil {
			return CreateResult{}, err
		}
		return CreateResult{JobID: jobID}, nil
	}

	jobID := ids.NewGenerationJobID()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return CreateResult{}, err
	}
	rolled := false
	defer func() {
		if !rolled {
			_ = tx.Rollback(ctx)
		}
	}()
	q := dbgen.New(tx)

	if err := s.insertJob(ctx, q, jobID, params, payload); err != nil {
		return CreateResult{}, fmt.Errorf("insert job: %w", err)
	}

	jobIDRef := jobID
	_, err = q.InsertIdempotencyKey(ctx, dbgen.InsertIdempotencyKeyParams{
		ID:              ids.NewIdempotencyKeyID(),
		TokenID:         params.RequestedByTokenID,
		Key:             params.IdempotencyKey,
		Endpoint:        params.Endpoint,
		RequestHash:     params.RequestHash,
		GenerationJobID: &jobIDRef,
		ExpiresAt:       pgtype.Timestamptz{Time: s.now().Add(s.ttl), Valid: true},
	})
	switch {
	case err == nil:
		if err := tx.Commit(ctx); err != nil {
			return CreateResult{}, err
		}
		rolled = true
		if err := s.enqueue(ctx, jobID, params.TenantID); err != nil {
			return CreateResult{}, err
		}
		return CreateResult{JobID: jobID}, nil
	case errors.Is(err, pgx.ErrNoRows):
		// Race: another writer committed first. Roll back our speculative
		// job insert and read the winner's row from a fresh connection.
		if err := tx.Rollback(ctx); err != nil {
			return CreateResult{}, err
		}
		rolled = true
		return s.replayExisting(ctx, params)
	default:
		return CreateResult{}, err
	}
}

func (s *Service) replayExisting(ctx context.Context, params CreateAndEnqueueParams) (CreateResult, error) {
	existing, err := dbgen.New(s.pool).GetIdempotencyKey(ctx, dbgen.GetIdempotencyKeyParams{
		TokenID: params.RequestedByTokenID,
		Key:     params.IdempotencyKey,
	})
	if err != nil {
		return CreateResult{}, fmt.Errorf("load idempotency record: %w", err)
	}
	if existing.Endpoint != params.Endpoint {
		return CreateResult{}, ErrIdempotencyConflict
	}
	if existing.RequestHash != params.RequestHash {
		return CreateResult{}, ErrIdempotencyConflict
	}
	if existing.GenerationJobID == nil {
		return CreateResult{}, errors.New("jobs: idempotency record missing job id")
	}
	return CreateResult{JobID: *existing.GenerationJobID, Replayed: true}, nil
}

func (s *Service) insertJob(ctx context.Context, q *dbgen.Queries, jobID string, params CreateAndEnqueueParams, payload []byte) error {
	worldID := params.WorldID
	tokenID := params.RequestedByTokenID
	fp := params.FallbackPolicy
	cr := params.CacheResult
	_, err := q.InsertGenerationJob(ctx, dbgen.InsertGenerationJobParams{
		ID:                 jobID,
		TenantID:           params.TenantID,
		WorldID:            &worldID,
		JobType:            params.JobType,
		RequestedByTokenID: &tokenID,
		InputPayload:       payload,
		FallbackPolicy:     &fp,
		CacheResult:        &cr,
	})
	return err
}

// enqueue places the task on the queue. If the queue is unreachable the
// already-committed generation_jobs row is marked failed so it doesn't sit
// at queued forever.
func (s *Service) enqueue(ctx context.Context, jobID, tenantID string) error {
	if err := s.enqueuer.EnqueueGenerateArtifact(ctx, jobID); err != nil {
		ec := "enqueue_failed"
		em := err.Error()
		rb := false
		if _, markErr := dbgen.New(s.pool).MarkGenerationJobFailed(ctx, dbgen.MarkGenerationJobFailedParams{
			ID:           jobID,
			TenantID:     tenantID,
			ErrorCode:    &ec,
			ErrorMessage: &em,
			Retryable:    &rb,
		}); markErr != nil {
			// Caller still gets ErrEnqueueFailed; the markFailed failure
			// is logged through the wrapped error so it doesn't get lost.
			return fmt.Errorf("%w (also mark-failed: %v): %v", ErrEnqueueFailed, markErr, err)
		}
		return fmt.Errorf("%w: %v", ErrEnqueueFailed, err)
	}
	return nil
}

// HashRequestBody hashes a request body for the idempotency comparison.
// Normalizes via a json.Marshal round-trip so insignificant whitespace
// differences don't collapse semantically-equal bodies to different hashes.
func HashRequestBody(raw []byte) string {
	if len(raw) == 0 {
		sum := sha256.Sum256(nil)
		return hex.EncodeToString(sum[:])
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		sum := sha256.Sum256(raw)
		return hex.EncodeToString(sum[:])
	}
	out, err := json.Marshal(v)
	if err != nil {
		sum := sha256.Sum256(raw)
		return hex.EncodeToString(sum[:])
	}
	sum := sha256.Sum256(out)
	return hex.EncodeToString(sum[:])
}
