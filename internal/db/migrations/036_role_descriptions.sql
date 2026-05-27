-- 036_role_descriptions.sql
-- Add a human-readable description column to roles so the new Roles UI
-- can render a one-line blurb under each role's name (admin / operator
-- / viewer / custom). The Permissions catalog in the API has been
-- richified separately with category + verb + danger_level metadata
-- — that one is per-handler, no DB column needed.
ALTER TABLE roles ADD COLUMN description TEXT NOT NULL DEFAULT '';

UPDATE roles SET description = 'Full control of the platform. Create, configure, destroy, manage users.' WHERE name = 'admin' AND description = '';
UPDATE roles SET description = 'Deploy and operate stacks. No editing of compose files, no user management.' WHERE name = 'operator' AND description = '';
UPDATE roles SET description = 'Read-only across the fleet. View dashboards, logs, audit log, metrics.' WHERE name = 'viewer' AND description = '';
