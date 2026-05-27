-- 038_role_scopes.sql
-- RBAC v2: stage R-2.
--
-- role_scopes is the per-role scope table. A role with no scope rows is
-- "unscoped" (= access to everything its permissions allow). Adding rows
-- narrows the role: empty + some host rows = restricted to those hosts;
-- empty + some stack rows = restricted to those stacks; mixing host
-- with stack rows = union ("any of these hosts OR any of these stacks").
-- host_tag rows expand to all hosts that carry that tag at check time.
--
-- See project_rbac_v2_spec.md memory.

CREATE TABLE IF NOT EXISTS role_scopes (
    role_name   TEXT NOT NULL REFERENCES roles(name) ON DELETE CASCADE,
    scope_type  TEXT NOT NULL CHECK (scope_type IN ('host', 'stack', 'host_tag')),
    scope_value TEXT NOT NULL,
    PRIMARY KEY (role_name, scope_type, scope_value)
);

-- Index for the InScope() lookup pattern: given a role, what scopes does
-- it carry? Hot path on every typed-resource request.
CREATE INDEX IF NOT EXISTS idx_role_scopes_lookup
    ON role_scopes(role_name, scope_type);
