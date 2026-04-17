-- 022_host_tags.sql
-- Host tags: arbitrary string labels attached to hosts (agent IDs or the
-- special "local" ID for the embedded daemon). Tags are the primitive
-- for RBAC scoping (P.11.3), alert targeting, backup job scoping, and
-- stack placement hints.
--
-- host_id is intentionally a plain TEXT — it can be either "local"
-- (the embedded docker.sock) or an agent ID (stringified INTEGER from
-- the agents table). We don't use a foreign key because the "local"
-- host isn't a row in any table; a check in the service layer
-- validates the host exists before inserting.
CREATE TABLE IF NOT EXISTS host_tags (
    host_id     TEXT    NOT NULL,
    tag         TEXT    NOT NULL,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (host_id, tag),
    CHECK (tag GLOB '[a-z0-9]*' AND length(tag) BETWEEN 1 AND 32)
);

-- Index for the "all hosts with tag X" query pattern used by RBAC
-- scoping and backup-job fan-out.
CREATE INDEX IF NOT EXISTS idx_host_tags_tag ON host_tags(tag);
