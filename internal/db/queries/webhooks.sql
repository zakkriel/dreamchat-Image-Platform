-- Phase 7C-4 (2/2): outbound webhooks. Queries for the per-tenant endpoint
-- config and the per-event delivery-attempt log.

-- name: GetActiveWebhookEndpointByTenant :one
-- The tenant's single active webhook endpoint (the partial unique index
-- guarantees at most one). The repository uses this both to serve the GET
-- config read and to decide insert-vs-update on the PUT upsert.
SELECT id, tenant_id, url, secret, is_active, created_at, updated_at
FROM webhook_endpoints
WHERE tenant_id = $1
  AND is_active = true;

-- name: InsertWebhookEndpoint :one
-- Create a tenant's active endpoint. The signing secret is server-generated
-- and passed in once here; it is never rewritten on a later URL update (see
-- UpdateWebhookEndpointURL), so a caller that has stored the secret keeps a
-- stable signing key across URL changes.
INSERT INTO webhook_endpoints (
    id, tenant_id, url, secret
) VALUES (
    $1, $2, $3, $4
)
RETURNING id, tenant_id, url, secret, is_active, created_at, updated_at;

-- name: UpdateWebhookEndpointURL :one
-- Change the URL of the tenant's active endpoint, preserving the existing
-- signing secret. Used by the upsert when a Get already found an active row.
UPDATE webhook_endpoints
SET url = $2,
    updated_at = now()
WHERE id = $1
  AND is_active = true
RETURNING id, tenant_id, url, secret, is_active, created_at, updated_at;

-- name: InsertWebhookDelivery :one
-- Record a freshly emitted event (status = pending, attempt_count = 0). The
-- asynq deliver task re-reads this row by id on every attempt; the queue
-- payload only carries the id.
INSERT INTO webhook_deliveries (
    id, tenant_id, webhook_endpoint_id, event_type, generation_job_id, payload, status, attempt_count
) VALUES (
    $1, $2, $3, $4, $5, $6, 'pending', 0
)
RETURNING id, tenant_id, webhook_endpoint_id, event_type, generation_job_id,
          payload, status, attempt_count, last_http_status, last_error,
          created_at, updated_at;

-- name: GetWebhookDeliveryByID :one
-- Load one delivery row for the deliver task. Joined with the endpoint by the
-- repository (a second query) so the deliverer has the URL + secret to sign.
SELECT id, tenant_id, webhook_endpoint_id, event_type, generation_job_id,
       payload, status, attempt_count, last_http_status, last_error,
       created_at, updated_at
FROM webhook_deliveries
WHERE id = $1;

-- name: GetWebhookEndpointByID :one
-- The endpoint a delivery row targets (the deliverer needs url + secret). Read
-- by id (the delivery carries webhook_endpoint_id) so a later deactivation of
-- the tenant's active endpoint does not strand in-flight deliveries.
SELECT id, tenant_id, url, secret, is_active, created_at, updated_at
FROM webhook_endpoints
WHERE id = $1;

-- name: MarkWebhookDeliveryResult :exec
-- Record the outcome of one delivery attempt: set the terminal/in-progress
-- status, bump attempt_count, and stamp the last HTTP status + error. Called
-- once per asynq attempt (delivered on 2xx, failed otherwise).
UPDATE webhook_deliveries
SET status = $2,
    attempt_count = attempt_count + 1,
    last_http_status = $3,
    last_error = $4,
    updated_at = now()
WHERE id = $1;
