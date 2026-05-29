-- 054_proxy_route_enabled.sql
-- Add a per-route enabled flag so operators can disable a route without
-- deleting it (Draft-vs-Applied pattern from the v2 proxy mockup).
-- Disabled routes stay in the table for editing but get filtered out
-- when the Caddyfile is regenerated, so traffic stops flowing without
-- losing the host/upstream/TLS-mode configuration.

ALTER TABLE proxy_routes ADD COLUMN enabled INTEGER NOT NULL DEFAULT 1;
