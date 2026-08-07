-- name: GetIdempotencyKey :one
-- Live-record lookup: an expired row is treated as absent so a stale key can
-- never replay past its TTL (the row itself is reclaimed by the insert's
-- expired-takeover below, or by DeleteExpiredIdempotencyKeys).
SELECT id, token_id, key, endpoint, request_hash, generation_job_id,
       expires_at, created_at
FROM idempotency_keys
WHERE token_id = $1
  AND key = $2
  AND expires_at > now();

-- name: InsertIdempotencyKey :one
-- First-writer-wins on a LIVE (token_id, key) record. A conflicting row that
-- has EXPIRED is taken over in place (updated to the new request's record)
-- instead of blocking the insert forever: without the takeover an expired row
-- would be invisible to GetIdempotencyKey yet still win every conflict, making
-- the key permanently unusable. A conflicting LIVE row leaves the DO UPDATE's
-- WHERE false → no row returned → the caller replays the existing record.
INSERT INTO idempotency_keys (
    id, token_id, key, endpoint, request_hash,
    generation_job_id, expires_at
) VALUES (
    $1, $2, $3, $4, $5,
    $6, $7
)
ON CONFLICT (token_id, key) DO UPDATE SET
    id = EXCLUDED.id,
    endpoint = EXCLUDED.endpoint,
    request_hash = EXCLUDED.request_hash,
    generation_job_id = EXCLUDED.generation_job_id,
    expires_at = EXCLUDED.expires_at,
    created_at = now()
WHERE idempotency_keys.expires_at <= now()
RETURNING id, token_id, key, endpoint, request_hash, generation_job_id,
          expires_at, created_at;

-- name: DeleteExpiredIdempotencyKeys :exec
DELETE FROM idempotency_keys
WHERE expires_at < now();
