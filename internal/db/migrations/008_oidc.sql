-- OIDC / SSO providers (§2.4). Client secrets are encrypted via the
-- secrets service when enabled.
CREATE TABLE IF NOT EXISTS oidc_providers (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    slug           TEXT NOT NULL UNIQUE,         -- /auth/oidc/{slug}/login
    display_name   TEXT NOT NULL,
    issuer_url     TEXT NOT NULL,
    client_id      TEXT NOT NULL,
    client_secret  TEXT NOT NULL,                -- ciphertext when secrets enabled
    scopes         TEXT NOT NULL DEFAULT 'openid,profile,email',
    group_claim    TEXT,                         -- claim name holding group memberships, e.g. "groups"
    admin_group    TEXT,                         -- exact match → role=admin
    operator_group TEXT,                         -- exact match → role=operator
    default_role   TEXT NOT NULL DEFAULT 'viewer',
    enabled        INTEGER NOT NULL DEFAULT 1,
    created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Link a local user to their SSO identity so subsequent logins find the
-- same row. subject is the stable "sub" claim; provider references slug.
ALTER TABLE users ADD COLUMN oidc_provider TEXT;
ALTER TABLE users ADD COLUMN oidc_subject  TEXT;
CREATE INDEX IF NOT EXISTS idx_users_oidc ON users(oidc_provider, oidc_subject);
