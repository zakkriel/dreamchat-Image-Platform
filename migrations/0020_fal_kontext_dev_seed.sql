-- +goose Up
-- 0020_fal_kontext_dev_seed
--
-- Seed FLUX.1 Kontext [dev] as a distinct provider so identity work resolves to
-- it instead of [pro].
--
-- WHY: measured on this platform's own renders, [dev] holds a recurring
-- character as well as [pro] on the drawn styles (anime, manhwa, comic) at a
-- fraction of the price, and its output resolution follows the reference image
-- so a 512 anchor buys a 512 render. Delivery resolution is rebuilt by
-- internal/imaging (Lanczos in linear light, damped back-projection, edge-masked
-- sharpening), which is free and deterministic.
--
--   [pro]  flat $0.0400 per image, no size parameter exists
--   [dev]  $0.00125 per compute-second: 2.64s at 512 = $0.0033, 8.45s at 1024
--
-- A DISTINCT provider_id ("fal_dev") rather than a second model under "fal":
-- capability reconciliation is per adapter (image:ADR-016 fails closed against
-- ADAPTER-REPORTED capabilities) and the two endpoints differ in request shape
-- and billing unit. This mirrors the reasoning in image:ADR-017 for keeping fal
-- off the bfl id.
--
-- PRICE HONESTY: price_per_unit here is an ESTIMATE, not a published rate. fal
-- bills this endpoint per compute-second and the price book only supports
-- unit_type='image' (internal/cost pins supportedUnitType and fails closed on
-- anything else). 0.0033 is the measured cost of a 512 render (2.64 compute-
-- seconds x $0.00125), which is what this route is seeded to produce. A 1024
-- render on the same route costs ~3x that and will under-reserve. The existing
-- provider-reported actual-cost reconciliation (Wave 3) is what closes the gap;
-- source is marked 'measured_estimate' rather than a vendor publication so the
-- distinction survives in the row.
--
-- SEED-ONLY DML: no new tables, no new columns, not listed in sqlc.yaml (same
-- convention as 0006/0011/0018). Table count is unchanged.

-- 1. The model row. Capabilities match what the adapter reports, or
--    reconciliation disables the route at boot.
INSERT INTO provider_models (
    id, provider_id, model_name, display_name,
    capabilities, preview_capability, supports_high_res,
    max_batch_size, supported_aspect_ratios, status
) VALUES (
    'pm_fal_flux_kontext_dev', 'fal_dev', 'flux-kontext-dev', 'FLUX.1 Kontext [dev] (open weights)',
    '{scene_capable,identity_capable,pack_capable}',
    'no_preview', false,
    1, '{1:1,16:9,9:16,4:3,3:4}', 'active'
)
ON CONFLICT (id) DO NOTHING;

-- 2. Price: see PRICE HONESTY above. Keyed on (provider, model, operation), so
--    the single-active-price index is unaffected by the [pro] row.
INSERT INTO provider_model_prices (
    id, provider_id, model_id, operation_type, unit_type,
    price_per_unit, currency, effective_from, effective_to, is_active, source
) VALUES (
    'price_fal_dev_text_to_image_001', 'fal_dev', 'pm_fal_flux_kontext_dev', 'text_to_image', 'image',
    0.0033, 'USD', now(), NULL, true, 'measured_estimate'
)
ON CONFLICT (id) DO NOTHING;

-- 3. Routes at priority 150. Lower is preferred, so this ranks AHEAD of the
--    [pro] routes at 200 and BEHIND mock at 100 (mock stays the dev default;
--    it is filtered out in production by the synthetic-provider policy).
--    [pro] is deliberately left enabled at 200: it remains the fallback, and it
--    is the multi-anchor path, because [dev] accepts a single image_url while
--    [pro] takes an array.
INSERT INTO provider_routes (
    id, provider_id, model_id, operation_type, required_capability,
    preview_capability, quality_tier, latency_tier,
    is_enabled, priority, weight, allow_unpriced_provider
) VALUES
    ('route_fal_dev_text_to_image_identity', 'fal_dev', 'pm_fal_flux_kontext_dev', 'text_to_image', 'identity_capable',
     'no_preview', 'standard', 'balanced', true, 150, 1, false),
    ('route_fal_dev_text_to_image_pack', 'fal_dev', 'pm_fal_flux_kontext_dev', 'text_to_image', 'pack_capable',
     'no_preview', 'standard', 'balanced', true, 150, 1, false)
ON CONFLICT (id) DO NOTHING;

-- +goose Down
DELETE FROM provider_routes WHERE id IN ('route_fal_dev_text_to_image_identity', 'route_fal_dev_text_to_image_pack');
DELETE FROM provider_model_prices WHERE id = 'price_fal_dev_text_to_image_001';
DELETE FROM provider_models WHERE id = 'pm_fal_flux_kontext_dev';
