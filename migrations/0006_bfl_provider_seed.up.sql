-- 0006_bfl_provider_seed
--
-- Phase 7A (real provider routing + BFL adapter) seed migration.
--
-- This migration is SEED-ONLY DML: it adds the Black Forest Labs (BFL) provider
-- model, route, and price rows so the data-driven route resolver
-- (internal/providers/routing) can select BFL when it is configured and
-- available (BFL_API_KEY set). It creates NO new tables and NO new columns, so
-- the table count stays 18 and this file is intentionally NOT listed in
-- sqlc.yaml (sqlc only needs schema-defining migrations).
--
-- The mock seed (0002) is left untouched: mock remains a first-class, default
-- route. BFL's route is given a HIGHER priority number (200) than mock's (100)
-- so that when both providers are available and no provider preference is
-- supplied, the resolver keeps choosing mock (lower priority = preferred). BFL
-- is selected when IMAGE_PROVIDER=bfl supplies a provider preference, which the
-- resolver ranks above route priority.
--
-- Endpoint/pricing assumptions are documented in internal/providers/bfl/bfl.go.

BEGIN;

-- 1. BFL provider model. Capabilities stay conservative
--    (draft_only + scene_capable, no_preview) per the provider-capability floor
--    documented in internal/providers/bfl; true_preview / pack / production
--    tiers are NOT claimed without benchmark evidence.
INSERT INTO provider_models (
    id, provider_id, model_name, display_name,
    capabilities, preview_capability, supports_high_res,
    max_batch_size, supported_aspect_ratios, status
) VALUES (
    'pm_bfl_flux_pro_11', 'bfl', 'flux-pro-1.1', 'FLUX 1.1 Pro',
    '{draft_only,scene_capable}',
    'no_preview', true,
    1, '{1:1,16:9,9:16,4:3,3:4}', 'active'
)
ON CONFLICT (id) DO NOTHING;

-- 2. BFL provider route for text_to_image at the standard tier. priority=200
--    keeps mock (priority=100) the default when both are available and no
--    provider preference is given.
INSERT INTO provider_routes (
    id, provider_id, model_id, operation_type, required_capability,
    preview_capability, quality_tier, latency_tier,
    is_enabled, priority, weight, allow_unpriced_provider
) VALUES (
    'route_bfl_text_to_image_standard', 'bfl', 'pm_bfl_flux_pro_11', 'text_to_image', 'scene_capable',
    'no_preview', 'standard', 'balanced',
    true, 200, 1, false
)
ON CONFLICT (id) DO NOTHING;

-- 3. BFL provider price: 0.0400 USD per image, currently active. The
--    single-active-price index (idx_provider_model_prices_one_active) still
--    holds because this is a different (provider, model, operation) triple.
INSERT INTO provider_model_prices (
    id, provider_id, model_id, operation_type, unit_type,
    price_per_unit, currency, effective_from, effective_to, is_active, source
) VALUES (
    'price_bfl_text_to_image_001', 'bfl', 'pm_bfl_flux_pro_11', 'text_to_image', 'image',
    0.0400, 'USD', now(), NULL, true, 'bfl_published'
)
ON CONFLICT (id) DO NOTHING;

COMMIT;
