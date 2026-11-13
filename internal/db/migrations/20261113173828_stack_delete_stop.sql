-- stack_delete_stop
-- A delete that waits for an offline agent keeps whether the user asked
-- to stop the containers, so the delete stops them on reconnect.
ALTER TABLE stack_deployments ADD COLUMN delete_stop INTEGER NOT NULL DEFAULT 0;
