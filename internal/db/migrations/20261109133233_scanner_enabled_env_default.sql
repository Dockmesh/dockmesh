-- scanner_enabled_env_default
-- The setting never controlled scanning and still holds its seeded 'true', so
-- that value says nothing about what should run. Empty lets the first start
-- take DOCKMESH_SCANNER_ENABLED, which is what scans have followed so far.
UPDATE settings SET value = '' WHERE key = 'scanner_enabled' AND value = 'true';
