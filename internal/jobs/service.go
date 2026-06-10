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

	"github.com/zakkriel/drchat-image-platform/internal/cost"
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

	// ErrNoPriceEntry and ErrBudgetExceeded are re-exported from the cost
	// package so handlers can map a denied pre-flight to 422 without
	// importing cost directly. Both wrap the cost sentinels so
	// errors.Is(err, cost.ErrNoPriceEntry) also holds.
	ErrNoPriceEntry   = cost.ErrNoPriceEntry
	ErrBudgetExceeded = cost.ErrBudgetExceeded
)

// AssetPackSpec describes the asset_packs row the create transaction inserts
// alongside the generation job (Phase 5A, ADR-008). Nil for single-asset
// jobs. When set, the service inserts the pack (status=planned), links
// generation_jobs.asset_pack_id, and enqueues a pack task instead of an
// artifact task.
type AssetPackSpec struct {
	PackType         string
	VisualIdentityID string
	QualityTier      string // defaults to "standard" when empty
}

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

	// AssetPack, when non-nil, makes this a pack job (Phase 5A).
	AssetPack *AssetPackSpec

	// Pre-flight cost context (docs/architecture/cost-control.md §3).
	// ProviderID/ModelID/OperationType select the price; Units is the
	// quantity priced (image count for unit_type=image). UserID is the
	// optional narrowest budget scope.
	ProviderID    string
	ModelID       string
	OperationType string
	Units         int32
	UserID        string

	// Idempotency context. When IdempotencyKey is empty the service skips
	// the idempotency table altogether and creates a fresh job.
	IdempotencyKey string
	Endpoint       string
	RequestHash    string
}

// CreateResult is the service's return shape. Replayed is true when the
// idempotency layer found a prior row and the caller should report the
// existing job_id instead of treating the response as a fresh insert.
// Status is the current generation_jobs.status — "queued" for fresh
// inserts, and the live status for replays (so a replay of a
// since-failed job reports "failed", not "queued").
type CreateResult struct {
	JobID    string
	Status   string
	Replayed bool

	// Cost pre-flight outputs surfaced in the 202 response body
	// (docs/architecture/cost-control.md §4.2). EstimatedCostUSD is the
	// textual estimate (e.g. "0.0100"); empty when no price applied.
	EstimatedCostUSD  string
	Currency          string
	CostReservationID string

	// AssetPackID is set for pack jobs (Phase 5A): the asset_packs row
	// created in the same transaction (or the replayed job's pack).
	AssetPackID string
}

// Creator is the handler-facing interface. Tests stub this.
type Creator interface {
	CreateAndEnqueue(ctx context.Context, params CreateAndEnqueueParams) (CreateResult, error)
}

// Service implements Creator against Postgres + the asynq Enqueuer, running
// the cost-control pre-flight inside the create transaction.
type Service struct {
	pool      *pgxpool.Pool
	enqueuer  Enqueuer
	reserver  cost.Reserver
	finalizer cost.Finalizer
	ttl       time.Duration
	now       func() time.Time
}

func NewService(pool *pgxpool.Pool, enqueuer Enqueuer, reserver cost.Reserver) *Service {
	return &Service{
		pool:     pool,
		enqueuer: enqueuer,
		reserver: reserver,
		ttl:      IdempotencyTTL,
		now:      time.Now,
	}
}

