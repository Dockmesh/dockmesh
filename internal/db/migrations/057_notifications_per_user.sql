-- 057_notifications_per_user.sql
-- Notifications were previously emit-as-broadcast (user_id NULL) with a
-- single read_at on the row. That created two real bugs:
--   1) User A marking a broadcast read flipped read_at to non-NULL,
--      which silently zero'd the bell badge for all other users.
--   2) A user signing up today saw broadcasts emitted last week — there
--      was no created_at floor scoped to "after I joined".
--
-- The fan-out fix: producers that want "all admins should see this"
-- write one row per user at emit time. user_id becomes NOT NULL so the
-- schema can't drift back into the old broadcast pattern.
--
-- Migration drops existing broadcast rows. They're short-lived signals
-- (deploy succeeded, alert fired) — keeping them as ghost rows after
-- the redesign would just leave the same bug pattern hanging around for
-- another N days until cleanup. Per-user rows from before this
-- migration are preserved unchanged.
--
-- A separate per-emit fan-out is done in code (internal/notifications/
-- service.go) so we don't need a `roles_targeted` column today; future
-- "alerts only to security team" routing would add it.

-- 1. Purge legacy broadcast rows.
DELETE FROM notifications WHERE user_id IS NULL;

-- 2. SQLite-style column-type tightening: ALTER TABLE … ALTER COLUMN
--    is unsupported, so we rebuild the table with NOT NULL on user_id.
--    Existing rows still satisfy the new constraint after step 1.
CREATE TABLE notifications_new (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     TEXT NOT NULL,
    kind        TEXT NOT NULL,
    severity    TEXT NOT NULL DEFAULT 'info',
    title       TEXT NOT NULL,
    body        TEXT,
    link        TEXT,
    read_at     DATETIME,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

INSERT INTO notifications_new (id, user_id, kind, severity, title, body, link, read_at, created_at)
SELECT id, user_id, kind, severity, title, body, link, read_at, created_at
  FROM notifications;

DROP TABLE notifications;
ALTER TABLE notifications_new RENAME TO notifications;

CREATE INDEX idx_notifications_user_unread
    ON notifications(user_id, read_at) WHERE read_at IS NULL;
CREATE INDEX idx_notifications_user_created
    ON notifications(user_id, created_at);
