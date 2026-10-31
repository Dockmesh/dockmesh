-- notified records whether a fire/resolve went out to channels (false
-- when muted or inside the cooldown), so cooldown and resolve
-- notifications can be decided across restarts.
ALTER TABLE alert_history ADD COLUMN notified INTEGER NOT NULL DEFAULT 1;

-- Delivery outcome of the last send per channel, shown in the UI.
ALTER TABLE notification_channels ADD COLUMN last_sent_at DATETIME;
ALTER TABLE notification_channels ADD COLUMN last_error TEXT NOT NULL DEFAULT '';
ALTER TABLE notification_channels ADD COLUMN last_error_at DATETIME;
