-- 046_oidc_extensions.sql
-- OIDC provider modal in the frontend renders a full group-mappings
-- table and per-provider status indicators (last_tested_at,
-- last_test_ok). The original schema only had two fixed slots
-- (admin_group, operator_group) and no test-status columns.

-- Per-provider runtime metadata.
ALTER TABLE oidc_providers ADD COLUMN is_default INTEGER NOT NULL DEFAULT 0;
ALTER TABLE oidc_providers ADD COLUMN last_tested_at DATETIME;
ALTER TABLE oidc_providers ADD COLUMN last_test_ok INTEGER;
ALTER TABLE oidc_providers ADD COLUMN last_test_error TEXT;

-- Group → role mapping table. Replaces the admin_group/operator_group
-- columns going forward; those columns stay on the row for backwards
-- compatibility with installs that haven't moved off the 2-slot model
-- yet. Login flow still reads them today; a follow-up slice will pivot
-- the flow onto this table.
CREATE TABLE IF NOT EXISTS oidc_provider_group_mappings (
    provider_id INTEGER NOT NULL REFERENCES oidc_providers(id) ON DELETE CASCADE,
    group_value TEXT NOT NULL,
    role_name   TEXT NOT NULL,
    PRIMARY KEY (provider_id, group_value)
);

-- Backfill the new mappings table from the existing 2-slot columns so
-- the modal renders consistent data after the migration. NULL/empty
-- columns are skipped — INSERT OR IGNORE handles re-runs safely.
INSERT OR IGNORE INTO oidc_provider_group_mappings (provider_id, group_value, role_name)
SELECT id, admin_group, 'admin' FROM oidc_providers
 WHERE admin_group IS NOT NULL AND admin_group <> '';

INSERT OR IGNORE INTO oidc_provider_group_mappings (provider_id, group_value, role_name)
SELECT id, operator_group, 'operator' FROM oidc_providers
 WHERE operator_group IS NOT NULL AND operator_group <> '';