// WithFinalizer wires the cost-reservation finalizer so an enqueue failure
// (which marks the just-committed job failed) also releases its budget hold
// instead of leaving it stuck in `reserved`. Optional; nil in tests that don't
// exercise the lifecycle.
func (s *Service) WithFinalizer(f cost.Finalizer) *Service {
	s.finalizer = f
	return s
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

	// 1. Insert the job (queued). The reservation FKs to it, so it must
	//    exist first.
	if err := s.insertJob(ctx, q, jobID, params, payload); err != nil {
		return CreateResult{}, fmt.Errorf("insert job: %w", err)
	}

	// 1b. Pack jobs: insert the asset_packs row (status=planned) and link it
	//     onto the job, all inside the same transaction (Phase 5A, ADR-008).
	packID := ""
	if params.AssetPack != nil {
		packID = ids.NewAssetPackID()
		if err := s.insertPack(ctx, q, packID, jobID, params); err != nil {
			return CreateResult{}, fmt.Errorf("insert asset pack: %w", err)
		}
		if err := q.SetGenerationJobAssetPack(ctx, dbgen.SetGenerationJobAssetPackParams{
			ID:          jobID,
			AssetPackID: &packID,
		}); err != nil {
			return CreateResult{}, fmt.Errorf("link asset pack: %w", err)
		}
	}

	// 2. Pre-flight: price → estimate → atomic budget hold. On a denied
	//    request this inserts a failed reservation (estimated/reserved per
	//    the failure mode) but holds no budget.
	res, err := s.reserver.Reserve(ctx, tx, cost.ReserveInput{
		JobID:         jobID,
		TenantID:      params.TenantID,
		TokenID:       params.RequestedByTokenID,
		WorldID:       params.WorldID,
		UserID:        params.UserID,
		ProviderID:    params.ProviderID,
		ModelID:       params.ModelID,
		OperationType: params.OperationType,
		Units:         params.Units,
	})
	if err != nil {
		return CreateResult{}, fmt.Errorf("reserve cost: %w", err)
	}

	// 3. Link the reservation + estimate onto the job.
	if err := q.SetGenerationJobCost(ctx, dbgen.SetGenerationJobCostParams{
		ID:                jobID,
		CostReservationID: &res.ID,
		CostEstimateUsd:   res.EstimatedAmount,
	}); err != nil {
		return CreateResult{}, fmt.Errorf("set job cost: %w", err)
	}

	// 4. A denied pre-flight still commits the job (status=failed) + the
	//    failed reservation for auditability. It is never enqueued.
	if res.Failed() {
		ec := res.FailureReason
		em := preflightMessage(res.FailureReason)
		rb := false
		if _, err := q.MarkGenerationJobFailed(ctx, dbgen.MarkGenerationJobFailedParams{
			ID:           jobID,
			TenantID:     params.TenantID,
			ErrorCode:    &ec,
			ErrorMessage: &em,
			Retryable:    &rb,
		}); err != nil {
			return CreateResult{}, fmt.Errorf("mark preflight failed: %w", err)
		}
	}

	// 5. Idempotency row (when a key was supplied). On a lost race the whole
	//    transaction — job, reservation, and any budget hold — rolls back,
	//    and we replay the winner's row.
	if params.IdempotencyKey != "" {
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
			// won the race; fall through to commit
		case errors.Is(err, pgx.ErrNoRows):
			if err := tx.Rollback(ctx); err != nil {
				return CreateResult{}, err
			}
			rolled = true
			return s.replayExisting(ctx, params)
		default:
			return CreateResult{}, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return CreateResult{}, err
	}
	rolled = true

	// 6. Terminal outcomes. A denied pre-flight returns its sentinel error
	//    (handler → 422) alongside the committed-job metadata.
	if res.Failed() {
		return CreateResult{
			JobID:             jobID,
			Status:            "failed",
			EstimatedCostUSD:  res.EstimateUSD,
			Currency:          res.Currency,
			CostReservationID: res.ID,
			AssetPackID:       packID,
		}, failureError(res.FailureReason)
	}

	if err := s.enqueue(ctx, jobID, params.TenantID, params.AssetPack != nil); err != nil {
		return CreateResult{JobID: jobID, Status: "failed", AssetPackID: packID}, err
	}
	return CreateResult{
		JobID:             jobID,
		Status:            "queued",
		EstimatedCostUSD:  res.EstimateUSD,
		Currency:          res.Currency,
		CostReservationID: res.ID,
		AssetPackID:       packID,
	}, nil
}

