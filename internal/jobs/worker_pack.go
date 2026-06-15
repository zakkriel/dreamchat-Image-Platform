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

	// Phase 7A: select the provider adapter from the resolved route persisted on
	// the job. The worker never re-resolves; a missing route/adapter fails the
	// pack terminally (an asynq retry could not help).
	resolved, rerr := resolvedRouteFromPayload(job.InputPayload)
	if rerr != nil {
		w.log().Error("worker: invalid resolved route (pack)", "job_id", jobID, "error", rerr)
		return w.failPackTerminal(ctx, job, plan.packID, errorCodeInvalidResolvedRoute, rerr.Error())
	}
	provider, ok := w.Providers.Get(resolved.providerID)
	if !ok {
		msg := fmt.Sprintf("no adapter registered for resolved provider %q", resolved.providerID)
		w.log().Error("worker: provider adapter missing (pack)", "job_id", jobID, "provider_id", resolved.providerID)
		return w.failPackTerminal(ctx, job, plan.packID, errorCodeProviderUnavailable, msg)
	}

	// Reference-conditioned providers (e.g. fal FLUX.1 Kontext) cannot hold a
	// recurring character from the role prompt alone — they must be given the
	// identity's reference images. Gather them ONCE for the whole pack (every role
	// conditions on the same anchors) and fail the pack closed if the identity has
	// no anchor assets, rather than fanning out and generating a different
	// character per role (PRD 03 §8). Prompt-only providers (mock, BFL scene) leave
	// RequiresReferenceImage false, so this is a no-op and the pack runs unchanged.
	var referenceURLs []string
	if provider.Capabilities().RequiresReferenceImage {
		refs, refErr := w.referenceURLsForIdentity(ctx, plan.visualIdentityID, job.TenantID)
		if refErr != nil {
			w.log().Error("worker: gather reference assets (pack)", "job_id", jobID, "identity_id", plan.visualIdentityID, "error", refErr)
			return w.failPackTerminal(ctx, job, plan.packID, errorCodeMissingReference, refErr.Error())
		}
		if len(refs) == 0 {
			msg := fmt.Sprintf("visual identity %q has no reference assets for reference-conditioned provider %q", plan.visualIdentityID, resolved.providerID)
			w.log().Error("worker: no reference assets (pack)", "job_id", jobID, "identity_id", plan.visualIdentityID, "provider_id", resolved.providerID)
			return w.failPackTerminal(ctx, job, plan.packID, errorCodeMissingReference, msg)
		}
		referenceURLs = refs
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
	providerID := resolved.providerID
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
		assetID, itemErr := w.generatePackItem(ctx, job, plan, provider, resolved, variantKey, i, referenceURLs)
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
		deliveredKeys = append(deliveredKeys, variantKey)
	}

	// Pack completeness (Phase 6A3): required = every template role
	// (plan.variantKeys), delivered = the roles backed by a ready item, missing =
	// the rest. Written once here so it is correct in every terminal branch
	// below (a partial run leaves the failed roles in missing; a total failure
	// leaves all roles missing). Recomputed identically on an asynq retry.
	missingKeys := missingRoles(plan.variantKeys, deliveredKeys)
	if err := w.Jobs.UpdateAssetPackCompleteness(ctx, plan.packID, deliveredKeys, missingKeys); err != nil {
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
func (w *Worker) generatePackItem(ctx context.Context, job Job, plan packPlan, provider providers.ImageProvider, resolved resolvedRoute, variantKey string, index int, referenceURLs []string) (string, error) {
	attempt, err := w.Jobs.InsertProviderAttempt(ctx, ProviderAttemptInsertParams{
		ID:              ids.NewProviderAttemptID(),
		GenerationJobID: job.ID,
		ProviderID:      resolved.providerID,
		AttemptNumber:   int32(index + 1),
	})
	if err != nil {
		return "", fmt.Errorf("insert attempt: %w", err)
	}

	// 5A keeps the prompt trivial: the variant_key is an opaque role string
	// appended to the identity's name. Variant semantics are 5B.
	prompt := plan.displayName + " — " + variantKey

	start := time.Now()
	result, providerErr := provider.Generate(ctx, providers.ProviderGenerateRequest{
		JobID:         job.ID,
		Operation:     providers.OperationTextToImage,
		Prompt:        prompt,
		Width:         deliveryRenderEdge,
		Height:        deliveryRenderEdge,
		ReferenceURLs: referenceURLs,
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
	var insertErr error
	if plan.forceRegenerate {
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
		w.markPackAttemptFailed(ctx, attempt.ID, fmt.Errorf("%w: insert asset + pack item: %v", errPersistence, insertErr), latency)
		return "", insertErr
	}

	if err := w.Jobs.MarkProviderAttemptSucceeded(ctx, attempt.ID, latency); err != nil {
		w.log().Warn("worker: mark pack attempt succeeded", "attempt_id", attempt.ID, "error", err)
	}
	return assetID, nil
}

// failPackTerminal marks a pack job and its pack row permanently failed (not
// retryable) and releases the cost reservation. Used for unrunnable pack jobs —
// a missing provider adapter or a payload missing its resolved route.
func (w *Worker) failPackTerminal(ctx context.Context, job Job, packID, code, msg string) error {
	if err := w.Jobs.UpdateAssetPackStatus(ctx, packID, packStatusFailed); err != nil {
		return err
	}
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

func (w *Worker) markPackAttemptFailed(ctx context.Context, attemptID string, callErr error, latencyMs int32) {
	if err := w.Jobs.MarkProviderAttemptFailed(ctx, attemptID, errorCodeFor(callErr), callErr.Error(), latencyMs); err != nil {
		w.log().Warn("worker: mark pack attempt failed", "attempt_id", attemptID, "error", err)
	}
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
