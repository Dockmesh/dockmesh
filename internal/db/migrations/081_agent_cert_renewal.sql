-- Agents renew their client cert over the live connection. The renewed
-- cert's fingerprint is accepted next to the current one until the agent
-- first connects with it, so a renewal the agent failed to save doesn't
-- lock it out.
ALTER TABLE agents ADD COLUMN pending_cert_fingerprint TEXT;
ALTER TABLE agents ADD COLUMN cert_expires_at DATETIME;
CREATE INDEX idx_agents_pending_cert_fp ON agents(pending_cert_fingerprint);
