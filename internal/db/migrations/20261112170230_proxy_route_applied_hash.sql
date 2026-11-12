-- proxy_route_applied_hash
-- The state of each route at Caddy's last successful /load of it. Rows
-- that exist now get a marker the proxy service resolves before its first
-- write: to the hash of their state if Caddy runs them, else empty.
ALTER TABLE proxy_routes ADD COLUMN applied_hash TEXT NOT NULL DEFAULT '';
UPDATE proxy_routes SET applied_hash = '@backfill';
