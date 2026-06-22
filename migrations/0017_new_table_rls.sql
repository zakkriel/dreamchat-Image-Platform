-- +goose Up
-- Chunk 1: RLS for the three new tenant-scoped tables, matching the platform
-- pattern established in 0009. Kept in its own migration and intentionally
-- EXCLUDED from sqlc.yaml (sqlc does not need policy DDL — same reason 0009 is
-- excluded). Unlike the irreversible baseline RLS migration, this one is fully
-- reversible (real Down below).
--
-- Direct tenant tables (own tenant_id): sprite_sheet_contract, identity_cost_ledger.
-- Child table (ownership via parent FK): sprite_sheet_slice -> sprite_sheet_contract.

ALTER TABLE sprite_sheet_contract ENABLE ROW LEVEL SECURITY;
ALTER TABLE sprite_sheet_contract FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON sprite_sheet_contract
    USING (tenant_id = NULLIF(current_setting('app.current_tenant', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.current_tenant', true), ''));

ALTER TABLE identity_cost_ledger ENABLE ROW LEVEL SECURITY;
ALTER TABLE identity_cost_ledger FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON identity_cost_ledger
    USING (tenant_id = NULLIF(current_setting('app.current_tenant', true), ''))
    WITH CHECK (tenant_id = NULLIF(current_setting('app.current_tenant', true), ''));

ALTER TABLE sprite_sheet_slice ENABLE ROW LEVEL SECURITY;
ALTER TABLE sprite_sheet_slice FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON sprite_sheet_slice
    USING (EXISTS (
        SELECT 1 FROM sprite_sheet_contract p
        WHERE p.id = sprite_sheet_slice.sprite_sheet_contract_id
          AND p.tenant_id = NULLIF(current_setting('app.current_tenant', true), '')))
    WITH CHECK (EXISTS (
        SELECT 1 FROM sprite_sheet_contract p
        WHERE p.id = sprite_sheet_slice.sprite_sheet_contract_id
          AND p.tenant_id = NULLIF(current_setting('app.current_tenant', true), '')));

-- +goose Down
DROP POLICY IF EXISTS tenant_isolation ON sprite_sheet_slice;
ALTER TABLE sprite_sheet_slice NO FORCE ROW LEVEL SECURITY;
ALTER TABLE sprite_sheet_slice DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation ON identity_cost_ledger;
ALTER TABLE identity_cost_ledger NO FORCE ROW LEVEL SECURITY;
ALTER TABLE identity_cost_ledger DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation ON sprite_sheet_contract;
ALTER TABLE sprite_sheet_contract NO FORCE ROW LEVEL SECURITY;
ALTER TABLE sprite_sheet_contract DISABLE ROW LEVEL SECURITY;
