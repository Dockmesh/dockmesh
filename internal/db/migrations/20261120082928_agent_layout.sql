-- agent_layout
-- The update layout an agent last reported in its hello. NULL until the
-- agent connects to a version that records it.
ALTER TABLE agents ADD COLUMN layout TEXT;
