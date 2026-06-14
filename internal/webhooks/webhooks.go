package webhooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/hibiken/asynq"

	"github.com/zakkriel/drchat-image-platform/internal/ids"
)

const (
	// TaskDeliver is the asynq task name for one webhook delivery attempt. The
	// payload carries only the delivery id; the deliverer re-reads the delivery
	// row (and its endpoint) from Postgres on each attempt.
	TaskDeliver = "webhook:deliver"

	// MaxDeliveryRetries bounds asynq's retry count for a webhook delivery
	// (asynq's default exponential backoff applies between attempts). After this
	// many retries the delivery stays status=failed with the last error recorded.
	MaxDeliveryRetries = 5

	// defaultHTTPTimeout bounds a single delivery POST. Overridable via the
	// Deliverer's injected *http.Client.
	defaultHTTPTimeout = 10 * time.Second
)

// Event types (MVP: exactly these three generation-job lifecycle events).
const (
	EventPreviewReady = "generation_job.preview_ready"
	EventCompleted    = "generation_job.completed"
	EventFailed       = "generation_job.failed"
)

// Delivery statuses (mirror the webhook_deliveries CHECK constraint).
const (
	StatusPending   = "pending"
	StatusDelivered = "delivered"
	StatusFailed    = "failed"
)

// Signature / event headers sent on every delivery POST.
const (
	headerEvent     = "X-DreamChat-Event"
	headerSignature = "X-DreamChat-Signature"
)

// DeliverTaskPayload is the on-the-wire asynq payload for TaskDeliver. It is
// intentionally tiny: the deliverer re-reads the durable delivery row by id so
// a retry always sees the current state.
type DeliverTaskPayload struct {
	DeliveryID string `json:"delivery_id"`
}

// Event is the JSON body a receiver gets on every delivery POST:
//
//	{
//	  "event":       "<event_type>",   // generation_job.{preview_ready|completed|failed}
//	  "job_id":      "...",            // the generation job, when the event is job-scoped
//	  "tenant_id":   "...",            // the tenant the endpoint belongs to
//	  "data":        { ... },          // event-specific payload (e.g. asset ids, error_code)
//	  "occurred_at": "<RFC3339>"       // when the event was emitted
//	}
//
// The exact serialized bytes are what is signed (X-DreamChat-Signature) and
// stored on the delivery row, so the signature a receiver verifies matches the
// stored payload byte-for-byte across retries.
type Event struct {
	Event      string         `json:"event"`
	JobID      string         `json:"job_id,omitempty"`
	TenantID   string         `json:"tenant_id"`
	Data       map[string]any `json:"data"`
	OccurredAt string         `json:"occurred_at"`
}

// EmitInput is one job-lifecycle event a caller (the worker) wants delivered.
type EmitInput struct {
	TenantID  string
	EventType string
	JobID     string
	Data      map[string]any
}

// Enqueuer enqueues the asynq webhook:deliver task. The interface is small so
// the Emitter can be unit-tested without Redis; a nil Enqueuer makes Emit a
// clean no-op (the worker's existing unit tests need no Redis).
type Enqueuer interface {
	EnqueueDeliver(ctx context.Context, deliveryID string) error
	Close() error
}

type asynqEnqueuer struct {
	client *asynq.Client
}

// NewEnqueuer builds an asynq-client-backed Enqueuer for the deliver task,
// mirroring internal/jobs/enqueue.go.
func NewEnqueuer(addr, password string) Enqueuer {
	client := asynq.NewClient(asynq.RedisClientOpt{
		Addr:     addr,
		Password: password,
	})
	return &asynqEnqueuer{client: client}
}

func (e *asynqEnqueuer) EnqueueDeliver(ctx context.Context, deliveryID string) error {
	if e == nil || e.client == nil {
		return fmt.Errorf("webhooks: enqueuer not initialized")
	}
	payload, err := json.Marshal(DeliverTaskPayload{DeliveryID: deliveryID})
	if err != nil {
		return err
	}
	_, err = e.client.EnqueueContext(ctx, asynq.NewTask(TaskDeliver, payload), asynq.MaxRetry(MaxDeliveryRetries))
	return err
}

func (e *asynqEnqueuer) Close() error {
	if e == nil || e.client == nil {
		return nil
	}
	return e.client.Close()
}

// Emitter looks up a tenant's active endpoint, records a pending delivery, and
// enqueues a deliver task. It is the worker-facing entry point (the worker
// holds it behind a narrow interface). Emission is best-effort: every error is
// returned for the caller to log, but the worker treats it as non-fatal — a
// webhook failure must never fail the underlying generation job.
//
// MVP limitation (documented here per the Phase 7C-4 plan): events are emitted
// only at the worker's durable job-lifecycle transitions (preview committed,
// job completed+committed, terminal failure). They are NOT emitted for admin
// cancel, a preflight denial at job creation, or an enqueue failure — those
// paths never reach the worker's emit points.
type Emitter struct {
	Repo     Repository
	Enqueuer Enqueuer
	Logger   *slog.Logger
	// Now is injectable so tests get a deterministic occurred_at; defaults to
	// time.Now when nil.
	Now func() time.Time
}

