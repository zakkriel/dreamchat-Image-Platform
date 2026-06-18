-- +goose Up
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

-- ---------------------------------------------------------------------------
-- Tenant isolation (Phase 7C-3 continuity).
--
-- Both new tables are directly tenant-scoped (tenant_id column), so they get
-- the SAME ENABLE + FORCE ROW LEVEL SECURITY and the SAME canonical text-safe,
-- deny-by-default tenant_isolation policy as the Phase 7C-3 direct tenant tables
-- (migrations/0009). Without this, the just-added RLS hardening would have a
-- gap on the platform's newest tenant data.
--
--   * The CONFIG path (PUT/GET /v1/admin/webhook-endpoint) runs on the
--     RLS-enforced tenant pool and sets app.current_tenant per transaction
--     (internal/webhooks/repository.go via internal/db.WithTenant), so these
--     policies actively gate it.
--   * The WORKER path (emitter + deliverer) runs on the BYPASSRLS system role,
--     exactly like the rest of the worker under 7C-3 — it bypasses the policy
--     but still scopes by explicit tenant_id / by-id predicates.
--
-- DML grants are made EXPLICIT below rather than relying only on the
-- ALTER DEFAULT PRIVILEGES that migration 0009 set for image_platform_api /
-- image_platform_system. Default privileges only apply to objects created by
-- the SAME role that ran the ALTER DEFAULT PRIVILEGES; in production the
-- migration owner may differ from the role that ran 0009, in which case these
-- tables would silently lack grants. Explicit grants make this production-
-- control PR robust to migration ownership.
-- ---------------------------------------------------------------------------
-- +goose StatementBegin
DO $$
DECLARE
  t text;
  webhook_tables text[] := ARRAY['webhook_endpoints', 'webhook_deliveries'];
BEGIN
  FOREACH t IN ARRAY webhook_tables LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON %I', t);
    EXECUTE format($f$
      CREATE POLICY tenant_isolation ON %I
        USING (tenant_id = NULLIF(current_setting('app.current_tenant', true), ''))
        WITH CHECK (tenant_id = NULLIF(current_setting('app.current_tenant', true), ''))
    $f$, t);
  END LOOP;
END $$;
-- +goose StatementEnd

-- Explicit table privileges for both roles (see the note above). This does NOT
-- weaken RLS: image_platform_api still has no BYPASSRLS, so it remains fully
-- subject to the tenant_isolation policy above; the grant only gives it the DML
-- privilege the policy then scopes. image_platform_system (BYPASSRLS) needs the
-- grant so the worker can emit/deliver webhooks (BYPASSRLS bypasses POLICIES,
-- not GRANTS).
GRANT SELECT, INSERT, UPDATE, DELETE
ON webhook_endpoints, webhook_deliveries
TO image_platform_api, image_platform_system;

-- +goose Down
-- Baseline migration: irreversible floor. Roll back by restoring from backup.
SELECT 'baseline migration 0010 is irreversible' WHERE false;
