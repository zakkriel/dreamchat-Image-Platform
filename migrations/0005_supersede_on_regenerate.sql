-- +goose Up
-- Phase 6A4 forced regeneration (supersede-on-regenerate).
--
-- A forced regeneration (force_regenerate: true) is a real generation that
-- bypasses reuse and always renders a fresh asset. When that asset lands in a
-- slot that already has ready asset(s), the prior ready asset(s) for the exact
-- same slot are marked status='archived' and linked forward to the new asset so
-- the supersede chain is observable, and the new asset becomes the single ready
-- row retrieval returns. The archive + insert happen in one transaction under a
-- slot advisory lock so committed readers never observe zero or multiple ready
-- rows for the slot.
--
-- This is additive: one nullable self-referential column on visual_assets, no
-- new table (the public BASE TABLE count stays 18). The predecessor of a
-- regenerated asset gets superseded_by_asset_id = <new asset id>; every
-- pre-6A4 row and any non-forced path leaves it NULL.
ALTER TABLE visual_assets
    ADD COLUMN superseded_by_asset_id TEXT REFERENCES visual_assets(id);

-- +goose Down
-- Baseline migration: irreversible floor. Roll back by restoring from backup.
SELECT 'baseline migration 0005 is irreversible' WHERE false;
