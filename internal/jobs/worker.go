package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/hibiken/asynq"

	"github.com/zakkriel/drchat-image-platform/internal/assets"
	"github.com/zakkriel/drchat-image-platform/internal/cost"
	"github.com/zakkriel/drchat-image-platform/internal/identities"
	"github.com/zakkriel/drchat-image-platform/internal/ids"
	"github.com/zakkriel/drchat-image-platform/internal/imaging"
	"github.com/zakkriel/drchat-image-platform/internal/providers"
	"github.com/zakkriel/drchat-image-platform/internal/storage"
	"github.com/zakkriel/drchat-image-platform/internal/telemetry"
	"github.com/zakkriel/drchat-image-platform/internal/webhooks"
)

const (
	errorCodeProviderFailure      = "provider_failure"
	errorCodePersistenceError     = "persistence_error"
	errorCodeStorageFailure       = "storage_failure"
	errorCodeProviderUnavailable  = "provider_unavailable"
	errorCodeInvalidResolvedRoute = "invalid_resolved_route"
	// errorCodeMissingReference is the terminal failure code when a
	// reference-conditioned provider is selected for identity/pack generation but
	// the visual identity has NO anchor/reference assets to condition on. The
	// worker fails the job closed instead of generating a different character
	// (PRD 03 §8 / recurring-character consistency).
	errorCodeMissingReference = "missing_reference_assets"
	// errorCodeInvalidReference is the terminal failure code when the identity DOES
	// reference anchors but one cannot be used (wrong tenant, missing, not ready, or
	// no resolvable high-res object). Distinct from missing_reference_assets so an
	// operator can tell "no anchors attached" from "an attached anchor is bad".
	errorCodeInvalidReference = "invalid_reference_asset"
	// errorCodeMaxMegapixelsExceeded is terminal: the provider returned pixels
	// outside the caller's explicit render budget, so retrying the same request
	// cannot make the already-produced output compliant.
	errorCodeMaxMegapixelsExceeded = "max_megapixels_exceeded"

	// errorCodeContentRejected is the terminal failure code when a provider
	// rejected the request on CONTENT-POLICY grounds (providers.
	// ErrContentPolicyRejected, e.g. BFL "Request Moderated"). It is distinct
	// from provider_failure so callers see the provider's policy decision
	// verbatim (docs/api/errors.md vocabulary). The platform itself takes no
	// content stance: the rejection is surfaced, never sanitized, and never
	// walked around via fallback routes or asynq retries.
	errorCodeContentRejected = "provider_content_rejected"

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

	// workerMaxMegapixels is the fail-closed default for older jobs that were
	// created before max_megapixels was persisted. New requests are validated
	// against the same platform ceiling at the HTTP boundary.
	workerMaxMegapixels = 4.0
)

// ProviderRegistry resolves a provider_id to its adapter (Phase 7A). The worker
// selects the adapter from the resolved provider_id persisted on the job — it
// never re-resolves a route and never falls back to a different provider.
// *providers.Registry satisfies this.
type ProviderRegistry interface {
	Get(providerID string) (providers.ImageProvider, bool)
}

// IdentityReader is the narrow read view the worker needs to gather a recurring
// character's reference assets for a reference-conditioned provider. It is
// satisfied by *identities.Repository; tests supply a fake. Optional/nil-safe:
// when unset (or the provider does not require references) the worker never
// gathers references and the existing prompt-only paths are unchanged.
type IdentityReader interface {
	GetByIDForTenant(ctx context.Context, id, tenantID string) (identities.VisualIdentity, error)
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

	// Identities resolves a visual identity's anchor/reference assets so the
	// worker can build ReferenceURLs for a reference-conditioned provider. Optional
	// (nil in unit tests that don't exercise reference generation); required in
	// production when a reference-conditioned provider (e.g. fal) is configured.
	Identities IdentityReader

	// RefPresignTTL bounds how long the presigned reference image URLs handed to a
	// reference-conditioned provider stay valid. They are minted at generation time
	// and consumed within the provider's submit/poll window, so a short TTL is
	// sufficient. Zero falls back to a built-in default.
	RefPresignTTL time.Duration

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

// discardedProviderAttempt captures a successful provider call whose output
// could not be published because cancellation or a concurrent terminal worker
// won the guarded persistence race. The provider was still billable, so its
// success and actual cost must be recorded before lifecycle release/commit.
type discardedProviderAttempt struct {
	AttemptID  string
	ProviderID string
	ModelID    string
	LatencyMs  int64
	Result     providers.ProviderGenerateResult
}

func discardedFromGen(out genResult) discardedProviderAttempt {
	return discardedProviderAttempt{
		AttemptID:  out.attemptID,
		ProviderID: out.route.providerID,
		ModelID:    out.route.modelID,
		LatencyMs:  out.latencyMs,
		Result:     out.result,
	}
}

// failTerminal marks a job permanently failed (not retryable) and releases its
// cost reservation. Used for unrunnable jobs — a missing provider adapter or a
// payload missing its resolved route — where an asynq retry could never help.
func (w *Worker) markJobCompleted(ctx context.Context, job Job, finalAssetIDs []string, expectedReservationIDs ...string) (Job, error) {
	expectedReservationID := ""
	if len(expectedReservationIDs) > 0 {
		expectedReservationID = expectedReservationIDs[0]
	}
	if expectedReservationID != "" {
		if updater, ok := w.Jobs.(ReservationBoundStateUpdater); ok {
			return updater.MarkCompletedForReservation(ctx, job.ID, job.TenantID, finalAssetIDs, expectedReservationID)
		}
	}
	return w.Jobs.MarkCompleted(ctx, job.ID, job.TenantID, finalAssetIDs)
}

func (w *Worker) markJobFailed(ctx context.Context, job Job, code, msg string, retryable bool, expectedReservationIDs ...string) (Job, error) {
	expectedReservationID := ""
	if len(expectedReservationIDs) > 0 {
		expectedReservationID = expectedReservationIDs[0]
	}
	if expectedReservationID != "" {
		if updater, ok := w.Jobs.(ReservationBoundStateUpdater); ok {
			return updater.MarkFailedForReservation(ctx, job.ID, job.TenantID, code, msg, retryable, expectedReservationID)
		}
	}
	return w.Jobs.MarkFailed(ctx, job.ID, job.TenantID, code, msg, retryable)
}

func (w *Worker) failTerminal(ctx context.Context, job Job, code, msg string, expectedReservationIDs ...string) error {
	if _, err := w.markJobFailed(ctx, job, code, msg, false, expectedReservationIDs...); err != nil {
		return err
	}
	if err := w.releaseReservation(ctx, job); err != nil {
		return err
	}
	return nil
}

// commitReservation/releaseReservation bind worker lifecycle work to the
// reservation captured in the job snapshot. Admin retry reuses a job id but
// replaces its reservation; a stale task must never commit or release that new
// hold. Legacy test repositories without a reservation-aware finalizer retain
// the job-id fallback.
func (w *Worker) commitReservation(ctx context.Context, job Job) error {
	if w.Finalizer == nil {
		return nil
	}
	if finalizer, ok := w.Finalizer.(cost.ReservationFinalizer); ok && job.CostReservationID != nil && *job.CostReservationID != "" {
		return finalizer.CommitForReservation(ctx, job.ID, *job.CostReservationID)
	}
	return w.Finalizer.Commit(ctx, job.ID)
}

func (w *Worker) releaseReservation(ctx context.Context, job Job) error {
	if w.Finalizer == nil {
		return nil
	}
	if finalizer, ok := w.Finalizer.(cost.ReservationFinalizer); ok && job.CostReservationID != nil && *job.CostReservationID != "" {
		return finalizer.ReleaseForReservation(ctx, job.ID, *job.CostReservationID)
	}
	return w.Finalizer.Release(ctx, job.ID)
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
		return w.ProcessForReservation(ctx, payload.JobID, payload.CostReservationID, int32(retryCount))
	}
}

