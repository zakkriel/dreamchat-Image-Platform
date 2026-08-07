-- +goose Up
-- 0018_fal_identity_route_seed
--
-- Wave 2: seed the identity_capable fal route for single-image generation.
--
-- Migration 0011 deliberately seeded ONLY fal's pack_capable route because the
-- single-image worker path did not pass reference images ("A dedicated
-- single-character (non-pack) identity generation endpoint is added → seed an
-- identity_capable fal route and wire references on that path too",
-- docs/adr/017-reference-conditioned-provider.md "Revisit when"). Wave 1 wired
-- reference propagation into the single-image path (the /v1/generations
-- worker path gathers + presigns identity anchors exactly like the pack
-- path), so that revisit-condition has fired: this migration adds the
-- explicit identity_capable route.
--
-- Resolution note: /v1/generations already resolves fal via the capability
-- hierarchy (pack_capable satisfies the identity_capable floor when Intent is
-- set — PRD 03 §8.3). This route makes the identity path first-class instead
-- of implicit-via-hierarchy: route semantics stay legible in the route table,
-- and exact-match callers (Intent-less resolution matches required_capability
-- exactly) can reach fal for identity work.
--
-- This is SEED-ONLY DML: no new tables, no new columns; intentionally NOT
-- listed in sqlc.yaml (same convention as 0006/0011). It reuses the existing
-- fal model and the existing text_to_image price (price is keyed on
-- provider+model+operation, not capability), so the single-active-price index
-- is unaffected. Priority 200 matches the other fal routes (mock's 100 keeps
-- mock preferred in dev; IMAGE_PROVIDER=fal or a request provider_id pin
-- ranks fal first).
INSERT INTO provider_routes (
    id, provider_id, model_id, operation_type, required_capability,
    preview_capability, quality_tier, latency_tier,
    is_enabled, priority, weight, allow_unpriced_provider
) VALUES (
    'route_fal_text_to_image_identity', 'fal', 'pm_fal_flux_kontext_multi', 'text_to_image', 'identity_capable',
    'no_preview', 'standard', 'balanced',
    true, 200, 1, false
)
ON CONFLICT (id) DO NOTHING;

-- +goose Down
DELETE FROM provider_routes WHERE id = 'route_fal_text_to_image_identity';
