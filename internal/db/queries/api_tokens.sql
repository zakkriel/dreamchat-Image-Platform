-- name: GetActiveAPITokenByPrefix :one
SELECT id, tenant_id, token_hash, scopes, environment, status, expires_at,
       rate_limit_rpm, rate_limit_rph, max_concurrent_jobs
FROM api_tokens
WHERE token_prefix = $1;

-- name: TouchAPITokenLastUsed :exec
UPDATE api_tokens SET last_used_at = now() WHERE id = $1;
