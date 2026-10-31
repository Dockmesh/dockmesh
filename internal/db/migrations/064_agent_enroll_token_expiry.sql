-- Enrollment tokens expire. Tokens that were already outstanding get a
-- fresh grace window instead of being invalidated by the upgrade.
ALTER TABLE agents ADD COLUMN enrollment_token_expires_at DATETIME;
UPDATE agents
   SET enrollment_token_expires_at = datetime('now', '+24 hours')
 WHERE enrollment_token_hash IS NOT NULL;
