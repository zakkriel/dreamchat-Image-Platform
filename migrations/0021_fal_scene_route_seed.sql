-- +goose Up
-- 0021_fal_scene_route_seed
--
-- Give `scene_capable` a SECOND real route, on fal's prompt-only endpoint.
--
-- Decision: image:ADR-I005.
--
-- WHY: until now scene work had exactly one route that could actually render -
-- bfl. fal's adapters are FLUX.1 Kontext, which is reference-conditioned and
-- fails closed without a reference image (correctly: it is what stops a
-- recurring character drifting). A place, an object or a world cover has nothing
-- to condition on, so scene work could never resolve to fal at all.
--
-- That made one provider's billing status a whole-product outage. Measured
-- 2026-09-01: BFL's account ran out of credit, `submit` began answering 402, and
-- 875 artifact jobs failed in 24 hours with nothing to fall back to. Worse, the
-- world backend re-commissioned them every two minutes, and those doomed submits
-- drained the shared 1000-requests/hour token budget that the asset READ path
-- also spends - so every picture that ALREADY existed became unfetchable too,
-- including the ones fal had rendered and been paid for.
--
-- mock sits at priority 100 but is filtered out in production by the
-- synthetic-provider policy (ALLOW_SYNTHETIC_PROVIDERS=false), so it was never a
-- fallback for anything real.
--
-- A DISTINCT provider_id ("fal_t2i") rather than another model under "fal":
-- capability reconciliation is per adapter and fails closed against
-- ADAPTER-REPORTED capabilities (image:ADR-016), and this endpoint's capability
-- set is deliberately NARROWER than kontext's - scene only, no identity, no pack.
-- Claiming identity here would let recurring-character work resolve to a
-- prompt-only endpoint and render a different face on every call. Same reasoning
-- as 0020 for [dev], and as image:ADR-017 for keeping fal off the bfl id.
--
-- PRICE HONESTY: 0.0400 is fal's published per-image rate for the FLUX1.1 [pro]
-- family, the same rate already seeded for kontext [pro] in 0011 with
-- source='fal_published'. It is marked the same way. If fal's v1.1 rate diverges
-- from kontext's, the existing provider-reported actual-cost reconciliation is
-- what closes the gap, and the row's source keeps the claim auditable.
--
-- SEED-ONLY DML: no new tables, no new columns, not listed in sqlc.yaml (same
-- convention as 0006/0011/0018/0020). Table count is unchanged.

-- 1. The model row. Capabilities must match what the adapter reports, or
--    reconciliation disables the route at boot. Scene only, on purpose.
INSERT INTO provider_models (
    id, provider_id, model_name, display_name,
    capabilities, preview_capability, supports_high_res,
    max_batch_size, supported_aspect_ratios, status
) VALUES (
    'pm_fal_flux_pro_11', 'fal_t2i', 'flux-pro-1.1', 'FLUX1.1 [pro] (prompt-only, scenes)',
    '{scene_capable}',
    'no_preview', false,
    1, '{1:1,16:9,9:16,4:3,3:4}', 'active'
)
ON CONFLICT (id) DO NOTHING;

-- 2. Price: see PRICE HONESTY above.
INSERT INTO provider_model_prices (
    id, provider_id, model_id, operation_type, unit_type,
    price_per_unit, currency, effective_from, effective_to, is_active, source
) VALUES (
    'price_fal_t2i_text_to_image_001', 'fal_t2i', 'pm_fal_flux_pro_11', 'text_to_image', 'image',
    0.0400, 'USD', now(), NULL, true, 'fal_published'
)
ON CONFLICT (id) DO NOTHING;

-- 3. Route at priority 150. Lower is preferred, so this ranks AHEAD of bfl's
--    scene route at 200 and behind mock at 100.
--
--    Ahead of bfl deliberately: this is the endpoint on the provider this
--    platform selected for its permissiveness dial (the provider research names
--    that dial as the reason we are on fal at all), and the one whose account is
--    in credit. bfl stays ENABLED at 200 as the genuine fallback - two paid
--    routes is the entire point, and an unpaid-account refusal is walkable
--    precisely so the walk reaches a provider that IS paid.
INSERT INTO provider_routes (
    id, provider_id, model_id, operation_type, required_capability,
    preview_capability, quality_tier, latency_tier,
    is_enabled, priority, weight, allow_unpriced_provider
) VALUES (
    'route_fal_t2i_text_to_image_scene', 'fal_t2i', 'pm_fal_flux_pro_11', 'text_to_image', 'scene_capable',
    'no_preview', 'standard', 'balanced', true, 150, 1, false
)
ON CONFLICT (id) DO NOTHING;

-- +goose Down
DELETE FROM provider_routes WHERE id = 'route_fal_t2i_text_to_image_scene';
DELETE FROM provider_model_prices WHERE id = 'price_fal_t2i_text_to_image_001';
DELETE FROM provider_models WHERE id = 'pm_fal_flux_pro_11';
