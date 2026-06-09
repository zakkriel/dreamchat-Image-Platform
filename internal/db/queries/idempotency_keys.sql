-- name: GetIdempotencyKey :one
SELECT id, token_id, key, endpoint, request_hash, generation_job_id,
       expires_at, created_at
FROM idempotency_keys
WHERE token_id = $1
  AND key = $2;

-- name: InsertIdempotencyKey :one
INSERT INTO idempotency_keys (
    id, token_id, key, endpoint, request_hash,
    generation_job_id, expires_at
) VALUES (
    $1, $2, $3, $4, $5,
    $6, $7
)
ON CONFLICT (token_id, key) DO NOTHING
RETURNING id, token_id, key, endpoint, request_hash, generation_job_id,
          expires_at, created_at;

-- name: DeleteExpiredIdempotencyKeys :exec
DELETE FROM idempotency_keys
WHERE expires_at < now();
