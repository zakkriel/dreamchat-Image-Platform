package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hibiken/asynq"

	"github.com/zakkriel/drchat-image-platform/internal/assets"
	"github.com/zakkriel/drchat-image-platform/internal/ids"
	"github.com/zakkriel/drchat-image-platform/internal/providers"
	"github.com/zakkriel/drchat-image-platform/internal/telemetry"
	"github.com/zakkriel/drchat-image-platform/internal/webhooks"
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
		retryCount, _ := asynq.GetRetryCount(ctx)
		return w.ProcessPackForReservation(ctx, payload.JobID, payload.CostReservationID, int32(retryCount))
	}
}

// ProcessPack is the pack fan-out body. Unlike the single-artifact Process,
// a pack run always reaches a terminal state in one pass: per-item failures
// are recorded and the batch continues, so there is no per-attempt retry
// loop — only infra errors (job lookup, terminal bookkeeping) return an
// error for asynq to retry, and the terminal short-circuit plus the
// existing-items skip make that retry safe.
func (w *Worker) ProcessPack(ctx context.Context, jobID string, retryCounts ...int32) error {
	return w.processPack(ctx, jobID, "", retryCounts...)
}

// ProcessPackForReservation rejects a delayed pack task whose reservation no
// longer owns the reusable generation job after an admin retry.
func (w *Worker) ProcessPackForReservation(ctx context.Context, jobID, reservationID string, retryCounts ...int32) error {
	return w.processPack(ctx, jobID, reservationID, retryCounts...)
}