// claimQueuedJob atomically claims a queued job. A task delivered for the
// first time (retryCount == 0) must never treat an already-running row as its
// own claim: that is a duplicate asynq delivery and would start another
// billable provider call. Running rows are accepted only for an actual asynq
// retry (retryCount > 0), after the original attempt returned an error.
func (w *Worker) claimQueuedJob(ctx context.Context, job Job, phase string, retryCount int32, expectedReservationIDs ...string) (bool, error) {
	expectedReservationID := ""
	if len(expectedReservationIDs) > 0 {
		expectedReservationID = expectedReservationIDs[0]
	}
	if job.Status != "queued" {
		if job.Status == "running" && (retryCount > 0 || retryCount < 0) {
			return true, nil
		}
		w.log().Info("worker: job is already active; ignoring duplicate delivery", "job_id", job.ID, "phase", phase, "status", job.Status, "retry_count", retryCount)
		return false, nil
	}
	var err error
	if expectedReservationID != "" {
		if updater, ok := w.Jobs.(ReservationBoundStateUpdater); ok {
			_, err = updater.MarkRunningForReservation(ctx, job.ID, job.TenantID, expectedReservationID)
		} else {
			_, err = w.Jobs.MarkRunning(ctx, job.ID, job.TenantID)
		}
	} else {
		_, err = w.Jobs.MarkRunning(ctx, job.ID, job.TenantID)
	}
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			w.log().Info("worker: queued job already claimed or terminal", "job_id", job.ID, "phase", phase)
			return false, nil
		}
		w.log().Error("worker: claim queued job", "job_id", job.ID, "phase", phase, "error", err)
		return false, err
	}
	return true, nil
}

// PreviewFinalizationClaimer atomically claims a preview's final
// phase. The claim preserves preview_ready for readers; a queued retry with an
// existing preview is promoted to running as part of the same CAS.
type PreviewFinalizationClaimer interface {
	ClaimPreviewFinalization(ctx context.Context, id, tenantID string) (Job, error)
}

