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
	"github.com/zakkriel/drchat-image-platform/internal/ids"
	"github.com/zakkriel/drchat-image-platform/internal/providers"
	"github.com/zakkriel/drchat-image-platform/internal/storage"
)

const (
	errorCodeProviderFailure  = "provider_failure"
	errorCodePersistenceError = "persistence_error"
	errorCodeStorageFailure   = "storage_failure"
)

// Worker holds the dependencies the asynq handler resolves a job against.
// Each task call re-reads the generation_jobs row from Postgres; the queue
// payload only carries the job_id.
type Worker struct {
	Jobs     Repository
	Assets   assets.Repository
	Storage  storage.Storage
	Provider providers.ImageProvider
	Logger   *slog.Logger
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

	if _, err := w.Jobs.MarkRunning(ctx, job.ID, job.TenantID); err != nil {
		w.log().Error("worker: mark running", "job_id", jobID, "error", err)
		return err
	}

	attempt, err := w.Jobs.InsertProviderAttempt(ctx, ProviderAttemptInsertParams{
		ID:              ids.NewProviderAttemptID(),
		GenerationJobID: job.ID,
		ProviderID:      w.Provider.Capabilities().ProviderID,
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
	result, providerErr := w.Provider.Generate(ctx, providers.ProviderGenerateRequest{
		JobID:     job.ID,
		Operation: providers.OperationTextToImage,
		Prompt:    description,
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

	providerID := attempt.ProviderID
	promptHash := result.PromptHash
	seed := result.Seed
	jobIDRef := job.ID

	// model_id is intentionally NULL: visual_assets.model_id references
	// provider_models(id), and Phase 3 does not populate the provider model
	// catalog. Phase 4 (provider routing + price book) introduces the
	// provider_models rows and wires this field.
	asset, err := w.Assets.Insert(ctx, assets.InsertParams{
		ID:              assetID,
		TenantID:        job.TenantID,
		WorldID:         worldID,
		AssetType:       "artifact",
		VariantKey:      "default",
		QualityTier:     "standard",
		LowResUrl:       strPtr(urls.low),
		HighResUrl:      strPtr(urls.high),
		ThumbnailUrl:    strPtr(urls.thumb),
		ProviderID:      &providerID,
		ModelID:         nil,
		PromptHash:      strPtr(promptHash),
		Seed:            strPtr(seed),
		GenerationJobID: &jobIDRef,
	})
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

	return nil
}

type uploadedURLs struct {
	high, low, thumb string
}

func (w *Worker) uploadImages(ctx context.Context, assetID string, images []providers.ProviderImage) (uploadedURLs, error) {
	if len(images) == 0 {
		return uploadedURLs{}, errors.New("worker: provider returned no images")
	}
	img := images[0]
	high, err := w.Storage.Put(ctx, storage.ObjectKey(assetID, storage.VariantHigh, "png"), img.Bytes, contentTypeOr(img.ContentType))
	if err != nil {
		return uploadedURLs{}, err
	}
	low, err := w.Storage.Put(ctx, storage.ObjectKey(assetID, storage.VariantLow, "png"), img.Bytes, contentTypeOr(img.ContentType))
	if err != nil {
		return uploadedURLs{}, err
	}
	thumb, err := w.Storage.Put(ctx, storage.ObjectKey(assetID, storage.VariantThumb, "png"), img.Bytes, contentTypeOr(img.ContentType))
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

func contentTypeOr(ct string) string {
	if ct == "" {
		return "image/png"
	}
	return ct
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	out := s
	return &out
}
