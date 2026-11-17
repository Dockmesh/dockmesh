-- agent_setup
-- needs_setup is only ever set by the create path of this version, so
-- hosts enrolled before it keep 0 and never wait for setup.
ALTER TABLE agents ADD COLUMN needs_setup INTEGER NOT NULL DEFAULT 0;
-- name_from_host: the host was created without a name and takes the
-- hostname its agent reports at enrollment.
ALTER TABLE agents ADD COLUMN name_from_host INTEGER NOT NULL DEFAULT 0;
-- The stacks directory asked for through the API or the Setup tab and how
-- the agent answered: queued, pending, applied or refused.
ALTER TABLE agents ADD COLUMN stacks_dir_path TEXT;
ALTER TABLE agents ADD COLUMN stacks_dir_status TEXT;
ALTER TABLE agents ADD COLUMN stacks_dir_error TEXT;
