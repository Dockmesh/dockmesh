-- 055_alert_mute_schedule.sql
-- Add recurring quiet-hour schedules to alert rules and notification
-- channels. Stored as JSON for flexibility: today only the
-- {"type":"weekly","ranges":[...]} shape is honoured by the engine, but
-- the column can hold richer shapes in the future without further
-- migrations.
--
-- A range example:
--   {"type":"weekly","ranges":[{"days":[1,2,3,4,5],"from":"22:00","to":"06:00"}]}
-- means: mute weekdays from 22:00 until 06:00 the next morning. Days
-- use Go time.Weekday numbering (0 = Sunday, 6 = Saturday).
--
-- mute_schedule complements the existing one-shot `muted_until` field on
-- alert_rules — they OR together: a rule is muted right now if EITHER
-- its muted_until is in the future OR its mute_schedule matches now.

ALTER TABLE alert_rules ADD COLUMN mute_schedule TEXT NOT NULL DEFAULT '';
ALTER TABLE notification_channels ADD COLUMN mute_schedule TEXT NOT NULL DEFAULT '';