// preflightMessage is the human-readable error_message stored on a job that a
// pre-flight denied.
func preflightMessage(reason string) string {
	switch reason {
	case cost.ReasonNoPriceEntry:
		return "no active price entry for the selected provider/model/operation"
	case cost.ReasonBudgetExceeded:
		return "cost budget exceeded for this request"
	default:
		return "cost pre-flight failed"
	}
}

// failureError maps a reservation failure reason to the sentinel the handler
// keys its 422 status code off.
func failureError(reason string) error {
	switch reason {
	case cost.ReasonNoPriceEntry:
		return ErrNoPriceEntry
	case cost.ReasonBudgetExceeded:
		return ErrBudgetExceeded
	default:
		return fmt.Errorf("jobs: pre-flight failed: %s", reason)
	}
}

func (s *Service) replayExisting(ctx context.Context, params CreateAndEnqueueParams) (CreateResult, error) {
	q := dbgen.New(s.pool)
	existing, err := q.GetIdempotencyKey(ctx, dbgen.GetIdempotencyKeyParams{
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
	job, err := q.GetGenerationJobByIDUnchecked(ctx, *existing.GenerationJobID)
	if err != nil {
		return CreateResult{}, fmt.Errorf("load replayed job: %w", err)
	}
	result := CreateResult{JobID: job.ID, Status: job.Status, Replayed: true}
	if job.CostReservationID != nil {
		result.CostReservationID = *job.CostReservationID
	}
	if job.AssetPackID != nil {
		result.AssetPackID = *job.AssetPackID
	}
	// A replay of a pre-flight-denied job must return the same 422 again, not
	// a 202 echoing status=failed (Phase 4 correction 1).
	if job.Status == "failed" && job.ErrorCode != nil {
		switch *job.ErrorCode {
		case cost.ReasonNoPriceEntry:
			return result, ErrNoPriceEntry
		case cost.ReasonBudgetExceeded:
			return result, ErrBudgetExceeded
		}
	}
	return result, nil
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

// insertPack writes the asset_packs row a pack job creates (status=planned).
func (s *Service) insertPack(ctx context.Context, q *dbgen.Queries, packID, jobID string, params CreateAndEnqueueParams) error {
	spec := params.AssetPack
	identityID := spec.VisualIdentityID
	jobIDRef := jobID
	tokenID := params.RequestedByTokenID
	quality := spec.QualityTier
	if quality == "" {
		quality = "standard"
	}
	_, err := q.InsertAssetPack(ctx, dbgen.InsertAssetPackParams{
		ID:               packID,
		TenantID:         params.TenantID,
		WorldID:          params.WorldID,
		VisualIdentityID: &identityID,
		PackType:         spec.PackType,
		StyleProfileID:   stylePayloadString(params.InputPayload),
		QualityTier:      quality,
		CreatedByJobID:   &jobIDRef,
		CreatedByTokenID: &tokenID,
	})
	return err
}

// stylePayloadString pulls style_profile_id out of the job's input payload —
// the handler always stores it there so the worker (and the pack row) need
// only the payload, not extra params.
func stylePayloadString(payload map[string]any) string {
	s, _ := payload["style_profile_id"].(string)
	return s
}

// enqueue places the task on the queue. If the queue is unreachable the
// already-committed generation_jobs row is marked failed so it doesn't sit
// at queued forever.
func (s *Service) enqueue(ctx context.Context, jobID, tenantID string, pack bool) error {
	enqueueFn := s.enqueuer.EnqueueGenerateArtifact
	if pack {
		enqueueFn = s.enqueuer.EnqueueGeneratePack
	}
	if err := enqueueFn(ctx, jobID); err != nil {
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
		// Enqueue failure after a successful reservation is a terminal failure
		// for this job: release the budget hold so it doesn't sit reserved
		// forever. Best-effort — the request already failed.
		if s.finalizer != nil {
			if relErr := s.finalizer.Release(ctx, jobID); relErr != nil {
				return fmt.Errorf("%w (also release-reservation: %v): %v", ErrEnqueueFailed, relErr, err)
			}
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