func (w *Worker) processPack(ctx context.Context, jobID, expectedReservationID string, retryCounts ...int32) error {
	// A direct call from a unit/integration test is a deliberate invocation and
	// may represent a retry without an asynq retry count. Queue deliveries pass
	// the real count through the handler; -1 preserves the direct-call contract.
	retryCount := int32(-1)
	if len(retryCounts) > 0 {
		retryCount = retryCounts[0]
	}
	job, err := w.Jobs.GetByID(ctx, jobID)
	if err != nil {
		w.log().Error("worker: lookup pack job", "job_id", jobID, "error", err)
		return err
	}
	if expectedReservationID != "" {
		if job.CostReservationID == nil || *job.CostReservationID != expectedReservationID {
			w.log().Info("worker: stale pack task ignored after reservation changed", "job_id", jobID, "expected_reservation_id", expectedReservationID, "actual_reservation_id", costReservationID(job))
			return nil
		}
	}

	// Retry-safety short-circuit (same rule as 4B): a terminal job never
	// re-fans-out; only the idempotent cost finalization may be outstanding.
	switch job.Status {
	case "completed":
		if err := w.commitReservation(ctx, job); err != nil {
			w.log().Error("worker: commit cost reservation (terminal pack job)", "job_id", jobID, "error", err)
			return err
		}
		return nil
	case "failed":
		// A terminal pack-status write can fail after the job CAS succeeds.
		// Retry the ownership-guarded correction before finalization so a pack
		// cannot remain in_progress while its owning job is failed.
		if job.AssetPackID != nil && *job.AssetPackID != "" {
			if err := w.updatePackStatus(ctx, *job.AssetPackID, job.ID, packStatusFailed); err != nil {
				w.log().Error("worker: correct terminal pack status", "job_id", jobID, "error", err)
				return err
			}
		}
		if err := w.releaseReservation(ctx, job); err != nil {
			w.log().Error("worker: release cost reservation (terminal pack job)", "job_id", jobID, "error", err)
			return err
		}
		return nil
	case statusCancelled:
		// Cancellation is terminal. A duplicate queued task may have read the
		// row before the admin cancel committed; never fan out provider work and
		// keep the release idempotent.
		if err := w.releaseReservation(ctx, job); err != nil {
			w.log().Error("worker: release cost reservation (cancelled pack job)", "job_id", jobID, "error", err)
			return err
		}
		return nil
	}

	claimed, err := w.claimQueuedJob(ctx, job, "pack", retryCount, expectedReservationID)
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}

	plan, planErr := packPlanFromJob(job)
	if planErr != nil {
		// A pack job without a pack link / variants is unrunnable; fail it
		// terminally rather than retrying a payload that can't change.
		w.log().Error("worker: invalid pack job", "job_id", jobID, "error", planErr)
		if _, err := w.markJobFailed(ctx, job, errorCodePackInvalidJob, planErr.Error(), false, expectedReservationID); err != nil {
			return err
		}
		w.emit(ctx, job.TenantID, webhooks.EventFailed, job.ID, map[string]any{
			"error_code": errorCodePackInvalidJob,
		})
		if err := w.releaseReservation(ctx, job); err != nil {
			return err
		}
		return nil
	}
	if _, mpErr := maxMegapixelsForWorker(job); mpErr != nil {
		w.log().Error("worker: invalid max_megapixels (pack)", "job_id", jobID, "error", mpErr)
		return w.failPackTerminal(ctx, job, plan.packID, errorCodeMaxMegapixelsExceeded, mpErr.Error(), expectedReservationID)
	}

	// Phase 7A: read the primary route persisted on the job. The worker never
	// re-resolves; generateWithFallback skips an unavailable primary and walks
	// the persisted same-price alternates per cell.
	resolved, rerr := resolvedRouteFromPayload(job.InputPayload)
	if rerr != nil {
		w.log().Error("worker: invalid resolved route (pack)", "job_id", jobID, "error", rerr)
		return w.failPackTerminal(ctx, job, plan.packID, errorCodeInvalidResolvedRoute, rerr.Error(), expectedReservationID)
	}
	// Reference-conditioned providers require the identity's anchors even when
	// they are only a persisted fallback. Gather once for the whole pack when
	// any configured route requires references, and thread the same references
	// through every route in every cell. This mirrors singleImageReferences and
	// keeps a fallback from silently changing the recurring subject.
	routes := append([]resolvedRoute{resolved}, fallbackRoutesFromPayload(job.InputPayload)...)
	needsReferences := false
	for _, route := range routes {
		adapter, routeOK := w.Providers.Get(route.providerID)
		if routeOK && adapter.Capabilities().RequiresReferenceImage {
			needsReferences = true
			break
		}
	}
	var referenceURLs []string
	if needsReferences {
		refs, refErr := w.referenceURLsForIdentity(ctx, plan.visualIdentityID, job.TenantID)
		if refErr != nil {
			// An attached anchor is unusable (wrong tenant / missing / not ready /
			// bad object) → invalid_reference_asset; any other gather failure is also
			// surfaced clearly. Either way the pack fails closed.
			code := errorCodeInvalidReference
			if !errors.Is(refErr, errInvalidReference) {
				code = errorCodeMissingReference
			}
			w.log().Error("worker: gather reference assets (pack)", "job_id", jobID, "identity_id", plan.visualIdentityID, "error", refErr)
			return w.failPackTerminal(ctx, job, plan.packID, code, refErr.Error(), expectedReservationID)
		}
		if len(refs) == 0 {
			msg := fmt.Sprintf("visual identity %q has no reference assets for reference-conditioned provider", plan.visualIdentityID)
			w.log().Error("worker: no reference assets (pack)", "job_id", jobID, "identity_id", plan.visualIdentityID)
			return w.failPackTerminal(ctx, job, plan.packID, errorCodeMissingReference, msg, expectedReservationID)
		}
		referenceURLs = refs
	}

	if err := w.updatePackStatus(ctx, plan.packID, job.ID, packStatusInProgress); err != nil {
		w.log().Error("worker: mark pack in_progress", "job_id", jobID, "pack_id", plan.packID, "error", err)
		return err
	}

	// Existing items short-circuit: if a previous attempt already delivered
	// some variants — OR a Phase 6A3 reused role was persisted at creation time
	// (an existing ready asset already satisfies it) — count them as delivered
	// instead of re-generating. The UNIQUE (asset_pack_id, variant_key)
	// constraint would reject a re-insert, and a reused role must never trigger a
	// provider call. This is what makes pack generation generate only the missing
	// roles: the reused roles are already present as asset_pack_items.
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
	var succeeded []string
	// deliveredKeys is the ordered set of required roles backed by a ready item
	// at the end of this run (reused + retry-skipped + freshly generated). It
	// drives the stored pack completeness (delivered vs missing).
	var deliveredKeys []string
	failedItems := 0

	for i, variantKey := range plan.variantKeys {
		if assetID, ok := delivered[variantKey]; ok {
			succeeded = append(succeeded, assetID)
			deliveredKeys = append(deliveredKeys, variantKey)
			continue
		}
		assetID, itemErr := w.generatePackItem(ctx, job, plan, resolved, variantKey, i, referenceURLs)
		if itemErr != nil {
			if errors.Is(itemErr, errPackJobNotActive) {
				// Cancellation/terminal completion won the guarded item
				// transaction. Stop fan-out immediately; do not recompute or
				// overwrite pack terminal state from this stale worker.
				return w.finishCancelled(ctx, job, "pack")
			}
			if errors.Is(itemErr, providers.ErrContentPolicyRejected) {
				// A policy rejection is terminal for this request. Never try the
				// next cell or another route after the provider made that decision.
				if err := w.updatePackCompleteness(ctx, plan.packID, job.ID, deliveredKeys, missingRoles(plan.variantKeys, deliveredKeys)); err != nil {
					return err
				}
				return w.failPackTerminal(ctx, job, plan.packID, errorCodeContentRejected, itemErr.Error(), expectedReservationID)
			}
			if errors.Is(itemErr, errBillableCapReached) {
				// The pack has billed every provider call its reservation priced
				// (MaxBillableCallsPerUnit per missing role). No later cell can
				// generate anything, so stop the fan-out here instead of walking
				// the remaining roles to refuse each one: record what was
				// delivered and fail the pack terminally.
				if err := w.updatePackCompleteness(ctx, plan.packID, job.ID, deliveredKeys, missingRoles(plan.variantKeys, deliveredKeys)); err != nil {
					return err
				}
				return w.failPackTerminal(ctx, job, plan.packID, errorCodeFor(itemErr), itemErr.Error(), expectedReservationID)
			}
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
		deliveredKeys = append(deliveredKeys, variantKey)
	}

	// Pack completeness (Phase 6A3): required = every template role
	// (plan.variantKeys), delivered = the roles backed by a ready item, missing =
	// the rest. Written once here so it is correct in every terminal branch
	// below (a partial run leaves the failed roles in missing; a total failure
	// leaves all roles missing). Recomputed identically on an asynq retry.
	missingKeys := missingRoles(plan.variantKeys, deliveredKeys)
	if err := w.updatePackCompleteness(ctx, plan.packID, job.ID, deliveredKeys, missingKeys); err != nil {
		w.log().Error("worker: update pack completeness", "job_id", job.ID, "pack_id", plan.packID, "error", err)
		return err
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
		// The pack aggregate is one logical event per job reservation. A
		// terminal bookkeeping retry must not append another aggregate row.
		ID:                packAggregateCostEventID(job),
		TenantID:          job.TenantID,
		JobID:             &job.ID,
		CostReservationID: job.CostReservationID,
		TokenID:           job.RequestedByTokenID,
		ProviderID:        &resolved.providerID,
		ModelID:           strPtr(resolved.modelID),
		Operation:         string(providers.OperationTextToImage),
		DurationMs:        &latencyInt,
		Status:            eventStatus,
		Metadata:          billableMetadata("pack", nil),
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
		if err := w.updatePackStatus(ctx, plan.packID, job.ID, packStatusFailed); err != nil {
			w.log().Error("worker: mark pack failed", "job_id", job.ID, "pack_id", plan.packID, "error", err)
			return err
		}
		if _, err := w.markJobFailed(ctx, job, errorCodePackAllFailed, errorMessagePackFailed, false, expectedReservationID); err != nil {
			w.log().Error("worker: mark pack job failed", "job_id", job.ID, "error", err)
			return err
		}
		w.emit(ctx, job.TenantID, webhooks.EventFailed, job.ID, map[string]any{
			"error_code": errorCodePackAllFailed,
		})
		if err := w.releaseReservation(ctx, job); err != nil {
			w.log().Error("worker: release pack cost reservation", "job_id", job.ID, "error", err)
			return err
		}
		return nil
	}

	packStatus := packStatusCompleted
	if failedItems > 0 {
		packStatus = packStatusWithWarnings
	}
	if err := w.updatePackStatus(ctx, plan.packID, job.ID, packStatus); err != nil {
		w.log().Error("worker: mark pack completed", "job_id", job.ID, "pack_id", plan.packID, "error", err)
		return err
	}
	if _, err := w.markJobCompleted(ctx, job, succeeded, expectedReservationID); err != nil {
		w.log().Error("worker: mark pack job completed", "job_id", job.ID, "error", err)
		return err
	}
	// A pack job is a first-class generation job: it reaches `completed` with
	// its delivered assets in final_asset_ids exactly like a single image, so it
	// emits the same event. `succeeded` is the ordered set of delivered assets
	// (reused + retry-skipped + freshly generated); a partial pack still
	// completes, and the receiver reads pack completeness back from the job.
	// Emitted before commitReservation for the same reason as the single-image
	// path: a commit error returns and the retry short-circuits on `completed`.
	w.emit(ctx, job.TenantID, webhooks.EventCompleted, job.ID, map[string]any{
		"final_asset_ids": succeeded,
	})
	if err := w.commitReservation(ctx, job); err != nil {
		w.log().Error("worker: commit pack cost reservation", "job_id", job.ID, "error", err)
		return err
	}
	return nil
}

