-- +goose Up
-- Wave 3: bind provider cost events to the reservation that priced the call.
-- A failed job may be retried under the same generation_job_id; without this
-- attribution, a later reservation would sum historical attempts again.
ALTER TABLE generation_cost_events
    ADD COLUMN cost_reservation_id TEXT REFERENCES cost_reservations(id);

CREATE INDEX generation_cost_events_reservation_idx
    ON generation_cost_events (cost_reservation_id)
    WHERE cost_reservation_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS generation_cost_events_reservation_idx;
ALTER TABLE generation_cost_events
    DROP COLUMN IF EXISTS cost_reservation_id;
