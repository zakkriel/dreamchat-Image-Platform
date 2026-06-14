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
	"github.com/zakkriel/drchat-image-platform/internal/webhooks"
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

// WebhookEmitter emits job-lifecycle webhook events (Phase 7C-4). The worker
// depends on this narrow interface rather than the concrete *webhooks.Emitter
// so it stays unit-testable (nil in tests; *webhooks.Emitter in production).
//
// MVP limitation: events are emitted ONLY at the worker's durable lifecycle
// transitions below (preview committed, completed+committed, terminal failure).
// They are NOT emitted for admin cancel, a preflight denial at job creation, or
// an enqueue failure — those paths never reach these emit points.
type WebhookEmitter interface {
	Emit(ctx context.Context, in webhooks.EmitInput) error
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

	// Webhooks emits job-lifecycle webhook events (Phase 7C-4). Optional/nil-safe:
	// nil in unit tests and when no emitter is wired. Emission is best-effort and
	// never fails the job.
	Webhooks WebhookEmitter
}

// emit best-effort emits one job-lifecycle webhook event. It no-ops when no
// emitter is wired (Webhooks == nil) and logs — never fails the job — on an
// emission error. Called only AFTER the relevant job state is durably committed.
func (w *Worker) emit(ctx context.Context, tenantID, eventType, jobID string, data map[string]any) {
	if w.Webhooks == nil {
		return
	}
	if err := w.Webhooks.Emit(ctx, webhooks.EmitInput{
		TenantID:  tenantID,
		EventType: eventType,
		JobID:     jobID,
		Data:      data,
	}); err != nil {
		w.log().Warn("worker: emit webhook", "job_id", jobID, "event", eventType, "error", err)
	}
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

// fallbackRoutesFromPayload reads the same-price fallback chain the handler
// persisted under "fallback_routes" (Phase 7C-4): a JSON array of objects, each
// carrying provider_id/model_id/provider_route_id/preview_capability. It is
// deliberately tolerant — a missing key, a wrong type, or an entry missing its
// provider_id/model_id is skipped rather than failing the job, because the
// primary route alone is always sufficient to run the job. The returned routes
// are the ALTERNATES only (the primary is never in this list); the worker
// prepends the primary before walking them.
func fallbackRoutesFromPayload(payload map[string]any) []resolvedRoute {
	raw, ok := payload["fallback_routes"].([]any)
	if !ok {
		return nil
	}
	out := make([]resolvedRoute, 0, len(raw))
	for _, entry := range raw {
		m, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		rr := resolvedRoute{
			providerID: payloadString(m, "provider_id"),
			modelID:    payloadString(m, "model_id"),
			routeID:    payloadString(m, "provider_route_id"),
		}
		if rr.providerID == "" || rr.modelID == "" {
			continue
		}
		out = append(out, rr)
	}
	return out
}

// genResult is the outcome of a successful provider generation walked across the
// resolved chain (Phase 7C-4): the provider result bytes/metadata, the route
// that actually produced them (for asset provenance + the success cost event),
// the provider_attempts row id to mark succeeded, and the measured latency.
type genResult struct {
	result    providers.ProviderGenerateResult
	route     resolvedRoute
	attemptID string
	latencyMs int64
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

	// Phase 7A: read the primary route the handler resolved at creation time and
	// persisted on the job. The worker never re-resolves.
	resolved, rerr := resolvedRouteFromPayload(job.InputPayload)
	if rerr != nil {
		w.log().Error("worker: invalid resolved route", "job_id", jobID, "error", rerr)
		return w.failTerminal(ctx, job, errorCodeInvalidResolvedRoute, rerr.Error())
	}
	// The primary adapter must be registered in this process. A missing primary
	// adapter is a terminal, non-retryable failure (Phase 7A) — an asynq retry
	// could never help. Phase 7C-4 fallbacks are walked inside
	// generateWithFallback (which re-looks up each route's adapter and skips a
	// missing one); the primary's presence is still a hard precondition here so a
	// misconfigured process fails fast and clearly rather than silently relying on
	// a fallback.
	if _, ok := w.Providers.Get(resolved.providerID); !ok {
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
		return w.processPreviewFirst(ctx, job, resolved, attemptNumber, finalAttempt)
	}

	if _, err := w.Jobs.MarkRunning(ctx, job.ID, job.TenantID); err != nil {
		w.log().Error("worker: mark running", "job_id", jobID, "error", err)
		return err
	}

	worldID := ""
	if job.WorldID != nil {
		worldID = *job.WorldID
	}
	description, _ := job.InputPayload["description"].(string)

	// Phase 7C-4: walk the resolved chain (primary first, then each persisted
	// same-price fallback) until one route succeeds. Each route records its own
	// provider attempt; per-route failures are recorded inside the walk. If every
	// route fails, do the terminal job-fail/release here on the final asynq
	// attempt, then return so asynq retries the whole chain.
	out, gerr := w.generateWithFallback(ctx, job, resolved, providers.ProviderGenerateRequest{
		JobID:     job.ID,
		Operation: providers.OperationTextToImage,
		Prompt:    description,
		Width:     deliveryRenderEdge,
		Height:    deliveryRenderEdge,
		Metadata: map[string]any{
			"world_id": worldID,
			"job_type": job.JobType,
		},
	}, attemptNumber)
	if gerr != nil {
		w.failJobOnFinalAttempt(ctx, job, gerr, finalAttempt)
		return gerr
	}
	result := out.result
	latency := out.latencyMs

	assetID := ids.NewVisualAssetID()
	urls, err := w.uploadImages(ctx, assetID, result.Images)
	if err != nil {
		w.recordFailure(ctx, job, out.attemptID, out.route.providerID, err, latency, finalAttempt)
		// Treat storage failures the same as provider failures for retry purposes.
		return err
	}

	// Phase 7C-4 provenance: stamp the WINNING route's provider/model/route (the
	// route that actually produced the bytes — may be a same-price fallback, not
	// the primary) so the stored asset records exactly which route produced it.
	// The shared builder also carries the request's render hash (prompt_hash),
	// quality tier, style provenance, and the provider hash in metadata.
	insertParams := w.buildArtifactInsertParams(job, out.route, assetID, urls, result, worldID)

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
		w.recordFailure(ctx, job, out.attemptID, out.route.providerID, fmt.Errorf("insert asset: %w", err), latency, finalAttempt)
		return err
	}
	if outcome == PersistSkippedCancelled {
		return w.finishCancelled(ctx, job, "final")
	}

	if err := w.Jobs.MarkProviderAttemptSucceeded(ctx, out.attemptID, int32(latency)); err != nil {
		w.log().Warn("worker: mark attempt succeeded", "attempt_id", out.attemptID, "error", err)
	}

	latencyInt := int32(latency)
	tokenID := job.RequestedByTokenID
	// Provenance reflects the WINNER (the route that produced the bytes), which may
	// be a same-price fallback rather than the primary (Phase 7C-4).
	providerID := out.route.providerID
	if err := w.Jobs.InsertCostEvent(ctx, CostEventInsertParams{
		ID:                ids.NewCostEventID(),
		TenantID:          job.TenantID,
		JobID:             &job.ID,
		AssetID:           &asset.ID,
		TokenID:           tokenID,
		ProviderID:        &providerID,
		ProviderAttemptID: &out.attemptID,
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

	// Phase 7C-4: the job is completed and cost committed — emit completed AFTER
	// the durable commit. Best-effort; never fails the job.
	w.emit(ctx, job.TenantID, webhooks.EventCompleted, job.ID, map[string]any{
		"final_asset_ids": []string{asset.ID},
	})

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
func (w *Worker) processPreviewFirst(ctx context.Context, job Job, resolved resolvedRoute, attemptNumber int32, finalAttempt bool) error {
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

		// Phase 7C-4: the preview phase walks the chain independently — its
		// provenance reflects whichever route produced the preview bytes.
		out, gerr := w.generateWithFallback(ctx, job, resolved, providers.ProviderGenerateRequest{
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
		}, attemptNumber)
		if gerr != nil {
			// Preview-phase chain exhausted: no preview asset is created. On the
			// terminal attempt fail the job + release the reservation. Per-route
			// failures were already recorded inside the walk.
			w.failJobOnFinalAttempt(ctx, job, gerr, finalAttempt)
			return gerr
		}
		result := out.result
		latency := out.latencyMs

		previewAssetID := ids.NewVisualAssetID()
		urls, err := w.uploadImages(ctx, previewAssetID, result.Images)
		if err != nil {
			w.recordFailure(ctx, job, out.attemptID, out.route.providerID, err, latency, finalAttempt)
			return err
		}

		previewParams := w.buildArtifactInsertParams(job, out.route, previewAssetID, urls, result, worldID)
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
			w.recordFailure(ctx, job, out.attemptID, out.route.providerID, fmt.Errorf("insert preview asset: %w", err), latency, finalAttempt)
			return err
		}
		if outcome == PersistSkippedCancelled {
			return w.finishCancelled(ctx, job, "preview")
		}
		if err := w.Jobs.MarkProviderAttemptSucceeded(ctx, out.attemptID, int32(latency)); err != nil {
			w.log().Warn("worker: mark attempt succeeded (preview)", "attempt_id", out.attemptID, "error", err)
		}

		// Phase 7C-4: the preview is durably committed (preview_ready) and not
		// cancelled — emit preview_ready AFTER the commit. Best-effort.
		w.emit(ctx, job.TenantID, webhooks.EventPreviewReady, job.ID, map[string]any{
			"preview_asset_ids": []string{previewAssetID},
		})
	}

	// --- Phase B: final -----------------------------------------------------
	// Phase 7C-4: the final phase walks the chain independently of the preview
	// phase — its winner (and thus the final asset's provenance + the success cost
	// event) may differ from the preview phase's winner.
	out, gerr := w.generateWithFallback(ctx, job, resolved, providers.ProviderGenerateRequest{
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
	}, attemptNumber)
	if gerr != nil {
		// Final-phase chain exhausted AFTER the preview was delivered: on the
		// terminal attempt the job is marked failed and the reservation released.
		// The preview asset stays preview_ready and final_asset_ids stays empty.
		w.failJobOnFinalAttempt(ctx, job, gerr, finalAttempt)
		return gerr
	}
	result := out.result
	latency := out.latencyMs

	finalAssetID := ids.NewVisualAssetID()
	urls, err := w.uploadImages(ctx, finalAssetID, result.Images)
	if err != nil {
		w.recordFailure(ctx, job, out.attemptID, out.route.providerID, err, latency, finalAttempt)
		return err
	}

	finalParams := w.buildArtifactInsertParams(job, out.route, finalAssetID, urls, result, worldID)
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
		w.recordFailure(ctx, job, out.attemptID, out.route.providerID, fmt.Errorf("insert asset: %w", err), latency, finalAttempt)
		return err
	}
	if outcome == PersistSkippedCancelled {
		return w.finishCancelled(ctx, job, "final")
	}
	if err := w.Jobs.MarkProviderAttemptSucceeded(ctx, out.attemptID, int32(latency)); err != nil {
		w.log().Warn("worker: mark attempt succeeded (final)", "attempt_id", out.attemptID, "error", err)
	}

	latencyInt := int32(latency)
	// Provenance reflects the final phase's WINNER (Phase 7C-4).
	providerID := out.route.providerID
	if err := w.Jobs.InsertCostEvent(ctx, CostEventInsertParams{
		ID:                ids.NewCostEventID(),
		TenantID:          job.TenantID,
		JobID:             &job.ID,
		AssetID:           &asset.ID,
		TokenID:           job.RequestedByTokenID,
		ProviderID:        &providerID,
		ProviderAttemptID: &out.attemptID,
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

	// Phase 7C-4: the two-phase job is completed and cost committed — emit
	// completed AFTER the durable commit. Best-effort.
	w.emit(ctx, job.TenantID, webhooks.EventCompleted, job.ID, map[string]any{
		"final_asset_ids": []string{asset.ID},
	})

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

// generateWithFallback attempts generation across the resolved provider chain
// (Phase 7C-4): the primary route first, then each persisted same-price fallback
// in order. Each route gets its own provider_attempts row; a route whose adapter
// is not registered in this process is skipped. The first success returns the
// winning route (for asset provenance + the success cost event) and its attempt.
// If every route fails, the LAST error is returned and per-route failures have
// already been recorded; the caller performs the terminal job-fail/release on the
// final asynq attempt. Because every fallback is same-price class, the single
// existing cost reservation stays valid regardless of which route wins.
//
// Provenance note: the cost reservation was priced on the PRIMARY model; a
// winning fallback is the same price class, so committing the unchanged
// reservation is correct, but the produced asset's provider/model/route
// provenance and the success cost event reflect the WINNER (an honest record of
// what actually produced the bytes). The job payload's persisted primary
// provider_id/model_id is unchanged.
func (w *Worker) generateWithFallback(ctx context.Context, job Job, primary resolvedRoute, genReq providers.ProviderGenerateRequest, attemptNumber int32) (genResult, error) {
	routes := append([]resolvedRoute{primary}, fallbackRoutesFromPayload(job.InputPayload)...)

	var lastErr error
	anyAdapter := false
	for _, route := range routes {
		adapter, ok := w.Providers.Get(route.providerID)
		if !ok {
			// A persisted fallback whose adapter is not registered in this process
			// is skipped (e.g. a provider configured only when a key is present).
			w.log().Warn("worker: fallback adapter missing; skipping route",
				"job_id", job.ID, "provider_id", route.providerID, "route_id", route.routeID)
			continue
		}
		anyAdapter = true

		attempt, err := w.Jobs.InsertProviderAttempt(ctx, ProviderAttemptInsertParams{
			ID:              ids.NewProviderAttemptID(),
			GenerationJobID: job.ID,
			ProviderID:      route.providerID,
			AttemptNumber:   attemptNumber,
		})
		if err != nil {
			w.log().Error("worker: insert attempt", "job_id", job.ID, "provider_id", route.providerID, "error", err)
			return genResult{}, err
		}

		start := time.Now()
		result, providerErr := adapter.Generate(ctx, genReq)
		latency := time.Since(start).Milliseconds()
		if providerErr != nil {
			// Record this route's failure (mark attempt failed + failed cost event).
			// Terminal job-fail/release is the caller's job once the whole chain is
			// exhausted on the final asynq attempt.
			w.recordAttemptFailure(ctx, job, attempt.ID, attempt.ProviderID, providerErr, latency)
			lastErr = providerErr
			continue
		}
		return genResult{result: result, route: route, attemptID: attempt.ID, latencyMs: latency}, nil
	}

	if !anyAdapter {
		return genResult{}, fmt.Errorf("no adapter registered for any route in the resolved chain (primary provider %q)", primary.providerID)
	}
	return genResult{}, lastErr
}

// recordAttemptFailure records a single provider attempt's failure: it marks the
// provider_attempts row failed (with the mapped error code + message + latency)
// and inserts a status=failed cost event for the attempt. It is the per-attempt
// half of the old recordFailure, shared by the fallback walk (one call per failed
// route) and the post-generate failure paths. It performs NO terminal job
// handling — that is failJobOnFinalAttempt's responsibility.
func (w *Worker) recordAttemptFailure(ctx context.Context, job Job, attemptID, providerID string, callErr error, latencyMs int64) {
	w.log().Error("worker: attempt failed",
		"job_id", job.ID,
		"attempt_id", attemptID,
		"provider_id", providerID,
		"error", callErr.Error(),
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
}

// failJobOnFinalAttempt performs the terminal job handling when an attempt
// exhausts its retries (Phase 7C-4 split out of recordFailure): on the final
// asynq attempt it marks the job failed (not retryable) and releases the cost
// reservation; on an earlier attempt it does nothing so the job stays for retry.
// callErr supplies the terminal error code + message. Per-attempt recording
// (mark attempt failed + failed cost event) is done separately by
// recordAttemptFailure / the fallback walk before this is called.
func (w *Worker) failJobOnFinalAttempt(ctx context.Context, job Job, callErr error, finalAttempt bool) {
	if !finalAttempt {
		return
	}
	errMsg := callErr.Error()
	markedFailed := true
	if _, err := w.Jobs.MarkFailed(ctx, job.ID, job.TenantID, errorCodeFor(callErr), errMsg, false); err != nil {
		w.log().Error("worker: mark job failed", "job_id", job.ID, "error", err)
		markedFailed = false
	}
	// Terminal failure: release the cost reservation (reserved → released,
	// return the held estimate to the budget; spent untouched). Idempotent.
	if w.Finalizer != nil {
		if err := w.Finalizer.Release(ctx, job.ID); err != nil {
			w.log().Error("worker: release cost reservation", "job_id", job.ID, "error", err)
		}
	}
	// Phase 7C-4: emit failed AFTER MarkFailed durably recorded the terminal
	// state (skipped when the mark itself failed). This centralizes ALL terminal
	// failures (chain exhaustion + post-generate failures) since every terminal
	// fail routes through here. Best-effort; never changes the control flow above.
	if markedFailed {
		w.emit(ctx, job.TenantID, webhooks.EventFailed, job.ID, map[string]any{
			"error_code": errorCodeFor(callErr),
		})
	}
}

// recordFailure records a post-generate failure keyed on a specific attempt id
// (the WINNER's attempt): the uploadImages / InsertFinalAsset / InsertPreviewAsset
// paths that fail AFTER a provider already succeeded. It marks that attempt
// failed + inserts a failed cost event (recordAttemptFailure) and, on the final
// asynq attempt, fails the job + releases the reservation
// (failJobOnFinalAttempt). Its behavior is unchanged from before the Phase 7C-4
// split; only the provider-call failure paths now go through generateWithFallback
// instead.
func (w *Worker) recordFailure(ctx context.Context, job Job, attemptID, providerID string, callErr error, latencyMs int64, finalAttempt bool) {
	w.recordAttemptFailure(ctx, job, attemptID, providerID, callErr, latencyMs)
	w.failJobOnFinalAttempt(ctx, job, callErr, finalAttempt)
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
