-- 042_user_profile_fields.sql
-- Account UI rebuild surfaced fields the schema didn't track yet.
-- password_changed_at already lives on the users table (migration 028),
-- so this slice only adds the two new columns:
--
-- - display_name: editable label shown above the username. Empty by
--   default so existing users render unchanged until they fill it in.
-- - last_login_at: populated on every successful sign-in. Avoids
--   the sessions-lookup hack which loses precision on cleanup.

ALTER TABLE users ADD COLUMN display_name TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN last_login_at DATETIME;
