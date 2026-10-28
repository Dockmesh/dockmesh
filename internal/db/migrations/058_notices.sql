-- Action-required notices: one row per condition (stable key), shown to an
-- audience until the producer resolves it. Audience is evaluated when a
-- user reads, so admins added later still see an open notice.
CREATE TABLE notices (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    key             TEXT NOT NULL UNIQUE,
    audience        TEXT NOT NULL,
    audience_value  TEXT NOT NULL DEFAULT '',
    severity        TEXT NOT NULL,
    title           TEXT NOT NULL,
    body            TEXT NOT NULL DEFAULT '',
    link            TEXT NOT NULL DEFAULT '',
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    resolved_at     DATETIME
);

CREATE INDEX idx_notices_open ON notices(resolved_at) WHERE resolved_at IS NULL;

CREATE TABLE notice_dismissals (
    notice_id     INTEGER NOT NULL REFERENCES notices(id) ON DELETE CASCADE,
    user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    dismissed_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (notice_id, user_id)
);
