package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/hibiken/asynq"

	"github.com/zakkriel/drchat-image-platform/internal/assets"
	"github.com/zakkriel/drchat-image-platform/internal/cost"
	"github.com/zakkriel/drchat-image-platform/internal/ids"
	"github.com/zakkriel/drchat-image-platform/internal/imaging"
	"github.com/zakkriel/drchat-image-platform/internal/providers"
	"github.com/zakkriel/drchat-image-platform/internal/storage"
)

const (
	errorCodeProviderFailure      = "provider_failure"
	errorCodePersistenceError     = "persistence_error"
	errorCodeStorageFailure       = "storage_failure"
	errorCodeProviderUnavailable  = "provider_unavailable"
	errorCodeInvalidResolvedRoute = "invalid_resolved_route"

	// deliveryRenderEdge is the square edge (px) the worker asks the provider
	// to produce so the "final" tier is genuinely higher resolution than the
	// downscaled preview/thumbnail tiers (PRD 06 §4). It exceeds both
	// imaging.PreviewShortEdge and imaging.ThumbnailShortEdge so the three
	// delivery tiers come out at distinct sizes.
	deliveryRenderEdge = 1024
)

// ProviderRegistry resolves a provider_id to its adapter (Phase 7A). The worker
// selects the adapter from the resolved provider_id persisted on the job — it
// never re-resolves a route and never falls back to a different provider.
// *providers.Registry satisfies this.
type ProviderRegistry interface {
	Get(providerID string) (providers.ImageProvider, bool)
}

// Worker holds the dependencies the asynq handler resolves a job against.
// Each task call re-reads the generation_jobs row from Postgres; the queue
// payload only carries the job_id.
type Worker struct {
	Jobs      Repository
	Assets    assets.Repository
	Storage   storage.Storage
	Providers ProviderRegistry
	Logger    *slog.Logger

	// Finalizer commits the cost reservation on success and releases it on
	// terminal failure (docs/architecture/cost-control.md §3 steps 9–10).
	// Optional: nil in unit tests that don't exercise the cost lifecycle.
	Finalizer cost.Finalizer
}

// resolvedRoute is the provider/model/route the handler resolved at job-creation
// time and persisted on the job's input_payload (generation_jobs has no
// first-class provider/model columns). The worker consumes it verbatim.
type resolvedRoute struct {
	providerID string
	modelID    string
	routeID    string
}

// resolvedRouteFromPayload reads the resolved route the handler persisted.
// provider_id and model_id are required; provider_route_id is best-effort
// provenance.
func resolvedRouteFromPayload(payload map[string]any) (resolvedRoute, error) {
	rr := resolvedRoute{
		providerID: payloadString(payload, "provider_id"),
		modelID:    payloadString(payload, "model_id"),
		routeID:    payloadString(payload, "provider_route_id"),
	}
	if rr.providerID == "" || rr.modelID == "" {
		return rr, fmt.Errorf("job payload missing resolved provider_id/model_id")
	}
	return rr, nil
}

// failTerminal marks a job permanently failed (not retryable) and releases its
// cost reservation. Used for unrunnable jobs — a missing provider adapter or a
// payload missing its resolved route — where an asynq retry could never help.
func (w *Worker) failTerminal(ctx context.Context, job Job, code, msg string) error {
	if _, err := w.Jobs.MarkFailed(ctx, job.ID, job.TenantID, code, msg, false); err != nil {
		return err
	}
	if w.Finalizer != nil {
		if err := w.Finalizer.Release(ctx, job.ID); err != nil {
			return err
		}
	}
	return nil
}

// NewHandlerFunc returns the asynq handler so the cmd/worker binary stays a
// thin wiring layer. The handler decodes the payload, looks up the job, and
// invokes Process.
func (w *Worker) NewHandlerFunc() func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		var payload TaskPayload
		if err := json.Unmarshal(t.Payload(), &payload); err != nil {
			return fmt.Errorf("worker: decode payload: %w", err)
		}
		retryCount, _ := asynq.GetRetryCount(ctx)
		return w.Process(ctx, payload.JobID, int32(retryCount))
	}
}

