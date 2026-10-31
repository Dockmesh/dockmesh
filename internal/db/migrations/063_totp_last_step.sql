-- Last TOTP time step accepted per user, so a code can be used only once.
ALTER TABLE users ADD COLUMN totp_last_step INTEGER NOT NULL DEFAULT 0;
