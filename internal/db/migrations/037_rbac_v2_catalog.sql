-- 037_rbac_v2_catalog.sql
-- RBAC v2: stage R-1.
--
-- Brings the existing built-in roles (admin, operator, viewer) up to the
-- new permission catalog while keeping their old permission strings in
-- place — middleware still resolves the old strings until later slices
-- migrate handlers one category at a time. The two new built-in roles
-- (deployer, host-admin) are added with new-shape grants only (no old
-- strings, since they didn't exist before).
--
-- See project_rbac_v2_spec.md memory for the full design.

-- ─── Add the two new built-in roles ──────────────────────────────────
INSERT OR IGNORE INTO roles (name, display, description, builtin) VALUES
    ('deployer',   'Deployer',   'Operate stacks and ship code: deploy + edit compose + pull images. No host or user management.', 1),
    ('host-admin', 'Host Admin', 'Full control of assigned hosts: manage proxy, backups, alerts, registries. No user, role, or audit-config access.', 1);

-- ─── Refresh built-in role descriptions to match new wording ─────────
UPDATE roles SET description = 'Read-only across the fleet. View dashboards, logs, audit log, metrics — no mutations.' WHERE name = 'viewer';
UPDATE roles SET description = 'Day-to-day operations: start/stop containers, view logs and exec into them. No deploy, no destroy.' WHERE name = 'operator';
UPDATE roles SET description = 'Full control of the platform: users, roles, audit config, system updates. The only role that can manage other admins.' WHERE name = 'admin';

-- ─── viewer ─────────────────────────────────────────────────────────
-- All *.view permissions across every category.
INSERT OR IGNORE INTO role_permissions (role_name, permission) VALUES
    ('viewer', 'containers.view'),
    ('viewer', 'stacks.view'),
    ('viewer', 'volumes.view'),
    ('viewer', 'networks.view'),
    ('viewer', 'images.view'),
    ('viewer', 'registries.view'),
    ('viewer', 'hosts.view'),
    ('viewer', 'users.view'),
    ('viewer', 'roles.view'),
    ('viewer', 'tokens.view'),
    ('viewer', 'backups.view'),
    ('viewer', 'proxy.view'),
    ('viewer', 'alerts.view'),
    ('viewer', 'templates.view'),
    ('viewer', 'audit.view'),
    ('viewer', 'system.view'),
    ('viewer', 'metrics.view');

-- ─── operator ────────────────────────────────────────────────────────
-- Viewer perms + container lifecycle + logs/exec + stack deploy.
INSERT OR IGNORE INTO role_permissions (role_name, permission) VALUES
    ('operator', 'containers.view'),
    ('operator', 'containers.update'),
    ('operator', 'containers.exec'),
    ('operator', 'containers.logs'),
    ('operator', 'stacks.view'),
    ('operator', 'stacks.deploy'),
    ('operator', 'volumes.view'),
    ('operator', 'networks.view'),
    ('operator', 'images.view'),
    ('operator', 'images.scan'),
    ('operator', 'registries.view'),
    ('operator', 'hosts.view'),
    ('operator', 'users.view'),
    ('operator', 'roles.view'),
    ('operator', 'tokens.view'),
    ('operator', 'tokens.update'),
    ('operator', 'tokens.delete'),
    ('operator', 'backups.view'),
    ('operator', 'proxy.view'),
    ('operator', 'alerts.view'),
    ('operator', 'templates.view'),
    ('operator', 'audit.view'),
    ('operator', 'system.view'),
    ('operator', 'metrics.view');

-- ─── deployer (NEW) ──────────────────────────────────────────────────
-- Operator + edit compose + pull images + create volumes/networks.
INSERT OR IGNORE INTO role_permissions (role_name, permission) VALUES
    ('deployer', 'containers.view'),
    ('deployer', 'containers.update'),
    ('deployer', 'containers.delete'),
    ('deployer', 'containers.exec'),
    ('deployer', 'containers.logs'),
    ('deployer', 'stacks.view'),
    ('deployer', 'stacks.deploy'),
    ('deployer', 'stacks.update'),
    ('deployer', 'stacks.delete'),
    ('deployer', 'stacks.adopt'),
    ('deployer', 'volumes.view'),
    ('deployer', 'volumes.update'),
    ('deployer', 'networks.view'),
    ('deployer', 'networks.update'),
    ('deployer', 'images.view'),
    ('deployer', 'images.update'),
    ('deployer', 'images.scan'),
    ('deployer', 'registries.view'),
    ('deployer', 'hosts.view'),
    ('deployer', 'users.view'),
    ('deployer', 'roles.view'),
    ('deployer', 'tokens.view'),
    ('deployer', 'tokens.update'),
    ('deployer', 'tokens.delete'),
    ('deployer', 'backups.view'),
    ('deployer', 'proxy.view'),
    ('deployer', 'alerts.view'),
    ('deployer', 'templates.view'),
    ('deployer', 'templates.update'),
    ('deployer', 'audit.view'),
    ('deployer', 'system.view'),
    ('deployer', 'metrics.view');

-- ─── host-admin (NEW) ─────────────────────────────────────────────────
-- Deployer + host-level management (drain/upgrade/tags), proxy CRUD,
-- backups + restore, registries CRUD, alerts CRUD, templates CRUD,
-- audit export. Excludes: users.*, roles.*, audit.write, system.upgrade,
-- tokens.manage_others — those are superadmin-only.
INSERT OR IGNORE INTO role_permissions (role_name, permission) VALUES
    ('host-admin', 'containers.view'),
    ('host-admin', 'containers.update'),
    ('host-admin', 'containers.delete'),
    ('host-admin', 'containers.exec'),
    ('host-admin', 'containers.logs'),
    ('host-admin', 'stacks.view'),
    ('host-admin', 'stacks.deploy'),
    ('host-admin', 'stacks.update'),
    ('host-admin', 'stacks.delete'),
    ('host-admin', 'stacks.migrate'),
    ('host-admin', 'stacks.adopt'),
    ('host-admin', 'volumes.view'),
    ('host-admin', 'volumes.update'),
    ('host-admin', 'volumes.delete'),
    ('host-admin', 'volumes.browse'),
    ('host-admin', 'volumes.read_file'),
    ('host-admin', 'networks.view'),
    ('host-admin', 'networks.update'),
    ('host-admin', 'networks.delete'),
    ('host-admin', 'images.view'),
    ('host-admin', 'images.update'),
    ('host-admin', 'images.delete'),
    ('host-admin', 'images.scan'),
    ('host-admin', 'registries.view'),
    ('host-admin', 'registries.update'),
    ('host-admin', 'registries.delete'),
    ('host-admin', 'hosts.view'),
    ('host-admin', 'hosts.update'),
    ('host-admin', 'hosts.delete'),
    ('host-admin', 'hosts.tag'),
    ('host-admin', 'users.view'),
    ('host-admin', 'roles.view'),
    ('host-admin', 'tokens.view'),
    ('host-admin', 'tokens.update'),
    ('host-admin', 'tokens.delete'),
    ('host-admin', 'backups.view'),
    ('host-admin', 'backups.update'),
    ('host-admin', 'backups.delete'),
    ('host-admin', 'backups.restore'),
    ('host-admin', 'proxy.view'),
    ('host-admin', 'proxy.update'),
    ('host-admin', 'proxy.delete'),
    ('host-admin', 'alerts.view'),
    ('host-admin', 'alerts.update'),
    ('host-admin', 'alerts.delete'),
    ('host-admin', 'templates.view'),
    ('host-admin', 'templates.update'),
    ('host-admin', 'templates.delete'),
    ('host-admin', 'audit.view'),
    ('host-admin', 'audit.export'),
    ('host-admin', 'system.view'),
    ('host-admin', 'system.update'),
    ('host-admin', 'metrics.view');

-- ─── admin (= superadmin, kept under "admin" name for backwards compat) ─
-- Everything. Existing old perm strings stay (middleware compat); new
-- strings are added on top so the new UI matrix lights up correctly.
INSERT OR IGNORE INTO role_permissions (role_name, permission) VALUES
    ('admin', 'containers.view'),
    ('admin', 'containers.update'),
    ('admin', 'containers.delete'),
    ('admin', 'containers.exec'),
    ('admin', 'containers.logs'),
    ('admin', 'stacks.view'),
    ('admin', 'stacks.deploy'),
    ('admin', 'stacks.update'),
    ('admin', 'stacks.delete'),
    ('admin', 'stacks.migrate'),
    ('admin', 'stacks.adopt'),
    ('admin', 'volumes.view'),
    ('admin', 'volumes.update'),
    ('admin', 'volumes.delete'),
    ('admin', 'volumes.browse'),
    ('admin', 'volumes.read_file'),
    ('admin', 'networks.view'),
    ('admin', 'networks.update'),
    ('admin', 'networks.delete'),
    ('admin', 'images.view'),
    ('admin', 'images.update'),
    ('admin', 'images.delete'),
    ('admin', 'images.scan'),
    ('admin', 'registries.view'),
    ('admin', 'registries.update'),
    ('admin', 'registries.delete'),
    ('admin', 'hosts.view'),
    ('admin', 'hosts.update'),
    ('admin', 'hosts.delete'),
    ('admin', 'hosts.tag'),
    ('admin', 'users.view'),
    ('admin', 'users.update'),
    ('admin', 'users.delete'),
    ('admin', 'users.password_reset'),
    ('admin', 'users.suspend'),
    ('admin', 'roles.view'),
    ('admin', 'roles.update'),
    ('admin', 'roles.delete'),
    ('admin', 'tokens.view'),
    ('admin', 'tokens.update'),
    ('admin', 'tokens.delete'),
    ('admin', 'tokens.manage_others'),
    ('admin', 'backups.view'),
    ('admin', 'backups.update'),
    ('admin', 'backups.delete'),
    ('admin', 'backups.restore'),
    ('admin', 'proxy.view'),
    ('admin', 'proxy.update'),
    ('admin', 'proxy.delete'),
    ('admin', 'alerts.view'),
    ('admin', 'alerts.update'),
    ('admin', 'alerts.delete'),
    ('admin', 'templates.view'),
    ('admin', 'templates.update'),
    ('admin', 'templates.delete'),
    ('admin', 'audit.view'),
    ('admin', 'audit.export'),
    ('admin', 'audit.write'),
    ('admin', 'system.view'),
    ('admin', 'system.update'),
    ('admin', 'system.upgrade'),
    ('admin', 'metrics.view');
