-- stack_targets
-- The host a stack was created for. Until the stack is deployed it has
-- no deployment row, so scope checks use this host instead; once a
-- deployment row exists, that row wins. created_by is the user who
-- created the stack.
CREATE TABLE stack_targets (
    stack_name TEXT     NOT NULL PRIMARY KEY,
    host_id    TEXT     NOT NULL,
    created_by TEXT     NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