func (w *Worker) claimPreviewFinalization(ctx context.Context, job Job, retryCount int32, expectedReservationIDs ...string) (bool, error) {
	expectedReservationID := ""
	if len(expectedReservationIDs) > 0 {
		expectedReservationID = expectedReservationIDs[0]
	}
	// A retry belongs to the final task that already owns the phase. The marker
	// is durable for preview_ready rows; running rows are the queued-retry form.
	if retryCount > 0 && (job.Status == "running" || payloadBool(job.InputPayload, "preview_finalization_claimed")) {
		return true, nil
	}
	if expectedReservationID != "" {
		if updater, ok := w.Jobs.(ReservationBoundStateUpdater); ok {
			if _, err := updater.ClaimPreviewFinalizationForReservation(ctx, job.ID, job.TenantID, expectedReservationID); err != nil {
				if errors.Is(err, ErrNotFound) {
					return false, nil
				}
				return false, err
			}
			return true, nil
		}
	}
	if claimer, ok := w.Jobs.(PreviewFinalizationClaimer); ok {
		if _, err := claimer.ClaimPreviewFinalization(ctx, job.ID, job.TenantID); err != nil {
			if errors.Is(err, ErrNotFound) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	}
	// Lightweight repositories used by direct tests predate the optional CAS
	// seam. They have no duplicate queue delivery, so a non-running snapshot is
	// an explicit final-phase invocation.
	if job.Status == "running" {
		return false, nil
	}
	return true, nil
}

// Process is the per-attempt worker body. retryCount is asynq's zero-based
// retry counter; attempt_number is retryCount+1. Direct callers that do not
// carry a queue token retain the legacy behavior; production task handlers use
// ProcessForReservation below.
func (w *Worker) Process(ctx context.Context, jobID string, retryCount int32) error {
	return w.process(ctx, jobID, "", retryCount)
}

// ProcessForReservation rejects a delayed task when its reservation no longer
// owns the reusable generation job. RetryJob creates a fresh reservation, so
// this token is the execution epoch that prevents an old queue delivery from
// calling providers or publishing output for the new run.
func (w *Worker) ProcessForReservation(ctx context.Context, jobID, reservationID string, retryCount int32) error {
	return w.process(ctx, jobID, reservationID, retryCount)
}

func (w *Worker) process(ctx context.Context, jobID, expectedReservationID string, retryCount int32) error {
	attemptNumber := retryCount + 1
	finalAttempt := attemptNumber >= int32(MaxAttempts)

	job, err := w.Jobs.GetByID(ctx, jobID)
	if err != nil {
		w.log().Error("worker: lookup job", "job_id", jobID, "error", err)
		return err
	}
	if expectedReservationID != "" {
		if job.CostReservationID == nil || *job.CostReservationID != expectedReservationID {
			w.log().Info("worker: stale task ignored after reservation changed", "job_id", jobID, "expected_reservation_id", expectedReservationID, "actual_reservation_id", costReservationID(job))
			return nil
		}
	}

	// Retry-safety: if the job is already terminal, a previous attempt did the
	// generation work and only the cost finalization may be outstanding (e.g.
	// the task was retried because Finalizer.Commit failed after the job was
	// marked completed). Re-run only the idempotent finalization — never the
	// provider call or asset insert — so a finalization failure can't trigger
	// duplicate generation.
	switch job.Status {
	case "completed":
		if err := w.commitReservation(ctx, job); err != nil {
			w.log().Error("worker: commit cost reservation (terminal job)", "job_id", jobID, "error", err)
			return err
		}
		return nil
	case "failed":
		if err := w.releaseReservation(ctx, job); err != nil {
			w.log().Error("worker: release cost reservation (terminal job)", "job_id", jobID, "error", err)
			return err
		}
		return nil
	case statusCancelled:
		// Phase 7C-1a: cancel is terminal. Do not call the provider, upload,
		// insert an asset, mark completed, or commit cost. Release the
		// reservation as a safe idempotent cleanup (admin cancel already
		// released it inside its own transaction) and stop cleanly.
		if err := w.releaseReservation(ctx, job); err != nil {
			w.log().Error("worker: release cost reservation (cancelled job)", "job_id", jobID, "error", err)
			return err
		}
		return nil
	}

	// Claim before parsing the route or doing any other work that can have
	// side effects. A duplicate first delivery must be a no-op even when the
	// persisted payload is malformed.
	if !w.isPreviewFirst(job) || len(job.PreviewAssetIds) == 0 {
		claimed, err := w.claimQueuedJob(ctx, job, "generation", retryCount, expectedReservationID)
		if err != nil {
			return err
		}
		if !claimed {
			return nil
		}
	}

	// Phase 7A: read the primary route the handler resolved at creation time and
	// persisted on the job. The worker never re-resolves; fallback execution
	// handles unavailable adapters from the persisted chain.
	resolved, rerr := resolvedRouteFromPayload(job.InputPayload)
	if rerr != nil {
		w.log().Error("worker: invalid resolved route", "job_id", jobID, "error", rerr)
		return w.failTerminal(ctx, job, errorCodeInvalidResolvedRoute, rerr.Error(), expectedReservationID)
	}
	if _, mpErr := maxMegapixelsForWorker(job); mpErr != nil {
		w.log().Error("worker: invalid max_megapixels", "job_id", jobID, "error", mpErr)
		if err := w.failTerminal(ctx, job, errorCodeMaxMegapixelsExceeded, mpErr.Error(), expectedReservationID); err != nil {
			return err
		}
		return nil
	}
	// Phase 7B two-phase preview-first generation applies only when the request
	// opted in (payload.delivery_mode == preview_first) AND the resolved route is
	// a true_preview route (preview_capability persisted on the payload at the
	// creation time, from the route the handler resolved — the worker never
	// re-resolves). Any other job takes the unchanged Phase 7A single-phase path.
	if w.isPreviewFirst(job) {
		return w.processPreviewFirst(ctx, job, resolved, attemptNumber, finalAttempt, retryCount, expectedReservationID)
	}

	// Reference-conditioned single-image generation: when any route in the
	// resolved chain is backed by a provider that REQUIRES reference images
	// (fal Kontext), gather the subject identity's anchor references ONCE and
	// thread them into every provider request — exactly like the pack path.
	// Failing closed here (missing/invalid references) is what keeps a
	// recurring character from being silently rendered as a different person.
	refs, terminal, rferr := w.singleImageReferences(ctx, job, resolved, expectedReservationID)
	if terminal || rferr != nil {
		return rferr
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
		JobID:         job.ID,
		Operation:     providers.OperationTextToImage,
		Prompt:        description,
		Width:         renderEdgeForMax(job, deliveryRenderEdge),
		Height:        renderEdgeForMax(job, deliveryRenderEdge),
		ReferenceURLs: refs,
		Metadata: map[string]any{
			"world_id": worldID,
			"job_type": job.JobType,
		},
	}, attemptNumber)
	if gerr != nil {
		if errors.Is(gerr, providers.ErrContentPolicyRejected) {
			// Content-policy rejection is terminal regardless of remaining asynq
			// attempts: retrying identical content re-bills a deterministic
			// rejection. Fail the job now (provider_content_rejected) and return
			// nil so asynq does not retry.
			w.failJobOnFinalAttempt(ctx, job, gerr, true, expectedReservationID)
			return nil
		}
		w.failJobOnFinalAttempt(ctx, job, gerr, finalAttempt, expectedReservationID)
		return gerr
	}
	result := out.result
	latency := out.latencyMs
	if sizeErr := validateProviderImages(job, result.Images); sizeErr != nil {
		w.recordAttemptFailureWithCost(ctx, job, out.attemptID, out.route.providerID, out.route.modelID, sizeErr, latency, reportedProviderCostPtr(result))
		if terminalErr := w.failTerminal(ctx, job, errorCodeMaxMegapixelsExceeded, sizeErr.Error(), expectedReservationID); terminalErr != nil {
			return terminalErr
		}
		return nil
	}

	assetID := ids.NewVisualAssetID()
	urls, err := w.uploadImages(ctx, assetID, result.Images)
	if err != nil {
		w.recordFailureWithCost(ctx, job, out.attemptID, out.route.providerID, out.route.modelID, err, latency, finalAttempt, reportedProviderCostPtr(result), expectedReservationID)
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
	latencyInt := int32(latency)
	tokenID := job.RequestedByTokenID
	// Provenance reflects the WINNER (the route that produced the bytes), which may
	// be a same-price fallback rather than the primary (Phase 7C-4).
	providerID := out.route.providerID
	costEvent := CostEventInsertParams{
		ID:                ids.NewCostEventID(),
		TenantID:          job.TenantID,
		JobID:             &job.ID,
		CostReservationID: job.CostReservationID,
		AssetID:           &assetID,
		TokenID:           tokenID,
		ProviderID:        &providerID,
		ModelID:           strPtr(out.route.modelID),
		ProviderAttemptID: &out.attemptID,
		Operation:         string(providers.OperationTextToImage),
		ActualCostUSD:     reportedProviderCostPtr(result),
		DurationMs:        &latencyInt,
		Status:            "completed",
		Metadata:          billableMetadata("final_call", nil),
	}
	asset, outcome, err, successAtomic := w.persistFinalAssetWithSuccess(ctx, job.ID, job.TenantID, insertParams, forced, artifactSlotFor(job, insertParams), PersistSuccessParams{
		AttemptID: out.attemptID,
		LatencyMs: latencyInt,
		CostEvent: costEvent,
	}, expectedReservationID)
	if err != nil {
		w.recordFailureWithCost(ctx, job, out.attemptID, out.route.providerID, out.route.modelID, fmt.Errorf("insert asset: %w", err), latency, finalAttempt, reportedProviderCostPtr(result), expectedReservationID)
		return err
	}
	switch outcome {
	case PersistSkippedCancelled:
		return w.finishCancelled(ctx, job, "final", discardedFromGen(out))
	case PersistAlreadyCompleted, PersistAlreadyTerminal:
		w.recordDiscardedProviderSuccess(ctx, job, discardedFromGen(out), "final")
		w.log().Info("worker: final output discarded because job is already terminal", "job_id", job.ID, "outcome", outcome)
		return nil
	}

	if !successAtomic {
		if err := w.Jobs.MarkProviderAttemptSucceeded(ctx, out.attemptID, latencyInt); err != nil {
			w.log().Warn("worker: mark attempt succeeded", "attempt_id", out.attemptID, "error", err)
		}
		if err := w.Jobs.InsertCostEvent(ctx, costEvent); err != nil {
			w.log().Warn("worker: insert cost event", "job_id", jobID, "error", err)
		}
	}

	// Commit the cost reservation: reserved → committed, move the held
	// estimate from reserved to spent, stamp actual_cost on the job + event.
	// Idempotent — safe if a later retry re-enters after a partial failure.
	if err := w.commitReservation(ctx, job); err != nil {
		w.log().Error("worker: commit cost reservation", "job_id", jobID, "error", err)
		return err
	}

	telemetry.DefaultMetrics().RecordUsableImage()

	// Phase 7C-4: the job is completed and cost committed — emit completed AFTER
	// the durable commit. Best-effort; never fails the job.
	w.emit(ctx, job.TenantID, webhooks.EventCompleted, job.ID, map[string]any{
		"final_asset_ids": []string{asset.ID},
	})

	return nil
}

func (w *Worker) persistFinalAssetWithSuccess(ctx context.Context, jobID, tenantID string, params assets.InsertParams, forced bool, slot assets.ArtifactSlot, success PersistSuccessParams, expectedReservationIDs ...string) (assets.VisualAsset, PersistOutcome, error, bool) {
	expectedReservationID := ""
	if len(expectedReservationIDs) > 0 {
		expectedReservationID = expectedReservationIDs[0]
	}
	if expectedReservationID != "" {
		if persister, ok := w.Jobs.(ReservationBoundSuccessPersister); ok {
			asset, outcome, err := persister.InsertFinalAssetAndCompleteJobWithSuccessForReservation(ctx, jobID, tenantID, expectedReservationID, params, forced, slot, success)
			return asset, outcome, err, true
		}
	}
	if persister, ok := w.Jobs.(GuardedSuccessPersister); ok {
		asset, outcome, err := persister.InsertFinalAssetAndCompleteJobWithSuccess(ctx, jobID, tenantID, params, forced, slot, success)
		return asset, outcome, err, true
	}
	asset, outcome, err := w.Jobs.InsertFinalAssetAndCompleteJobIfNotCancelled(ctx, jobID, tenantID, params, forced, slot)
	return asset, outcome, err, false
}

func (w *Worker) persistPreviewAssetWithSuccess(ctx context.Context, jobID, tenantID string, params assets.InsertParams, success PersistSuccessParams, expectedReservationIDs ...string) (assets.VisualAsset, PersistOutcome, error, bool) {
	expectedReservationID := ""
	if len(expectedReservationIDs) > 0 {
		expectedReservationID = expectedReservationIDs[0]
	}
	if expectedReservationID != "" {
		if persister, ok := w.Jobs.(ReservationBoundSuccessPersister); ok {
			asset, outcome, err := persister.InsertPreviewAssetAndMarkPreviewReadyWithSuccessForReservation(ctx, jobID, tenantID, expectedReservationID, params, success)
			return asset, outcome, err, true
		}
	}
	if persister, ok := w.Jobs.(GuardedSuccessPersister); ok {
		asset, outcome, err := persister.InsertPreviewAssetAndMarkPreviewReadyWithSuccess(ctx, jobID, tenantID, params, success)
		return asset, outcome, err, true
	}
	asset, outcome, err := w.Jobs.InsertPreviewAssetAndMarkPreviewReadyIfNotCancelled(ctx, jobID, tenantID, params)
	return asset, outcome, err, false
}

// finishCancelled handles a guarded persist that skipped because the job was
// cancelled before persistence (Phase 7C-1a). Provider work that already
// completed is still billable even though its output is discarded, so those
// attempts are closed and cost events are written before lifecycle release.
func (w *Worker) finishCancelled(ctx context.Context, job Job, phase string, attempts ...discardedProviderAttempt) error {
	w.log().Info("worker: job cancelled before persist; skipping output",
		"job_id", job.ID, "phase", phase)
	for _, attempt := range attempts {
		w.recordDiscardedProviderSuccess(ctx, job, attempt, phase)
	}
	// errPackJobNotActive is also returned when a concurrent worker has already
	// completed or failed the job. Only an actual cancelled row may release;
	// otherwise a stale loser could release the winner's still-reserved success
	// before the winner commits it.
	current, err := w.Jobs.GetByID(ctx, job.ID)
	if err != nil {
		return err
	}
	if current.Status != statusCancelled {
		w.log().Info("worker: stale output discarded without releasing non-cancelled job", "job_id", job.ID, "status", current.Status, "phase", phase)
		return nil
	}
	if err := w.releaseReservation(ctx, job); err != nil {
		w.log().Error("worker: release cost reservation (cancelled before persist)", "job_id", job.ID, "error", err)
		return err
	}
	return nil
}

func (w *Worker) recordDiscardedProviderSuccess(ctx context.Context, job Job, attempt discardedProviderAttempt, phase string) {
	if attempt.AttemptID == "" {
		return
	}
	if err := w.Jobs.MarkProviderAttemptSucceeded(ctx, attempt.AttemptID, int32(attempt.LatencyMs)); err != nil {
		w.log().Warn("worker: mark discarded provider attempt succeeded", "attempt_id", attempt.AttemptID, "error", err)
	}
	providerID := attempt.ProviderID
	attemptID := attempt.AttemptID
	latency := int32(attempt.LatencyMs)
	if err := w.Jobs.InsertCostEvent(ctx, CostEventInsertParams{
		ID:                discardedCostEventID(attemptID),
		TenantID:          job.TenantID,
		JobID:             &job.ID,
		CostReservationID: job.CostReservationID,
		TokenID:           job.RequestedByTokenID,
		ProviderID:        &providerID,
		ModelID:           strPtr(attempt.ModelID),
		ProviderAttemptID: &attemptID,
		Operation:         string(providers.OperationTextToImage),
		ActualCostUSD:     reportedProviderCostPtr(attempt.Result),
		DurationMs:        &latency,
		Status:            "completed",
		Metadata:          billableMetadata("discarded_"+phase, map[string]any{"output_discarded": true}),
	}); err != nil {
		w.log().Warn("worker: insert discarded provider cost event", "job_id", job.ID, "attempt_id", attemptID, "error", err)
	}
	if reconciler, ok := w.Finalizer.(cost.ReservationReconciler); ok && job.CostReservationID != nil && *job.CostReservationID != "" {
		if err := reconciler.ReconcileForReservation(ctx, job.ID, *job.CostReservationID); err != nil {
			w.log().Warn("worker: reconcile discarded provider cost", "job_id", job.ID, "reservation_id", *job.CostReservationID, "attempt_id", attemptID, "error", err)
		}
	}
}

func discardedCostEventID(attemptID string) string {
	return fmt.Sprintf("%s_discarded_%s", ids.PrefixCostEvent, attemptID)
}

func costReservationID(job Job) string {
	if job.CostReservationID == nil {
		return ""
	}
	return *job.CostReservationID
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
func (w *Worker) processPreviewFirst(ctx context.Context, job Job, resolved resolvedRoute, attemptNumber int32, finalAttempt bool, retryCount int32, expectedReservationIDs ...string) error {
	expectedReservationID := ""
	if len(expectedReservationIDs) > 0 {
		expectedReservationID = expectedReservationIDs[0]
	}
	worldID := ""
	if job.WorldID != nil {
		worldID = *job.WorldID
	}
	description, _ := job.InputPayload["description"].(string)

	// Reference-conditioned two-phase generation: gather the identity's
	// references once; both the preview and the final walk condition on the
	// same anchors (presigned fresh for this attempt).
	refs, terminal, rferr := w.singleImageReferences(ctx, job, resolved, expectedReservationID)
	if terminal || rferr != nil {
		return rferr
	}

	// --- Phase A: preview ---------------------------------------------------
	// Resume safety: a non-empty preview_asset_ids means a prior attempt already
	// generated and committed the preview. Skip straight to final so a retry
	// never regenerates the preview and never recharges.
	if len(job.PreviewAssetIds) == 0 {
		// The parent Process method claims the job before entering this phase.
		// Keeping the phase free of a second claim is important for the CAS: the
		// first delivery must not claim queued -> running and then self-reject.

		// Phase 7C-4: the preview phase walks the chain independently — its
		// provenance reflects whichever route produced the preview bytes.
		out, gerr := w.generateWithFallback(ctx, job, resolved, providers.ProviderGenerateRequest{
			JobID:         job.ID,
			Operation:     providers.OperationTextToImage,
			Prompt:        description,
			Width:         renderEdgeForMax(job, previewRenderEdge),
			Height:        renderEdgeForMax(job, previewRenderEdge),
			ReferenceURLs: refs,
			Metadata: map[string]any{
				"world_id": worldID,
				"job_type": job.JobType,
				"tier":     "preview",
			},
		}, attemptNumber)
		if gerr != nil {
			// Preview-phase chain exhausted: no preview asset is created. On the
			// terminal attempt fail the job + release the reservation. Per-route
			// failures were already recorded inside the walk. A content-policy
			// rejection is terminal immediately (see the single-phase path).
			if errors.Is(gerr, providers.ErrContentPolicyRejected) {
				w.failJobOnFinalAttempt(ctx, job, gerr, true, expectedReservationID)
				return nil
			}
			w.failJobOnFinalAttempt(ctx, job, gerr, finalAttempt, expectedReservationID)
			return gerr
		}
		result := out.result
		latency := out.latencyMs
		if sizeErr := validateProviderImages(job, result.Images); sizeErr != nil {
			w.recordAttemptFailureWithCost(ctx, job, out.attemptID, out.route.providerID, out.route.modelID, sizeErr, latency, reportedProviderCostPtr(result))
			if terminalErr := w.failTerminal(ctx, job, errorCodeMaxMegapixelsExceeded, sizeErr.Error(), expectedReservationID); terminalErr != nil {
				return terminalErr
			}
			return nil
		}

		previewAssetID := ids.NewVisualAssetID()
		urls, err := w.uploadImages(ctx, previewAssetID, result.Images)
		if err != nil {
			w.recordFailureWithCost(ctx, job, out.attemptID, out.route.providerID, out.route.modelID, err, latency, finalAttempt, reportedProviderCostPtr(result), expectedReservationID)
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
		previewProviderID := out.route.providerID
		previewLatency := int32(latency)
		previewCostEvent := CostEventInsertParams{
			ID:                ids.NewCostEventID(),
			TenantID:          job.TenantID,
			JobID:             &job.ID,
			CostReservationID: job.CostReservationID,
			AssetID:           &previewAssetID,
			TokenID:           job.RequestedByTokenID,
			ProviderID:        &previewProviderID,
			ModelID:           strPtr(out.route.modelID),
			ProviderAttemptID: &out.attemptID,
			Operation:         string(providers.OperationTextToImage),
			ActualCostUSD:     reportedProviderCostPtr(result),
			DurationMs:        &previewLatency,
			Status:            "completed",
			Metadata:          billableMetadata("preview_call", nil),
		}
		_, outcome, err, successAtomic := w.persistPreviewAssetWithSuccess(ctx, job.ID, job.TenantID, previewParams, PersistSuccessParams{
			AttemptID: out.attemptID,
			LatencyMs: previewLatency,
			CostEvent: previewCostEvent,
		}, expectedReservationID)
		if err != nil {
			w.recordFailureWithCost(ctx, job, out.attemptID, out.route.providerID, out.route.modelID, fmt.Errorf("insert preview asset: %w", err), latency, finalAttempt, reportedProviderCostPtr(result), expectedReservationID)
			return err
		}
		previewPersisted := true
		switch outcome {
		case PersistSkippedCancelled:
			return w.finishCancelled(ctx, job, "preview", discardedFromGen(out))
		case PersistAlreadyCompleted, PersistAlreadyTerminal:
			w.recordDiscardedProviderSuccess(ctx, job, discardedFromGen(out), "preview")
			w.log().Info("worker: preview output discarded because job is already terminal", "job_id", job.ID, "outcome", outcome)
			return nil
		case PersistAlreadyPreviewReady:
			// Another attempt committed the preview while this attempt was
			// generating. The provider call remains billable even though its
			// duplicate preview output is discarded.
			w.recordDiscardedProviderSuccess(ctx, job, discardedFromGen(out), "preview")
			previewPersisted = false
		}
		if previewPersisted {
			if !successAtomic {
				if err := w.Jobs.MarkProviderAttemptSucceeded(ctx, out.attemptID, previewLatency); err != nil {
					w.log().Warn("worker: mark attempt succeeded (preview)", "attempt_id", out.attemptID, "error", err)
				}
				if err := w.Jobs.InsertCostEvent(ctx, previewCostEvent); err != nil {
					w.log().Warn("worker: insert preview cost event", "job_id", job.ID, "error", err)
				}
			}

			// Phase 7C-4: the preview is durably committed (preview_ready) and not
			// cancelled — emit preview_ready AFTER the commit. Best-effort.
			w.emit(ctx, job.TenantID, webhooks.EventPreviewReady, job.ID, map[string]any{
				"preview_asset_ids": []string{previewAssetID},
			})
		}
	}

	// Claim the final phase before any final provider call. The first
	// delivery wins the durable marker; a duplicate first delivery stops, while
	// an asynq retry is allowed to resume the owner after a provider error.
	claimedFinal, claimErr := w.claimPreviewFinalization(ctx, job, retryCount, expectedReservationID)
	if claimErr != nil {
		return claimErr
	}
	if !claimedFinal {
		return nil
	}

	// --- Phase B: final -----------------------------------------------------
	// Phase 7C-4: the final phase walks the chain independently of the preview
	// phase — its winner (and thus the final asset's provenance + the success cost
	// event) may differ from the preview phase's winner.
	out, gerr := w.generateWithFallback(ctx, job, resolved, providers.ProviderGenerateRequest{
		JobID:         job.ID,
		Operation:     providers.OperationTextToImage,
		Prompt:        description,
		Width:         renderEdgeForMax(job, deliveryRenderEdge),
		Height:        renderEdgeForMax(job, deliveryRenderEdge),
		ReferenceURLs: refs,
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
		// A content-policy rejection is terminal immediately.
		if errors.Is(gerr, providers.ErrContentPolicyRejected) {
			w.failJobOnFinalAttempt(ctx, job, gerr, true, expectedReservationID)
			return nil
		}
		w.failJobOnFinalAttempt(ctx, job, gerr, finalAttempt, expectedReservationID)
		return gerr
	}
	result := out.result
	latency := out.latencyMs
	if sizeErr := validateProviderImages(job, result.Images); sizeErr != nil {
		w.recordAttemptFailureWithCost(ctx, job, out.attemptID, out.route.providerID, out.route.modelID, sizeErr, latency, reportedProviderCostPtr(result))
		if terminalErr := w.failTerminal(ctx, job, errorCodeMaxMegapixelsExceeded, sizeErr.Error(), expectedReservationID); terminalErr != nil {
			return terminalErr
		}
		return nil
	}

	finalAssetID := ids.NewVisualAssetID()
	urls, err := w.uploadImages(ctx, finalAssetID, result.Images)
	if err != nil {
		w.recordFailureWithCost(ctx, job, out.attemptID, out.route.providerID, out.route.modelID, err, latency, finalAttempt, reportedProviderCostPtr(result), expectedReservationID)
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
	latencyInt := int32(latency)
	// Provenance reflects the final phase's WINNER (Phase 7C-4).
	providerID := out.route.providerID
	costEvent := CostEventInsertParams{
		ID:                ids.NewCostEventID(),
		TenantID:          job.TenantID,
		JobID:             &job.ID,
		CostReservationID: job.CostReservationID,
		AssetID:           &finalAssetID,
		TokenID:           job.RequestedByTokenID,
		ProviderID:        &providerID,
		ModelID:           strPtr(out.route.modelID),
		ProviderAttemptID: &out.attemptID,
		Operation:         string(providers.OperationTextToImage),
		ActualCostUSD:     reportedProviderCostPtr(result),
		DurationMs:        &latencyInt,
		Status:            "completed",
		Metadata:          billableMetadata("final_call", nil),
	}
	asset, outcome, err, successAtomic := w.persistFinalAssetWithSuccess(ctx, job.ID, job.TenantID, finalParams, forced, artifactSlotFor(job, finalParams), PersistSuccessParams{
		AttemptID: out.attemptID,
		LatencyMs: latencyInt,
		CostEvent: costEvent,
	}, expectedReservationID)
	if err != nil {
		w.recordFailureWithCost(ctx, job, out.attemptID, out.route.providerID, out.route.modelID, fmt.Errorf("insert asset: %w", err), latency, finalAttempt, reportedProviderCostPtr(result), expectedReservationID)
		return err
	}
	switch outcome {
	case PersistSkippedCancelled:
		return w.finishCancelled(ctx, job, "final", discardedFromGen(out))
	case PersistAlreadyCompleted, PersistAlreadyTerminal:
		w.recordDiscardedProviderSuccess(ctx, job, discardedFromGen(out), "final")
		w.log().Info("worker: preview-first final output discarded because job is already terminal", "job_id", job.ID, "outcome", outcome)
		return nil
	}
	if !successAtomic {
		if err := w.Jobs.MarkProviderAttemptSucceeded(ctx, out.attemptID, latencyInt); err != nil {
			w.log().Warn("worker: mark attempt succeeded (final)", "attempt_id", out.attemptID, "error", err)
		}
		if err := w.Jobs.InsertCostEvent(ctx, costEvent); err != nil {
			w.log().Warn("worker: insert cost event (final)", "job_id", job.ID, "error", err)
		}
	}

	// Commit the cost reservation ONCE, only after final success. The reservation
	// covers both preview and final calls when preview-first is selected; the
	// lifecycle reconciles provider-reported amounts across both events.
	// Idempotent — a retry that re-enters after the job
	// is completed re-commits via the terminal short-circuit in Process.
	if err := w.commitReservation(ctx, job); err != nil {
		w.log().Error("worker: commit cost reservation (preview-first)", "job_id", job.ID, "error", err)
		return err
	}

	telemetry.DefaultMetrics().RecordUsableImage()

	// Phase 7C-4: the two-phase job is completed and cost committed — emit
	// completed AFTER the durable commit. Best-effort.
	w.emit(ctx, job.TenantID, webhooks.EventCompleted, job.ID, map[string]any{
		"final_asset_ids": []string{asset.ID},
	})

	return nil
}

// singleImageReferences gathers the subject identity's reference URLs for a
// single-image job when any route in the resolved chain is backed by a
// reference-conditioned provider (Capabilities().RequiresReferenceImage). It
// is the single-image analogue of the pack path's gather-once step: the
// identity id comes from the job payload (persisted by the generations
// handler), and the anchors are validated + presigned by
// referenceURLsForIdentity. Prompt-only chains return (nil, false, nil) and
// are unchanged.
//
// terminal=true means the job was failed terminally here (missing or invalid
// references — fail closed, never render a different character; PRD 03 §8)
// and the returned error is failTerminal's result: the caller returns it
// as-is without further provider work.
func (w *Worker) singleImageReferences(ctx context.Context, job Job, primary resolvedRoute, expectedReservationIDs ...string) ([]string, bool, error) {
	expectedReservationID := ""
	if len(expectedReservationIDs) > 0 {
		expectedReservationID = expectedReservationIDs[0]
	}
	routes := append([]resolvedRoute{primary}, fallbackRoutesFromPayload(job.InputPayload)...)
	needs := false
	for _, rt := range routes {
		if adapter, ok := w.Providers.Get(rt.providerID); ok && adapter.Capabilities().RequiresReferenceImage {
			needs = true
			break
		}
	}
	if !needs {
		return nil, false, nil
	}
	identityID := payloadString(job.InputPayload, "identity_id")
	if identityID == "" {
		msg := "reference-conditioned provider routed but the job carries no identity_id to gather references from"
		w.log().Error("worker: reference route without identity", "job_id", job.ID, "provider_id", primary.providerID)
		return nil, true, w.failTerminal(ctx, job, errorCodeMissingReference, msg, expectedReservationID)
	}
	refs, err := w.referenceURLsForIdentity(ctx, identityID, job.TenantID)
	if err != nil {
		code := errorCodeInvalidReference
		if !errors.Is(err, errInvalidReference) {
			code = errorCodeMissingReference
		}
		w.log().Error("worker: gather reference assets", "job_id", job.ID, "identity_id", identityID, "error", err)
		return nil, true, w.failTerminal(ctx, job, code, err.Error(), expectedReservationID)
	}
	if len(refs) == 0 {
		msg := fmt.Sprintf("visual identity %q has no reference assets for reference-conditioned generation", identityID)
		w.log().Error("worker: no reference assets", "job_id", job.ID, "identity_id", identityID)
		return nil, true, w.failTerminal(ctx, job, errorCodeMissingReference, msg, expectedReservationID)
	}
	return refs, false, nil
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

// defaultRefPresignTTL bounds reference image URLs when RefPresignTTL is unset.
// References are minted at generation time and consumed within the provider's
// submit/poll window, so a few minutes is ample.
const defaultRefPresignTTL = 10 * time.Minute

// assetStatusReady is the visual_assets.status a reference anchor must have to be
// usable: a non-ready (preview/archived/failed) asset is not a stable reference.
const assetStatusReady = "ready"

// errInvalidReference marks a reference anchor that exists in the identity record
// but cannot be used: wrong tenant, missing, not ready, or with no resolvable
// high-res object. The caller maps it to the invalid_reference_asset failure code
// (distinct from missing_reference_assets, which is "the identity has no anchors
// at all"). Both fail the job closed rather than generating a different character.
var errInvalidReference = errors.New("invalid_reference_asset")

// referenceURLsForIdentity gathers the reference image URLs a reference-
// conditioned provider needs to hold a recurring character: it loads the visual
// identity, then for each anchor asset id LOADS the asset through the assets
// repository and validates it before minting a presigned URL for its ACTUAL
// high-res object.
//
// Each anchor is validated: it must belong to the tenant (GetByIDForTenant is
// tenant-scoped, so a wrong-tenant/missing id surfaces as a load error),
// status=ready, and carry a high-res object whose canonical key is parseable. A
// stale/missing/non-ready/foreign anchor fails with errInvalidReference rather
// than silently presigning a guessed key that may 404 or point at the wrong
// object. The presigned URL is derived from the asset's stored high_res_url, not
// from a reconstructed key.
//
// It returns an empty slice (no error) when the identity exists but has no anchor
// assets — the caller fails the job closed (missing_reference_assets) in that
// case. A nil Identities dependency or a failed identity load is a real error the
// caller surfaces.
func (w *Worker) referenceURLsForIdentity(ctx context.Context, identityID, tenantID string) ([]string, error) {
	if w.Identities == nil {
		return nil, fmt.Errorf("worker: identity reader not configured for reference-conditioned generation")
	}
	if w.Assets == nil {
		return nil, fmt.Errorf("worker: assets repository not configured for reference-conditioned generation")
	}
	if identityID == "" {
		return nil, nil
	}
	identity, err := w.Identities.GetByIDForTenant(ctx, identityID, tenantID)
	if err != nil {
		return nil, fmt.Errorf("worker: load identity for references: %w", err)
	}
	ttl := w.RefPresignTTL
	if ttl <= 0 {
		ttl = defaultRefPresignTTL
	}
	anchorIDs := identity.AnchorAssetIds
	urls := make([]string, 0, len(anchorIDs))
	for _, anchorID := range anchorIDs {
		if anchorID == "" {
			continue
		}
		asset, err := w.Assets.GetByIDForTenant(ctx, anchorID, tenantID)
		if err != nil {
			// Wrong-tenant or missing anchor: tenant-scoped lookup fails closed.
			return nil, fmt.Errorf("%w: anchor %q: %v", errInvalidReference, anchorID, err)
		}
		if asset.Status != assetStatusReady {
			return nil, fmt.Errorf("%w: anchor %q is not ready (status %q)", errInvalidReference, anchorID, asset.Status)
		}
		if asset.HighResUrl == nil || *asset.HighResUrl == "" {
			return nil, fmt.Errorf("%w: anchor %q has no high-res object", errInvalidReference, anchorID)
		}
		key, ok := storage.KeyFromCanonicalURL(*asset.HighResUrl)
		if !ok {
			return nil, fmt.Errorf("%w: anchor %q has an unparseable high-res url", errInvalidReference, anchorID)
		}
		signed, err := w.Storage.Presign(ctx, key, ttl)
		if err != nil {
			return nil, fmt.Errorf("worker: presign reference anchor %q: %w", anchorID, err)
		}
		urls = append(urls, signed)
	}
	return urls, nil
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
	for routeIndex, route := range routes {
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
			ModelID:         strPtr(route.modelID),
			ProviderRouteID: strPtr(route.routeID),
			AttemptNumber:   attemptNumber,
		})
		if err != nil {
			w.log().Error("worker: insert attempt", "job_id", job.ID, "provider_id", route.providerID, "error", err)
			return genResult{}, err
		}

		if routeIndex > 0 {
			telemetry.DefaultMetrics().RecordFallbackAttempt()
		}
		telemetry.DefaultMetrics().RecordProviderCall()
		start := time.Now()
		result, providerErr := adapter.Generate(ctx, genReq)
		latency := time.Since(start).Milliseconds()
		w.updateProviderAttemptCost(ctx, attempt.ID, result)
		if providerErr != nil {
			// Record this route's failure (mark attempt failed + failed cost event).
			// Terminal job-fail/release is the caller's job once the whole chain is
			// exhausted on the final asynq attempt.
			w.recordAttemptFailureWithCost(ctx, job, attempt.ID, attempt.ProviderID, route.modelID, providerErr, latency, reportedProviderCostPtr(result))
			if errors.Is(providerErr, providers.ErrContentPolicyRejected) {
				telemetry.DefaultMetrics().RecordPolicyReject()
				// A content-policy rejection MUST NOT be walked around: trying the
				// same content on another route would circumvent the rejecting
				// provider's policy decision (and bill more attempts for a
				// deterministic outcome). Stop the walk; the caller fails the job
				// terminally with provider_content_rejected.
				return genResult{}, providerErr
			}
			lastErr = providerErr
			continue
		}
		if routeIndex > 0 {
			telemetry.DefaultMetrics().RecordFallbackSuccess()
		}
		return genResult{result: result, route: route, attemptID: attempt.ID, latencyMs: latency}, nil
	}

	if !anyAdapter {
		return genResult{}, fmt.Errorf("%w: no adapter registered for any route in the resolved chain (primary provider %q)", errProviderUnavailable, primary.providerID)
	}
	return genResult{}, lastErr
}

// updateProviderAttemptCost is best-effort because billing metadata must not
// turn a successful image into a failed job. Production repositories implement
// ProviderAttemptCostUpdater; unit fakes can omit the optional extension. The
// update is also sent when only a provider request id is available, so the
// attempt remains traceable even when the adapter has no billing amount.
func (w *Worker) updateProviderAttemptCost(ctx context.Context, attemptID string, result providers.ProviderGenerateResult) {
	actual, currency, hasCost := reportedProviderCost(result)
	requestID := result.ProviderRequestID
	if requestID == "" {
		// ProviderJobID predates ProviderRequestID and is still the only
		// identifier returned by older adapters. Preserve it for billing
		// reconciliation instead of silently losing provider traceability.
		requestID = result.ProviderJobID
	}
	if requestID == "" && !hasCost {
		return
	}
	updater, ok := w.Jobs.(ProviderAttemptCostUpdater)
	if !ok {
		return
	}
	var actualPtr *string
	if hasCost {
		actualPtr = &actual
	}
	if err := updater.UpdateProviderAttemptCost(ctx, attemptID, requestID, actualPtr, currency); err != nil {
		w.log().Warn("worker: persist provider billing metadata", "attempt_id", attemptID, "error", err)
	}
}

// reportedProviderCost accepts the explicit adapter field and a metadata
// fallback for older adapters. The latter keeps provider integrations additive:
// an adapter can expose billing data before it is rebuilt against this field.
func reportedProviderCost(result providers.ProviderGenerateResult) (string, string, bool) {
	currency := normalizeProviderCurrency(result.CostCurrency)
	if result.ActualCostUSD != nil {
		if text, ok := normalizeProviderCost(*result.ActualCostUSD); ok {
			return text, currency, true
		}
	}
	for _, key := range []string{"actual_cost_usd", "actual_cost"} {
		value, ok := result.Metadata[key]
		if !ok || value == nil {
			continue
		}
		var text string
		switch v := value.(type) {
		case string:
			text = v
		case float64:
			text = strconv.FormatFloat(v, 'f', -1, 64)
		case json.Number:
			text = v.String()
		default:
			text = fmt.Sprint(v)
		}
		if text, ok := normalizeProviderCost(text); ok {
			return text, currency, true
		}
	}
	return "", currency, false
}

func normalizeProviderCost(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	n, err := strconv.ParseFloat(value, 64)
	// generation_cost_events / provider_attempts use NUMERIC(14,4). Reject
	// values that cannot be stored instead of logging a successful job while
	// silently dropping its provider actual.
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > 9_999_999_999.9999 {
		return "", false
	}
	return value, true
}

func normalizeProviderCurrency(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	if len(value) != 3 {
		return "USD"
	}
	return value
}

func billableMetadata(operation string, fields map[string]any) []byte {
	payload := make(map[string]any, len(fields)+1)
	for key, value := range fields {
		payload[key] = value
	}
	payload["billable_operation"] = operation
	encoded, err := json.Marshal(payload)
	if err != nil {
		return []byte(`{"billable_operation":"unknown"}`)
	}
	return encoded
}

func reportedProviderCostPtr(result providers.ProviderGenerateResult) *string {
	actual, currency, ok := reportedProviderCost(result)
	if !ok || currency != "USD" {
		// generation_cost_events.actual_cost_usd is intentionally USD-only. The
		// provider_attempts row still keeps a non-USD amount plus currency.
		return nil
	}
	return &actual
}

// renderEdgeForMax requests no more pixels than the caller allowed when the
// provider honors dimensions. validateProviderImages remains mandatory because
// providers may ignore dimensions or return a different size.
func renderEdgeForMax(job Job, desired int) int {
	maxMP, err := maxMegapixelsForWorker(job)
	if err != nil {
		// Validation after the provider call still fails the job closed; use the
		// platform default here so malformed legacy payloads do not create an
		// invalid request dimension before that validation runs.
		maxMP = workerMaxMegapixels
	}
	maxEdge := int(math.Sqrt(maxMP * 1_000_000))
	if maxEdge > 0 && maxEdge < desired {
		return maxEdge
	}
	return desired
}

func payloadFloat64(payload map[string]any, key string) float64 {
	switch value := payload[key].(type) {
	case float64:
		return value
	case float32:
		return float64(value)
	case int:
		return float64(value)
	case json.Number:
		f, _ := value.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(value, 64)
		return f
	default:
		return 0
	}
}

func validateProviderImages(job Job, images []providers.ProviderImage) error {
	maxMP, err := maxMegapixelsForWorker(job)
	if err != nil {
		return fmt.Errorf("%w: %v", errMaxMegapixelsExceeded, err)
	}
	for index, img := range images {
		width, height, err := providerImageDimensions(img)
		if err != nil {
			return fmt.Errorf("%w: image %d dimensions unavailable: %v", errMaxMegapixelsExceeded, index, err)
		}
		megapixels := (float64(width) * float64(height)) / 1_000_000
		if megapixels > maxMP+1e-9 {
			return fmt.Errorf("%w: image %d is %.4f megapixels, maximum is %.4f", errMaxMegapixelsExceeded, index, megapixels, maxMP)
		}
	}
	return nil
}

func maxMegapixelsForWorker(job Job) (float64, error) {
	raw, present := job.InputPayload["max_megapixels"]
	if !present {
		return workerMaxMegapixels, nil
	}
	value := payloadFloat64(job.InputPayload, "max_megapixels")
	if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
		return 0, fmt.Errorf("invalid max_megapixels %v", raw)
	}
	if value > workerMaxMegapixels {
		return 0, fmt.Errorf("max_megapixels %.4f exceeds platform ceiling %.4f", value, workerMaxMegapixels)
	}
	return value, nil
}

func providerImageDimensions(img providers.ProviderImage) (int, int, error) {
	// Provider-supplied width/height metadata is provenance, not a safety
	// boundary: an adapter can report plausible dimensions for bytes that are
	// larger (or simply different). Decode the bytes that will actually be
	// persisted and enforce the pixel budget against those dimensions.
	if len(img.Bytes) == 0 {
		return 0, 0, errors.New("provider returned no image bytes")
	}
	decoded, _, err := image.Decode(bytes.NewReader(img.Bytes))
	if err != nil {
		return 0, 0, err
	}
	bounds := decoded.Bounds()
	if bounds.Dx() <= 0 || bounds.Dy() <= 0 {
		return 0, 0, errors.New("provider returned invalid image dimensions")
	}
	return bounds.Dx(), bounds.Dy(), nil
}

func (w *Worker) recordAttemptFailureWithCost(ctx context.Context, job Job, attemptID, providerID, modelID string, callErr error, latencyMs int64, actualCostUSD *string) {
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
		CostReservationID: job.CostReservationID,
		TokenID:           tokenID,
		ProviderID:        providerIDPtr,
		ModelID:           strPtr(modelID),
		ProviderAttemptID: attemptIDPtr,
		Operation:         string(providers.OperationTextToImage),
		ActualCostUSD:     actualCostUSD,
		DurationMs:        &latencyInt,
		Status:            "failed",
		Metadata:          billableMetadata("provider_attempt", nil),
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
func (w *Worker) failJobOnFinalAttempt(ctx context.Context, job Job, callErr error, finalAttempt bool, expectedReservationIDs ...string) {
	if !finalAttempt {
		return
	}
	errMsg := callErr.Error()
	markedFailed := true
	if _, err := w.markJobFailed(ctx, job, errorCodeFor(callErr), errMsg, false, expectedReservationIDs...); err != nil {
		w.log().Error("worker: mark job failed", "job_id", job.ID, "error", err)
		markedFailed = false
	}
	// Terminal failure: release the cost reservation only after the terminal
	// status CAS succeeded. If MarkFailed failed, the job may still be running or
	// completed (or an admin retry may already have reopened it); releasing by
	// job id in that case could unbill the active reservation.
	if markedFailed {
		if err := w.releaseReservation(ctx, job); err != nil {
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

func (w *Worker) recordFailureWithCost(ctx context.Context, job Job, attemptID, providerID, modelID string, callErr error, latencyMs int64, finalAttempt bool, actualCostUSD *string, expectedReservationIDs ...string) {
	w.recordAttemptFailureWithCost(ctx, job, attemptID, providerID, modelID, callErr, latencyMs, actualCostUSD)
	w.failJobOnFinalAttempt(ctx, job, callErr, finalAttempt, expectedReservationIDs...)
}

func errorCodeFor(err error) string {
	if errors.Is(err, errStorageFailure) {
		return errorCodeStorageFailure
	}
	if errors.Is(err, errPersistence) {
		return errorCodePersistenceError
	}
	if errors.Is(err, errMaxMegapixelsExceeded) {
		return errorCodeMaxMegapixelsExceeded
	}
	if errors.Is(err, errProviderUnavailable) {
		return errorCodeProviderUnavailable
	}
	if errors.Is(err, providers.ErrContentPolicyRejected) {
		return errorCodeContentRejected
	}
	if errors.Is(err, providers.ErrReferenceRequired) {
		return errorCodeMissingReference
	}
	return errorCodeProviderFailure
}

var (
	errStorageFailure        = errors.New("storage_failure")
	errPersistence           = errors.New("persistence_error")
	errMaxMegapixelsExceeded = errors.New("max_megapixels_exceeded")
	errProviderUnavailable   = errors.New("provider_unavailable")
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
