-- +goose Up
-- Chunk 1: anchor lineage on the asset and job tables. Expand-only, nullable.
-- anchor_asset_id is a scalar "derived-from-this-anchor" FK to visual_assets
-- (distinct from the existing visual_identities.anchor_asset_ids ARRAY).
-- derive_from is a deferred soft reference (no FK): its target type (asset vs
-- job) is resolved by the Chunk 2 router. Existing rows are NULL, so the FK and
-- the new columns validate trivially at add time (no backfill).
ALTER TABLE visual_assets
    ADD COLUMN anchor_asset_id TEXT REFERENCES visual_assets(id),
    ADD COLUMN derive_from     TEXT;

ALTER TABLE generation_jobs
    ADD COLUMN anchor_asset_id TEXT REFERENCES visual_assets(id),
    ADD COLUMN derive_from     TEXT;

-- +goose Down
ALTER TABLE generation_jobs
    DROP COLUMN IF EXISTS anchor_asset_id,
    DROP COLUMN IF EXISTS derive_from;

ALTER TABLE visual_assets
    DROP COLUMN IF EXISTS anchor_asset_id,
    DROP COLUMN IF EXISTS derive_from;
