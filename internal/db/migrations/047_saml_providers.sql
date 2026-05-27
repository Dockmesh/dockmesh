-- SAML 2.0 Service Provider config. Mirrors oidc_providers in shape so
-- the frontend Provider-Modal can render both kinds uniformly.
--
-- entity_id is the IdP entity-ID we expect on inbound assertions
-- (matched against assertion.Issuer). idp_metadata_xml is the raw
-- metadata blob the admin uploaded — we re-parse it on every login so
-- a cert rotation at the IdP only needs a fresh metadata paste, not a
-- code change. Field names follow OASIS SAML 2.0 terminology
-- (*_attribute, nameid_format) so they line up with the frontend
-- without a translation layer.
CREATE TABLE IF NOT EXISTS saml_providers (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    slug                TEXT NOT NULL UNIQUE,
    display_name        TEXT NOT NULL,
    entity_id           TEXT NOT NULL,
    sso_url             TEXT NOT NULL,
    slo_url             TEXT,
    idp_metadata_xml    TEXT NOT NULL,
    idp_cert_pem        TEXT NOT NULL,
    nameid_format       TEXT NOT NULL DEFAULT 'urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress',
    username_attribute  TEXT NOT NULL DEFAULT 'NameID',
    email_attribute     TEXT NOT NULL DEFAULT 'email',
    groups_attribute    TEXT NOT NULL DEFAULT 'groups',
    default_role        TEXT NOT NULL DEFAULT 'viewer',
    enabled             INTEGER NOT NULL DEFAULT 1,
    is_default          INTEGER NOT NULL DEFAULT 0,
    last_tested_at      DATETIME,
    last_test_ok        INTEGER,
    last_test_error     TEXT,
    created_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS saml_provider_group_mappings (
    provider_id INTEGER NOT NULL REFERENCES saml_providers(id) ON DELETE CASCADE,
    group_value TEXT NOT NULL,
    role_name   TEXT NOT NULL,
    PRIMARY KEY (provider_id, group_value)
);

-- Link the local user row to the SAML identity so subsequent logins
-- find the same account. saml_subject holds the NameID; provider is
-- the slug. Mirrors oidc_provider/oidc_subject on users.
ALTER TABLE users ADD COLUMN saml_provider TEXT;
ALTER TABLE users ADD COLUMN saml_subject  TEXT;
CREATE INDEX IF NOT EXISTS idx_users_saml ON users(saml_provider, saml_subject);

-- Process-wide SP signing keypair. We hold this once for the install
-- rather than per-provider — same Dockmesh, same SP identity across
-- every IdP it integrates with. Stored as PEM so it survives backups
-- via the normal db tarball.
CREATE TABLE IF NOT EXISTS saml_sp_keys (
    id          INTEGER PRIMARY KEY CHECK (id = 1),
    key_pem     TEXT NOT NULL,
    cert_pem    TEXT NOT NULL,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
