-- Archive downloads get their own permission instead of riding on
-- backups.view, container file access moves to containers.exec, system
-- archives and the backup key become admin-only, and the built-in
-- host-admin role loses system.update (global settings, SSO, global env).
--
-- Custom roles that could restore keep downloading archives. Admins are
-- told which custom roles lost something so nothing changes silently.

INSERT INTO notices (key, audience, audience_value, severity, title, body, link)
SELECT 'rbac-custom-roles-narrowed', 'admins', '', 'warning',
       'Custom roles lost permissions in this update',
       'Backup archive downloads now need "Download backups", container file access needs "Shell into container", and system backups and the backup key are admin-only. Review these custom roles and grant the new permissions where intended.'
       || CASE WHEN dl <> '' THEN char(10) || '- No longer download backup archives: ' || dl ELSE '' END
       || CASE WHEN files <> '' THEN char(10) || '- No longer read container files: ' || files ELSE '' END
       || CASE WHEN sys <> '' THEN char(10) || '- No longer download system backups or export the backup key: ' || sys ELSE '' END,
       '/users'
  FROM (
    SELECT
      COALESCE((SELECT group_concat(name, ', ') FROM roles r WHERE builtin = 0
                 AND EXISTS (SELECT 1 FROM role_permissions p WHERE p.role_name = r.name AND p.permission = 'backups.view')
                 AND NOT EXISTS (SELECT 1 FROM role_permissions p WHERE p.role_name = r.name AND p.permission = 'backups.restore')), '') AS dl,
      COALESCE((SELECT group_concat(name, ', ') FROM roles r WHERE builtin = 0
                 AND EXISTS (SELECT 1 FROM role_permissions p WHERE p.role_name = r.name AND p.permission = 'containers.view')
                 AND NOT EXISTS (SELECT 1 FROM role_permissions p WHERE p.role_name = r.name AND p.permission = 'containers.exec')), '') AS files,
      COALESCE((SELECT group_concat(name, ', ') FROM roles r WHERE builtin = 0
                 AND EXISTS (SELECT 1 FROM role_permissions p WHERE p.role_name = r.name AND p.permission = 'backups.restore')
                 AND NOT EXISTS (SELECT 1 FROM role_permissions p WHERE p.role_name = r.name AND p.permission = 'system.update')), '') AS sys
  )
 WHERE dl <> '' OR files <> '' OR sys <> '';

INSERT OR IGNORE INTO role_permissions (role_name, permission)
SELECT p.role_name, 'backups.download'
  FROM role_permissions p JOIN roles r ON r.name = p.role_name
 WHERE r.builtin = 0 AND p.permission = 'backups.restore';

INSERT OR IGNORE INTO role_permissions (role_name, permission) VALUES
    ('admin', 'backups.download'),
    ('host-admin', 'backups.download');

INSERT INTO notices (key, audience, audience_value, severity, title, body, link)
SELECT 'rbac-host-admin-no-system-settings', 'admins', '', 'info',
       'Host admins no longer change system settings',
       'The built-in Host Admin role manages its own hosts only. It can no longer edit system settings, global environment variables, password and sign-in policy or SSO providers, and no longer sees admin notices. Users who need that must be given the Admin role.',
       '/users'
 WHERE EXISTS (SELECT 1 FROM users WHERE role = 'host-admin')
   AND EXISTS (SELECT 1 FROM role_permissions WHERE role_name = 'host-admin' AND permission = 'system.update');

DELETE FROM role_permissions WHERE role_name = 'host-admin' AND permission = 'system.update';

UPDATE roles SET description = 'Full control of assigned hosts: manage proxy, backups, alerts, registries. No user, role, audit-config or system-settings access.'
 WHERE name = 'host-admin' AND builtin = 1;
