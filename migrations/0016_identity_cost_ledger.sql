-- +goose Up
-- Chunk 1: per-identity lifetime-cost accumulator. One row per visual_identity.
-- The estimated-vs-actual distinction lives in the two *_total columns;
-- cost_reservations is intentionally unchanged (its estimated_amount/actual_amount
-- already encode that distinction at the reservation grain). Tenant-scoped; RLS
-- lands in 0017.
CREATE TABLE identity_cost_ledger (
    id                    TEXT PRIMARY KEY,
    tenant_id             TEXT NOT NULL,
    visual_identity_id    TEXT NOT NULL REFERENCES visual_identities(id),
    cost_estimated_total  NUMERIC(14, 4) NOT NULL DEFAULT 0,
    cost_actual_total     NUMERIC(14, 4) NOT NULL DEFAULT 0,
    currency              TEXT NOT NULL DEFAULT 'USD' CHECK (char_length(currency) = 3),
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (visual_identity_id)
);

-- +goose Down
DROP TABLE IF EXISTS identity_cost_ledger;
