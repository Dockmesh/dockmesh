-- proxy_enabled_unsaved_default
-- A fresh database holds no saved proxy choice yet, so the seeded 'false'
-- becomes empty and DOCKMESH_PROXY_ENABLED decides the first start.
-- Existing installs keep their value: it may be a choice saved in the UI.
UPDATE settings SET value = ''
WHERE key = 'proxy_enabled' AND value = 'false'
  AND NOT EXISTS (SELECT 1 FROM users);
