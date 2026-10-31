-- Per-site proxy options (performance, routing, HTTPS, access, headers,
-- logs) as one JSON object. '{}' means Caddy's defaults, so existing
-- routes are served unchanged.

ALTER TABLE proxy_routes ADD COLUMN options TEXT NOT NULL DEFAULT '{}';
