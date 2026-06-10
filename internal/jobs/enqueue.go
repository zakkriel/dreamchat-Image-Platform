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
)

// TaskPayload is the on-the-wire payload for every generation task. The
// worker re-reads the job from Postgres on each attempt so we keep the queue
// payload tiny.
type TaskPayload struct {
	JobID string `json:"job_id"`
}

// Enqueuer enqueues asynq tasks. The interface is small so the API layer can
// be stubbed without a real Redis.
type Enqueuer interface {
	EnqueueGenerateArtifact(ctx context.Context, jobID string) error
	EnqueueGeneratePack(ctx context.Context, jobID string) error
	Close() error
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
	return e.enqueue(ctx, TaskGenerateArtifact, jobID)
}

func (e *asynqEnqueuer) EnqueueGeneratePack(ctx context.Context, jobID string) error {
	return e.enqueue(ctx, TaskGeneratePack, jobID)
}

func (e *asynqEnqueuer) enqueue(ctx context.Context, taskName, jobID string) error {
	payload, err := json.Marshal(TaskPayload{JobID: jobID})
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
