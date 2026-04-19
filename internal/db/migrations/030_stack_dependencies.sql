-- 030_stack_dependencies.sql
-- P.12.7: declarative "stack A needs stack B running before it can
-- deploy". Keyed by stack name because stacks live on the filesystem,
-- not in SQL (there is no stacks table to foreign-key against).
--
-- One row per edge: `stack_name` depends on `depends_on`. A stack can
-- depend on many, and be depended on by many. Cycle detection is done
-- in Go on write, not by the schema.
--
-- Cleanup: when a stack is deleted (DeleteStack handler), remove every
-- row where it appears in either column so we don't leave orphan edges
-- pointing at a gone stack.
CREATE TABLE IF NOT EXISTS stack_dependencies (
    stack_name  TEXT NOT NULL,
    depends_on  TEXT NOT NULL,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (stack_name, depends_on),
    CHECK (stack_name <> depends_on)
);

CREATE INDEX IF NOT EXISTS idx_stack_dependencies_depends_on
    ON stack_dependencies(depends_on);
