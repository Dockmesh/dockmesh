-- LDAP / Active Directory directory binding. Unlike OIDC/SAML the
-- "login flow" is a direct bind from Dockmesh to the LDAP server with
-- the user's plaintext password, so the schema also holds the
-- service-account bind DN + password used to look up the user's DN
-- before that second bind.
--
-- Field names follow LDAP convention (*_attribute, user_search_*,
-- group_*) so they line up 1:1 with the frontend ProviderModal.
-- tls_mode collapses ldaps / starttls / none into one enum column —
-- the previous two-bool layout invited inconsistent state (both true,
-- neither true).
CREATE TABLE IF NOT EXISTS ldap_providers (
    id                          INTEGER PRIMARY KEY AUTOINCREMENT,
    slug                        TEXT NOT NULL UNIQUE,
    display_name                TEXT NOT NULL,
    host                        TEXT NOT NULL,
    port                        INTEGER NOT NULL DEFAULT 636,
    tls_mode                    TEXT NOT NULL DEFAULT 'ldaps',  -- ldaps | starttls | none
    skip_verify                 INTEGER NOT NULL DEFAULT 0,
    bind_dn                     TEXT,
    bind_password               TEXT,  -- encrypted at rest when secrets enabled
    user_search_base            TEXT NOT NULL,
    user_search_filter          TEXT NOT NULL DEFAULT '(&(objectClass=person)(uid=%s))',
    username_attribute          TEXT NOT NULL DEFAULT 'uid',
    email_attribute             TEXT NOT NULL DEFAULT 'mail',
    group_search_base           TEXT,
    group_membership_attribute  TEXT NOT NULL DEFAULT 'memberOf',
    default_role                TEXT NOT NULL DEFAULT 'viewer',
    enabled                     INTEGER NOT NULL DEFAULT 1,
    is_default                  INTEGER NOT NULL DEFAULT 0,
    last_tested_at              DATETIME,
    last_test_ok                INTEGER,
    last_test_error             TEXT,
    created_at                  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at                  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS ldap_provider_group_mappings (
    provider_id INTEGER NOT NULL REFERENCES ldap_providers(id) ON DELETE CASCADE,
    group_value TEXT NOT NULL,
    role_name   TEXT NOT NULL,
    PRIMARY KEY (provider_id, group_value)
);

-- LDAP-linked users: subject is the user's DN as returned by the
-- initial search bind.
ALTER TABLE users ADD COLUMN ldap_provider TEXT;
ALTER TABLE users ADD COLUMN ldap_subject  TEXT;
CREATE INDEX IF NOT EXISTS idx_users_ldap ON users(ldap_provider, ldap_subject);
