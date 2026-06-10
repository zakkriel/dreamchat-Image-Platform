package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hibiken/asynq"

	"github.com/zakkriel/drchat-image-platform/internal/assets"
	"github.com/zakkriel/drchat-image-platform/internal/ids"
	"github.com/zakkriel/drchat-image-platform/internal/providers"
)

// Pack job types and statuses (Phase 5A). Pack orchestration is
// platform-side per ADR-008: the worker fans out one provider call per
// variant_key, owns per-item generation, and writes asset_pack_items.
const (
	JobTypeCharacterPack = "character_pack"
	JobTypePlacePack     = "place_pack"

	packStatusInProgress    = "in_progress"
	packStatusCompleted     = "completed"
	packStatusWithWarnings  = "completed_with_warnings"
	packStatusFailed        = "failed"
	errorCodePackAllFailed  = "pack_all_items_failed"
	errorMessagePackFailed  = "all pack items failed to generate"
	errorCodePackInvalidJob = "pack_invalid_job"
)

// NewPackHandlerFunc returns the asynq handler for TaskGeneratePack, so
// cmd/worker stays a thin wiring layer (mirrors NewHandlerFunc).
func (w *Worker) NewPackHandlerFunc() func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		var payload TaskPayload
		if err := json.Unmarshal(t.Payload(), &payload); err != nil {
			return fmt.Errorf("worker: decode pack payload: %w", err)
		}
		return w.ProcessPack(ctx, payload.JobID)
	}
}

// ProcessPack is the pack fan-out body. Unlike the single-artifact Process,
// a pack run always reaches a terminal state in one pass: per-item failures
// are recorded and the batch continues, so there is no per-attempt retry
// loop — only infra errors (job lookup, terminal bookkeeping) return an
// error for asynq to retry, and the terminal short-circuit plus the
// existing-items skip make that retry safe.
func (w *Worker) ProcessPack(ctx context.Context, jobID string) error {
	job, err := w.Jobs.GetByID(ctx, jobID)
	if err != nil {
		w.log().Error("worker: lookup pack job", "job_id", jobID, "error", err)
		return err
	}

	// Retry-safety short-circuit (same rule as 4B): a terminal job never
	// re-fans-out; only the idempotent cost finalization may be outstanding.
	switch job.Status {
	case "completed":
		if w.Finalizer != nil {
			if err := w.Finalizer.Commit(ctx, job.ID); err != nil {
				w.log().Error("worker: commit cost reservation (terminal pack job)", "job_id", jobID, "error", err)
				return err
			}
		}
		return nil
	case "failed":
		if w.Finalizer != nil {
			if err := w.Finalizer.Release(ctx, job.ID); err != nil {
				w.log().Error("worker: release cost reservation (terminal pack job)", "job_id", jobID, "error", err)
				return err
			}
		}
		return nil
	}

	plan, planErr := packPlanFromJob(job)
	if planErr != nil {
		// A pack job without a pack link / variants is unrunnable; fail it
		// terminally rather than retrying a payload that can't change.
		w.log().Error("worker: invalid pack job", "job_id", jobID, "error", planErr)
		if _, err := w.Jobs.MarkFailed(ctx, job.ID, job.TenantID, errorCodePackInvalidJob, planErr.Error(), false); err != nil {
			return err
		}
		if w.Finalizer != nil {
			if err := w.Finalizer.Release(ctx, job.ID); err != nil {
				return err
			}
		}
		return nil
	}

	if _, err := w.Jobs.MarkRunning(ctx, job.ID, job.TenantID); err != nil {
		w.log().Error("worker: mark pack running", "job_id", jobID, "error", err)
		return err
	}
	if err := w.Jobs.UpdateAssetPackStatus(ctx, plan.packID, packStatusInProgress); err != nil {
		w.log().Error("worker: mark pack in_progress", "job_id", jobID, "pack_id", plan.packID, "error", err)
		return err
	}

	// Existing items short-circuit: if a previous attempt already delivered
	// some variants (e.g. asynq retried after a transient terminal-write
	// failure), count them as succeeded instead of re-generating — the
	// UNIQUE (asset_pack_id, variant_key) constraint would reject a re-insert.
	existing, err := w.Jobs.ListAssetPackItems(ctx, plan.packID)
	if err != nil {
		w.log().Error("worker: list pack items", "job_id", jobID, "pack_id", plan.packID, "error", err)
		return err
	}
	delivered := make(map[string]string, len(existing))
	for _, item := range existing {
		delivered[item.VariantKey] = item.VisualAssetID
	}

	start := time.Now()
	providerID := w.Provider.Capabilities().ProviderID
	var succeeded []string
	failedItems := 0

	for i, variantKey := range plan.variantKeys {
		if assetID, ok := delivered[variantKey]; ok {
			succeeded = append(succeeded, assetID)
			continue
		}
		assetID, itemErr := w.generatePackItem(ctx, job, plan, providerID, variantKey, i)
		if itemErr != nil {
			// Per-item failure (provider/storage/persistence): record it and
			// continue with the next variant — never abort the batch.
			w.log().Warn("worker: pack item failed",
				"job_id", job.ID, "pack_id", plan.packID,
				"variant_key", variantKey, "error", itemErr.Error(),
			)
			failedItems++
			continue
		}
		succeeded = append(succeeded, assetID)
	}

	// One cost event for the whole pack (operation text_to_image); the
	// finalizer stamps estimated/actual onto it as in 4B. Per-item provider
	// telemetry lives in provider_attempts.
	latencyInt := int32(time.Since(start).Milliseconds())
	eventStatus := "completed"
	if len(succeeded) == 0 {
		eventStatus = "failed"
	}
	if err := w.Jobs.InsertCostEvent(ctx, CostEventInsertParams{
		ID:         ids.NewCostEventID(),
		TenantID:   job.TenantID,
		JobID:      &job.ID,
		TokenID:    job.RequestedByTokenID,
		ProviderID: &providerID,
		Operation:  string(providers.OperationTextToImage),
		DurationMs: &latencyInt,
		Status:     eventStatus,
	}); err != nil {
		w.log().Warn("worker: insert pack cost event", "job_id", job.ID, "error", err)
	}

	// Terminal rule. The pack status is written before the job status so a
	// retry after a partial terminal write re-enters fan-out (skipping the
	// delivered items) instead of short-circuiting past the pack update.
	//
	// Cost rule for 5A: the reservation holds N × price and commits in full
	// on any success — provider cost is per attempt/call, not per delivered
	// asset, so a partial pack still incurred N calls. Total failure releases
	// in full (mirrors 4B). Proportional per-item reconciliation is deferred
	// to real provider reconciliation.
	if len(succeeded) == 0 {
		if err := w.Jobs.UpdateAssetPackStatus(ctx, plan.packID, packStatusFailed); err != nil {
			w.log().Error("worker: mark pack failed", "job_id", job.ID, "pack_id", plan.packID, "error", err)
			return err
		}
		if _, err := w.Jobs.MarkFailed(ctx, job.ID, job.TenantID, errorCodePackAllFailed, errorMessagePackFailed, false); err != nil {
			w.log().Error("worker: mark pack job failed", "job_id", job.ID, "error", err)
			return err
		}
		if w.Finalizer != nil {
			if err := w.Finalizer.Release(ctx, job.ID); err != nil {
				w.log().Error("worker: release pack cost reservation", "job_id", job.ID, "error", err)
				return err
			}
		}
		return nil
	}

	packStatus := packStatusCompleted
	if failedItems > 0 {
		packStatus = packStatusWithWarnings
	}
	if err := w.Jobs.UpdateAssetPackStatus(ctx, plan.packID, packStatus); err != nil {
		w.log().Error("worker: mark pack completed", "job_id", job.ID, "pack_id", plan.packID, "error", err)
		return err
	}
	if _, err := w.Jobs.MarkCompleted(ctx, job.ID, job.TenantID, succeeded); err != nil {
		w.log().Error("worker: mark pack job completed", "job_id", job.ID, "error", err)
		return err
	}
	if w.Finalizer != nil {
		if err := w.Finalizer.Commit(ctx, job.ID); err != nil {
			w.log().Error("worker: commit pack cost reservation", "job_id", job.ID, "error", err)
			return err
		}
	}
	return nil
}

