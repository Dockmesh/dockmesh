-- rbac_assignments
-- Roles become levels per area plus extras, and access moves from
-- users.role + users.scope_tags + role_scopes onto assignments of a role
-- on a scope. This file creates the new tables; the data move, the admin
-- notice and dropping the old tables and columns run right after it in
-- the same transaction (internal/rbac/migrate.go).

CREATE TABLE role_levels (
    role_name TEXT NOT NULL REFERENCES roles(name) ON DELETE CASCADE,
    area      TEXT NOT NULL,
    level     TEXT NOT NULL,
    PRIMARY KEY (role_name, area)
);

CREATE TABLE role_extras (
    role_name TEXT NOT NULL REFERENCES roles(name) ON DELETE CASCADE,
    extra     TEXT NOT NULL,
    PRIMARY KEY (role_name, extra)
);

-- scope_host is the host id of host and stack scopes; scope_value is the
-- host tag or the stack name. source is 'manual' or 'idp' (written by an
-- identity provider's group mapping at sign-in); via names the group.
CREATE TABLE role_assignments (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role_name   TEXT NOT NULL REFERENCES roles(name),
    scope_type  TEXT NOT NULL CHECK (scope_type IN ('global', 'host', 'host_tag', 'stack')),
    scope_host  TEXT NOT NULL DEFAULT '',
    scope_value TEXT NOT NULL DEFAULT '',
    source      TEXT NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'idp')),
    via         TEXT NOT NULL DEFAULT '',
    created_by  TEXT,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (user_id, role_name, scope_type, scope_host, scope_value, source, via)
);

CREATE INDEX idx_role_assignments_role ON role_assignments(role_name);

-- An API token may be restricted to a role on a scope; its access is the
-- owner's current access narrowed by that. Empty role = no restriction.
ALTER TABLE api_tokens ADD COLUMN scope_type  TEXT NOT NULL DEFAULT 'global';
ALTER TABLE api_tokens ADD COLUMN scope_host  TEXT NOT NULL DEFAULT '';
ALTER TABLE api_tokens ADD COLUMN scope_value TEXT NOT NULL DEFAULT '';

-- Invites carry the assignments the new account gets.
ALTER TABLE invite_tokens ADD COLUMN scopes_json TEXT NOT NULL DEFAULT '[{"type":"global"}]';

-- Group mappings get a scope and may map one group to several roles.
CREATE TABLE oidc_provider_group_mappings_new (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    provider_id INTEGER NOT NULL REFERENCES oidc_providers(id) ON DELETE CASCADE,
    group_value TEXT NOT NULL,
    role_name   TEXT NOT NULL,
    scope_type  TEXT NOT NULL DEFAULT 'global',
    scope_host  TEXT NOT NULL DEFAULT '',
    scope_value TEXT NOT NULL DEFAULT '',
    UNIQUE (provider_id, group_value, role_name, scope_type, scope_host, scope_value)
);
INSERT INTO oidc_provider_group_mappings_new (provider_id, group_value, role_name)
SELECT provider_id, group_value, role_name FROM oidc_provider_group_mappings;
DROP TABLE oidc_provider_group_mappings;
ALTER TABLE oidc_provider_group_mappings_new RENAME TO oidc_provider_group_mappings;

CREATE TABLE saml_provider_group_mappings_new (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    provider_id INTEGER NOT NULL REFERENCES saml_providers(id) ON DELETE CASCADE,
    group_value TEXT NOT NULL,
    role_name   TEXT NOT NULL,
    scope_type  TEXT NOT NULL DEFAULT 'global',
    scope_host  TEXT NOT NULL DEFAULT '',
    scope_value TEXT NOT NULL DEFAULT '',
    UNIQUE (provider_id, group_value, role_name, scope_type, scope_host, scope_value)
);
INSERT INTO saml_provider_group_mappings_new (provider_id, group_value, role_name)
SELECT provider_id, group_value, role_name FROM saml_provider_group_mappings;
DROP TABLE saml_provider_group_mappings;
ALTER TABLE saml_provider_group_mappings_new RENAME TO saml_provider_group_mappings;

CREATE TABLE ldap_provider_group_mappings_new (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    provider_id INTEGER NOT NULL REFERENCES ldap_providers(id) ON DELETE CASCADE,
    group_value TEXT NOT NULL,
    role_name   TEXT NOT NULL,
    scope_type  TEXT NOT NULL DEFAULT 'global',
    scope_host  TEXT NOT NULL DEFAULT '',
    scope_value TEXT NOT NULL DEFAULT '',
    UNIQUE (provider_id, group_value, role_name, scope_type, scope_host, scope_value)
);
INSERT INTO ldap_provider_group_mappings_new (provider_id, group_value, role_name)
SELECT provider_id, group_value, role_name FROM ldap_provider_group_mappings;
DROP TABLE ldap_provider_group_mappings;
ALTER TABLE ldap_provider_group_mappings_new RENAME TO ldap_provider_group_mappings;

CREATE TABLE oauth2_provider_group_mappings_new (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    provider_id INTEGER NOT NULL REFERENCES oauth2_providers(id) ON DELETE CASCADE,
    group_value TEXT NOT NULL,
    role_name   TEXT NOT NULL,
    scope_type  TEXT NOT NULL DEFAULT 'global',
    scope_host  TEXT NOT NULL DEFAULT '',
    scope_value TEXT NOT NULL DEFAULT '',
    UNIQUE (provider_id, group_value, role_name, scope_type, scope_host, scope_value)
);
INSERT INTO oauth2_provider_group_mappings_new (provider_id, group_value, role_name)
SELECT provider_id, group_value, role_name FROM oauth2_provider_group_mappings;
DROP TABLE oauth2_provider_group_mappings;
ALTER TABLE oauth2_provider_group_mappings_new RENAME TO oauth2_provider_group_mappings;
