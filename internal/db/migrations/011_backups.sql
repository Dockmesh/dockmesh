-- Backup jobs + run history (concept §3.4).
--
-- backup_jobs
--   target_type    "local" or "s3"
--   target_config  type-specific JSON ({"path":"/srv/backups"} or {"endpoint":...,"bucket":...})
--   sources        JSON array of {"type":"volume"|"stack","name":"..."}
--   schedule       robfig/cron expression, empty string = manual only
--   retention_*    keep last N runs OR newer than N days (0 = unlimited)
--   pre_hooks      JSON array of {"container":"...","cmd":["..."]} run BEFORE tar
--   post_hooks     same shape, run AFTER tar (e.g. cleanup of pg_dump file)
--   encrypt        wraps the archive in age, key shared with secrets svc
--
-- backup_runs records every execution, success or failure.

CREATE TABLE IF NOT EXISTS backup_jobs (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    name            TEXT NOT NULL UNIQUE,
    target_type     TEXT NOT NULL,
    target_config   TEXT NOT NULL DEFAULT '{}',
    sources         TEXT NOT NULL DEFAULT '[]',
    schedule        TEXT NOT NULL DEFAULT '',
    retention_count INTEGER NOT NULL DEFAULT 0,
    retention_days  INTEGER NOT NULL DEFAULT 0,
    encrypt         INTEGER NOT NULL DEFAULT 1,
    pre_hooks       TEXT NOT NULL DEFAULT '[]',
    post_hooks      TEXT NOT NULL DEFAULT '[]',
    enabled         INTEGER NOT NULL DEFAULT 1,
    last_run_at     DATETIME,
    next_run_at     DATETIME,
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS backup_runs (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id       INTEGER NOT NULL,
    job_name     TEXT NOT NULL,
    status       TEXT NOT NULL,                         -- "running" | "success" | "failed"
    started_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finished_at  DATETIME,
    size_bytes   INTEGER NOT NULL DEFAULT 0,
    target_path  TEXT,
    sha256       TEXT,
    encrypted    INTEGER NOT NULL DEFAULT 0,
    error        TEXT,
    sources_json TEXT NOT NULL DEFAULT '[]'
);

CREATE INDEX IF NOT EXISTS idx_backup_runs_job ON backup_runs(job_id);
CREATE INDEX IF NOT EXISTS idx_backup_runs_started ON backup_runs(started_at);
