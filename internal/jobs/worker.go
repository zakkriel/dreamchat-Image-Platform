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

	// previewRenderEdge is the square edge (px) the worker asks the provider to
	// produce for the Phase 7B preview tier. It is deliberately smaller than
	// deliveryRenderEdge so the preview asset is genuinely lighter than the final
	// asset (smaller source → smaller delivered bytes where the provider honors
	// dimensions, e.g. mock). The preview is not a downscale of the final — it is
	// a separate, lower-resolution provider render.
	previewRenderEdge = 512

	// deliveryModePreviewFirst is the payload value (persisted by the handler at
	// job-creation time) that opts a job into two-phase preview-first delivery.
	deliveryModePreviewFirst = "preview_first"
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
	case statusCancelled:
		// Phase 7C-1a: cancel is terminal. Do not call the provider, upload,
		// insert an asset, mark completed, or commit cost. Release the
		// reservation as a safe idempotent cleanup (admin cancel already
		// released it inside its own transaction) and stop cleanly.
		if w.Finalizer != nil {
			if err := w.Finalizer.Release(ctx, job.ID); err != nil {
				w.log().Error("worker: release cost reservation (cancelled job)", "job_id", jobID, "error", err)
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

	// Phase 7B two-phase preview-first generation applies only when the request
	// opted in (payload.delivery_mode == preview_first) AND the resolved route is
	// a true_preview route (preview_capability persisted on the payload at
	// creation time, from the route the handler resolved — the worker never
	// re-resolves). Any other job takes the unchanged Phase 7A single-phase path.
	if w.isPreviewFirst(job) {
		return w.processPreviewFirst(ctx, job, provider, resolved, attemptNumber, finalAttempt)
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

	// Phase 7A provenance: stamp the resolved provider/model/route (the same the
	// handler priced and persisted) so the stored asset records exactly which
	// route produced it. The shared builder also carries the request's render
	// hash (prompt_hash), quality tier, style provenance, and the provider hash
	// in metadata.
	insertParams := w.buildArtifactInsertParams(job, resolved, assetID, urls, result, worldID)

	// Phase 6A4 forced regeneration: a forced job (force_regenerate carried on
	// the payload) supersedes its slot — in one transaction, under a slot lock,
	// it inserts the new asset as the single ready row (version = prior_max + 1)
	// and archives every prior ready row of the EXACT artifact slot, linking them
	// forward. The exact slot is the FindReadyArtifactByPromptHash predicate, so a
	// regenerate never archives a compatible/preview neighbor. A non-forced job
	// takes the byte-for-byte unchanged single insert (version defaults to 1).
	// Phase 7C-1a guarded persist: insert the final asset and complete the job
	// in ONE transaction under the job row lock. If a cancel landed before this
	// write, nothing is inserted, the job stays cancelled, and we stop cleanly
	// without committing cost — closing the race between a provider returning
	// and a cancel arriving. Forced jobs supersede their slot inside the same
	// guarded transaction.
	forced := payloadBool(job.InputPayload, "force_regenerate")
	asset, outcome, err := w.Jobs.InsertFinalAssetAndCompleteJobIfNotCancelled(ctx, job.ID, job.TenantID, insertParams, forced, artifactSlotFor(job, insertParams))
	if err != nil {
		w.recordFailure(ctx, job, attempt.ID, attempt.ProviderID, fmt.Errorf("insert asset: %w", err), latency, finalAttempt)
		return err
	}
	if outcome == PersistSkippedCancelled {
		return w.finishCancelled(ctx, job, "final")
	}

	if err := w.Jobs.MarkProviderAttemptSucceeded(ctx, attempt.ID, int32(latency)); err != nil {
		w.log().Warn("worker: mark attempt succeeded", "attempt_id", attempt.ID, "error", err)
	}

	latencyInt := int32(latency)
	tokenID := job.RequestedByTokenID
	providerID := resolved.providerID
	if err := w.Jobs.InsertCostEvent(ctx, CostEventInsertParams{
		ID:                ids.NewCostEventID(),
		TenantID:          job.TenantID,
		JobID:             &job.ID,
		AssetID:           &asset.ID,
		TokenID:           tokenID,
		ProviderID:        &providerID,
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

// finishCancelled handles a guarded persist that skipped because the job was
// cancelled before persistence (Phase 7C-1a). The worker records no output,
// does not commit cost, releases the reservation idempotently (admin cancel
// already released it in its own transaction), and returns nil so the task is
// not retried as an error. Provider work that already completed is simply
// discarded — its result is never recorded as job output.
func (w *Worker) finishCancelled(ctx context.Context, job Job, phase string) error {
	w.log().Info("worker: job cancelled before persist; skipping output",
		"job_id", job.ID, "phase", phase)
	if w.Finalizer != nil {
		if err := w.Finalizer.Release(ctx, job.ID); err != nil {
			w.log().Error("worker: release cost reservation (cancelled before persist)", "job_id", job.ID, "error", err)
			return err
		}
	}
	return nil
}

// isPreviewFirst reports whether a job takes the Phase 7B two-phase path. Both
// must hold: the request opted in (payload.delivery_mode == preview_first) and
// the resolved route is a true_preview route (preview_capability persisted on
// the payload at creation time). The resolver guarantees a preview_first request
// only resolves a true_preview route — the second check is a belt-and-suspenders
// guard so a payload missing the preview capability never silently two-phases.
func (w *Worker) isPreviewFirst(job Job) bool {
	return payloadString(job.InputPayload, "delivery_mode") == deliveryModePreviewFirst &&
		payloadString(job.InputPayload, "preview_capability") == string(providers.PreviewCapabilityTrue)
}

// processPreviewFirst runs the Phase 7B two-phase lifecycle for one job:
//
//	Phase A (preview): generate a lighter preview render, upload its tiers,
//	  insert a visual_asset with status=preview_ready + the preview_safe tag,
//	  then commit the job to preview_ready with preview_asset_ids. This is
//	  committed in its own DB transactions BEFORE final generation begins, so
//	  the preview is externally observable (job read + job-assets read) before
//	  the final asset exists.
//	Phase B (final): generate the full-resolution render, upload its tiers,
//	  insert a visual_asset with status=ready, complete the job with
//	  final_asset_ids, and commit the cost reservation ONCE.
//
// Retry safety: the preview phase is skipped entirely when preview_asset_ids
// already exists (a prior attempt committed it), so a retry of a preview_ready
// job resumes at final without duplicating the preview or re-reserving cost. A
// failure in either phase routes through recordFailure: on the terminal attempt
// the job is marked failed and the reservation released. A final-phase failure
// after the preview was delivered leaves the preview asset readable and
// final_asset_ids empty (the preview is not superseded — it is the last useful
// output of the failed two-phase job).
func (w *Worker) processPreviewFirst(ctx context.Context, job Job, provider providers.ImageProvider, resolved resolvedRoute, attemptNumber int32, finalAttempt bool) error {
	worldID := ""
	if job.WorldID != nil {
		worldID = *job.WorldID
	}
	description, _ := job.InputPayload["description"].(string)

	// --- Phase A: preview ---------------------------------------------------
	// Resume safety: a non-empty preview_asset_ids means a prior attempt already
	// generated and committed the preview. Skip straight to final so a retry
	// never regenerates the preview and never recharges.
	if len(job.PreviewAssetIds) == 0 {
		if _, err := w.Jobs.MarkRunning(ctx, job.ID, job.TenantID); err != nil {
			w.log().Error("worker: mark running (preview)", "job_id", job.ID, "error", err)
			return err
		}
		attempt, err := w.Jobs.InsertProviderAttempt(ctx, ProviderAttemptInsertParams{
			ID:              ids.NewProviderAttemptID(),
			GenerationJobID: job.ID,
			ProviderID:      resolved.providerID,
			AttemptNumber:   attemptNumber,
		})
		if err != nil {
			w.log().Error("worker: insert attempt (preview)", "job_id", job.ID, "error", err)
			return err
		}

		start := time.Now()
		result, providerErr := provider.Generate(ctx, providers.ProviderGenerateRequest{
			JobID:     job.ID,
			Operation: providers.OperationTextToImage,
			Prompt:    description,
			Width:     previewRenderEdge,
			Height:    previewRenderEdge,
			Metadata: map[string]any{
				"world_id": worldID,
				"job_type": job.JobType,
				"tier":     "preview",
			},
		})
		latency := time.Since(start).Milliseconds()
		if providerErr != nil {
			// Preview-phase failure: no preview asset is created. On the terminal
			// attempt recordFailure marks the job failed and releases the reservation.
			w.recordFailure(ctx, job, attempt.ID, attempt.ProviderID, providerErr, latency, finalAttempt)
			return providerErr
		}

		previewAssetID := ids.NewVisualAssetID()
		urls, err := w.uploadImages(ctx, previewAssetID, result.Images)
		if err != nil {
			w.recordFailure(ctx, job, attempt.ID, attempt.ProviderID, err, latency, finalAttempt)
			return err
		}

		previewParams := w.buildArtifactInsertParams(job, resolved, previewAssetID, urls, result, worldID)
		// The preview tier is tagged preview_safe and lands status=preview_ready;
		// it is never a reuse target.
		previewParams.CompatibilityTags = []string{assets.TagPreviewSafe}
		// Phase 7C-1a guarded persist: insert the preview asset and mark the job
		// preview_ready in ONE transaction under the job row lock. If a cancel
		// landed first, nothing is inserted and the job stays cancelled — a
		// cancelled preview-first job never gets a preview output recorded. The
		// preview state is committed before final generation begins, so it stays
		// externally observable through the job read and the job-assets read.
		_, outcome, err := w.Jobs.InsertPreviewAssetAndMarkPreviewReadyIfNotCancelled(ctx, job.ID, job.TenantID, previewParams)
		if err != nil {
			w.recordFailure(ctx, job, attempt.ID, attempt.ProviderID, fmt.Errorf("insert preview asset: %w", err), latency, finalAttempt)
			return err
		}
		if outcome == PersistSkippedCancelled {
			return w.finishCancelled(ctx, job, "preview")
		}
		if err := w.Jobs.MarkProviderAttemptSucceeded(ctx, attempt.ID, int32(latency)); err != nil {
			w.log().Warn("worker: mark attempt succeeded (preview)", "attempt_id", attempt.ID, "error", err)
		}
	}

	// --- Phase B: final -----------------------------------------------------
	attempt, err := w.Jobs.InsertProviderAttempt(ctx, ProviderAttemptInsertParams{
		ID:              ids.NewProviderAttemptID(),
		GenerationJobID: job.ID,
		ProviderID:      resolved.providerID,
		AttemptNumber:   attemptNumber,
	})
	if err != nil {
		w.log().Error("worker: insert attempt (final)", "job_id", job.ID, "error", err)
		return err
	}

	start := time.Now()
	result, providerErr := provider.Generate(ctx, providers.ProviderGenerateRequest{
		JobID:     job.ID,
		Operation: providers.OperationTextToImage,
		Prompt:    description,
		Width:     deliveryRenderEdge,
		Height:    deliveryRenderEdge,
		Metadata: map[string]any{
			"world_id": worldID,
			"job_type": job.JobType,
			"tier":     "final",
		},
	})
	latency := time.Since(start).Milliseconds()
	if providerErr != nil {
		// Final-phase failure AFTER the preview was delivered: on the terminal
		// attempt the job is marked failed and the reservation released. The
		// preview asset stays preview_ready and final_asset_ids stays empty.
		w.recordFailure(ctx, job, attempt.ID, attempt.ProviderID, providerErr, latency, finalAttempt)
		return providerErr
	}

	finalAssetID := ids.NewVisualAssetID()
	urls, err := w.uploadImages(ctx, finalAssetID, result.Images)
	if err != nil {
		w.recordFailure(ctx, job, attempt.ID, attempt.ProviderID, err, latency, finalAttempt)
		return err
	}

	finalParams := w.buildArtifactInsertParams(job, resolved, finalAssetID, urls, result, worldID)
	// Phase 7C-1a guarded persist: insert the final asset and complete the job
	// in ONE transaction under the job row lock. If a cancel landed after the
	// preview was delivered but before this final write, nothing is inserted,
	// the job stays cancelled, and final_asset_ids stays empty — the preview
	// asset (committed earlier) remains readable. Forced jobs supersede prior
	// ready finals inside the same guarded transaction (Phase 6A4); the preview
	// asset is a different status and is never superseded.
	forced := payloadBool(job.InputPayload, "force_regenerate")
	asset, outcome, err := w.Jobs.InsertFinalAssetAndCompleteJobIfNotCancelled(ctx, job.ID, job.TenantID, finalParams, forced, artifactSlotFor(job, finalParams))
	if err != nil {
		w.recordFailure(ctx, job, attempt.ID, attempt.ProviderID, fmt.Errorf("insert asset: %w", err), latency, finalAttempt)
		return err
	}
	if outcome == PersistSkippedCancelled {
		return w.finishCancelled(ctx, job, "final")
	}
	if err := w.Jobs.MarkProviderAttemptSucceeded(ctx, attempt.ID, int32(latency)); err != nil {
		w.log().Warn("worker: mark attempt succeeded (final)", "attempt_id", attempt.ID, "error", err)
	}

	latencyInt := int32(latency)
	providerID := resolved.providerID
	if err := w.Jobs.InsertCostEvent(ctx, CostEventInsertParams{
		ID:                ids.NewCostEventID(),
		TenantID:          job.TenantID,
		JobID:             &job.ID,
		AssetID:           &asset.ID,
		TokenID:           job.RequestedByTokenID,
		ProviderID:        &providerID,
		ProviderAttemptID: &attempt.ID,
		Operation:         string(providers.OperationTextToImage),
		DurationMs:        &latencyInt,
		Status:            "completed",
	}); err != nil {
		w.log().Warn("worker: insert cost event (final)", "job_id", job.ID, "error", err)
	}

	// Commit the cost reservation ONCE, only after final success. There is no
	// separate preview charge. Idempotent — a retry that re-enters after the job
	// is completed re-commits via the terminal short-circuit in Process.
	if w.Finalizer != nil {
		if err := w.Finalizer.Commit(ctx, job.ID); err != nil {
			w.log().Error("worker: commit cost reservation (preview-first)", "job_id", job.ID, "error", err)
			return err
		}
	}

	return nil
}

// buildArtifactInsertParams assembles the visual_assets InsertParams shared by
// the single-phase write and both tiers of the two-phase write: provenance from
// the resolved route, the request's render hash (prompt_hash) and quality tier,
// style provenance, and the provider hash in metadata. Callers set
// status-specific fields (e.g. the preview tier's compatibility tags) and choose
// Insert vs InsertPreview.
func (w *Worker) buildArtifactInsertParams(job Job, resolved resolvedRoute, assetID string, urls uploadedURLs, result providers.ProviderGenerateResult, worldID string) assets.InsertParams {
	providerID := resolved.providerID
	modelID := resolved.modelID
	routeID := resolved.routeID
	seed := result.Seed
	jobIDRef := job.ID

	// Phase 6A2: the asset's prompt_hash is the deterministic artifact render
	// hash the handler computed and carried in the payload — the same key the
	// reuse lookup matches on. The provider's own hash (if any) is provenance,
	// not the cache key, so it goes in metadata.provider_prompt_hash. Fall back
	// to the provider hash only if the payload has no render hash (pre-6A2 jobs).
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

	return assets.InsertParams{
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
}

// artifactSlotFor builds the Phase 6A4 forced-regeneration supersede slot from
// the asset insert params — the exact FindReadyArtifactByPromptHash predicate
// (owner + style + quality + render hash). Shared by the single-phase and
// two-phase final writes.
func artifactSlotFor(job Job, p assets.InsertParams) assets.ArtifactSlot {
	promptHash := ""
	if p.PromptHash != nil {
		promptHash = *p.PromptHash
	}
	return assets.ArtifactSlot{
		TenantID:       job.TenantID,
		WorldID:        p.WorldID,
		StyleProfileID: payloadString(job.InputPayload, "style_profile_id"),
		QualityTier:    p.QualityTier,
		PromptHash:     promptHash,
	}
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
