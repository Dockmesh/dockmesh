-- 037 seeded operator with tokens.update and 040 removed it without
-- granting the renamed tokens.create, so operators could not create their
-- own API tokens. 037 also seeded update verbs for images, volumes and
-- networks that no catalog entry defines; they inflated the per-role
-- permission counts without granting anything.
INSERT OR IGNORE INTO role_permissions (role_name, permission) VALUES
    ('operator', 'tokens.create');

DELETE FROM role_permissions
WHERE permission IN ('images.update', 'volumes.update', 'networks.update');
