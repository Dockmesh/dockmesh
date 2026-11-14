-- agent_daemon_info
-- What the agent's Docker daemon reported in the last hello. NULL means
-- the agent predates the report, so arch and Swarm state are unknown.
ALTER TABLE agents ADD COLUMN daemon_os TEXT;
ALTER TABLE agents ADD COLUMN daemon_arch TEXT;
ALTER TABLE agents ADD COLUMN swarm_node_id TEXT;
ALTER TABLE agents ADD COLUMN swarm_node_state TEXT;
ALTER TABLE agents ADD COLUMN swarm_role TEXT;
ALTER TABLE agents ADD COLUMN swarm_cluster_id TEXT;
