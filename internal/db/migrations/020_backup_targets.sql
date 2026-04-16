-- 020_backup_targets.sql
-- Backup targets as reusable entities. Jobs reference targets by ID
-- instead of embedding credentials inline.
CREATE TABLE IF NOT EXISTS backup_targets (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT    NOT NULL UNIQUE,
    type        TEXT    NOT NULL,  -- local | s3 | sftp | smb | webdav
    config_json TEXT    NOT NULL DEFAULT '{}',
    status      TEXT    NOT NULL DEFAULT 'unknown',  -- connected | error | unknown
    total_bytes INTEGER NOT NULL DEFAULT 0,
    used_bytes  INTEGER NOT NULL DEFAULT 0,
    last_checked_at DATETIME,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Add target_id to jobs. Nullable for backwards compat with existing
-- jobs that have inline target_type/target_config.
ALTER TABLE backup_jobs ADD COLUMN target_id INTEGER REFERENCES backup_targets(id);
