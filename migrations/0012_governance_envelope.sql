-- +goose Up
-- Chunk 1 (Governance + Cost Contract): governance envelope fields on
-- generation_jobs. Expand-only — all columns nullable, no default, no backfill.
-- governance_envelope is a validated JSONB blob (rule D-4): JSONB + a
-- schema_version presence check. content_class is stored OPAQUE and never parsed
-- here (parsing/enforcement is Chunk 2+).
ALTER TABLE generation_jobs
    ADD COLUMN governance_envelope    JSONB,
    ADD COLUMN classification_id      TEXT,
    ADD COLUMN visibility             TEXT,
    ADD COLUMN content_class          TEXT,
    ADD COLUMN authorized_by          TEXT,
    ADD COLUMN governance_verified_at TIMESTAMPTZ,
    ADD CONSTRAINT generation_jobs_governance_envelope_schema_version
        CHECK (governance_envelope IS NULL OR jsonb_exists(governance_envelope, 'schema_version'));

-- +goose Down
ALTER TABLE generation_jobs
    DROP COLUMN IF EXISTS governance_envelope,
    DROP COLUMN IF EXISTS classification_id,
    DROP COLUMN IF EXISTS visibility,
    DROP COLUMN IF EXISTS content_class,
    DROP COLUMN IF EXISTS authorized_by,
    DROP COLUMN IF EXISTS governance_verified_at;
