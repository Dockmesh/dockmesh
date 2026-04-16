-- 016_custom_roles.sql
-- RBAC v2: DB-backed custom roles. Seed the three built-in roles so
-- existing users keep working. Custom roles added via the API get their
-- own rows.
CREATE TABLE IF NOT EXISTS roles (
    name        TEXT NOT NULL PRIMARY KEY,
    display     TEXT NOT NULL,
    builtin     INTEGER NOT NULL DEFAULT 0,  -- 1 for admin/operator/viewer
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS role_permissions (
    role_name   TEXT NOT NULL REFERENCES roles(name) ON DELETE CASCADE,
    permission  TEXT NOT NULL,
    PRIMARY KEY (role_name, permission)
);

-- Seed built-in roles.
INSERT OR IGNORE INTO roles (name, display, builtin) VALUES
    ('admin',    'Admin',    1),
    ('operator', 'Operator', 1),
    ('viewer',   'Viewer',   1);

-- Seed admin permissions.
INSERT OR IGNORE INTO role_permissions (role_name, permission) VALUES
    ('admin', 'read'),
    ('admin', 'container.control'),
    ('admin', 'container.exec'),
    ('admin', 'stack.write'),
    ('admin', 'stack.deploy'),
    ('admin', 'image.write'),
    ('admin', 'image.scan'),
    ('admin', 'network.write'),
    ('admin', 'volume.write'),
    ('admin', 'user.manage'),
    ('admin', 'audit.read');

-- Seed operator permissions.
INSERT OR IGNORE INTO role_permissions (role_name, permission) VALUES
    ('operator', 'read'),
    ('operator', 'container.control'),
    ('operator', 'container.exec'),
    ('operator', 'stack.deploy'),
    ('operator', 'image.scan'),
    ('operator', 'audit.read');

-- Seed viewer permissions.
INSERT OR IGNORE INTO role_permissions (role_name, permission) VALUES
    ('viewer', 'read');
