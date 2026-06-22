-- +goose Up
-- Chunk 1: grid parent/child fields on visual_assets plus the sprite-sheet
-- contract/slice tables. Expand-only for visual_assets (nullable columns). The
-- two new tables are tenant-scoped; their RLS lands in 0017 (kept out of sqlc,
-- like 0009). crop_box / contract are validated JSONB blobs (rule D-4).
-- row_count / col_count avoid the SQL ROWS keyword.
ALTER TABLE visual_assets
    ADD COLUMN parent_asset_id TEXT REFERENCES visual_assets(id),
    ADD COLUMN crop_index      INT,
    ADD COLUMN crop_box        JSONB,
    ADD COLUMN expression_key  TEXT,
    ADD CONSTRAINT visual_assets_crop_box_schema_version
        CHECK (crop_box IS NULL OR jsonb_exists(crop_box, 'schema_version'));

CREATE TABLE sprite_sheet_contract (
    id                  TEXT PRIMARY KEY,
    tenant_id           TEXT NOT NULL,
    world_id            TEXT,
    visual_identity_id  TEXT REFERENCES visual_identities(id),
    sheet_asset_id      TEXT REFERENCES visual_assets(id),     -- composite sheet image
    generation_job_id   TEXT REFERENCES generation_jobs(id),   -- producing job
    row_count           INT,
    col_count           INT,
    contract            JSONB,
    status              TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT sprite_sheet_contract_schema_version
        CHECK (contract IS NULL OR jsonb_exists(contract, 'schema_version'))
);

CREATE TABLE sprite_sheet_slice (
    id                        TEXT PRIMARY KEY,
    sprite_sheet_contract_id  TEXT NOT NULL REFERENCES sprite_sheet_contract(id) ON DELETE CASCADE,
    crop_index                INT,
    crop_box                  JSONB,
    expression_key            TEXT,
    asset_id                  TEXT REFERENCES visual_assets(id),  -- sliced child asset (nullable until sliced)
    created_at                TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT sprite_sheet_slice_crop_box_schema_version
        CHECK (crop_box IS NULL OR jsonb_exists(crop_box, 'schema_version'))
);

CREATE INDEX idx_sprite_sheet_slice_contract ON sprite_sheet_slice (sprite_sheet_contract_id);

-- +goose Down
DROP TABLE IF EXISTS sprite_sheet_slice;
DROP TABLE IF EXISTS sprite_sheet_contract;

ALTER TABLE visual_assets
    DROP COLUMN IF EXISTS parent_asset_id,
    DROP COLUMN IF EXISTS crop_index,
    DROP COLUMN IF EXISTS crop_box,
    DROP COLUMN IF EXISTS expression_key;