// Process is the per-attempt worker body. retryCount is asynq's zero-based
// retry counter; attempt_number is retryCount+1.
func (w *Worker) Process(ctx context.Context, jobID string, retryCount int32) error {
	attemptNumber := retryCount + 1
	finalAttempt := attemptNumber >= int32(MaxAttempts)

	job, err := w.Jobs.GetByID(ctx, jobID)
	if err != nil {
		w.log().Error("worker: lookup job", "job_id", jobID, "error", err)
		return err
	}

	// Retry-safety: if the job is already terminal, a previous attempt did the
	// generation work and only the cost finalization may be outstanding (e.g.
	// the task was retried because Finalizer.Commit failed after the job was
	// marked completed). Re-run only the idempotent finalization — never the
	// provider call or asset insert — so a finalization failure can't trigger
	// duplicate generation.
	switch job.Status {
	case "completed":
		if w.Finalizer != nil {
			if err := w.Finalizer.Commit(ctx, job.ID); err != nil {
				w.log().Error("worker: commit cost reservation (terminal job)", "job_id", jobID, "error", err)
				return err
			}
		}
		return nil
	case "failed":
		if w.Finalizer != nil {
			if err := w.Finalizer.Release(ctx, job.ID); err != nil {
				w.log().Error("worker: release cost reservation (terminal job)", "job_id", jobID, "error", err)
				return err
			}
		}
		return nil
	}

	// Phase 7A: select the provider adapter from the route the handler resolved
	// at creation time and persisted on the job. The worker never re-resolves.
	resolved, rerr := resolvedRouteFromPayload(job.InputPayload)
	if rerr != nil {
		w.log().Error("worker: invalid resolved route", "job_id", jobID, "error", rerr)
		return w.failTerminal(ctx, job, errorCodeInvalidResolvedRoute, rerr.Error())
	}
	provider, ok := w.Providers.Get(resolved.providerID)
	if !ok {
		msg := fmt.Sprintf("no adapter registered for resolved provider %q", resolved.providerID)
		w.log().Error("worker: provider adapter missing", "job_id", jobID, "provider_id", resolved.providerID)
		return w.failTerminal(ctx, job, errorCodeProviderUnavailable, msg)
	}

	if _, err := w.Jobs.MarkRunning(ctx, job.ID, job.TenantID); err != nil {
		w.log().Error("worker: mark running", "job_id", jobID, "error", err)
		return err
	}

	attempt, err := w.Jobs.InsertProviderAttempt(ctx, ProviderAttemptInsertParams{
		ID:              ids.NewProviderAttemptID(),
		GenerationJobID: job.ID,
		ProviderID:      resolved.providerID,
		AttemptNumber:   attemptNumber,
	})
	if err != nil {
		w.log().Error("worker: insert attempt", "job_id", jobID, "error", err)
		return err
	}

	start := time.Now()
	worldID := ""
	if job.WorldID != nil {
		worldID = *job.WorldID
	}
	description, _ := job.InputPayload["description"].(string)
	result, providerErr := provider.Generate(ctx, providers.ProviderGenerateRequest{
		JobID:     job.ID,
		Operation: providers.OperationTextToImage,
		Prompt:    description,
		Width:     deliveryRenderEdge,
		Height:    deliveryRenderEdge,
		Metadata: map[string]any{
			"world_id": worldID,
			"job_type": job.JobType,
		},
	})
	latency := time.Since(start).Milliseconds()

	if providerErr != nil {
		w.recordFailure(ctx, job, attempt.ID, attempt.ProviderID, providerErr, latency, finalAttempt)
		return providerErr
	}

	assetID := ids.NewVisualAssetID()
	urls, err := w.uploadImages(ctx, assetID, result.Images)
	if err != nil {
		w.recordFailure(ctx, job, attempt.ID, attempt.ProviderID, err, latency, finalAttempt)
		// Treat storage failures the same as provider failures for retry purposes.
		return err
	}

	providerID := resolved.providerID
	modelID := resolved.modelID
	routeID := resolved.routeID
	seed := result.Seed
	jobIDRef := job.ID

	// Phase 6A2: the asset's prompt_hash is the deterministic artifact render
	// hash the handler computed and carried in the payload — the same key the
	// reuse lookup matches on, so this asset is found by an identical repeat
	// request. The provider's own hash (if any) is provenance, not the cache
	// key, so it goes in metadata.provider_prompt_hash. Fall back to the
	// provider hash only if the payload has no render hash (pre-6A2 jobs).
	promptHash := payloadString(job.InputPayload, "prompt_hash")
	if promptHash == "" {
		promptHash = result.PromptHash
	}
	var metadata map[string]any
	if result.PromptHash != "" {
		metadata = map[string]any{"provider_prompt_hash": result.PromptHash}
	}

	// quality_tier comes from the request payload (the handler resolves and
	// stores the effective tier), not a hardcoded "standard", so the stored
	// asset's tier matches what the reuse lookup queries on.
	qualityTier := payloadString(job.InputPayload, "quality_tier")
	if qualityTier == "" {
		qualityTier = "standard"
	}

	// Phase 7A provenance: stamp the resolved provider/model/route (the same the
	// handler priced and persisted) so the stored asset records exactly which
	// route produced it.
	insertParams := assets.InsertParams{
		ID:         assetID,
		TenantID:   job.TenantID,
		WorldID:    worldID,
		AssetType:  "artifact",
		VariantKey: "default",
		// Persist the style profile provenance from the request (carried in
		// input_payload) so retrieval can later find this asset by style. The
		// request has no style_profile_version yet, so it stays nil.
		StyleProfileID:      payloadStrPtr(job.InputPayload, "style_profile_id"),
		StyleProfileVersion: payloadInt32Ptr(job.InputPayload, "style_profile_version"),
		QualityTier:         qualityTier,
		Metadata:            metadata,
		LowResUrl:           strPtr(urls.low),
		HighResUrl:          strPtr(urls.high),
		ThumbnailUrl:        strPtr(urls.thumb),
		ProviderID:          &providerID,
		ModelID:             &modelID,
		ProviderRouteID:     strPtr(routeID),
		PromptHash:          strPtr(promptHash),
		Seed:                strPtr(seed),
		GenerationJobID:     &jobIDRef,
	}

	// Phase 6A4 forced regeneration: a forced job (force_regenerate carried on
	// the payload) supersedes its slot — in one transaction, under a slot lock,
	// it inserts the new asset as the single ready row (version = prior_max + 1)
	// and archives every prior ready row of the EXACT artifact slot, linking them
	// forward. The exact slot is the FindReadyArtifactByPromptHash predicate, so a
	// regenerate never archives a compatible/preview neighbor. A non-forced job
	// takes the byte-for-byte unchanged single insert (version defaults to 1).
	var asset assets.VisualAsset
	if payloadBool(job.InputPayload, "force_regenerate") {
		asset, err = w.Assets.SupersedeAndInsertArtifact(ctx, insertParams, assets.ArtifactSlot{
			TenantID:       job.TenantID,
			WorldID:        worldID,
			StyleProfileID: payloadString(job.InputPayload, "style_profile_id"),
			QualityTier:    qualityTier,
			PromptHash:     promptHash,
		})
	} else {
		asset, err = w.Assets.Insert(ctx, insertParams)
	}
	if err != nil {
		w.recordFailure(ctx, job, attempt.ID, attempt.ProviderID, fmt.Errorf("insert asset: %w", err), latency, finalAttempt)
		return err
	}

	if _, err := w.Jobs.MarkCompleted(ctx, job.ID, job.TenantID, []string{asset.ID}); err != nil {
		w.log().Error("worker: mark completed", "job_id", jobID, "error", err)
		return err
	}

	if err := w.Jobs.MarkProviderAttemptSucceeded(ctx, attempt.ID, int32(latency)); err != nil {
		w.log().Warn("worker: mark attempt succeeded", "attempt_id", attempt.ID, "error", err)
	}

	latencyInt := int32(latency)
	tokenID := job.RequestedByTokenID
	providerIDPtr := &providerID
	if err := w.Jobs.InsertCostEvent(ctx, CostEventInsertParams{
		ID:                ids.NewCostEventID(),
		TenantID:          job.TenantID,
		JobID:             &job.ID,
		AssetID:           &asset.ID,
		TokenID:           tokenID,
		ProviderID:        providerIDPtr,
		ProviderAttemptID: &attempt.ID,
		Operation:         string(providers.OperationTextToImage),
		DurationMs:        &latencyInt,
		Status:            "completed",
	}); err != nil {
		w.log().Warn("worker: insert cost event", "job_id", jobID, "error", err)
	}

	// Commit the cost reservation: reserved → committed, move the held
	// estimate from reserved to spent, stamp actual_cost on the job + event.
	// Idempotent — safe if a later retry re-enters after a partial failure.
	if w.Finalizer != nil {
		if err := w.Finalizer.Commit(ctx, job.ID); err != nil {
			w.log().Error("worker: commit cost reservation", "job_id", jobID, "error", err)
			return err
		}
	}

	return nil
}

