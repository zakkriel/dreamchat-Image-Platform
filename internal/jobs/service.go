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

	// ErrConcurrentJobsExceeded is returned by CreateAndEnqueue when the token
	// already has max_concurrent_jobs live jobs (queued|running|preview_ready).
	// The handler maps it to 429 concurrent_jobs_exceeded. The denial happens
	// inside the create transaction, before cost reservation / job insert /
	// idempotency insert / enqueue, so it has no side effects. It is NEVER
	// returned for an idempotency replay — a replay returns the existing job
	// even when the token is at the cap.
	ErrConcurrentJobsExceeded = errors.New("jobs: concurrent jobs exceeded")

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
//
// Phase 6A3 makes pack creation retrieval-first: the handler resolves each
// required role through the retrieval layer before reserving cost, splits roles
// into reused (an existing ready asset satisfies them) and missing (must be
// generated), and prices only the misses. RequiredRoles/MissingRoles/ReusedItems
// carry that decision so the create transaction can persist the reused items +
// pack completeness up front. ReusedItems is empty (and MissingRoles == every
// role) when retrieval is disabled or found no reusable asset — the pre-6A3
// "generate the whole pack" behavior.
type AssetPackSpec struct {
	PackType         string
	VisualIdentityID string
	QualityTier      string // defaults to "standard" when empty

	// RequiredRoles is every role the pack template requires, in order. Stored
	// on asset_packs.required_roles for completeness.
	RequiredRoles []string
	// MissingRoles is the subset of RequiredRoles with no reusable asset; these
	// are the roles the worker generates, and the count the request is priced
	// for. Stored on asset_packs.missing_roles.
	MissingRoles []string
	// ReusedItems are the retrieval hits to persist as asset_pack_items pointing
	// at existing assets, in the create transaction. Their variant keys become
	// asset_packs.delivered_roles.
	ReusedItems []PackReuseItem
}

// PackReuseItem is one role a pack reuses: an existing ready asset that the
// retrieval layer returned as a usable hit for the request's fallback_policy.
// MatchType is the 6A1 outcome (exact_match | compatible_match |
// preview_fallback). SortOrder is the role's position in the resolved template
// so a reused item and a worker-generated item for the same pack share one
// ordering.
type PackReuseItem struct {
	VariantKey string
	AssetID    string
	MatchType  string
	SortOrder  int32
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
	// optional narrowest budget scope. ProviderRouteID is the resolved route
	// (Phase 7A) persisted alongside provider/model for the worker + provenance.
	ProviderID      string
	ModelID         string
	ProviderRouteID string
	OperationType   string
	Units           int32
	UserID          string

	// Idempotency context. When IdempotencyKey is empty the service skips
	// the idempotency table altogether and creates a fresh job.
	IdempotencyKey string
	Endpoint       string
	RequestHash    string

	// MaxConcurrentJobs is the effective per-token cap on live generation jobs
	// (Phase 7C-2), threaded in from the principal's resolved limits — the jobs
	// service does NOT read the request context. <= 0 disables the cap (used by
	// callers/tests that don't enforce it). When > 0, CreateAndEnqueue counts the
	// token's live jobs under a per-token advisory lock and returns
	// ErrConcurrentJobsExceeded before any side effect if at/over the cap.
	MaxConcurrentJobs int
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

	// CacheResult and FinalAssetIDs are set for Phase 6A2 exact artifact
	// reuse: a completed cache-hit job reports cache_result=exact_match and the
	// reused asset id(s). Empty for the normal generate path (the cache result
	// lives on the job row and the worker fills final_asset_ids later).
	CacheResult   string
	FinalAssetIDs []string

	// Concurrent-job accounting (Phase 7C-2), set on the reserve/enqueue create
	// path when a cap was enforced (MaxConcurrentJobs > 0). ConcurrentJobsLimit
	// is the effective cap; ConcurrentJobsUsed is the token's live-job count
	// after this job lands (the pre-insert count + 1). Handlers surface these as
	// X-RateLimit-Concurrent-Jobs[-Remaining] headers. Zero when no cap applied.
	ConcurrentJobsLimit int
	ConcurrentJobsUsed  int
}

