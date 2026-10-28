-- API tokens are bound to the user who created them. The owner column was
-- declared INTEGER while users.id is a TEXT UUID; rebuild it as TEXT.
-- Tokens whose creator can't be resolved (never recorded, or the user is
-- gone) are marked legacy: they keep working with their pinned role and
-- admins are asked to recreate them.
CREATE TABLE api_tokens_new (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    token_prefix       TEXT    NOT NULL UNIQUE,
    token_hash         TEXT    NOT NULL,
    name               TEXT    NOT NULL,
    role               TEXT    NOT NULL,
    created_by_user_id TEXT    REFERENCES users(id) ON DELETE SET NULL,
    legacy             INTEGER NOT NULL DEFAULT 0,
    created_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at         DATETIME,
    last_used_at       DATETIME,
    last_used_ip       TEXT,
    revoked_at         DATETIME,
    CHECK (token_prefix LIKE 'dmt_%')
);

INSERT INTO api_tokens_new (id, token_prefix, token_hash, name, role, created_by_user_id, legacy,
                            created_at, expires_at, last_used_at, last_used_ip, revoked_at)
SELECT id, token_prefix, token_hash, name, role,
       CASE WHEN CAST(created_by_user_id AS TEXT) IN (SELECT id FROM users)
            THEN CAST(created_by_user_id AS TEXT) END,
       CASE WHEN CAST(created_by_user_id AS TEXT) IN (SELECT id FROM users) THEN 0 ELSE 1 END,
       created_at, expires_at, last_used_at, last_used_ip, revoked_at
  FROM api_tokens;

DROP TABLE api_tokens;
ALTER TABLE api_tokens_new RENAME TO api_tokens;

CREATE INDEX idx_api_tokens_active ON api_tokens(revoked_at) WHERE revoked_at IS NULL;
CREATE INDEX idx_api_tokens_owner ON api_tokens(created_by_user_id);
