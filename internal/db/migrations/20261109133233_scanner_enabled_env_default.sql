-- scanner_enabled_env_default
-- A stored 'true' is only the seeded value, not an admin's choice. Empty lets
-- the first start take DOCKMESH_SCANNER_ENABLED.
UPDATE settings SET value = '' WHERE key = 'scanner_enabled' AND value = 'true';
