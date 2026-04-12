-- Caddy reverse proxy routes (concept §2.6). One row per host name;
-- the proxy service compiles the table into a Caddy JSON config and
-- pushes it to the admin API on every change.
CREATE TABLE IF NOT EXISTS proxy_routes (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    host         TEXT NOT NULL UNIQUE,
    upstream     TEXT NOT NULL,                 -- e.g. 127.0.0.1:8080
    tls_mode     TEXT NOT NULL DEFAULT 'auto',  -- auto | internal | none
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_proxy_host ON proxy_routes(host);
