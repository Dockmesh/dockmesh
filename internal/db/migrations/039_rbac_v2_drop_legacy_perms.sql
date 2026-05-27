-- 039_rbac_v2_drop_legacy_perms.sql
-- RBAC v2: stage R-5 cutover.
--
-- Removes the legacy permission strings (read / container.control /
-- stack.write / image.write / volume.write / user.manage / audit.read
-- / metrics.read / etc.) from role_permissions now that every router
-- middleware + handler call site has been switched to the new
-- category.verb names. Migration 037 had granted both old and new
-- strings to each built-in role to keep things working during the
-- staged R-1 → R-4 rollout. R-5 finalises the cut.
--
-- Custom roles in user DBs that still reference legacy strings will
-- silently lose those grants after this migration — they will need
-- the new category.verb perms granted instead. v0.3.0 patch notes
-- include the mapping table so admins can update their custom roles.
--
-- Mapping (informational, kept here as the canonical record):
--   read              → containers.view + stacks.view + volumes.view +
--                       networks.view + images.view + registries.view +
--                       hosts.view + users.view + roles.view +
--                       tokens.view + backups.view + proxy.view +
--                       alerts.view + templates.view + audit.view +
--                       system.view + metrics.view
--   container.control → containers.update
--   container.exec    → containers.exec
--   stack.write       → stacks.update
--   stack.deploy      → stacks.deploy
--   stack.adopt       → stacks.adopt
--   image.write       → images.update
--   image.scan        → images.scan
--   network.write     → networks.update
--   volume.write      → volumes.update
--   user.manage       → (decomposed into) users.update + roles.update +
--                       hosts.update + hosts.tag + tokens.manage_others +
--                       backups.update + backups.restore + proxy.update +
--                       alerts.update + registries.update +
--                       settings.update (= system.update) +
--                       audit.write + system.upgrade
--   audit.read        → audit.view
--   metrics.read      → metrics.view

DELETE FROM role_permissions WHERE permission IN (
    'read',
    'container.control',
    'container.exec',
    'stack.write',
    'stack.deploy',
    'stack.adopt',
    'image.write',
    'image.scan',
    'network.write',
    'volume.write',
    'user.manage',
    'audit.read',
    'metrics.read'
);
