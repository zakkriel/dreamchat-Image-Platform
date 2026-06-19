-- +goose Up
-- Chunk 1: cost-routing fields on generation_jobs. Expand-only — all nullable,
-- no default. intent is constrained to the known (draft|commit) vocabulary;
-- transform is a validated JSONB blob (rule D-4).
ALTER TABLE generation_jobs
    ADD COLUMN intent         TEXT CHECK (intent IS NULL OR intent IN ('draft', 'commit')),
    ADD COLUMN transform_only BOOLEAN,
    ADD COLUMN transform      JSONB,
    ADD COLUMN max_megapixels NUMERIC(6, 2),
    ADD COLUMN lazy           BOOLEAN,
    ADD CONSTRAINT generation_jobs_transform_schema_version
        CHECK (transform IS NULL OR jsonb_exists(transform, 'schema_version'));

-- +goose Down
ALTER TABLE generation_jobs
    DROP COLUMN IF EXISTS intent,
    DROP COLUMN IF EXISTS transform_only,
    DROP COLUMN IF EXISTS transform,
    DROP COLUMN IF EXISTS max_megapixels,
    DROP COLUMN IF EXISTS lazy;