func packAggregateCostEventID(job Job) string {
	reservationID := "none"
	if job.CostReservationID != nil && *job.CostReservationID != "" {
		reservationID = *job.CostReservationID
	}
	return fmt.Sprintf("%s_pack_%s_%s", ids.PrefixCostEvent, job.ID, reservationID)
}

// generatePackItem runs one variant end to end: provider attempt row,
// provider call, image upload, visual_assets insert, asset_pack_items
// insert. Returns the new asset id, or the per-item error.
func (w *Worker) generatePackItem(ctx context.Context, job Job, plan packPlan, resolved resolvedRoute, variantKey string, index int, referenceURLs []string) (string, error) {
	// A caller-authored cell renders exactly the subject prose the caller sent — the platform does
	// not know what a variant key MEANS, only how to render, condition, store, and reuse it. A cell
	// with no caller prompt keeps the 5A shape (name — role) unchanged.
	subject := plan.displayName + " — " + variantKey
	if p, ok := plan.variantPrompts[variantKey]; ok && strings.TrimSpace(p) != "" {
		subject = p
	}
	aspectRatio := plan.aspectRatio
	prompt := composePromptWithStyle(subject, payloadString(job.InputPayload, "style_positive_prompt"))
	// A transparent cell keyed locally needs the model to paint the backdrop the
	// keyer looks for. When the remover is the hosted matting model this must
	// NOT be added: it would put a magenta backdrop into an image that is about
	// to be matted, making the matting job harder for no reason.
	if payloadString(job.InputPayload, "output_background") == "transparent" && w.chromaKeyEnabled() {
		prompt = composeChromaBackdrop(prompt)
	}
	negativePrompt := payloadString(job.InputPayload, "style_negative_prompt")

	// Pack cells use the same persisted primary + same-price fallback chain as
	// single-image generation. Failed routes are recorded as provider attempts,
	// and content-policy rejection stops the walk in generateWithFallback.
	out, providerErr := w.generateWithFallback(ctx, job, resolved, providers.ProviderGenerateRequest{
		JobID:          job.ID,
		Operation:      providers.OperationTextToImage,
		Prompt:         prompt,
		NegativePrompt: negativePrompt,
		Width:          renderEdgeForMax(job, deliveryRenderEdge),
		Height:         renderEdgeForMax(job, deliveryRenderEdge),
		AspectRatio:    aspectRatio,
		ReferenceURLs:  referenceURLs,
		Metadata: map[string]any{
			"world_id":    plan.worldID,
			"job_type":    job.JobType,
			"variant_key": variantKey,
		},
	}, int32(index+1))
	if providerErr != nil {
		return "", providerErr
	}
	result := out.result
	resolved = out.route
	latency := int32(out.latencyMs)
	if sizeErr := validateProviderImages(job, result.Images); sizeErr != nil {
		w.recordAttemptFailureWithCost(ctx, job, out.attemptID, resolved.providerID, resolved.modelID, sizeErr, out.latencyMs, reportedProviderCostPtr(result))
		return "", sizeErr
	}

	// The sprite contract: a transparent pack background-removes every provider image BEFORE tier
	// encoding, so all three stored tiers carry real alpha. Failure is a per-variant failure with
	// its own code — an opaque image must never ship under a transparent promise.
	if payloadString(job.InputPayload, "output_background") == "transparent" {
		cleaned, remErr := w.removeBackgrounds(ctx, result.Images)
		if remErr != nil {
			w.recordAttemptFailureWithCost(ctx, job, out.attemptID, resolved.providerID, resolved.modelID, remErr, out.latencyMs, reportedProviderCostPtr(result))
			return "", remErr
		}
		result.Images = cleaned
	}

	assetID := ids.NewVisualAssetID()
	urls, err := w.uploadImages(ctx, assetID, result.Images)
	if err != nil {
		w.recordAttemptFailureWithCost(ctx, job, out.attemptID, resolved.providerID, resolved.modelID, fmt.Errorf("%w: %v", errStorageFailure, err), out.latencyMs, reportedProviderCostPtr(result))
		return "", err
	}

	// The visual_assets row and its asset_pack_items row commit in one
	// transaction: a delivered variant is observable atomically, so a failed
	// item insert can't strand an orphan asset the retry path (which detects
	// delivery via asset_pack_items) would never see — and therefore can't
	// produce duplicate assets for the same pack variant.
	providerID := resolved.providerID
	modelID := resolved.modelID
	routeID := resolved.routeID
	jobIDRef := job.ID
	identityID := plan.visualIdentityID
	// Phase 5B: classify the variant_key deterministically and stamp the
	// compatibility/provenance fields (variant_family, compatibility_tags,
	// fallback_allowed, fallback_rank) plus structured metadata onto the asset.
	// An unrecognized key classifies as family "unknown" with no fallback
	// eligibility — never silently generic-safe.
	assetParams := assets.InsertParams{
		ID:                  assetID,
		TenantID:            job.TenantID,
		WorldID:             plan.worldID,
		VisualIdentityID:    &identityID,
		AssetType:           plan.assetType,
		VariantKey:          variantKey,
		StyleProfileID:      plan.styleProfileID,
		StyleProfileVersion: plan.styleProfileVersion,
		QualityTier:         plan.qualityTier,
		LowResUrl:           strPtr(urls.low),
		HighResUrl:          strPtr(urls.high),
		ThumbnailUrl:        strPtr(urls.thumb),
		ProviderID:          &providerID,
		ModelID:             &modelID,
		ProviderRouteID:     strPtr(routeID),
		PromptHash:          strPtr(result.PromptHash),
		Seed:                strPtr(result.Seed),
		GenerationJobID:     &jobIDRef,
	}
	assets.ClassifyVariant(plan.entityType, variantKey).ApplyTo(&assetParams)
	item := AssetPackItemInsertParams{
		ID:            ids.NewAssetPackItemID(),
		AssetPackID:   plan.packID,
		VisualAssetID: assetID,
		VariantKey:    variantKey,
		SortOrder:     int32(index),
	}
	// Phase 6A4 forced regeneration: a forced pack supersedes each role's slot.
	// The atomic asset + pack-item write archives the prior ready asset of the
	// EXACT pack-role slot (FindExactVisualAsset predicate) and versions the new
	// one (prior_max + 1), in the same transaction and under a slot lock. A
	// forced pack has no reused items, so every role takes this path; a non-forced
	// pack uses the byte-for-byte unchanged InsertPackItemWithAsset.
	packCostEvent := CostEventInsertParams{
		ID:                ids.NewCostEventID(),
		TenantID:          job.TenantID,
		JobID:             &job.ID,
		CostReservationID: job.CostReservationID,
		AssetID:           &assetID,
		TokenID:           job.RequestedByTokenID,
		ProviderID:        &providerID,
		ModelID:           strPtr(resolved.modelID),
		ProviderAttemptID: &out.attemptID,
		Operation:         string(providers.OperationTextToImage),
		ActualCostUSD:     reportedProviderCostPtr(result),
		DurationMs:        &latency,
		Status:            "completed",
		Metadata:          billableMetadata("pack_cell", map[string]any{"variant_key": variantKey}),
	}
	success := PersistSuccessParams{AttemptID: out.attemptID, LatencyMs: latency, CostEvent: packCostEvent}
	var insertErr error
	successAtomic := false
	if packPersister, ok := w.Jobs.(PackSuccessPersister); ok {
		if plan.forceRegenerate {
			insertErr = packPersister.InsertPackItemWithAssetSupersedingAndSuccess(ctx, assetParams, item, assets.VariantSlot{
				TenantID:         job.TenantID,
				WorldID:          plan.worldID,
				VisualIdentityID: plan.visualIdentityID,
				VariantKey:       variantKey,
				StateVersion:     packSupersedeStateVersion,
				StyleProfileID:   derefStr(plan.styleProfileID),
				QualityTier:      plan.qualityTier,
			}, success)
		} else {
			insertErr = packPersister.InsertPackItemWithAssetAndSuccess(ctx, assetParams, item, success)
		}
		successAtomic = true
	} else if plan.forceRegenerate {
		insertErr = w.Jobs.InsertPackItemWithAssetSuperseding(ctx, assetParams, item, assets.VariantSlot{
			TenantID:         job.TenantID,
			WorldID:          plan.worldID,
			VisualIdentityID: plan.visualIdentityID,
			VariantKey:       variantKey,
			StateVersion:     packSupersedeStateVersion,
			StyleProfileID:   derefStr(plan.styleProfileID),
			QualityTier:      plan.qualityTier,
		})
	} else {
		insertErr = w.Jobs.InsertPackItemWithAsset(ctx, assetParams, item)
	}
	if insertErr != nil {
		if errors.Is(insertErr, errPackJobNotActive) {
			// The provider call succeeded but cancellation/terminal completion
			// won the guarded persistence lock. It remains billable even though
			// no pack item can be published.
			w.recordDiscardedProviderSuccess(ctx, job, discardedProviderAttempt{
				AttemptID: out.attemptID, ProviderID: resolved.providerID,
				ModelID: resolved.modelID, LatencyMs: int64(latency), Result: result,
			}, "pack_cell")
			return "", errPackJobNotActive
		}
		w.recordAttemptFailureWithCost(ctx, job, out.attemptID, resolved.providerID, resolved.modelID, fmt.Errorf("%w: insert asset + pack item: %v", errPersistence, insertErr), int64(latency), reportedProviderCostPtr(result))
		return "", insertErr
	}

	if !successAtomic {
		if err := w.Jobs.MarkProviderAttemptSucceeded(ctx, out.attemptID, latency); err != nil {
			w.log().Warn("worker: mark pack attempt succeeded", "attempt_id", out.attemptID, "error", err)
		}
		if err := w.Jobs.InsertCostEvent(ctx, packCostEvent); err != nil {
			w.log().Warn("worker: insert pack cell cost event", "job_id", job.ID, "variant_key", variantKey, "error", err)
		}
	}

	telemetry.DefaultMetrics().RecordUsableImage()
	return assetID, nil
}

