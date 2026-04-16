-- 014_migrations.sql
-- P.9: Stack migration tracking. One row per migration attempt.
CREATE TABLE IF NOT EXISTS migrations (
    id              TEXT    NOT NULL PRIMARY KEY,
    stack_name      TEXT    NOT NULL,
    source_host_id  TEXT    NOT NULL,
    target_host_id  TEXT    NOT NULL,
    status          TEXT    NOT NULL DEFAULT 'pending',
    phase           TEXT,
    progress_json   TEXT,
    started_at      DATETIME,
    completed_at    DATETIME,
    error_message   TEXT,
    initiated_by    TEXT    NOT NULL,
    drain_id        TEXT,
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_migrations_stack ON migrations(stack_name, status);
-- Partial index for active migrations (SQLite supports WHERE on index).
CREATE INDEX IF NOT EXISTS idx_migrations_active ON migrations(status)
    WHERE status NOT IN ('completed', 'failed', 'rolled_back');