type uploadedURLs struct {
	high, low, thumb string
}

// uploadImages writes the three genuine resolution tiers (PRD 06 §4) for an
// asset: high = final (provider output), low = preview, thumb = thumbnail.
// The tiers are produced deterministically by imaging.EncodeTiers from the
// provider's first image, so a regenerate/reupload of the same bytes yields
// the same objects. A downscale failure is treated as a storage failure so the
// asset is never persisted referencing objects that were never written.
func (w *Worker) uploadImages(ctx context.Context, assetID string, images []providers.ProviderImage) (uploadedURLs, error) {
	if len(images) == 0 {
		return uploadedURLs{}, errors.New("worker: provider returned no images")
	}
	tiers, err := imaging.EncodeTiers(images[0].Bytes)
	if err != nil {
		return uploadedURLs{}, fmt.Errorf("%w: encode tiers: %v", errStorageFailure, err)
	}
	high, err := w.Storage.Put(ctx, storage.ObjectKey(assetID, storage.VariantHigh, "png"), tiers.Final, "image/png")
	if err != nil {
		return uploadedURLs{}, err
	}
	low, err := w.Storage.Put(ctx, storage.ObjectKey(assetID, storage.VariantLow, "png"), tiers.Preview, "image/png")
	if err != nil {
		return uploadedURLs{}, err
	}
	thumb, err := w.Storage.Put(ctx, storage.ObjectKey(assetID, storage.VariantThumb, "png"), tiers.Thumb, "image/png")
	if err != nil {
		return uploadedURLs{}, err
	}
	return uploadedURLs{high: high, low: low, thumb: thumb}, nil
}

