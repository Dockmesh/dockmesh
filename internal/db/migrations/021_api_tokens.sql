-- 021_api_tokens.sql
-- Long-lived API tokens for CI/CD and external integrations. Unlike
-- the short-lived user JWTs, these don't expire by default and carry
-- a pinned role at creation time. Token value is argon2id-hashed; the
-- prefix (first 12 chars, "dmt_XXXXXXXX") is stored in cleartext for
-- display in the UI and to help humans identify tokens in logs.
CREATE TABLE IF NOT EXISTS api_tokens (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    token_prefix       TEXT    NOT NULL UNIQUE,          -- 'dmt_' + first 8 raw chars, shown in UI
    token_hash         TEXT    NOT NULL,                 -- argon2id hash of the plaintext
    name               TEXT    NOT NULL,                 -- user label, e.g. "github-actions"
    role               TEXT    NOT NULL,                 -- role name this token authenticates as
    created_by_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at         DATETIME,                         -- NULL = no expiry
    last_used_at       DATETIME,
    last_used_ip       TEXT,
    revoked_at         DATETIME,
    CHECK (token_prefix LIKE 'dmt_%')
);

CREATE INDEX IF NOT EXISTS idx_api_tokens_prefix ON api_tokens(token_prefix);
CREATE INDEX IF NOT EXISTS idx_api_tokens_active ON api_tokens(revoked_at)
    WHERE revoked_at IS NULL;
