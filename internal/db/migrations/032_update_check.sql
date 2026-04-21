-- 032_update_check.sql
-- Seed the settings keys used by the self-update checker. Values are
-- maintained by internal/selfupdate — check result is cached so restarts
-- don't re-fetch GitHub on every boot.
INSERT OR IGNORE INTO settings (key, value) VALUES
    ('update_check_enabled',          'true'),
    ('update_check_interval_minutes', '120'),
    ('update_last_check',             ''),
    ('update_latest_version',         ''),
    ('update_release_url',            ''),
    ('update_release_notes',          ''),
    ('update_published_at',           '');
