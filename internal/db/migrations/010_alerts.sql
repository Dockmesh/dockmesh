-- Notifications + alerts (concept §3.2).
--
-- notification_channels:
--   type     one of: webhook, ntfy, discord, slack, teams, gotify, email
--   config   type-specific JSON (e.g. {"url":"..."} or SMTP creds)
--
-- alert_rules:
--   metric            "cpu_percent" | "mem_percent"
--   operator          "gt" | "lt"
--   threshold         float
--   duration_seconds  how long the breach must persist before firing
--   container_filter  "*" matches every running container, else exact name
--   channel_ids       JSON array of notification_channels.id
--
-- alert_history records each fire/resolve transition for the audit trail.

CREATE TABLE IF NOT EXISTS notification_channels (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    type        TEXT NOT NULL,
    name        TEXT NOT NULL,
    config      TEXT NOT NULL DEFAULT '{}',
    enabled     INTEGER NOT NULL DEFAULT 1,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS alert_rules (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    name              TEXT NOT NULL,
    container_filter  TEXT NOT NULL DEFAULT '*',
    metric            TEXT NOT NULL,
    operator          TEXT NOT NULL,
    threshold         REAL NOT NULL,
    duration_seconds  INTEGER NOT NULL DEFAULT 60,
    channel_ids       TEXT NOT NULL DEFAULT '[]',
    enabled           INTEGER NOT NULL DEFAULT 1,
    firing_since      DATETIME,
    last_triggered_at DATETIME,
    last_resolved_at  DATETIME,
    created_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS alert_history (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    rule_id         INTEGER NOT NULL,
    rule_name       TEXT NOT NULL,
    container_name  TEXT NOT NULL,
    status          TEXT NOT NULL,        -- "fired" | "resolved"
    message         TEXT NOT NULL,
    value           REAL,
    threshold       REAL,
    occurred_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_alert_history_rule ON alert_history(rule_id);
CREATE INDEX IF NOT EXISTS idx_alert_history_ts ON alert_history(occurred_at);
