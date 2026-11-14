-- proxy_route_paths
-- Path routes of a site, sorted as Caddy tries them. '[]' serves the
-- site exactly as before.
ALTER TABLE proxy_routes ADD COLUMN paths TEXT NOT NULL DEFAULT '[]';
