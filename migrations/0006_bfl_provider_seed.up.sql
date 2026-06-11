-- 0006_bfl_provider_seed
--
-- Phase 7A (real provider routing + BFL adapter) seed migration.
--
-- This migration is SEED-ONLY DML: it adds the Black Forest Labs (BFL) provider
-- model/route/price rows AND a pack_capable mock route so the data-driven route
-- resolver (internal/providers/routing) can resolve every Phase 7A generation
-- path. It creates NO new tables and NO new columns, so the table count stays 18
-- and this file is intentionally NOT listed in sqlc.yaml (sqlc only needs
-- schema-defining migrations).
--
-- The mock seed (0002) is left untouched: mock remains a first-class, default
-- route. BFL's route is given a HIGHER priority number (200) than mock's (100)
-- so that when both providers are available and no provider preference is
-- supplied, the resolver keeps choosing mock (lower priority = preferred). BFL
-- is selected when IMAGE_PROVIDER=bfl supplies a provider preference, which the
-- resolver ranks above route priority.
--
-- Capability-aware routing (Phase 7A): single-image generation (artifact /
-- style preview) requires `scene_capable` and resolves the 0002 mock route or
-- the BFL route; pack generation requires `pack_capable` and resolves the
-- pack_capable mock route added below. BFL's model floor is
-- {draft_only,scene_capable} (no pack_capable), so BFL is correctly NOT eligible
-- for pack generation.
--
-- Endpoint/pricing assumptions are documented in internal/providers/bfl/bfl.go.

BEGIN;

-- 1. BFL provider model. Capabilities stay conservative
--    (draft_only + scene_capable, no_preview, NO high-res) per the
--    provider-capability floor documented in internal/providers/bfl;
--    true_preview / pack / production tiers and high-res are NOT claimed
--    without benchmark evidence. supports_high_res=false matches the adapter's
--    Capabilities().SupportsHighRes=false (Phase 7A seed/adapter consistency).
INSERT INTO provider_models (
    id, provider_id, model_name, display_name,
    capabilities, preview_capability, supports_high_res,
    max_batch_size, supported_aspect_ratios, status
) VALUES (
    'pm_bfl_flux_pro_11', 'bfl', 'flux-pro-1.1', 'FLUX 1.1 Pro',
    '{draft_only,scene_capable}',
    'no_preview', false,
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

-- 4. pack_capable mock route (Phase 7A capability-aware routing). Pack
--    generation fans out per-role as text_to_image but requires a provider that
--    is pack_capable; the mock model advertises pack_capable, so this route lets
--    pack requests resolve to mock. It reuses the existing mock text_to_image
--    price (price keyed on provider+model+operation, not capability), so the
--    single-active-price index is unaffected. BFL has no pack_capable route, so
--    BFL is not eligible for pack generation.
INSERT INTO provider_routes (
    id, provider_id, model_id, operation_type, required_capability,
    preview_capability, quality_tier, latency_tier,
    is_enabled, priority, weight, allow_unpriced_provider
) VALUES (
    'route_mock_text_to_image_pack', 'mock', 'pm_mock_v1', 'text_to_image', 'pack_capable',
    'true_preview', 'standard', 'balanced',
    true, 100, 1, false
)
ON CONFLICT (id) DO NOTHING;

COMMIT;
