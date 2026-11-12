-- proxy_route_applied_hash
-- The state of each route at Caddy's last successful /load of it. Routes
-- that exist now were loaded as they are; the marker makes the proxy
-- service record the hash of their current state before its first write.
ALTER TABLE proxy_routes ADD COLUMN applied_hash TEXT NOT NULL DEFAULT '';
UPDATE proxy_routes SET applied_hash = '@backfill';