// CreateCacheHitParams carries what the service needs to land an already-
// completed generation job for a Phase 6A2 exact artifact reuse. There is no
// cost, provider, or enqueue context because a cache hit does none of that
// work: it records that an existing ready asset satisfied the request.
type CreateCacheHitParams struct {
	TenantID           string
	RequestedByTokenID string
	JobType            string
	WorldID            string
	InputPayload       map[string]any
	FallbackPolicy     string
	// FinalAssetID is the existing ready asset the request reuses; it becomes
	// the job's single final_asset_ids entry.
	FinalAssetID string
	// RequestedOutputs mirrors the request's output set; defaults to
	// ['default'] (the single artifact variant) when empty.
	RequestedOutputs []string

	// Idempotency context. When IdempotencyKey is empty the service skips the
	// idempotency table and creates a fresh completed job (still reusing the
	// same asset and doing no provider work).
	IdempotencyKey string
	Endpoint       string
	RequestHash    string
}

// CreatePackReuseParams carries what the service needs to land an already-
// completed pack job for a Phase 6A3 all-hits pack reuse: every required role
// was satisfied by an existing ready asset, so the pack completes synchronously
// with no cost reservation, no provider attempt, and no enqueue. It is the pack
// analogue of CreateCacheHitParams.
type CreatePackReuseParams struct {
	TenantID           string
	RequestedByTokenID string
	JobType            string
	WorldID            string
	InputPayload       map[string]any
	FallbackPolicy     string
	// CacheResult is the aggregate pack reuse tier stored on the job
	// (exact_match | compatible_match | preview_fallback) — the weakest reuse
	// outcome across the roles.
	CacheResult string

	PackType         string
	VisualIdentityID string
	QualityTier      string
	// RequiredRoles is every template role (all delivered for an all-hits pack).
	RequiredRoles []string
	// ReusedItems are the per-role hits to persist as asset_pack_items pointing
	// at existing assets. For an all-hits pack this covers every required role.
	ReusedItems []PackReuseItem

	// Idempotency context. When IdempotencyKey is empty the service skips the
	// idempotency table and creates a fresh completed pack job.
	IdempotencyKey string
	Endpoint       string
	RequestHash    string
}

// ReplayLookup is the idempotency replay pre-check input. The handler runs this
// BEFORE route resolution and cost reservation (Phase 7A lifecycle): a replay
// returns the existing job without re-resolving a route, re-reserving cost, or
// re-enqueuing.
type ReplayLookup struct {
	TokenID     string
	Key         string
	Endpoint    string
	RequestHash string
}

