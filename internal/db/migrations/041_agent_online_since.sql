-- 041_agent_online_since.sql
-- Track when an agent transitioned into "online" so the UI can render a
-- real "up Xh / Xd" value next to the status pill on the Hosts page.
--
-- Set on each online transition (markOnline), cleared on offline
-- (markOffline). For agents currently online when this migration runs
-- we backfill from last_seen_at — best-effort, gives the right order of
-- magnitude until the next reconnect updates the value precisely.
ALTER TABLE agents ADD COLUMN online_since DATETIME;

UPDATE agents
   SET online_since = last_seen_at
 WHERE status = 'online'
   AND online_since IS NULL
   AND last_seen_at IS NOT NULL;