func (w *Worker) recordFailure(ctx context.Context, job Job, attemptID, providerID string, callErr error, latencyMs int64, finalAttempt bool) {
	w.log().Error("worker: attempt failed",
		"job_id", job.ID,
		"attempt_id", attemptID,
		"error", callErr.Error(),
		"final", finalAttempt,
	)
	errMsg := callErr.Error()
	if err := w.Jobs.MarkProviderAttemptFailed(ctx, attemptID, errorCodeFor(callErr), errMsg, int32(latencyMs)); err != nil {
		w.log().Warn("worker: mark attempt failed", "attempt_id", attemptID, "error", err)
	}
	latencyInt := int32(latencyMs)
	tokenID := job.RequestedByTokenID
	providerIDPtr := &providerID
	attemptIDPtr := &attemptID
	if err := w.Jobs.InsertCostEvent(ctx, CostEventInsertParams{
		ID:                ids.NewCostEventID(),
		TenantID:          job.TenantID,
		JobID:             &job.ID,
		TokenID:           tokenID,
		ProviderID:        providerIDPtr,
		ProviderAttemptID: attemptIDPtr,
		Operation:         string(providers.OperationTextToImage),
		DurationMs:        &latencyInt,
		Status:            "failed",
	}); err != nil {
		w.log().Warn("worker: insert cost event (failure)", "job_id", job.ID, "error", err)
	}
	if finalAttempt {
		if _, err := w.Jobs.MarkFailed(ctx, job.ID, job.TenantID, errorCodeFor(callErr), errMsg, false); err != nil {
			w.log().Error("worker: mark job failed", "job_id", job.ID, "error", err)
		}
		// Terminal failure: release the cost reservation (reserved → released,
		// return the held estimate to the budget; spent untouched). Idempotent.
		if w.Finalizer != nil {
			if err := w.Finalizer.Release(ctx, job.ID); err != nil {
				w.log().Error("worker: release cost reservation", "job_id", job.ID, "error", err)
			}
		}
	}
}

func errorCodeFor(err error) string {
	if errors.Is(err, errStorageFailure) {
		return errorCodeStorageFailure
	}
	if errors.Is(err, errPersistence) {
		return errorCodePersistenceError
	}
	return errorCodeProviderFailure
}

var (
	errStorageFailure = errors.New("storage_failure")
	errPersistence    = errors.New("persistence_error")
)

func (w *Worker) log() *slog.Logger {
	if w.Logger == nil {
		return slog.Default()
	}
	return w.Logger
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	out := s
	return &out
}

// payloadString reads an optional string out of a job input payload, returning
// "" when the key is absent or not a string.
func payloadString(payload map[string]any, key string) string {
	s, _ := payload[key].(string)
	return s
}

// payloadBool reads an optional boolean out of a job input payload, returning
// false when the key is absent or not a bool. JSON booleans decode as bool, so
// force_regenerate carried by the handler reads back cleanly here.
func payloadBool(payload map[string]any, key string) bool {
	b, _ := payload[key].(bool)
	return b
}

// payloadStrPtr reads an optional string out of a job input payload, returning
// nil when the key is absent or empty.
func payloadStrPtr(payload map[string]any, key string) *string {
	return strPtr(payloadString(payload, key))
}

// payloadInt32Ptr reads an optional integer out of a job input payload. JSON
// numbers decode as float64; an absent or non-numeric value yields nil. This
// lets a style_profile_version flow through if a future request carries one,
// while today's requests (which don't) leave it nil.
func payloadInt32Ptr(payload map[string]any, key string) *int32 {
	switch v := payload[key].(type) {
	case float64:
		n := int32(v)
		return &n
	case int:
		n := int32(v)
		return &n
	case int32:
		n := v
		return &n
	default:
		return nil
	}
}
