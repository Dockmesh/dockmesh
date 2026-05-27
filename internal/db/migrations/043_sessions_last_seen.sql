-- 043_sessions_last_seen.sql
-- Account UI shows "last active 12m ago" per session. We had only
-- created_at on the row, which mis-represented a session that was
-- created days ago but used moments ago (vs. one that was created
-- recently but abandoned). Add last_seen_at + bump it from the JWT
-- middleware on every authenticated request.

ALTER TABLE sessions ADD COLUMN last_seen_at DATETIME;

-- Backfill: treat existing sessions as "last seen at create time" so
-- the column is non-empty for already-online users on first deploy.
UPDATE sessions SET last_seen_at = created_at WHERE last_seen_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_sessions_last_seen ON sessions(user_id, last_seen_at);
