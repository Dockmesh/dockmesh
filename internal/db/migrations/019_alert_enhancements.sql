-- 019_alert_enhancements.sql
-- P2/P3 alert features: severity, cooldown, mute/silence.
ALTER TABLE alert_rules ADD COLUMN severity TEXT NOT NULL DEFAULT 'warning';
ALTER TABLE alert_rules ADD COLUMN cooldown_seconds INTEGER NOT NULL DEFAULT 300;
ALTER TABLE alert_rules ADD COLUMN muted_until DATETIME;
