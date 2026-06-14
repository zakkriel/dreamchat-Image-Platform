-- Phase 7C-4 (2/2): Outbound webhooks (MVP-tight).
--
-- Additive only. This is the FIRST deliberate table growth since Phase 6A3
-- (table count 18 -> 20). Every migration since 6A3 was either a seed, a
-- column add, or an index — webhooks genuinely need two new persistent tables:
--
--   webhook_endpoints   -> one signing endpoint config per tenant. The signing
--                          secret is server-generated and lives here so the
--                          deliverer can recompute the HMAC on each retry
--                          without the caller re-supplying it.
--   webhook_deliveries  -> a per-event delivery-attempt log. Each row is the
--                          durable record the asynq `webhook:deliver` task
--                          re-reads on every attempt (the queue payload only
--                          carries the delivery id), and the audit trail of
--                          what we tried to send, how many times, and the last
--                          transport/HTTP result.
--
-- MVP scope (deliberately small — see the Phase 7C-4 plan): exactly one active
-- endpoint per tenant, HMAC-SHA256 signing, three event types, bounded
-- asynq retry/backoff, and this delivery log. No subscription management, no
-- dead-letter queue, no event filtering, no multiple endpoints, no signature
-- rotation endpoint.
--
-- Conventions follow 0001_initial.up.sql: TEXT ids minted at the API layer,
-- TIMESTAMPTZ timestamps, enum-shaped columns guarded by CHECK (val IN (...)),
-- tenant_id required on every tenant-scoped row, indexes at the bottom of each
-- table block. 0001 does NOT use IF NOT EXISTS on its CREATE TABLE/INDEX
-- statements, so neither do these (the one exception in the tree, 0008, is an
-- index on a pre-existing table); these are brand-new objects.

-- ---------------------------------------------------------------------------
-- webhook_endpoints — one active outbound webhook config per tenant.
-- ---------------------------------------------------------------------------
CREATE TABLE webhook_endpoints (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    url TEXT NOT NULL,
    secret TEXT NOT NULL,                 -- HMAC-SHA256 signing secret (server-generated)
    is_active BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- One active endpoint per tenant (MVP: a single config per tenant). The
-- partial unique index lets a tenant's prior endpoint be deactivated and a new
-- one created without colliding on tenant_id alone.
CREATE UNIQUE INDEX uq_webhook_endpoints_active_tenant ON webhook_endpoints(tenant_id) WHERE is_active = true;

-- ---------------------------------------------------------------------------
-- webhook_deliveries — one row per emitted event; the asynq deliver task's
-- durable source of truth and the delivery-attempt audit log.
-- ---------------------------------------------------------------------------
CREATE TABLE webhook_deliveries (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    webhook_endpoint_id TEXT NOT NULL,
    event_type TEXT NOT NULL CHECK (event_type IN ('generation_job.preview_ready','generation_job.completed','generation_job.failed')),
    generation_job_id TEXT,
    payload JSONB NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending','delivered','failed')),
    attempt_count INT NOT NULL DEFAULT 0,
    last_http_status INT,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Tenant-scoped recent-first listing (the natural admin read pattern).
CREATE INDEX idx_webhook_deliveries_tenant ON webhook_deliveries(tenant_id, created_at DESC);
-- Look up all deliveries emitted for one generation job.
CREATE INDEX idx_webhook_deliveries_job ON webhook_deliveries(generation_job_id);
