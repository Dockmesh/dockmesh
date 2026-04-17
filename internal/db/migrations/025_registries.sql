-- P.11.7 — Registry credentials storage.
--
-- Passwords are age-encrypted at rest via the existing secrets service
-- (same key as stack .env.age). scope_tags uses the same JSON-array
-- string encoding as users.scope_tags so the same OR-matching helper
-- (rbac.ScopeMatchesHost) can be reused for "which registry credentials
-- apply to this host?".
CREATE TABLE IF NOT EXISTS registries (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    name               TEXT NOT NULL,
    url                TEXT NOT NULL,
    username           TEXT,
    password_encrypted BLOB,
    scope_tags         TEXT,
    last_tested_at     DATETIME,
    last_test_ok       INTEGER,
    last_test_error    TEXT,
    created_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (url)
);

CREATE INDEX IF NOT EXISTS idx_registries_url ON registries(url);
