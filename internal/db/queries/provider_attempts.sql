-- name: InsertProviderAttempt :one
INSERT INTO provider_attempts (
    id, generation_job_id, provider_id, attempt_number, status
) VALUES (
    $1, $2, $3, $4, 'started'
)
RETURNING id, generation_job_id, provider_id, model_id, provider_route_id,
          provider_request_id, attempt_number, status,
          error_code, error_message, request_payload_hash,
          started_at, completed_at, latency_ms,
          estimated_cost, actual_cost, currency, created_at;

-- name: MarkProviderAttemptSucceeded :exec
UPDATE provider_attempts
SET status = 'succeeded',
    completed_at = now(),
    latency_ms = $2
WHERE id = $1;

-- name: MarkProviderAttemptFailed :exec
UPDATE provider_attempts
SET status = 'failed',
    error_code = $2,
    error_message = $3,
    completed_at = now(),
    latency_ms = $4
WHERE id = $1;

-- name: CountProviderAttemptsForJob :one
SELECT count(*)::int AS attempt_count
FROM provider_attempts
WHERE generation_job_id = $1;
