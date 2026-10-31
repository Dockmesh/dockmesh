-- Per-site proxy options (performance, routing, HTTPS, access, headers,
-- logs) as one JSON object. '{}' means Caddy's defaults, which is what
-- every route got before the column existed.

ALTER TABLE proxy_routes ADD COLUMN options TEXT NOT NULL DEFAULT '{}';
