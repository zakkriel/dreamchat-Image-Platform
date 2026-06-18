-- +goose Up
-- 0009_rls_tenant_isolation
--
-- Phase 7C-3: RLS / tenant isolation hardening (PR #22).
--
-- Until now multi-tenant isolation has been application-level only: every
-- query author had to remember to include `WHERE tenant_id = $1`. A single
-- missing or wrong predicate in any current or future query could leak jobs,
-- assets, identities, budgets, packs, tokens, or cost data across tenants.
--
-- This migration makes the DATABASE enforce tenant isolation as defense in
-- depth. It does NOT replace the app-level predicates (those remain); it adds
-- a Postgres-enforced safety net underneath them.
--
-- Design (see PHASE7C3_CONFIDENCE_INDEX.md for the full rationale):
--
--   * tenant_id is TEXT in this repo (e.g. 'tenant_it_jobs'), NOT uuid. The
--     canonical policy compares text and MUST NOT cast to uuid — a uuid cast
--     would raise at runtime on these ids.
--
--   * The canonical predicate is:
--
--         tenant_id = NULLIF(current_setting('app.current_tenant', true), '')
--
--     This is DENY BY DEFAULT: when the `app.current_tenant` GUC is unset or
--     empty it becomes NULL, and `tenant_id = NULL` matches no rows. A request
--     that never set the GUC therefore sees zero tenant-owned rows.
--
--   * Tenant-scoped DB work sets the GUC transaction-locally:
--
--         SELECT set_config('app.current_tenant', $1, true);   -- third arg true
--
--     so the setting never leaks across pooled connections.
--
--   * Two non-owner roles split request-path vs system access:
--
--         image_platform_api      LOGIN, no BYPASSRLS  -> subject to RLS
--         image_platform_system   LOGIN, BYPASSRLS     -> system/worker/auth/admin
--
--     Table OWNERS normally bypass RLS, so we FORCE row level security on every
--     protected table to subject the owner to policies too. Superusers still
--     bypass RLS, which is why CI must prove enforcement under the non-superuser
--     image_platform_api role (owner/superuser-only checks are insufficient).
--
-- Table count stays 18 — this migration adds NO business table. It only adds
-- roles, grants, RLS enablement, and policies.

-- ---------------------------------------------------------------------------
-- 22.1 Roles
--
-- Role creation is cluster-scoped, so guard it (CREATE ROLE is not idempotent).
-- The passwords here are DEV/CI defaults only; deployments must override them
-- with real secrets (e.g. ALTER ROLE ... PASSWORD, or managed-secret rotation),
-- exactly like API_TOKEN_PEPPER=dev-pepper-change-me. Do not ship these.
-- ---------------------------------------------------------------------------

-- image_platform_api: the RLS-enforced API tenant role. Non-superuser,
-- non-owner, NO BYPASSRLS. Normal API request-path tenant work connects as this
-- role and is fully subject to the policies below.
-- +goose StatementBegin
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'image_platform_api') THEN
    CREATE ROLE image_platform_api LOGIN PASSWORD 'image_platform_api';
  END IF;
END $$;
-- +goose StatementEnd

-- image_platform_system: the system/bypass role. Non-superuser but with
-- BYPASSRLS, used only for legitimate cross-tenant / pre-tenant operations:
-- migrations, seed, the worker (job lookup by id before tenant is known), auth
-- token lookup + async last-used touch, system cost lifecycle, and explicit
-- admin cross-tenant endpoints after an admin:* scope check.
-- +goose StatementBegin
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'image_platform_system') THEN
    CREATE ROLE image_platform_system LOGIN PASSWORD 'image_platform_system';
  END IF;
END $$;
-- +goose StatementEnd

-- Explicit RLS bypass for the system role. Under FORCE ROW LEVEL SECURITY the
-- table owner is also subject to policies, so the system path cannot rely on
-- "being owner" — it needs an explicit bypass. (Granting BYPASSRLS requires a
-- superuser, which the migration runs as in local/CI; in production run this as
-- a role-admin/superuser.)
ALTER ROLE image_platform_system BYPASSRLS;

-- ---------------------------------------------------------------------------
-- Grants
--
-- BYPASSRLS bypasses POLICIES, not GRANTS — the system role still needs table
-- privileges. Grant both roles the DML they need on the app tables/sequences,
-- plus default privileges so tables created by later migrations are covered.
-- ---------------------------------------------------------------------------
GRANT USAGE ON SCHEMA public TO image_platform_api, image_platform_system;

GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public
  TO image_platform_api, image_platform_system;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public
  TO image_platform_api, image_platform_system;

ALTER DEFAULT PRIVILEGES IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO image_platform_api, image_platform_system;
ALTER DEFAULT PRIVILEGES IN SCHEMA public
  GRANT USAGE, SELECT ON SEQUENCES TO image_platform_api, image_platform_system;

-- ---------------------------------------------------------------------------
-- 22.4 Direct tenant tables
--
-- Every directly tenant-scoped table (has a tenant_id column) gets ENABLE +
-- FORCE RLS and the canonical text-safe, deny-by-default tenant_isolation
-- policy. audit_events is included: it carries a (nullable, for global events)
-- tenant_id, so the same policy protects its tenant rows; its global NULL-tenant
-- rows are reachable only via the BYPASSRLS system role (the admin/system audit
-- surface), which is correct.
-- ---------------------------------------------------------------------------

-- +goose StatementBegin
DO $$
DECLARE
  t text;
  direct_tables text[] := ARRAY[
    'api_tokens',
    'style_profiles',
    'visual_identities',
    'visual_assets',
    'generation_jobs',
    'asset_packs',
    'cost_budgets',
    'cost_reservations',
    'generation_cost_events',
    'audit_events'
  ];
BEGIN
  FOREACH t IN ARRAY direct_tables LOOP
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

-- ---------------------------------------------------------------------------
-- Child tables (transitive tenant ownership)
--
-- These tables have no tenant_id of their own; ownership is via a parent FK.
-- We use a parent-join EXISTS policy: a child row is visible/writable only when
-- its parent row belongs to the current tenant GUC. This is also deny-by-default
-- — when the GUC is unset the parent comparison is against NULL, so the EXISTS
-- finds nothing and zero child rows are visible. No denormalized tenant_id
-- column is added (table count stays 18, and sqlc-generated models are
-- unchanged).
--
--   table                          | parent             | join column
--   -------------------------------|--------------------|-----------------------
--   visual_identity_versions       | visual_identities  | visual_identity_id
--   asset_pack_items               | asset_packs        | asset_pack_id
--   provider_attempts              | generation_jobs    | generation_job_id
--   idempotency_keys               | api_tokens         | token_id
--   cost_reservation_budget_holds  | cost_reservations  | cost_reservation_id
-- ---------------------------------------------------------------------------

-- +goose StatementBegin
DO $$
DECLARE
  rec record;
  child_tables text[][] := ARRAY[
    ARRAY['visual_identity_versions', 'visual_identities', 'visual_identity_id'],
    ARRAY['asset_pack_items',         'asset_packs',       'asset_pack_id'],
    ARRAY['provider_attempts',        'generation_jobs',   'generation_job_id'],
    ARRAY['idempotency_keys',         'api_tokens',        'token_id'],
    ARRAY['cost_reservation_budget_holds', 'cost_reservations', 'cost_reservation_id']
  ];
  i int;
  child text;
  parent text;
  joincol text;
BEGIN
  FOR i IN 1 .. array_length(child_tables, 1) LOOP
    child   := child_tables[i][1];
    parent  := child_tables[i][2];
    joincol := child_tables[i][3];
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', child);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', child);
    EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON %I', child);
    EXECUTE format($f$
      CREATE POLICY tenant_isolation ON %1$I
        USING (EXISTS (
          SELECT 1 FROM %2$I p
          WHERE p.id = %1$I.%3$I
            AND p.tenant_id = NULLIF(current_setting('app.current_tenant', true), '')
        ))
        WITH CHECK (EXISTS (
          SELECT 1 FROM %2$I p
          WHERE p.id = %1$I.%3$I
            AND p.tenant_id = NULLIF(current_setting('app.current_tenant', true), '')
        ))
    $f$, child, parent, joincol);
  END LOOP;
END $$;
-- +goose StatementEnd

-- ---------------------------------------------------------------------------
-- Global reference tables — intentionally NOT protected.
--
--   provider_models, provider_routes, provider_model_prices
--
-- These are global configuration/reference data consulted by route resolution
-- and pricing for every tenant; they carry no tenant_id and must remain readable
-- regardless of the tenant GUC. RLS is deliberately NOT enabled on them.
-- ---------------------------------------------------------------------------

-- +goose Down
-- Baseline migration: irreversible floor. Roll back by restoring from backup.
SELECT 'baseline migration 0009 is irreversible' WHERE false;