// Emit records and enqueues one event for delivery. It no-ops (returns nil)
// when the tenant has no active endpoint or when the Enqueuer is nil/unwired,
// so a process without webhook wiring (or a tenant without a config) silently
// does nothing.
func (e *Emitter) Emit(ctx context.Context, in EmitInput) error {
	if e == nil || e.Repo == nil || e.Enqueuer == nil {
		// No webhook wiring in this process: nothing to do.
		return nil
	}

	endpoint, err := e.Repo.GetActiveEndpointByTenant(ctx, in.TenantID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			// Tenant has no endpoint configured: a clean no-op, not an error.
			return nil
		}
		e.log().Error("webhooks: lookup endpoint", "tenant_id", in.TenantID, "error", err)
		return err
	}

	body, err := json.Marshal(Event{
		Event:      in.EventType,
		JobID:      in.JobID,
		TenantID:   in.TenantID,
		Data:       in.Data,
		OccurredAt: e.now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		e.log().Error("webhooks: marshal event", "tenant_id", in.TenantID, "error", err)
		return err
	}

	var jobIDRef *string
	if in.JobID != "" {
		j := in.JobID
		jobIDRef = &j
	}
	delivery, err := e.Repo.InsertDelivery(ctx, InsertDeliveryParams{
		ID:                ids.NewWebhookDeliveryID(),
		TenantID:          in.TenantID,
		WebhookEndpointID: endpoint.ID,
		EventType:         in.EventType,
		GenerationJobID:   jobIDRef,
		Payload:           body,
	})
	if err != nil {
		e.log().Error("webhooks: insert delivery", "tenant_id", in.TenantID, "error", err)
		return err
	}

	if err := e.Enqueuer.EnqueueDeliver(ctx, delivery.ID); err != nil {
		e.log().Error("webhooks: enqueue deliver", "delivery_id", delivery.ID, "error", err)
		return err
	}
	return nil
}

func (e *Emitter) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Emitter) log() *slog.Logger {
	if e == nil || e.Logger == nil {
		return slog.Default()
	}
	return e.Logger
}

// Deliverer performs one webhook delivery attempt. It loads the delivery row
// and its endpoint, signs the stored payload, POSTs it, and records the
// outcome. On a non-2xx response or a transport error it records status=failed
// AND returns an error so asynq retries with bounded backoff (MaxDeliveryRetries).
type Deliverer struct {
	Repo   Repository
	Client *http.Client
	Logger *slog.Logger
}

// NewDeliverer wires a Deliverer with a bounded-timeout HTTP client when none
// is supplied. The client is injectable so tests can point it at an
// httptest.Server.
func NewDeliverer(repo Repository, client *http.Client, logger *slog.Logger) *Deliverer {
	if client == nil {
		client = &http.Client{Timeout: defaultHTTPTimeout}
	}
	return &Deliverer{Repo: repo, Client: client, Logger: logger}
}

// NewHandlerFunc returns the asynq handler for TaskDeliver, mirroring
// jobs.Worker.NewHandlerFunc: decode the payload, then Handle the delivery id.
func (d *Deliverer) NewHandlerFunc() func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		var payload DeliverTaskPayload
		if err := json.Unmarshal(t.Payload(), &payload); err != nil {
			return fmt.Errorf("webhooks: decode deliver payload: %w", err)
		}
		return d.Handle(ctx, payload.DeliveryID)
	}
}

// Handle delivers one webhook. Loads the delivery + its endpoint, signs the
// stored payload bytes, POSTs them, and records the result. A 2xx marks the
// delivery delivered and returns nil (asynq acks). Any non-2xx or transport
// error marks it failed and returns an error so asynq retries with backoff.
func (d *Deliverer) Handle(ctx context.Context, deliveryID string) error {
	delivery, err := d.Repo.GetDeliveryByID(ctx, deliveryID)
	if err != nil {
		d.log().Error("webhooks: load delivery", "delivery_id", deliveryID, "error", err)
		return err
	}
	endpoint, err := d.Repo.GetEndpointByID(ctx, delivery.WebhookEndpointID)
	if err != nil {
		d.log().Error("webhooks: load endpoint", "delivery_id", deliveryID, "endpoint_id", delivery.WebhookEndpointID, "error", err)
		return err
	}

	body := delivery.Payload
	signature := Sign(endpoint.Secret, body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.URL, bytes.NewReader(body))
	if err != nil {
		// A malformed stored URL is recorded as a failure; return the error so
		// asynq retries (the URL could be fixed via the config endpoint).
		return d.recordFailure(ctx, deliveryID, 0, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(headerEvent, delivery.EventType)
	req.Header.Set(headerSignature, signature)

	resp, err := d.Client.Do(req)
	if err != nil {
		return d.recordFailure(ctx, deliveryID, 0, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if err := d.Repo.MarkDeliveryResult(ctx, DeliveryResult{
			DeliveryID: deliveryID,
			Status:     StatusDelivered,
			HTTPStatus: resp.StatusCode,
			Err:        "",
		}); err != nil {
			d.log().Error("webhooks: mark delivered", "delivery_id", deliveryID, "error", err)
			return err
		}
		return nil
	}

	return d.recordFailure(ctx, deliveryID, resp.StatusCode, fmt.Errorf("non-2xx response: %d", resp.StatusCode))
}

// recordFailure marks the delivery failed (with the HTTP status and error) and
// returns an error so asynq retries with backoff. A failure recording the
// result is itself returned so the task is retried.
func (d *Deliverer) recordFailure(ctx context.Context, deliveryID string, httpStatus int, cause error) error {
	d.log().Warn("webhooks: delivery failed", "delivery_id", deliveryID, "http_status", httpStatus, "error", cause.Error())
	if err := d.Repo.MarkDeliveryResult(ctx, DeliveryResult{
		DeliveryID: deliveryID,
		Status:     StatusFailed,
		HTTPStatus: httpStatus,
		Err:        cause.Error(),
	}); err != nil {
		d.log().Error("webhooks: mark failed", "delivery_id", deliveryID, "error", err)
		return err
	}
	return cause
}

func (d *Deliverer) log() *slog.Logger {
	if d == nil || d.Logger == nil {
		return slog.Default()
	}
	return d.Logger
}
