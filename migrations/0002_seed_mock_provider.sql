-- +goose Up
-- 0002_seed_mock_provider
--
-- Phase 4 (cost-control pre-flight) seed + schema migration.
--
-- This migration is NOT purely data-only. In addition to seeding the mock
-- provider model / route / price that the pre-flight estimation pipeline
-- (docs/architecture/cost-control.md §3) needs to resolve a price, it adds a
-- partial unique index guaranteeing at most one *active* price per
-- (provider × model × operation_type).
--
-- NOTE: 0001 already ships an equivalent constraint named
-- `uq_provider_model_prices_active`. The Phase 4 spec requires the index to
-- exist under the name `idx_provider_model_prices_one_active`; we create it
-- with IF NOT EXISTS so applying both migrations is safe and so the named
-- index the spec/CI assert on is present regardless of 0001's history. See
-- frustration_log.md (Phase 4) for the rationale.

-- 1. Mock provider model. capabilities/aspect-ratios are Postgres text[]
--    literals; preview_capability=true_preview gates the preview-first UX.
INSERT INTO provider_models (
    id, provider_id, model_name, display_name,
    capabilities, preview_capability, supports_high_res,
    max_batch_size, supported_aspect_ratios, status
) VALUES (
    'pm_mock_v1', 'mock', 'mock-v1', 'Mock v1',
    '{draft_only,scene_capable,identity_capable,pack_capable,production_capable}',
    'true_preview', true,
    4, '{1:1,16:9,9:16,4:3,3:4}', 'active'
)
ON CONFLICT (id) DO NOTHING;

-- 2. Mock provider route for text_to_image at the standard tier.
INSERT INTO provider_routes (
    id, provider_id, model_id, operation_type, required_capability,
    preview_capability, quality_tier, latency_tier,
    is_enabled, priority, weight, allow_unpriced_provider
) VALUES (
    'route_mock_text_to_image_standard', 'mock', 'pm_mock_v1', 'text_to_image', 'scene_capable',
    'true_preview', 'standard', 'balanced',
    true, 100, 1, false
)
ON CONFLICT (id) DO NOTHING;

-- 3. Mock provider price: 0.0100 USD per image, currently active.
INSERT INTO provider_model_prices (
    id, provider_id, model_id, operation_type, unit_type,
    price_per_unit, currency, effective_from, effective_to, is_active, source
) VALUES (
    'price_mock_text_to_image_001', 'mock', 'pm_mock_v1', 'text_to_image', 'image',
    0.0100, 'USD', now(), NULL, true, 'internal_mock'
)
ON CONFLICT (id) DO NOTHING;

-- 4. At most one active price per (provider × model × operation_type).
CREATE UNIQUE INDEX IF NOT EXISTS idx_provider_model_prices_one_active
    ON provider_model_prices (provider_id, model_id, operation_type)
    WHERE is_active = true;

-- +goose Down
-- Baseline migration: irreversible floor. Roll back by restoring from backup.
SELECT 'baseline migration 0002 is irreversible' WHERE false;
