-- 017_settings.sql
-- DB-backed system settings. Key-value store for runtime-configurable
-- settings that previously required .env changes + restart.
CREATE TABLE IF NOT EXISTS settings (
    key         TEXT NOT NULL PRIMARY KEY,
    value       TEXT NOT NULL DEFAULT '',
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Seed defaults from common env vars so existing installs keep working.
-- The Go code checks DB first, falls back to env, then to hardcoded default.
INSERT OR IGNORE INTO settings (key, value) VALUES
    ('proxy_enabled',     'false'),
    ('scanner_enabled',   'true'),
    ('base_url',          'http://localhost:8080'),
    ('agent_public_url',  '');
