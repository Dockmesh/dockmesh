-- Notification center (the bell icon in the top-bar). Each row is a
-- discrete event the UI should surface to a user — both transient
-- toast equivalents and the audit-trail-like history users browse
-- when they come back after time away.
--
-- user_id nullable = broadcast to every user (e.g. system upgrade
-- available). When set, only that user sees the notification.
-- kind is a stable string the UI maps to an icon + filter category;
-- severity drives the badge color.
CREATE TABLE IF NOT EXISTS notifications (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     TEXT,                      -- nullable = broadcast to all users
    kind        TEXT NOT NULL,             -- deploy.ok, deploy.fail, alert.fire, backup.ok, …
    severity    TEXT NOT NULL DEFAULT 'info', -- info | success | warning | error
    title       TEXT NOT NULL,
    body        TEXT,
    link        TEXT,                      -- optional in-app URL to navigate on click
    read_at     DATETIME,                  -- NULL = unread
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Common access patterns: list per user (own + broadcast) ordered by
-- recency; unread-count badge. Composite index covers both.
CREATE INDEX IF NOT EXISTS idx_notifications_user_recent
    ON notifications(user_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_notifications_unread
    ON notifications(user_id, read_at)
    WHERE read_at IS NULL;