// PackStatusUpdater and PackCompletenessUpdater are optional production
// extensions that keep a stale worker from changing a terminal pack belonging
// to a different execution. The legacy methods remain the fallback for small
// in-memory repositories and older integrations.
type PackStatusUpdater interface {
	UpdateAssetPackStatusForJob(ctx context.Context, packID, jobID, status string) error
}

type PackCompletenessUpdater interface {
	UpdateAssetPackCompletenessForJob(ctx context.Context, packID, jobID string, delivered, missing []string) error
}

func (w *Worker) updatePackStatus(ctx context.Context, packID, jobID, status string) error {
	if updater, ok := w.Jobs.(PackStatusUpdater); ok {
		return updater.UpdateAssetPackStatusForJob(ctx, packID, jobID, status)
	}
	return w.Jobs.UpdateAssetPackStatus(ctx, packID, status)
}

func (w *Worker) updatePackCompleteness(ctx context.Context, packID, jobID string, delivered, missing []string) error {
	if updater, ok := w.Jobs.(PackCompletenessUpdater); ok {
		return updater.UpdateAssetPackCompletenessForJob(ctx, packID, jobID, delivered, missing)
	}
	return w.Jobs.UpdateAssetPackCompleteness(ctx, packID, delivered, missing)
}

// failPackTerminal marks a pack job and its pack row permanently failed (not
// retryable) and releases the cost reservation. Used for unrunnable pack jobs —
// a missing provider adapter or a payload missing its resolved route.
func (w *Worker) failPackTerminal(ctx context.Context, job Job, packID, code, msg string, expectedReservationIDs ...string) error {
	// Claim the job's terminal state before writing the pack status. If a
	// concurrent winner already completed or replaced this reservation, the
	// reservation-bound CAS fails and this stale worker must not overwrite the
	// winner's terminal pack status.
	if _, err := w.markJobFailed(ctx, job, code, msg, false, expectedReservationIDs...); err != nil {
		return err
	}
	w.emit(ctx, job.TenantID, webhooks.EventFailed, job.ID, map[string]any{
		"error_code": code,
	})
	if err := w.updatePackStatus(ctx, packID, job.ID, packStatusFailed); err != nil {
		return err
	}
	if err := w.releaseReservation(ctx, job); err != nil {
		return err
	}
	return nil
}

