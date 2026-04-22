-- 033_rbac_stack_adopt.sql
-- Grant the new stack.adopt permission to the admin built-in role.
--
-- Context: `stack.adopt` was added to the Go constant map in v0.2.0
-- alongside the "dmctl stack adopt" feature, and RoleAdmin got it in
-- rolePerms. But Store.AllowedDB (the live path taken by the RequirePerm
-- middleware) consults role_permissions in the DB first and only falls
-- back to the Go map if the role isn't cached. Existing installs
-- therefore hit 403 on POST /stacks/adopt because migration 016 seeded
-- admin's permissions ahead of this one being defined.
--
-- Fresh installs are already covered because migration 016 ships with
-- its own INSERT OR IGNORE, but we keep this migration idempotent for
-- future-proofing.
INSERT OR IGNORE INTO role_permissions (role_name, permission) VALUES
    ('admin', 'stack.adopt');
