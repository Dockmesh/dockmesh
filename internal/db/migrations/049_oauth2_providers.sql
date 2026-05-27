-- Generic OAuth2 providers (non-OIDC) — GitHub, Bitbucket, etc.
-- These don't ship an id_token; we trust the userinfo endpoint's
-- response since we got there with a freshly-exchanged access token.
-- Field names follow RFC 6749 + provider conventions (authorization_url,
-- token_url, *_field for JSON paths into the userinfo body) so the
-- backend schema matches the frontend ProviderModal directly.
CREATE TABLE IF NOT EXISTS oauth2_providers (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    slug                TEXT NOT NULL UNIQUE,
    display_name        TEXT NOT NULL,
    authorization_url   TEXT NOT NULL,
    token_url           TEXT NOT NULL,
    userinfo_url        TEXT NOT NULL,
    client_id           TEXT NOT NULL,
    client_secret       TEXT NOT NULL,
    scopes              TEXT NOT NULL DEFAULT '',
    username_field      TEXT NOT NULL DEFAULT 'username',
    email_field         TEXT NOT NULL DEFAULT 'email',
    groups_field        TEXT NOT NULL DEFAULT 'groups',
    default_role        TEXT NOT NULL DEFAULT 'viewer',
    enabled             INTEGER NOT NULL DEFAULT 1,
    is_default          INTEGER NOT NULL DEFAULT 0,
    last_tested_at      DATETIME,
    last_test_ok        INTEGER,
    last_test_error     TEXT,
    created_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS oauth2_provider_group_mappings (
    provider_id INTEGER NOT NULL REFERENCES oauth2_providers(id) ON DELETE CASCADE,
    group_value TEXT NOT NULL,
    role_name   TEXT NOT NULL,
    PRIMARY KEY (provider_id, group_value)
);

ALTER TABLE users ADD COLUMN oauth2_provider TEXT;
ALTER TABLE users ADD COLUMN oauth2_subject  TEXT;
CREATE INDEX IF NOT EXISTS idx_users_oauth2 ON users(oauth2_provider, oauth2_subject);
