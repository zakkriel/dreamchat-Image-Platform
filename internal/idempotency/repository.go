// Package idempotency holds the Idempotency-Key header constant used by
// generation endpoints. The transactional first-writer-wins flow lives in
// internal/jobs.Service so the idempotency_keys insert and the
// generation_jobs insert share a single transaction; per-table repositories
// for the idempotency_keys table land alongside the sweep / admin code that
// will need them.
package idempotency

import "time"

// HeaderKey is the request header callers send to opt in to idempotent
// replay. Re-exported here so handlers don't have to import internal/jobs.
const HeaderKey = "Idempotency-Key"

// TTL is the storage retention for an idempotency record. Matches the
// `docs/api/idempotency.md` recommendation for generation requests.
const TTL = 24 * time.Hour
