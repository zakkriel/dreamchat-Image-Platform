-- +goose Up
-- Phase 6A3 pack reuse-first + completeness storage.
--
-- Packs become retrieval-first: at creation time the platform resolves each
-- required role through the Phase 6A1 identity/matrix retrieval layer, persists
-- the roles that an existing ready asset already satisfies as asset_pack_items,
-- and generates only the genuinely-missing roles. To let a consumer observe
-- pack completeness (delivered vs missing required roles) without re-deriving it
-- from asset_pack_items + the 5B template, the completeness is recorded directly
-- on asset_packs.
--
-- Role identity == the variant_key / role name used by the 5B pack template
-- (PRD 04 §8), so these arrays are directly comparable with
-- asset_pack_items.variant_key and visual_assets.variant_key.
--
-- This is additive: three columns on an existing table, no new table (the
-- public BASE TABLE count stays 18). All three default to '{}' so pre-6A3 rows
-- and any path that does not populate them remain valid.
ALTER TABLE asset_packs
    ADD COLUMN required_roles  TEXT[] NOT NULL DEFAULT '{}',  -- every role the pack template requires
    ADD COLUMN delivered_roles TEXT[] NOT NULL DEFAULT '{}',  -- required roles backed by a ready asset_pack_items row
    ADD COLUMN missing_roles   TEXT[] NOT NULL DEFAULT '{}';  -- required roles still awaiting (or failed) generation

-- +goose Down
-- Baseline migration: irreversible floor. Roll back by restoring from backup.
SELECT 'baseline migration 0004 is irreversible' WHERE false;
