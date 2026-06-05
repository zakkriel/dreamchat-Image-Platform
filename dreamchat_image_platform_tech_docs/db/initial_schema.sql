-- DreamChat Image Platform initial schema draft
-- This is a starting point, not a final migration.

CREATE TABLE api_tokens (
    id TEXT PRIMARY KEY,
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

CREATE TABLE style_profiles (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    style_mode TEXT NOT NULL CHECK (style_mode IN ('open_prompt', 'preset', 'creator_pack')),
    positive_prompt TEXT NOT NULL,
    negative_prompt TEXT,
    default_quality_tier TEXT NOT NULL DEFAULT 'standard',
    status TEXT NOT NULL DEFAULT 'active',
    metadata JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE provider_models (
    id TEXT PRIMARY KEY,
    provider_id TEXT NOT NULL,
    model_name TEXT NOT NULL,
    display_name TEXT NOT NULL,
    capabilities TEXT[] NOT NULL DEFAULT '{}',
    supports_preview BOOLEAN NOT NULL DEFAULT false,
    supports_high_res BOOLEAN NOT NULL DEFAULT true,
    status TEXT NOT NULL CHECK (status IN ('active', 'degraded', 'disabled')),
    metadata JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE visual_identities (
    id TEXT PRIMARY KEY,
    world_id TEXT NOT NULL,
    owner_type TEXT NOT NULL CHECK (owner_type IN ('character', 'place', 'artifact')),
    owner_id TEXT NOT NULL,
    display_name TEXT NOT NULL,
    canonical_visual_traits JSONB NOT NULL DEFAULT '{}',
    style_profile_id TEXT NOT NULL REFERENCES style_profiles(id),
    consistency_key TEXT,
    anchor_asset_ids TEXT[] NOT NULL DEFAULT '{}',
    current_version INT NOT NULL DEFAULT 1,
    status TEXT NOT NULL CHECK (status IN ('active', 'archived')) DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(world_id, owner_type, owner_id)
);

CREATE TABLE visual_assets (
    id TEXT PRIMARY KEY,
    visual_identity_id TEXT REFERENCES visual_identities(id),
    world_id TEXT NOT NULL,
    asset_type TEXT NOT NULL,
    variant_key TEXT NOT NULL,
    version INT NOT NULL DEFAULT 1,
    status TEXT NOT NULL CHECK (status IN ('pending', 'preview_ready', 'ready', 'failed', 'archived')),
    low_res_url TEXT,
    high_res_url TEXT,
    thumbnail_url TEXT,
    provider_id TEXT,
    model_id TEXT REFERENCES provider_models(id),
    prompt_hash TEXT,
    seed TEXT,
    metadata JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_visual_assets_identity_variant ON visual_assets(visual_identity_id, variant_key, version);
CREATE INDEX idx_visual_assets_world ON visual_assets(world_id);

CREATE TABLE generation_jobs (
    id TEXT PRIMARY KEY,
    job_type TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'preview_ready', 'completed', 'failed', 'cancelled')),
    requested_by_token_id TEXT REFERENCES api_tokens(id),
    world_id TEXT,
    visual_identity_id TEXT REFERENCES visual_identities(id),
    input_payload JSONB NOT NULL DEFAULT '{}',
    preview_asset_ids TEXT[] NOT NULL DEFAULT '{}',
    final_asset_ids TEXT[] NOT NULL DEFAULT '{}',
    error_code TEXT,
    error_message TEXT,
    retryable BOOLEAN,
    cost_estimate_usd NUMERIC(12,4),
    actual_cost_usd NUMERIC(12,4),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ
);

CREATE INDEX idx_generation_jobs_status ON generation_jobs(status);
CREATE INDEX idx_generation_jobs_world ON generation_jobs(world_id);

CREATE TABLE generation_cost_events (
    id TEXT PRIMARY KEY,
    job_id TEXT REFERENCES generation_jobs(id),
    asset_id TEXT REFERENCES visual_assets(id),
    token_id TEXT REFERENCES api_tokens(id),
    provider_id TEXT,
    model_id TEXT REFERENCES provider_models(id),
    operation TEXT NOT NULL,
    estimated_cost_usd NUMERIC(12,4),
    actual_cost_usd NUMERIC(12,4),
    duration_ms INT,
    status TEXT NOT NULL,
    metadata JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE idempotency_keys (
    id TEXT PRIMARY KEY,
    token_id TEXT NOT NULL REFERENCES api_tokens(id),
    key TEXT NOT NULL,
    endpoint TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    generation_job_id TEXT REFERENCES generation_jobs(id),
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(token_id, key)
);

CREATE TABLE audit_events (
    id TEXT PRIMARY KEY,
    event_type TEXT NOT NULL,
    actor_token_id TEXT REFERENCES api_tokens(id),
    world_id TEXT,
    resource_type TEXT,
    resource_id TEXT,
    metadata JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
