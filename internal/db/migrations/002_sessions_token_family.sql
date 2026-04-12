-- Switch sessions to token-family refresh rotation.
-- The MVP schema had one row per refresh-hash; we now keep one row per
-- family and track the current sequence number directly.
DROP TABLE IF EXISTS sessions;

CREATE TABLE sessions (
    family_id    TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    current_seq  INTEGER NOT NULL DEFAULT 0,
    user_agent   TEXT,
    ip           TEXT,
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at   DATETIME NOT NULL,
    revoked_at   DATETIME
);

CREATE INDEX idx_sessions_user ON sessions(user_id);
