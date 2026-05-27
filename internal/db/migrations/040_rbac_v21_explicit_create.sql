-- 040_rbac_v21_explicit_create.sql
-- RBAC v2.1: explicit create verb.
--
-- Splits each resource's "create" out of "update". Migration 037
-- granted *.update to roles where the bucket meant "create + edit"
-- (volumes, networks, images, registries, users, roles, backups,
-- proxy, alerts, templates). Now those become explicit *.create
-- entries on the same set of roles. *.update narrows to "edit
-- existing only" — semantically the same on resources where edit was
-- never the focus (volumes / networks / images are immutable in
-- Docker), so existing roles keep working unchanged.
--
-- See project_rbac_v2_spec.md (updated for v2.1 in this slice) and
-- the comparison rationale in conversation: "create implies deploy"
-- mental model — built-in roles grant the natural pairing where
-- relevant (e.g., deployer gets stacks.create + stacks.deploy).

-- ─── viewer: nothing to change (no creates) ─────────────────────────

-- ─── operator: still NO creates (operator deploys existing only) ────
-- Specifically does NOT get stacks.create — only stacks.deploy, which
-- means the operator can re-deploy existing stacks but cannot author
-- new compose files. This matches the user's mental model: "create
-- implies deploy" (deployer-tier or higher), but "deploy alone" is
-- valid (operator-tier).

-- ─── deployer: + stacks.create / images.create / volumes.create /
--      networks.create / templates.create ─────────────────────────────
INSERT OR IGNORE INTO role_permissions (role_name, permission) VALUES
    ('deployer', 'stacks.create'),
    ('deployer', 'images.create'),
    ('deployer', 'volumes.create'),
    ('deployer', 'networks.create'),
    ('deployer', 'templates.create'),
    ('deployer', 'tokens.create');

-- ─── host-admin: + every *.create except users/roles + sensitive ones ─
INSERT OR IGNORE INTO role_permissions (role_name, permission) VALUES
    ('host-admin', 'stacks.create'),
    ('host-admin', 'images.create'),
    ('host-admin', 'volumes.create'),
    ('host-admin', 'networks.create'),
    ('host-admin', 'templates.create'),
    ('host-admin', 'registries.create'),
    ('host-admin', 'hosts.create'),
    ('host-admin', 'backups.create'),
    ('host-admin', 'proxy.create'),
    ('host-admin', 'alerts.create'),
    ('host-admin', 'tokens.create');

-- ─── admin: every *.create ──────────────────────────────────────────
INSERT OR IGNORE INTO role_permissions (role_name, permission) VALUES
    ('admin', 'stacks.create'),
    ('admin', 'images.create'),
    ('admin', 'volumes.create'),
    ('admin', 'networks.create'),
    ('admin', 'templates.create'),
    ('admin', 'registries.create'),
    ('admin', 'hosts.create'),
    ('admin', 'users.create'),
    ('admin', 'roles.create'),
    ('admin', 'backups.create'),
    ('admin', 'proxy.create'),
    ('admin', 'alerts.create'),
    ('admin', 'tokens.create');

-- ─── tokens.update → tokens.create rename (was misnamed) ─────────────
-- The old "tokens.update" perm string actually meant "create your own
-- API token" — that's now properly tokens.create. Drop the old string
-- on every role that had it; tokens.create has been granted above.
DELETE FROM role_permissions WHERE permission = 'tokens.update';
