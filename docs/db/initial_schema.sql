-- DreamChat Image Platform — initial schema (v0.5, matching OpenAPI v0.5.0)
--
-- This is a starting point for the first migration, not a frozen production
-- schema. It is kept in sync with:
--
--   docs/api/openapi.yaml                    (v0.5.0 — types and enums)
--   docs/architecture/data-model.md          (entity-level descriptions)
--   docs/architecture/admin-control-surface.md
--   docs/architecture/cost-control.md        (price book + budgets + reservations)
--   docs/architecture/variant-compatibility-matrix.md
--   prds/schemas/image_platform_data_model.json
--
-- Conventions
-- -----------
-- - IDs are TEXT (matches OpenAPI string IDs; allows opaque slug-style keys).
-- - Money is NUMERIC(14,4) plus a separate currency column (default USD).
-- - Timestamps are TIMESTAMPTZ.
-- - Enum-shaped columns use CHECK (val IN (...)). Values must match the
--   enums in docs/api/openapi.yaml; when an enum value is added there, the
--   matching CHECK constraint here must be migrated.
-- - tenant_id is required on every tenant-scoped row (resolved from the
--   bearer token at the API boundary per ADR-004 and
--   docs/api/authentication.md).
-- - Foreign keys use ON DELETE behavior conservative by default (no
--   cascading deletes; archival is preferred over destruction).
-- - Indexes intended to support known query patterns live at the bottom.
--
-- Canonical enums and where each lives
-- ------------------------------------
-- OwnerType                  character | place | artifact
-- AssetType                  character_portrait | place_scene | artifact | expression | angle_variant
-- AssetStatus                pending | preview_ready | ready | failed | archived
-- StyleMode                  open_prompt | preset_style | creator_style | provider_native
-- ProviderCapability         draft_only | scene_capable | identity_capable | pack_capable | production_capable
-- PreviewCapability          true_preview | derived_preview | no_preview
-- QualityTier                draft | standard | high
-- LatencyTier                fast | balanced | quality
-- GenerationJobStatus        queued | running | preview_ready | completed | failed | cancelled
-- ProviderModelStatus        active | degraded | disabled
-- PackType                   character_minimal_portrait_pack | character_expression_pack |
--                            character_full_reference_pack | place_minimal_scene_pack |
--                            place_time_of_day_pack | place_state_pack |
--                            artifact_card_single | artifact_document_preview |
--                            artifact_icon_and_closeup
-- PackStatus                 planned | in_progress | preview_ready | completed |
--                            completed_with_warnings | failed
-- ProviderAttemptStatus      started | succeeded | failed | timed_out | cancelled
-- PriceOperationType         text_to_image | image_to_image | upscale | variant_pack | edit
-- PriceUnitType              image | megapixel | second | credit | request
-- BudgetScopeType            tenant | token | world | user
-- BudgetPeriod               daily | monthly
-- BudgetStatus               active | paused | exceeded
-- CostReservationStatus      reserved | committed | released | failed
-- TokenEnvironment           dev | test | live
-- TokenStatus                active | revoked
-- IdentityStatus             active | archived
--
-- ---------------------------------------------------------------------------
-- Tables
-- ---------------------------------------------------------------------------

