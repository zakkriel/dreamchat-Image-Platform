package jobs

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/hibiken/asynq"
)

const (
	// TaskGenerateArtifact is the asynq task name for the single
	// `POST /v1/artifacts/{id}/generate` flow Phase 3 implements.
	TaskGenerateArtifact = "image:generate_artifact"

	// TaskGeneratePack is the asynq task name for the generate-pack flows
	// (Phase 5A). One task per pack job; the worker fans out per variant.
	TaskGeneratePack = "image:generate_pack"

	// MaxAttempts caps asynq's retry count for generation tasks. Worker
	// callers rely on it to know when to set retryable=false on the final
	// failure.
	MaxAttempts = 3

	// MaxBillableCallsPerUnit bounds the lifetime provider calls a job may bill
	// per billable unit its cost reservation priced. Without a cap a job bills
	// MaxAttempts x (1 + fallback routes) full-price calls against a reservation
	// priced for the planned unit count: generateWithFallback walks the primary
	// plus every persisted same-price fallback, and asynq re-runs the whole walk
	// on every attempt. With bfl and fal both seeded at $0.0400 the single-image
	// worst case is 6 billable calls, $0.24, against a $0.04 reservation.
	//
	// It is per UNIT, not per job, because the reservation itself is
	// cells x phases units (service.go worstCaseBillableUnits): a six-role pack
	// legitimately needs six provider calls, so a flat per-job cap would deliver
	// half a pack the caller already paid for. The count is read from the
	// persisted provider_attempts rows, so it spans asynq attempts rather than
	// resetting with each one.
	MaxBillableCallsPerUnit = 3
)

// TaskPayload is the on-the-wire payload for every generation task. The worker
// re-reads the job from Postgres on each attempt, while CostReservationID binds
// the task to the reservation created for that execution. This prevents a
// delayed task from a failed run from claiming a later admin retry that reused
// the same generation job ID.
type TaskPayload struct {
	JobID             string `json:"job_id"`
	CostReservationID string `json:"cost_reservation_id,omitempty"`
}

// Enqueuer enqueues asynq tasks. The interface is small so the API layer can
// be stubbed without a real Redis.
type Enqueuer interface {
	EnqueueGenerateArtifact(ctx context.Context, jobID string) error
	EnqueueGeneratePack(ctx context.Context, jobID string) error
	Close() error
}

// ReservationAwareEnqueuer is the production extension used to include the
// reservation/run token in a task payload. Older lightweight enqueuers may
// implement only Enqueuer; callers retain that compatibility for tests and
// integrations that do not model the queue payload.
type ReservationAwareEnqueuer interface {
	EnqueueGenerateArtifactForReservation(ctx context.Context, jobID, reservationID string) error
	EnqueueGeneratePackForReservation(ctx context.Context, jobID, reservationID string) error
}

type asynqEnqueuer struct {
	client *asynq.Client
}

func NewEnqueuer(addr, password string) Enqueuer {
	client := asynq.NewClient(asynq.RedisClientOpt{
		Addr:     addr,
		Password: password,
	})
	return &asynqEnqueuer{client: client}
}

func (e *asynqEnqueuer) EnqueueGenerateArtifact(ctx context.Context, jobID string) error {
	return e.enqueue(ctx, TaskGenerateArtifact, jobID, "")
}

func (e *asynqEnqueuer) EnqueueGeneratePack(ctx context.Context, jobID string) error {
	return e.enqueue(ctx, TaskGeneratePack, jobID, "")
}

func (e *asynqEnqueuer) EnqueueGenerateArtifactForReservation(ctx context.Context, jobID, reservationID string) error {
	return e.enqueue(ctx, TaskGenerateArtifact, jobID, reservationID)
}

func (e *asynqEnqueuer) EnqueueGeneratePackForReservation(ctx context.Context, jobID, reservationID string) error {
	return e.enqueue(ctx, TaskGeneratePack, jobID, reservationID)
}

func (e *asynqEnqueuer) enqueue(ctx context.Context, taskName, jobID, reservationID string) error {
	payload, err := json.Marshal(TaskPayload{JobID: jobID, CostReservationID: reservationID})
	if err != nil {
		return err
	}
	if e == nil || e.client == nil {
		return errors.New("jobs: enqueuer not initialized")
	}
	_, err = e.client.EnqueueContext(ctx, asynq.NewTask(taskName, payload), asynq.MaxRetry(MaxAttempts-1))
	return err
}

func (e *asynqEnqueuer) Close() error {
	if e == nil || e.client == nil {
		return nil
	}
	return e.client.Close()
}