// generatePackItem runs one variant end to end: provider attempt row,
// provider call, image upload, visual_assets insert, asset_pack_items
// insert. Returns the new asset id, or the per-item error.
func (w *Worker) generatePackItem(ctx context.Context, job Job, plan packPlan, providerID, variantKey string, index int) (string, error) {
	attempt, err := w.Jobs.InsertProviderAttempt(ctx, ProviderAttemptInsertParams{
		ID:              ids.NewProviderAttemptID(),
		GenerationJobID: job.ID,
		ProviderID:      providerID,
		AttemptNumber:   int32(index + 1),
	})
	if err != nil {
		return "", fmt.Errorf("insert attempt: %w", err)
	}

	// 5A keeps the prompt trivial: the variant_key is an opaque role string
	// appended to the identity's name. Variant semantics are 5B.
	prompt := plan.displayName + " — " + variantKey

	start := time.Now()
	result, providerErr := w.Provider.Generate(ctx, providers.ProviderGenerateRequest{
		JobID:     job.ID,
		Operation: providers.OperationTextToImage,
		Prompt:    prompt,
		Metadata: map[string]any{
			"world_id":    plan.worldID,
			"job_type":    job.JobType,
			"variant_key": variantKey,
		},
	})
	latency := int32(time.Since(start).Milliseconds())
	if providerErr != nil {
		w.markPackAttemptFailed(ctx, attempt.ID, providerErr, latency)
		return "", providerErr
	}

	assetID := ids.NewVisualAssetID()
	urls, err := w.uploadImages(ctx, assetID, result.Images)
	if err != nil {
		w.markPackAttemptFailed(ctx, attempt.ID, fmt.Errorf("%w: %v", errStorageFailure, err), latency)
		return "", err
	}

	// The visual_assets row and its asset_pack_items row commit in one
	// transaction: a delivered variant is observable atomically, so a failed
	// item insert can't strand an orphan asset the retry path (which detects
	// delivery via asset_pack_items) would never see — and therefore can't
	// produce duplicate assets for the same pack variant.
	modelID := mockProviderModelID
	jobIDRef := job.ID
	identityID := plan.visualIdentityID
	// Phase 5B: classify the variant_key deterministically and stamp the
	// compatibility/provenance fields (variant_family, compatibility_tags,
	// fallback_allowed, fallback_rank) plus structured metadata onto the asset.
	// An unrecognized key classifies as family "unknown" with no fallback
	// eligibility — never silently generic-safe.
	assetParams := assets.InsertParams{
		ID:               assetID,
		TenantID:         job.TenantID,
		WorldID:          plan.worldID,
		VisualIdentityID: &identityID,
		AssetType:        plan.assetType,
		VariantKey:       variantKey,
		QualityTier:      plan.qualityTier,
		LowResUrl:        strPtr(urls.low),
		HighResUrl:       strPtr(urls.high),
		ThumbnailUrl:     strPtr(urls.thumb),
		ProviderID:       &providerID,
		ModelID:          &modelID,
		PromptHash:       strPtr(result.PromptHash),
		Seed:             strPtr(result.Seed),
		GenerationJobID:  &jobIDRef,
	}
	assets.ClassifyVariant(plan.entityType, variantKey).ApplyTo(&assetParams)
	if err := w.Jobs.InsertPackItemWithAsset(ctx, assetParams, AssetPackItemInsertParams{
		ID:            ids.NewAssetPackItemID(),
		AssetPackID:   plan.packID,
		VisualAssetID: assetID,
		VariantKey:    variantKey,
		SortOrder:     int32(index),
	}); err != nil {
		w.markPackAttemptFailed(ctx, attempt.ID, fmt.Errorf("%w: insert asset + pack item: %v", errPersistence, err), latency)
		return "", err
	}

	if err := w.Jobs.MarkProviderAttemptSucceeded(ctx, attempt.ID, latency); err != nil {
		w.log().Warn("worker: mark pack attempt succeeded", "attempt_id", attempt.ID, "error", err)
	}
	return assetID, nil
}

