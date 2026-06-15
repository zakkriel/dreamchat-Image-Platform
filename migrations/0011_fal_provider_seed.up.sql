-- 0011_fal_provider_seed
--
-- Seed migration for the first REAL reference-conditioned provider: fal.ai
-- running FLUX.1 Kontext [pro] multi (fal-ai/flux-pro/kontext/multi). Unlike BFL
-- flux-pro-1.1 (scene_capable, prompt-only), FLUX.1 Kontext is reference-
-- conditioned and identity/pack-capable: it renders the SAME subject in a
-- prompted variation from one or more reference images, so it can back recurring-
-- character pack generation.
--
-- This is SEED-ONLY DML: it adds the fal provider model/route/price rows so the
-- data-driven route resolver (internal/providers/routing) can resolve the
-- pack_capable path to a REAL provider when FAL_KEY is configured. It creates NO
-- new tables and NO new columns; it is intentionally NOT listed in sqlc.yaml.
--
-- Capability honesty (PRD 03 §8):
--   * The model advertises {scene_capable, identity_capable, pack_capable}. It is
--     deliberately NOT production_capable: that tier is claimed only after an
--     acceptance/quality benchmark demonstrates recurring-character consistency.
--   * Only the pack_capable route is seeded — pack generation is the path wired
--     end to end with reference images in this slice. (A scene_capable fal route
--     is intentionally NOT seeded: fal Kontext requires references, and single-
--     image scene/artifact requests carry none. BFL keeps serving scene work.)
--   * fal is a REAL (non-synthetic) provider, so its pack route is selectable in
--     production WITHOUT ALLOW_SYNTHETIC_PROVIDERS=true. The existing pack_capable
--     mock route (0006) stays synthetic and continues to fail closed in
--     production. When both fal and mock pack routes are available, fal's higher
--     priority number (200) keeps mock the default in dev (lower = preferred);
--     IMAGE_PROVIDER=fal supplies the provider preference that ranks fal first.
--
-- Endpoint / pricing assumptions are documented in internal/providers/fal/fal.go
-- and docs/architecture/providers.md. fal bills FLUX.1 Kontext [pro] at $0.04 per
-- output image (per-image unit, representable by the existing price schema).

BEGIN;

-- 1. fal provider model. Capabilities are reference-conditioned identity/pack
--    (no production_capable, no_preview, no high-res) per the provider-capability
--    floor documented in internal/providers/fal. supports_high_res=false matches
--    the adapter's Capabilities().SupportsHighRes=false.
INSERT INTO provider_models (
    id, provider_id, model_name, display_name,
    capabilities, preview_capability, supports_high_res,
    max_batch_size, supported_aspect_ratios, status
) VALUES (
    'pm_fal_flux_kontext_multi', 'fal', 'flux-pro-kontext-multi', 'FLUX.1 Kontext [pro] (multi-reference)',
    '{scene_capable,identity_capable,pack_capable}',
    'no_preview', false,
    1, '{1:1,16:9,9:16,4:3,3:4}', 'active'
)
ON CONFLICT (id) DO NOTHING;

-- 2. fal pack_capable route for text_to_image at the standard tier. priority=200
--    keeps mock (priority=100) the dev default when both are available and no
--    provider preference is given; in production the synthetic mock pack route is
--    filtered out and this fal route is selected.
INSERT INTO provider_routes (
    id, provider_id, model_id, operation_type, required_capability,
    preview_capability, quality_tier, latency_tier,
    is_enabled, priority, weight, allow_unpriced_provider
) VALUES (
    'route_fal_text_to_image_pack', 'fal', 'pm_fal_flux_kontext_multi', 'text_to_image', 'pack_capable',
    'no_preview', 'standard', 'balanced',
    true, 200, 1, false
)
ON CONFLICT (id) DO NOTHING;

-- 3. fal provider price: 0.0400 USD per image, currently active. The
--    single-active-price index (idx_provider_model_prices_one_active) still holds
--    because this is a different (provider, model, operation) triple.
INSERT INTO provider_model_prices (
    id, provider_id, model_id, operation_type, unit_type,
    price_per_unit, currency, effective_from, effective_to, is_active, source
) VALUES (
    'price_fal_text_to_image_001', 'fal', 'pm_fal_flux_kontext_multi', 'text_to_image', 'image',
    0.0400, 'USD', now(), NULL, true, 'fal_published'
)
ON CONFLICT (id) DO NOTHING;

COMMIT;