// Creator is the handler-facing interface. Tests stub this.
type Creator interface {
	// LookupReplay reports whether the (token, key) idempotency record already
	// exists. found=false means it is a new request and the handler proceeds to
	// route resolution; found=true returns the existing job's CreateResult (with
	// the same 422 sentinel re-raised for a previously-denied job, or
	// ErrIdempotencyConflict on an endpoint/body mismatch). It never resolves a
	// route, reserves cost, or enqueues.
	LookupReplay(ctx context.Context, in ReplayLookup) (result CreateResult, found bool, err error)
	CreateAndEnqueue(ctx context.Context, params CreateAndEnqueueParams) (CreateResult, error)
	// CreateCompletedCacheHitJob lands an already-completed job for an exact
	// artifact reuse (Phase 6A2): no cost reservation, no provider attempt, no
	// enqueue. Idempotency is honored exactly like CreateAndEnqueue.
	CreateCompletedCacheHitJob(ctx context.Context, params CreateCacheHitParams) (CreateResult, error)
	// CreateCompletedPackReuseJob lands an already-completed pack job for an
	// all-hits pack reuse (Phase 6A3): every required role was satisfied by an
	// existing ready asset, so no reservation, provider attempt, or enqueue
	// happens. Idempotency is honored exactly like CreateAndEnqueue.
	CreateCompletedPackReuseJob(ctx context.Context, params CreatePackReuseParams) (CreateResult, error)
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
	// Phase 7A: persist the resolved provider/model/route onto the job payload
	// from the cost params, so the worker consumes EXACTLY what was priced
	// (resolved model id = pricing key = job model id = asset model id). This is
	// the single source of persistence — every caller that sets the pricing
	// context (handlers, tests) gets a worker-consumable job, with no separate
	// payload-writing step to keep in sync.
	params.InputPayload = withResolvedRoutePayload(params.InputPayload, params.ProviderID, params.ModelID, params.ProviderRouteID)
	// Phase 7C-1b: persist the priced operation_type + units alongside the
	// resolved route so an admin retry can re-reserve cost against the exact same
	// operation/units/model without re-resolving the route.
	params.InputPayload = withCostContextPayload(params.InputPayload, params.OperationType, params.Units)

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

	// 0. Phase 7C-2 hard concurrent-job cap. This block runs FIRST, before any
	//    side effect (cost reserve / job insert / idempotency insert / enqueue),
	//    so a denial leaves nothing behind.
	//
	//    a) Take a transaction-scoped advisory lock keyed on the token. Reusing
	//       the Phase 6A4 helper (pg_advisory_xact_lock(hashtextextended(...)))
	//       serializes concurrent creates for the SAME token so the count below
	//       is consistent under parallel requests; the lock auto-releases at
	//       commit/rollback. (hashtextextended collisions across distinct keys
	//       only over-serialize — never a correctness issue.)
	if err := q.AcquireSupersedeLock(ctx, concurrentLockKey(params.RequestedByTokenID)); err != nil {
		return CreateResult{}, fmt.Errorf("acquire concurrent lock: %w", err)
	}

	//    b) Idempotency replay ALWAYS wins over the cap. Under the lock, if an
	//       idempotency row already exists for (token, key), roll back and replay
	//       the existing job — without evaluating the cap. This closes the
	//       concurrent same-key duplicate race: a second same-key request blocks
	//       on the lock until the first commits, then sees the committed row here
	//       and replays instead of being counted/denied.
	if params.IdempotencyKey != "" {
		_, lookupErr := q.GetIdempotencyKey(ctx, dbgen.GetIdempotencyKeyParams{
			TokenID: params.RequestedByTokenID,
			Key:     params.IdempotencyKey,
		})
		switch {
		case lookupErr == nil:
			if err := tx.Rollback(ctx); err != nil {
				return CreateResult{}, err
			}
			rolled = true
			return s.replayExisting(ctx, params)
		case errors.Is(lookupErr, pgx.ErrNoRows):
			// New (token, key): fall through to the cap count.
		default:
			return CreateResult{}, fmt.Errorf("lookup idempotency under lock: %w", lookupErr)
		}
	}

	//    c) Count the token's live jobs and deny at/over the cap. The count
	//       excludes the not-yet-inserted job, so the post-create usage is
	//       liveCount + 1.
	liveCount := int64(0)
	if params.MaxConcurrentJobs > 0 {
		var err error
		liveCount, err = q.CountLiveGenerationJobsByToken(ctx, params.RequestedByTokenID)
		if err != nil {
			return CreateResult{}, fmt.Errorf("count live jobs: %w", err)
		}
		if liveCount >= int64(params.MaxConcurrentJobs) {
			if err := tx.Rollback(ctx); err != nil {
				return CreateResult{}, err
			}
			rolled = true
			return CreateResult{}, ErrConcurrentJobsExceeded
		}
	}

	// 1. Insert the job (queued). The reservation FKs to it, so it must
	//    exist first.
	if err := s.insertJob(ctx, q, jobID, params, payload); err != nil {
		return CreateResult{}, fmt.Errorf("insert job: %w", err)
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

	// 4b. Pack jobs: insert the asset_packs row (status=planned) and link it
	//     onto the job, in the same transaction (Phase 5A, ADR-008). Only
	//     after the pre-flight passed — a denied request commits the failed
	//     job + failed reservation but never an asset pack, so no pack can
	//     sit at status=planned for a job that will never run.
	packID := ""
	if params.AssetPack != nil && !res.Failed() {
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
	//    (handler → 422) alongside the committed-job metadata. No asset pack
	//    exists for a denied request (step 4b skipped it).
	if res.Failed() {
		return CreateResult{
			JobID:             jobID,
			Status:            "failed",
			EstimatedCostUSD:  res.EstimateUSD,
			Currency:          res.Currency,
			CostReservationID: res.ID,
		}, failureError(res.FailureReason)
	}

	if err := s.enqueue(ctx, jobID, params.TenantID, packID); err != nil {
		return CreateResult{JobID: jobID, Status: "failed", AssetPackID: packID}, err
	}
	result := CreateResult{
		JobID:             jobID,
		Status:            "queued",
		EstimatedCostUSD:  res.EstimateUSD,
		Currency:          res.Currency,
		CostReservationID: res.ID,
		AssetPackID:       packID,
	}
	if params.MaxConcurrentJobs > 0 {
		result.ConcurrentJobsLimit = params.MaxConcurrentJobs
		result.ConcurrentJobsUsed = int(liveCount) + 1
	}
	return result, nil
}

// concurrentLockKey is the per-token advisory-lock key for the Phase 7C-2
// concurrent-job cap. Namespaced with a "concurrent:" prefix so it does not
// alias the Phase 6A4 supersede slot keys (which use other prefixes); even an
// accidental hash collision would only over-serialize, never mis-count.
func concurrentLockKey(tokenID string) string {
	return "concurrent:" + tokenID
}

// CreateCompletedCacheHitJob lands an already-completed generation job for a
// Phase 6A2 exact artifact reuse. It is deliberately NOT a thin wrapper over
// CreateAndEnqueue: a cache hit must never reserve cost, insert a provider
// attempt, or enqueue a task — so it shares only the idempotency machinery, not
// the reserve/enqueue path. The job is committed at status=completed with
// cache_result=exact_match, final_asset_ids=[asset], and zero estimated/actual
// cost. It is never enqueued, so the worker never processes it and the
// terminal-job cost finalizer is never invoked on it.
func (s *Service) CreateCompletedCacheHitJob(ctx context.Context, params CreateCacheHitParams) (CreateResult, error) {
	payload, err := marshalPayload(params.InputPayload)
	if err != nil {
		return CreateResult{}, err
	}

	requestedOutputs := params.RequestedOutputs
	if len(requestedOutputs) == 0 {
		requestedOutputs = []string{"default"}
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

	worldID := params.WorldID
	tokenID := params.RequestedByTokenID
	fp := params.FallbackPolicy
	if _, err := q.InsertCompletedCacheHitJob(ctx, dbgen.InsertCompletedCacheHitJobParams{
		ID:                 jobID,
		TenantID:           params.TenantID,
		WorldID:            &worldID,
		JobType:            params.JobType,
		RequestedByTokenID: &tokenID,
		InputPayload:       payload,
		FallbackPolicy:     &fp,
		RequestedOutputs:   requestedOutputs,
		FinalAssetIds:      []string{params.FinalAssetID},
	}); err != nil {
		return CreateResult{}, fmt.Errorf("insert cache-hit job: %w", err)
	}

	// Idempotency: same machinery as CreateAndEnqueue. On a lost race the whole
	// transaction (just the completed job) rolls back and we replay the
	// winner's row, so a same-key replay returns the same cache-hit job without
	// creating a duplicate.
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
			return s.replayExisting(ctx, CreateAndEnqueueParams{
				RequestedByTokenID: params.RequestedByTokenID,
				IdempotencyKey:     params.IdempotencyKey,
				Endpoint:           params.Endpoint,
				RequestHash:        params.RequestHash,
			})
		default:
			return CreateResult{}, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return CreateResult{}, err
	}
	rolled = true

	return CreateResult{
		JobID:            jobID,
		Status:           "completed",
		EstimatedCostUSD: "0.0000",
		CacheResult:      "exact_match",
		FinalAssetIDs:    []string{params.FinalAssetID},
	}, nil
}

// CreateCompletedPackReuseJob lands an already-completed pack job for a Phase
// 6A3 all-hits pack reuse. Like CreateCompletedCacheHitJob it is deliberately
// NOT a thin wrapper over CreateAndEnqueue: an all-hits pack must never reserve
// cost, insert a provider attempt, or enqueue a task. It commits, in one
// transaction: the generation job at status=completed (cache_result = the
// aggregate reuse tier, final_asset_ids = the reused assets, zero cost), the
// asset_packs row at status=completed with full completeness (every required
// role delivered, none missing), the link from job to pack, and one
// asset_pack_items row per reused role pointing at the existing asset. It shares
// only the idempotency machinery with CreateAndEnqueue.
func (s *Service) CreateCompletedPackReuseJob(ctx context.Context, params CreatePackReuseParams) (CreateResult, error) {
	payload, err := marshalPayload(params.InputPayload)
	if err != nil {
		return CreateResult{}, err
	}

	requiredRoles := nonNilStrings(params.RequiredRoles)
	finalAssetIDs := make([]string, 0, len(params.ReusedItems))
	for _, item := range params.ReusedItems {
		finalAssetIDs = append(finalAssetIDs, item.AssetID)
	}
	cacheResult := params.CacheResult
	if cacheResult == "" {
		// An all-hits pack always has at least one reused role; default to the
		// strongest tier if the caller did not compute an aggregate.
		cacheResult = "exact_match"
	}

	jobID := ids.NewGenerationJobID()
	packID := ids.NewAssetPackID()
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

	worldID := params.WorldID
	tokenID := params.RequestedByTokenID
	fp := params.FallbackPolicy
	if _, err := q.InsertCompletedPackReuseJob(ctx, dbgen.InsertCompletedPackReuseJobParams{
		ID:                 jobID,
		TenantID:           params.TenantID,
		WorldID:            &worldID,
		JobType:            params.JobType,
		RequestedByTokenID: &tokenID,
		InputPayload:       payload,
		FallbackPolicy:     &fp,
		RequestedOutputs:   requiredRoles,
		CacheResult:        &cacheResult,
		FinalAssetIds:      finalAssetIDs,
	}); err != nil {
		return CreateResult{}, fmt.Errorf("insert completed pack reuse job: %w", err)
	}

	identityID := params.VisualIdentityID
	jobIDRef := jobID
	quality := params.QualityTier
	if quality == "" {
		quality = "standard"
	}
	if _, err := q.InsertAssetPack(ctx, dbgen.InsertAssetPackParams{
		ID:               packID,
		TenantID:         params.TenantID,
		WorldID:          params.WorldID,
		VisualIdentityID: &identityID,
		PackType:         params.PackType,
		StyleProfileID:   stylePayloadString(params.InputPayload),
		QualityTier:      quality,
		// All-hits: the pack is terminal at creation — every required role is
		// delivered by a reused asset, nothing is missing, the worker never runs.
		Status:           packStatusCompleted,
		RequiredRoles:    requiredRoles,
		DeliveredRoles:   reuseVariantKeys(params.ReusedItems),
		MissingRoles:     []string{},
		CreatedByJobID:   &jobIDRef,
		CreatedByTokenID: &tokenID,
	}); err != nil {
		return CreateResult{}, fmt.Errorf("insert completed asset pack: %w", err)
	}
	if err := q.SetGenerationJobAssetPack(ctx, dbgen.SetGenerationJobAssetPackParams{
		ID:          jobID,
		AssetPackID: &packID,
	}); err != nil {
		return CreateResult{}, fmt.Errorf("link asset pack: %w", err)
	}
	if err := insertReusedPackItems(ctx, q, packID, params.ReusedItems); err != nil {
		return CreateResult{}, err
	}

	// Idempotency: same machinery as CreateAndEnqueue. On a lost race the whole
	// transaction (job + pack + items) rolls back and we replay the winner's row,
	// so a same-key replay returns the same pack job + asset_pack_id without
	// creating duplicate jobs/packs/items.
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
			return s.replayExisting(ctx, CreateAndEnqueueParams{
				RequestedByTokenID: params.RequestedByTokenID,
				IdempotencyKey:     params.IdempotencyKey,
				Endpoint:           params.Endpoint,
				RequestHash:        params.RequestHash,
			})
		default:
			return CreateResult{}, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return CreateResult{}, err
	}
	rolled = true

	return CreateResult{
		JobID:            jobID,
		Status:           "completed",
		EstimatedCostUSD: "0.0000",
		CacheResult:      cacheResult,
		FinalAssetIDs:    finalAssetIDs,
		AssetPackID:      packID,
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

// LookupReplay is the handler-facing idempotency pre-check (Phase 7A): it runs
// before route resolution so a replay never re-resolves a route or re-reserves
// cost. It returns found=false when no record exists for (token, key) — a new
// request — and otherwise the existing job's result (or a sentinel: a 422 for a
// previously-denied job, ErrIdempotencyConflict on endpoint/body mismatch).
func (s *Service) LookupReplay(ctx context.Context, in ReplayLookup) (CreateResult, bool, error) {
	q := dbgen.New(s.pool)
	existing, err := q.GetIdempotencyKey(ctx, dbgen.GetIdempotencyKeyParams{
		TokenID: in.TokenID,
		Key:     in.Key,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return CreateResult{}, false, nil
	}
	if err != nil {
		return CreateResult{}, false, fmt.Errorf("load idempotency record: %w", err)
	}
	result, sentinel := s.replayResult(ctx, q, existing, in.Endpoint, in.RequestHash)
	return result, true, sentinel
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
	return s.replayResult(ctx, q, existing, params.Endpoint, params.RequestHash)
}

// replayResult validates an idempotency record against the request's endpoint +
// body hash and loads the referenced job into a CreateResult. Shared by the
// in-transaction race path (replayExisting) and the upfront pre-check
// (LookupReplay) so both report identical replay semantics.
func (s *Service) replayResult(ctx context.Context, q *dbgen.Queries, existing dbgen.IdempotencyKey, endpoint, requestHash string) (CreateResult, error) {
	if existing.Endpoint != endpoint {
		return CreateResult{}, ErrIdempotencyConflict
	}
	if existing.RequestHash != requestHash {
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

// insertPack writes the asset_packs row a pack job creates (status=planned for
// the worker to advance), records pack completeness (required/delivered/missing
// roles), and inserts any reused asset_pack_items pointing at existing assets —
// all in the create transaction (Phase 5A + 6A3). delivered_roles is the set of
// reused roles; the worker fills in the rest as it generates the missing roles.
func (s *Service) insertPack(ctx context.Context, q *dbgen.Queries, packID, jobID string, params CreateAndEnqueueParams) error {
	spec := params.AssetPack
	identityID := spec.VisualIdentityID
	jobIDRef := jobID
	tokenID := params.RequestedByTokenID
	quality := spec.QualityTier
	if quality == "" {
		quality = "standard"
	}
	delivered := reuseVariantKeys(spec.ReusedItems)
	if _, err := q.InsertAssetPack(ctx, dbgen.InsertAssetPackParams{
		ID:               packID,
		TenantID:         params.TenantID,
		WorldID:          params.WorldID,
		VisualIdentityID: &identityID,
		PackType:         spec.PackType,
		StyleProfileID:   stylePayloadString(params.InputPayload),
		QualityTier:      quality,
		Status:           "planned",
		RequiredRoles:    nonNilStrings(spec.RequiredRoles),
		DeliveredRoles:   delivered,
		MissingRoles:     nonNilStrings(spec.MissingRoles),
		CreatedByJobID:   &jobIDRef,
		CreatedByTokenID: &tokenID,
	}); err != nil {
		return err
	}
	return insertReusedPackItems(ctx, q, packID, spec.ReusedItems)
}

// insertReusedPackItems persists the retrieval hits as asset_pack_items pointing
// at the existing assets, in the create transaction. The worker's existing-items
// skip then treats them as already delivered, so it never regenerates or
// duplicates a reused role.
func insertReusedPackItems(ctx context.Context, q *dbgen.Queries, packID string, items []PackReuseItem) error {
	for _, item := range items {
		if err := q.InsertAssetPackItem(ctx, dbgen.InsertAssetPackItemParams{
			ID:            ids.NewAssetPackItemID(),
			AssetPackID:   packID,
			VisualAssetID: item.AssetID,
			VariantKey:    item.VariantKey,
			SortOrder:     item.SortOrder,
		}); err != nil {
			return fmt.Errorf("insert reused pack item %q: %w", item.VariantKey, err)
		}
	}
	return nil
}

// reuseVariantKeys returns the variant keys of the reused items (the pack's
// delivered roles at creation), always non-nil so the NOT NULL array column is
// satisfied.
func reuseVariantKeys(items []PackReuseItem) []string {
	keys := make([]string, 0, len(items))
	for _, item := range items {
		keys = append(keys, item.VariantKey)
	}
	return keys
}

// nonNilStrings maps a nil slice to an empty (non-nil) one so a NOT NULL TEXT[]
// column is never sent a NULL.
func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// withResolvedRoutePayload stamps the resolved provider/model/route onto a job's
// input_payload (generation_jobs has no first-class provider/model columns, so
// the payload is the carrier the worker reads). provider_id/model_id are set
// when non-empty; provider_route_id is best-effort provenance.
func withResolvedRoutePayload(payload map[string]any, providerID, modelID, routeID string) map[string]any {
	if payload == nil {
		payload = map[string]any{}
	}
	if providerID != "" {
		payload["provider_id"] = providerID
	}
	if modelID != "" {
		payload["model_id"] = modelID
	}
	if routeID != "" {
		payload["provider_route_id"] = routeID
	}
	return payload
}

// withCostContextPayload stamps the priced operation_type and units onto a
// job's input_payload (Phase 7C-1b). generation_jobs has no first-class
// operation/units columns, so the payload is the carrier an admin retry reads
// to re-reserve cost against the same operation/units without re-resolving.
func withCostContextPayload(payload map[string]any, operationType string, units int32) map[string]any {
	if payload == nil {
		payload = map[string]any{}
	}
	if operationType != "" {
		payload["operation_type"] = operationType
	}
	if units > 0 {
		payload["units"] = units
	}
	return payload
}

// stylePayloadString pulls style_profile_id out of the job's input payload —
// the handler always stores it there so the worker (and the pack row) need
// only the payload, not extra params.
func stylePayloadString(payload map[string]any) string {
	s, _ := payload["style_profile_id"].(string)
	return s
}

// enqueue places the task on the queue. packID is non-empty for pack jobs
// (selecting the pack task) and empty for artifacts. If the queue is
// unreachable the already-committed generation_jobs row is marked failed so
// it doesn't sit at queued forever — and a pack job's asset_packs row is
// marked failed too, so no pack can sit at status=planned for a job that
// will never run.
func (s *Service) enqueue(ctx context.Context, jobID, tenantID, packID string) error {
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
			// Caller still gets ErrEnqueueFailed; the markFailed failure
			// is logged through the wrapped error so it doesn't get lost.
			return fmt.Errorf("%w (also mark-failed: %v): %v", ErrEnqueueFailed, markErr, err)
		}
		if packID != "" {
			if packErr := q.UpdateAssetPackStatus(ctx, dbgen.UpdateAssetPackStatusParams{
				ID:     packID,
				Status: "failed",
			}); packErr != nil {
				return fmt.Errorf("%w (also mark-pack-failed: %v): %v", ErrEnqueueFailed, packErr, err)
			}
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
