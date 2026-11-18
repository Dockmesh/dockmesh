-- agent_setup
-- Defaults to 0, so hosts enrolled before this column never wait for setup.
ALTER TABLE agents ADD COLUMN needs_setup INTEGER NOT NULL DEFAULT 0;
-- name_from_host: the host was created without a name and takes the
-- hostname its agent reports at enrollment.
ALTER TABLE agents ADD COLUMN name_from_host INTEGER NOT NULL DEFAULT 0;
-- The stacks directory last asked for and how the agent answered:
-- pending, applied or refused.
ALTER TABLE agents ADD COLUMN stacks_dir_path TEXT;
ALTER TABLE agents ADD COLUMN stacks_dir_status TEXT;
ALTER TABLE agents ADD COLUMN stacks_dir_error TEXT;
