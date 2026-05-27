-- 044_template_metadata.sql
-- Templates UI was designed with categories, featured rail, and a
-- popular sort that lean on three new columns we hadn't introduced
-- yet on the stack_templates table.

ALTER TABLE stack_templates ADD COLUMN category TEXT NOT NULL DEFAULT '';
ALTER TABLE stack_templates ADD COLUMN featured INTEGER NOT NULL DEFAULT 0;
ALTER TABLE stack_templates ADD COLUMN deploys_count INTEGER NOT NULL DEFAULT 0;

-- Seed sensible categories on the built-in templates we recognise so
-- the UI sidebar isn't all "uncategorised" on fresh installs.
UPDATE stack_templates SET category = 'databases'    WHERE slug IN ('postgres', 'postgres-16', 'postgresql', 'mariadb', 'mysql', 'redis', 'mongodb');
UPDATE stack_templates SET category = 'monitoring'   WHERE slug IN ('grafana', 'prometheus', 'loki', 'uptime-kuma');
UPDATE stack_templates SET category = 'productivity' WHERE slug IN ('nextcloud', 'paperless', 'bookstack', 'vaultwarden');
UPDATE stack_templates SET category = 'media'        WHERE slug IN ('jellyfin', 'plex', 'navidrome', 'audiobookshelf');
UPDATE stack_templates SET category = 'automation'   WHERE slug IN ('n8n', 'home-assistant', 'mosquitto');
UPDATE stack_templates SET category = 'developer'    WHERE slug IN ('gitea', 'gitlab', 'drone', 'verdaccio', 'minio');
UPDATE stack_templates SET category = 'security'     WHERE slug IN ('authelia', 'authentik', 'keycloak', 'cloudflared');

CREATE INDEX IF NOT EXISTS idx_stack_templates_category ON stack_templates(category);
CREATE INDEX IF NOT EXISTS idx_stack_templates_featured ON stack_templates(featured) WHERE featured = 1;