-- API tokens (ADR-004, ADR-005). Token raw secret is never stored; only the
-- non-secret prefix and a hash of the secret portion (HMAC-SHA256 + pepper
-- or Argon2id, per docs/architecture/security-and-auth.md).
CREATE TABLE api_tokens (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    token_prefix TEXT NOT NULL UNIQUE,
    token_hash TEXT NOT NULL,
    name TEXT NOT NULL,
    owner_type TEXT NOT NULL,
    owner_id TEXT,
    scopes TEXT[] NOT NULL DEFAULT '{}',
    environment TEXT NOT NULL CHECK (environment IN ('dev', 'test', 'live')),
    status TEXT NOT NULL CHECK (status IN ('active', 'revoked')),
    expires_at TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Style profiles (PRD 02 §6.9, OpenAPI StyleProfile).
CREATE TABLE style_profiles (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    world_id TEXT,                                   -- NULL for tenant-wide / global presets
    name TEXT NOT NULL,
    style_mode TEXT NOT NULL CHECK (style_mode IN ('open_prompt', 'preset_style', 'creator_style', 'provider_native')),
    style_profile_version INT NOT NULL DEFAULT 1,
    positive_prompt TEXT NOT NULL,
    negative_prompt TEXT,
    loras TEXT[] NOT NULL DEFAULT '{}',
    reference_style_asset_ids TEXT[] NOT NULL DEFAULT '{}',
    default_quality_tier TEXT NOT NULL DEFAULT 'standard' CHECK (default_quality_tier IN ('draft', 'standard', 'high')),
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    metadata JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Provider models (ADR-007). One row per (provider × model) the platform
-- can route to. preview_capability gates preview-first UX (ADR-010, PRD 06 §3.0).
CREATE TABLE provider_models (
    id TEXT PRIMARY KEY,
    provider_id TEXT NOT NULL,
    model_name TEXT NOT NULL,
    display_name TEXT NOT NULL,
    capabilities TEXT[] NOT NULL DEFAULT '{}',       -- subset of ProviderCapability values
    preview_capability TEXT NOT NULL CHECK (preview_capability IN ('true_preview', 'derived_preview', 'no_preview')),
    supports_high_res BOOLEAN NOT NULL DEFAULT true,
    max_batch_size INT,
    supported_aspect_ratios TEXT[] NOT NULL DEFAULT '{}',
    status TEXT NOT NULL CHECK (status IN ('active', 'degraded', 'disabled')),
    metadata JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Provider routes (admin-control-surface, ADR-007). A route is the
-- (provider_model × operation_type × tier) combination the router consults.
-- Routes can be disabled independently of the underlying provider model.
CREATE TABLE provider_routes (
    id TEXT PRIMARY KEY,
    provider_id TEXT NOT NULL,
    model_id TEXT NOT NULL REFERENCES provider_models(id),
    route_name TEXT,
    operation_type TEXT NOT NULL CHECK (operation_type IN ('text_to_image', 'image_to_image', 'upscale', 'variant_pack', 'edit')),
    required_capability TEXT NOT NULL CHECK (required_capability IN ('draft_only', 'scene_capable', 'identity_capable', 'pack_capable', 'production_capable')),
    preview_capability TEXT NOT NULL CHECK (preview_capability IN ('true_preview', 'derived_preview', 'no_preview')),
    quality_tier TEXT NOT NULL CHECK (quality_tier IN ('draft', 'standard', 'high')),
    latency_tier TEXT NOT NULL CHECK (latency_tier IN ('fast', 'balanced', 'quality')),
    is_enabled BOOLEAN NOT NULL DEFAULT true,
    priority INT NOT NULL DEFAULT 100,               -- lower = preferred
    weight INT NOT NULL DEFAULT 1,                   -- relative weight for weighted-random within ties
    max_concurrent_jobs INT,                         -- per-route concurrency cap (NULL = unbounded)
    allow_unpriced_provider BOOLEAN NOT NULL DEFAULT false,  -- cost-control §4.1 escape hatch; admin-only
    disabled_reason TEXT,
    disabled_by_token_id TEXT REFERENCES api_tokens(id),
    disabled_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Visual identities (PRD 03). One row per (world × owner_type × owner_id);
-- canonical_visual_traits is loose-schema per PRD 03 §4.2/§5.2.
CREATE TABLE visual_identities (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    world_id TEXT NOT NULL,
    owner_type TEXT NOT NULL CHECK (owner_type IN ('character', 'place', 'artifact')),
    owner_id TEXT NOT NULL,
    display_name TEXT NOT NULL,
    canonical_visual_traits JSONB NOT NULL DEFAULT '{}',
    allowed_variation JSONB NOT NULL DEFAULT '{}',
    forbidden_drift JSONB NOT NULL DEFAULT '{}',
    style_profile_id TEXT NOT NULL REFERENCES style_profiles(id),
    consistency_key TEXT,
    identity_seed TEXT,
    anchor_asset_ids TEXT[] NOT NULL DEFAULT '{}',
    reference_asset_ids TEXT[] NOT NULL DEFAULT '{}',
    current_version INT NOT NULL DEFAULT 1,
    current_state_version INT NOT NULL DEFAULT 1,
    status TEXT NOT NULL CHECK (status IN ('active', 'archived')) DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, world_id, owner_type, owner_id)
);

-- Visual identity version history (data model: visual_identity_version).
CREATE TABLE visual_identity_versions (
    visual_identity_id TEXT NOT NULL REFERENCES visual_identities(id),
    version INT NOT NULL,
    reason TEXT NOT NULL CHECK (reason IN ('initial', 'canonical_change', 'style_migration', 'identity_correction', 'place_state_change')),
    canonical_traits_snapshot JSONB NOT NULL DEFAULT '{}',
    anchor_asset_ids TEXT[] NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (visual_identity_id, version)
);

-- Visual assets (PRD 05, variant-compatibility-matrix). The new fields after
-- the variant matrix landed are: variant_family, state_version,
-- compatibility_tags, fallback_allowed, fallback_rank, is_identity_anchor.
CREATE TABLE visual_assets (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    world_id TEXT NOT NULL,
    visual_identity_id TEXT REFERENCES visual_identities(id),
    asset_type TEXT NOT NULL CHECK (asset_type IN ('character_portrait', 'place_scene', 'artifact', 'expression', 'angle_variant')),
    variant_key TEXT NOT NULL,
    variant_family TEXT,                              -- variant-compatibility-matrix §6
    version INT NOT NULL DEFAULT 1,                   -- per-asset version (regenerations)
    state_version INT NOT NULL DEFAULT 1,             -- state version for the entity at generation time
    style_profile_id TEXT REFERENCES style_profiles(id),
    style_profile_version INT,
    quality_tier TEXT NOT NULL DEFAULT 'standard' CHECK (quality_tier IN ('draft', 'standard', 'high')),
    status TEXT NOT NULL CHECK (status IN ('pending', 'preview_ready', 'ready', 'failed', 'archived')),
    compatibility_tags TEXT[] NOT NULL DEFAULT '{}',  -- e.g. {generic_presence, preview_safe}
    fallback_allowed BOOLEAN NOT NULL DEFAULT false,  -- may serve as preview_fallback for other variants
    fallback_rank INT,                                -- lower = preferred fallback within tier
    is_identity_anchor BOOLEAN NOT NULL DEFAULT false,
    low_res_url TEXT,
    high_res_url TEXT,
    thumbnail_url TEXT,
    provider_id TEXT,
    model_id TEXT REFERENCES provider_models(id),
    provider_route_id TEXT REFERENCES provider_routes(id),
    prompt_hash TEXT,
    seed TEXT,
    reference_asset_ids TEXT[] NOT NULL DEFAULT '{}',
    generation_job_id TEXT,                           -- soft FK; jobs reference assets via array and vice versa
    metadata JSONB NOT NULL DEFAULT '{}',
    generated_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Generation jobs (ADR-006). The cost_reservation_id is the link into the
-- cost-control pipeline (docs/architecture/cost-control.md §3 step 8).
CREATE TABLE generation_jobs (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    world_id TEXT,
    job_type TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'preview_ready', 'completed', 'failed', 'cancelled')),
    requested_by_token_id TEXT REFERENCES api_tokens(id),
    visual_identity_id TEXT REFERENCES visual_identities(id),
    asset_pack_id TEXT,                               -- set when this job created/extended a pack
    input_payload JSONB NOT NULL DEFAULT '{}',
    requested_outputs TEXT[] NOT NULL DEFAULT '{}',
    fallback_policy TEXT CHECK (fallback_policy IN ('none', 'compatible_only', 'preview_allowed', 'any_existing')),
    cache_result TEXT CHECK (cache_result IN ('exact_match', 'compatible_match', 'preview_fallback', 'generated_required')),
    preview_asset_ids TEXT[] NOT NULL DEFAULT '{}',
    final_asset_ids TEXT[] NOT NULL DEFAULT '{}',
    error_code TEXT,
    error_message TEXT,
    retryable BOOLEAN,
    cost_reservation_id TEXT,                         -- FK added after cost_reservations exists; see ALTER below
    cost_estimate_usd NUMERIC(14, 4),
    actual_cost_usd NUMERIC(14, 4),
    queue_duration_ms INT,
    generation_duration_ms INT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ
);

-- Asset packs (PRD 04). A pack groups assets (character starter, place
-- starter, artifact set, custom). Pack items live in asset_pack_items.
CREATE TABLE asset_packs (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    world_id TEXT NOT NULL,
    visual_identity_id TEXT REFERENCES visual_identities(id),  -- nullable for ad-hoc artifact sets
    pack_type TEXT NOT NULL,                          -- PRD 04 names; see canonical PackType list above
    style_profile_id TEXT NOT NULL REFERENCES style_profiles(id),
    style_profile_version INT,
    visual_identity_version INT,
    quality_tier TEXT NOT NULL DEFAULT 'standard' CHECK (quality_tier IN ('draft', 'standard', 'high')),
    status TEXT NOT NULL CHECK (status IN ('planned', 'in_progress', 'preview_ready', 'completed', 'completed_with_warnings', 'failed')),
    created_by_job_id TEXT,                           -- soft ref into generation_jobs
    created_by_token_id TEXT REFERENCES api_tokens(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE asset_pack_items (
    id TEXT PRIMARY KEY,
    asset_pack_id TEXT NOT NULL REFERENCES asset_packs(id) ON DELETE CASCADE,
    visual_asset_id TEXT NOT NULL REFERENCES visual_assets(id),
    variant_key TEXT NOT NULL,
    sort_order INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (asset_pack_id, visual_asset_id),
    UNIQUE (asset_pack_id, variant_key)
);

-- Provider attempts (ADR-007). One row per provider call attempt for a job;
-- includes retries and fallbacks. Used by failed-jobs runbook and the
-- cost-spike runbook's reconciliation step.
CREATE TABLE provider_attempts (
    id TEXT PRIMARY KEY,
    generation_job_id TEXT NOT NULL REFERENCES generation_jobs(id),
    provider_id TEXT NOT NULL,
    model_id TEXT REFERENCES provider_models(id),
    provider_route_id TEXT REFERENCES provider_routes(id),
    provider_request_id TEXT,                          -- provider's own ID for the call, if returned
    attempt_number INT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('started', 'succeeded', 'failed', 'timed_out', 'cancelled')),
    error_code TEXT,
    error_message TEXT,
    request_payload_hash TEXT,
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ,
    latency_ms INT,
    estimated_cost NUMERIC(14, 4),
    actual_cost NUMERIC(14, 4),
    currency TEXT NOT NULL DEFAULT 'USD' CHECK (char_length(currency) = 3),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Provider model prices (docs/architecture/cost-control.md §2.1). Multiple
-- entries per (provider × model × operation_type) are expected; current is
-- selected by is_active = true. Price changes go via a new row, not editing
-- (so audit history is preserved).
CREATE TABLE provider_model_prices (
    id TEXT PRIMARY KEY,
    provider_id TEXT NOT NULL,
    model_id TEXT NOT NULL,
    operation_type TEXT NOT NULL CHECK (operation_type IN ('text_to_image', 'image_to_image', 'upscale', 'variant_pack', 'edit')),
    unit_type TEXT NOT NULL CHECK (unit_type IN ('image', 'megapixel', 'second', 'credit', 'request')),
    price_per_unit NUMERIC(14, 6) NOT NULL,            -- finer precision than budgets — per-unit can be sub-cent
    currency TEXT NOT NULL DEFAULT 'USD' CHECK (char_length(currency) = 3),
    effective_from TIMESTAMPTZ NOT NULL,
    effective_to TIMESTAMPTZ,                          -- NULL = current
    is_active BOOLEAN NOT NULL DEFAULT false,
    source TEXT,
    notes TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Cost budgets (docs/architecture/cost-control.md §2.2). reserved_amount and
-- spent_amount are computed and maintained by the cost-control pipeline; do
-- not write them from handlers directly.
CREATE TABLE cost_budgets (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    scope_type TEXT NOT NULL CHECK (scope_type IN ('tenant', 'token', 'world', 'user')),
    scope_id TEXT NOT NULL,
    period TEXT NOT NULL CHECK (period IN ('daily', 'monthly')),
    limit_amount NUMERIC(14, 4) NOT NULL,
    reserved_amount NUMERIC(14, 4) NOT NULL DEFAULT 0,
    spent_amount NUMERIC(14, 4) NOT NULL DEFAULT 0,
    currency TEXT NOT NULL DEFAULT 'USD' CHECK (char_length(currency) = 3),
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused', 'exceeded')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, scope_type, scope_id, period)
);

-- Cost reservations (docs/architecture/cost-control.md §2.3). One row per
-- generation_job; created at pipeline step 7, mutated to committed/released
-- at step 9/10.
CREATE TABLE cost_reservations (
    id TEXT PRIMARY KEY,
    generation_job_id TEXT NOT NULL REFERENCES generation_jobs(id),
    tenant_id TEXT NOT NULL,
    estimated_amount NUMERIC(14, 4) NOT NULL,
    reserved_amount NUMERIC(14, 4) NOT NULL,
    actual_amount NUMERIC(14, 4),
    currency TEXT NOT NULL DEFAULT 'USD' CHECK (char_length(currency) = 3),
    status TEXT NOT NULL CHECK (status IN ('reserved', 'committed', 'released', 'failed')),
    failure_reason TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Finalize the FK from generation_jobs.cost_reservation_id now that
-- cost_reservations exists.
ALTER TABLE generation_jobs
    ADD CONSTRAINT generation_jobs_cost_reservation_fk
    FOREIGN KEY (cost_reservation_id) REFERENCES cost_reservations(id);

-- Generation cost events (per-call telemetry). Companion to provider_attempts;
-- aggregated by the cost-spike runbook and admin cost-events endpoint.
CREATE TABLE generation_cost_events (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    job_id TEXT REFERENCES generation_jobs(id),
    asset_id TEXT REFERENCES visual_assets(id),
    token_id TEXT REFERENCES api_tokens(id),
    provider_id TEXT,
    model_id TEXT REFERENCES provider_models(id),
    provider_attempt_id TEXT REFERENCES provider_attempts(id),
    operation TEXT NOT NULL,
    estimated_cost_usd NUMERIC(14, 4),
    actual_cost_usd NUMERIC(14, 4),
    duration_ms INT,
    status TEXT NOT NULL,
    metadata JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Idempotency keys (docs/api/idempotency.md). The first writer wins; same
-- key + same body hash returns the same generation_job_id; same key +
-- different hash returns 409.
CREATE TABLE idempotency_keys (
    id TEXT PRIMARY KEY,
    token_id TEXT NOT NULL REFERENCES api_tokens(id),
    key TEXT NOT NULL,
    endpoint TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    generation_job_id TEXT REFERENCES generation_jobs(id),
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (token_id, key)
);

-- Audit events. Admin write actions and security-relevant events.
-- event_type follows the dotted convention from
-- docs/architecture/admin-control-surface.md (e.g. admin.provider.disabled,
-- admin.cost_budget.updated, admin.job.retried).
CREATE TABLE audit_events (
    id TEXT PRIMARY KEY,
    tenant_id TEXT,                                    -- nullable for global events
    event_type TEXT NOT NULL,
    actor_token_id TEXT REFERENCES api_tokens(id),
    world_id TEXT,
    resource_type TEXT,
    resource_id TEXT,
    metadata JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------------------
-- Indexes
-- ---------------------------------------------------------------------------

-- api_tokens: bearer-token middleware looks up by prefix; tenant_id scoping
-- supports admin listing.
CREATE INDEX idx_api_tokens_tenant ON api_tokens(tenant_id);
CREATE INDEX idx_api_tokens_status_expires ON api_tokens(status, expires_at);

-- style_profiles: list by tenant/world.
CREATE INDEX idx_style_profiles_tenant ON style_profiles(tenant_id);
CREATE INDEX idx_style_profiles_tenant_world ON style_profiles(tenant_id, world_id);

-- provider_models: status filter.
CREATE INDEX idx_provider_models_status ON provider_models(status);

-- provider_routes: router lookup by operation + capability + enabled.
CREATE INDEX idx_provider_routes_active ON provider_routes(operation_type, required_capability, priority) WHERE is_enabled = true;
CREATE INDEX idx_provider_routes_model ON provider_routes(model_id);

-- visual_identities: owner lookup + tenant scoping.
CREATE INDEX idx_visual_identities_tenant_world ON visual_identities(tenant_id, world_id);
CREATE INDEX idx_visual_identities_owner ON visual_identities(world_id, owner_type, owner_id);

-- visual_assets: the primary retrieval path is
-- (visual_identity_id, variant_key, state_version, style_profile_id).
CREATE INDEX idx_visual_assets_identity_variant ON visual_assets(visual_identity_id, variant_key, state_version);
CREATE INDEX idx_visual_assets_identity_family ON visual_assets(visual_identity_id, variant_family);
CREATE INDEX idx_visual_assets_tenant_world ON visual_assets(tenant_id, world_id);
CREATE INDEX idx_visual_assets_status ON visual_assets(status);
-- compatibility_tags is queried by overlap during fallback search.
CREATE INDEX idx_visual_assets_compat_tags ON visual_assets USING GIN (compatibility_tags);
-- Anchors are looked up by identity when constructing reference packages.
CREATE INDEX idx_visual_assets_anchors ON visual_assets(visual_identity_id) WHERE is_identity_anchor = true;

-- generation_jobs: dashboard + admin list + poll-by-id.
CREATE INDEX idx_generation_jobs_tenant_status ON generation_jobs(tenant_id, status);
CREATE INDEX idx_generation_jobs_status ON generation_jobs(status);
CREATE INDEX idx_generation_jobs_world ON generation_jobs(world_id);
CREATE INDEX idx_generation_jobs_token ON generation_jobs(requested_by_token_id);
CREATE INDEX idx_generation_jobs_created ON generation_jobs(created_at DESC);

-- asset_packs: listing by identity / world.
CREATE INDEX idx_asset_packs_identity ON asset_packs(visual_identity_id);
CREATE INDEX idx_asset_packs_tenant_world ON asset_packs(tenant_id, world_id);
CREATE INDEX idx_asset_packs_status ON asset_packs(status);

-- asset_pack_items: lookup of items by pack is via the FK PK index;
-- reverse lookup of "which pack(s) is this asset in" needs an index.
CREATE INDEX idx_asset_pack_items_asset ON asset_pack_items(visual_asset_id);

-- provider_attempts: investigation by job.
CREATE INDEX idx_provider_attempts_job ON provider_attempts(generation_job_id, attempt_number);
CREATE INDEX idx_provider_attempts_route ON provider_attempts(provider_route_id);
CREATE INDEX idx_provider_attempts_status ON provider_attempts(status, started_at DESC);

-- provider_model_prices: pipeline step 4 is a hot path.
CREATE UNIQUE INDEX uq_provider_model_prices_active
    ON provider_model_prices(provider_id, model_id, operation_type)
    WHERE is_active = true;
-- Audit / history lookup including inactive entries.
CREATE INDEX idx_provider_model_prices_history
    ON provider_model_prices(provider_id, model_id, operation_type, effective_from DESC);

-- cost_budgets: lookup of the right budget for a (tenant × scope × period).
CREATE INDEX idx_cost_budgets_lookup ON cost_budgets(tenant_id, scope_type, scope_id, period);
CREATE INDEX idx_cost_budgets_status ON cost_budgets(status);

-- cost_reservations: investigation + reconciliation.
CREATE INDEX idx_cost_reservations_job ON cost_reservations(generation_job_id);
CREATE INDEX idx_cost_reservations_tenant_status ON cost_reservations(tenant_id, status);
CREATE INDEX idx_cost_reservations_created ON cost_reservations(created_at DESC);

-- generation_cost_events: cost-spike runbook queries by token/world/provider
-- over a time window.
CREATE INDEX idx_cost_events_tenant_created ON generation_cost_events(tenant_id, created_at DESC);
CREATE INDEX idx_cost_events_token ON generation_cost_events(token_id, created_at DESC);
CREATE INDEX idx_cost_events_provider ON generation_cost_events(provider_id, model_id, created_at DESC);
CREATE INDEX idx_cost_events_job ON generation_cost_events(job_id);

-- idempotency_keys: replay lookup is via UNIQUE(token_id, key); add an
-- expiry sweep index.
CREATE INDEX idx_idempotency_keys_expires ON idempotency_keys(expires_at);

-- audit_events: tenant filter + chronological browse.
CREATE INDEX idx_audit_events_tenant ON audit_events(tenant_id, created_at DESC);
CREATE INDEX idx_audit_events_resource ON audit_events(resource_type, resource_id);
CREATE INDEX idx_audit_events_actor ON audit_events(actor_token_id, created_at DESC);

-- ---------------------------------------------------------------------------
-- Confidence to Implement
--
-- Score: 92/100 — Very High (was 85)
--
-- This schema now matches the data model documented across the architecture
-- docs and OpenAPI v0.5.0. Phases 0–3 of the implementation plan
-- (Phase 0 setup → Phase 3 generation + cost-control pipeline) can build
-- against this schema without further migrations:
--
--   * tenant_id present on every tenant-scoped row.
--   * AssetType / GenerationJobStatus / preview_capability / required_capability
--     all enforced via CHECK constraints matching the OpenAPI enums.
--   * variant-compatibility-matrix v1 fields are columns (variant_family,
--     state_version, compatibility_tags, fallback_allowed, fallback_rank,
--     is_identity_anchor) so retrieval queries are well-indexed instead of
--     JSONB scans.
--   * cost-control: provider_model_prices, cost_budgets, cost_reservations
--     plus the generation_jobs.cost_reservation_id link complete the
--     pre-flight pipeline §3.
--   * provider_routes carries the admin disable/enable surface and the
--     capability filtering the router needs.
--   * asset_packs + asset_pack_items model PRD 04 pack semantics with FKs
--     into visual_assets.
--   * provider_attempts records each provider call for failed-jobs and
--     cost-spike investigations.
--   * Indexes cover the retrieval, router, budget, idempotency, cost-event,
--     and audit query paths.
--
-- Why not 100:
--   * No row-level security (RLS) policies yet; multi-tenant isolation
--     relies on the application layer enforcing tenant_id filtering. RLS is
--     a future hardening pass once the API stabilizes.
--   * Some loose-schema JSONB columns (canonical_visual_traits,
--     allowed_variation, forbidden_drift, asset metadata) defer validation
--     to the app. That's intentional per the PRDs but it means a future
--     migration may promote heavily-queried JSONB keys to columns.
--   * The `pack_type` column is currently free text; promoting to CHECK or
--     a lookup table is left for when the PRD 04 template list stabilizes.
-- ---------------------------------------------------------------------------
