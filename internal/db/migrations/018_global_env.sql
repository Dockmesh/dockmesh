-- 018_global_env.sql
-- Global environment variables that can be injected into stack deploys.
-- Users manage these via the WebGUI; the compose deploy flow merges
-- them with each stack's own .env before running.
CREATE TABLE IF NOT EXISTS global_env (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    key         TEXT NOT NULL,
    value       TEXT NOT NULL DEFAULT '',
    group_name  TEXT NOT NULL DEFAULT '',  -- e.g. "database", "smtp", "" for ungrouped
    encrypted   INTEGER NOT NULL DEFAULT 0,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(key)
);