// packPlan is the per-run view of the pack job's input payload, written by
// the generate-pack handlers at request time so the worker needs only job_id.
type packPlan struct {
	packID              string
	variantKeys         []string
	worldID             string
	visualIdentityID    string
	displayName         string
	assetType           string
	entityType          string // assets.EntityCharacter | assets.EntityPlace
	qualityTier         string
	styleProfileID      *string
	styleProfileVersion *int32
	// forceRegenerate (Phase 6A4) makes every role supersede its slot instead of
	// a plain insert. Carried on the job input_payload by the pack handler.
	forceRegenerate bool
	// variantPrompts is the caller-authored subject prose per variant key (the `variants` request
	// field). The keys are opaque vocabulary owned by the caller; a key with no entry falls back to
	// the 5A prompt shape.
	variantPrompts map[string]string
	// aspectRatio, when set, is forwarded to the provider for every cell.
	aspectRatio string
}

// packSupersedeStateVersion is the state version a forced pack regeneration
// supersedes on. It mirrors the handler's packReuseStateVersion: pack assets are
// generated at the entity's default state (state_version = 1, the visual_assets
// default), so the supersede slot must target that same state — otherwise the
// archive predicate would miss the prior ready row and leave two ready rows.
const packSupersedeStateVersion = 1

// derefStr returns the pointed-to string, or "" for a nil pointer.
func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// missingRoles returns the required roles not present in delivered, preserving
// the required order. Used to record final pack completeness.
func missingRoles(required, delivered []string) []string {
	have := make(map[string]struct{}, len(delivered))
	for _, k := range delivered {
		have[k] = struct{}{}
	}
	missing := make([]string, 0)
	for _, role := range required {
		if _, ok := have[role]; !ok {
			missing = append(missing, role)
		}
	}
	return missing
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
	if rawPrompts, ok := job.InputPayload["variant_prompts"].(map[string]any); ok {
		plan.variantPrompts = make(map[string]string, len(rawPrompts))
		for key, v := range rawPrompts {
			if s, ok := v.(string); ok && s != "" {
				plan.variantPrompts[key] = s
			}
		}
	}
	plan.aspectRatio = payloadString(job.InputPayload, "aspect_ratio")

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
	// Style profile provenance from the pack request (carried in
	// input_payload) so retrieval can later find generated pack assets by
	// style. style_profile_version is optional (no request carries one yet).
	plan.styleProfileID = payloadStrPtr(job.InputPayload, "style_profile_id")
	plan.styleProfileVersion = payloadInt32Ptr(job.InputPayload, "style_profile_version")
	plan.forceRegenerate = payloadBool(job.InputPayload, "force_regenerate")
	return plan, nil
}
