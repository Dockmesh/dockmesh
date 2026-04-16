-- 013_stack_deployments.sql
-- P.7: persistent association between stacks and hosts.
--
-- Stacks live on the filesystem (source of truth), not in SQL, so
-- there is no stacks table to reference. Cleanup on stack deletion
-- is handled in Go (DeleteStack handler removes the row).
CREATE TABLE IF NOT EXISTS stack_deployments (
    stack_name  TEXT    NOT NULL PRIMARY KEY,
    host_id     TEXT    NOT NULL,
    status      TEXT    NOT NULL DEFAULT 'deployed',  -- deployed | stopped | migrating | migrated_away
    deployed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_stack_deployments_host ON stack_deployments(host_id);
CREATE INDEX IF NOT EXISTS idx_stack_deployments_status ON stack_deployments(status);