func (w *Worker) markPackAttemptFailed(ctx context.Context, attemptID string, callErr error, latencyMs int32) {
	if err := w.Jobs.MarkProviderAttemptFailed(ctx, attemptID, errorCodeFor(callErr), callErr.Error(), latencyMs); err != nil {
		w.log().Warn("worker: mark pack attempt failed", "attempt_id", attemptID, "error", err)
	}
}

// packPlan is the per-run view of the pack job's input payload, written by
// the generate-pack handlers at request time so the worker needs only job_id.
type packPlan struct {
	packID           string
	variantKeys      []string
	worldID          string
	visualIdentityID string
	displayName      string
	assetType        string
	entityType       string // assets.EntityCharacter | assets.EntityPlace
	qualityTier      string
}

func packPlanFromJob(job Job) (packPlan, error) {
	plan := packPlan{}
	if job.AssetPackID == nil || *job.AssetPackID == "" {
		return plan, fmt.Errorf("pack job %s has no asset_pack_id", job.ID)
	}
	plan.packID = *job.AssetPackID

	switch job.JobType {
	case JobTypeCharacterPack:
		plan.assetType = "character_portrait"
		plan.entityType = assets.EntityCharacter
	case JobTypePlacePack:
		plan.assetType = "place_scene"
		plan.entityType = assets.EntityPlace
	default:
		return plan, fmt.Errorf("job type %q is not a pack job", job.JobType)
	}

	raw, _ := job.InputPayload["variant_keys"].([]any)
	for _, v := range raw {
		if s, ok := v.(string); ok && s != "" {
			plan.variantKeys = append(plan.variantKeys, s)
		}
	}
	if len(plan.variantKeys) == 0 {
		return plan, fmt.Errorf("pack job %s has no variant_keys in input_payload", job.ID)
	}

	if job.WorldID != nil {
		plan.worldID = *job.WorldID
	}
	plan.visualIdentityID, _ = job.InputPayload["visual_identity_id"].(string)
	if plan.visualIdentityID == "" {
		return plan, fmt.Errorf("pack job %s has no visual_identity_id in input_payload", job.ID)
	}
	plan.displayName, _ = job.InputPayload["display_name"].(string)
	plan.qualityTier, _ = job.InputPayload["quality_tier"].(string)
	if plan.qualityTier == "" {
		plan.qualityTier = "standard"
	}
	return plan, nil
}
