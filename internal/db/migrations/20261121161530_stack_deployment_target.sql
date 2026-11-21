-- stack_deployment_target
-- A deployment names its target: a host or a Swarm cluster. Every existing
-- row is a host deployment with its target id equal to its host id. The
-- key is (stack, target) so several targets per stack only need the
-- unique index on stack_name dropped; until then a stack has one target.
-- host_id stays the host a host deployment runs on and is empty for a
-- cluster, so host-only code never mistakes a cluster for a host.
CREATE TABLE stack_deployments_new (
    stack_name  TEXT     NOT NULL,
    target_kind TEXT     NOT NULL DEFAULT 'host' CHECK (target_kind IN ('host', 'cluster')),
    target_id   TEXT     NOT NULL,
    host_id     TEXT     NOT NULL,
    status      TEXT     NOT NULL DEFAULT 'deployed',
    deployed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    delete_stop INTEGER  NOT NULL DEFAULT 0,
    PRIMARY KEY (stack_name, target_kind, target_id),
    CHECK ((target_kind = 'host' AND host_id = target_id) OR (target_kind = 'cluster' AND host_id = ''))
);

INSERT INTO stack_deployments_new (stack_name, target_kind, target_id, host_id, status, deployed_at, updated_at, delete_stop)
SELECT stack_name, 'host', host_id, host_id, status, deployed_at, updated_at, delete_stop FROM stack_deployments;

DROP TABLE stack_deployments;
ALTER TABLE stack_deployments_new RENAME TO stack_deployments;
CREATE UNIQUE INDEX idx_stack_deployments_one_target ON stack_deployments(stack_name);
CREATE INDEX idx_stack_deployments_host ON stack_deployments(host_id);
CREATE INDEX idx_stack_deployments_status ON stack_deployments(status);
CREATE INDEX idx_stack_deployments_target ON stack_deployments(target_kind, target_id);
