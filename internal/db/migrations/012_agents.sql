-- Remote agents (concept §3.1, §15.3). Each agent represents one external
-- docker host that connects outbound via mTLS. Enrollment is a two-phase
-- flow: an admin creates a row with a one-time token, the agent presents
-- the token to /agents/enroll and exchanges it for a client cert, the
-- token hash is wiped and the cert fingerprint is stored.
CREATE TABLE IF NOT EXISTS agents (
    id                    TEXT PRIMARY KEY,
    name                  TEXT NOT NULL UNIQUE,
    enrollment_token_hash TEXT,                -- SHA-256 hex; NULL once enrolled
    cert_fingerprint      TEXT,                -- SHA-256 hex of the client cert SPKI
    status                TEXT NOT NULL DEFAULT 'pending', -- pending | online | offline | revoked
    version               TEXT,
    os                    TEXT,
    arch                  TEXT,
    hostname              TEXT,
    docker_version        TEXT,
    last_seen_at          DATETIME,
    created_at            DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at            DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_agents_fingerprint ON agents(cert_fingerprint);
CREATE INDEX IF NOT EXISTS idx_agents_status ON agents(status);
