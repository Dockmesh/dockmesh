-- MFA columns on users. totp_secret is the base32-encoded TOTP seed,
-- totp_verified flips to 1 only after the user confirms the first code.
-- totp_recovery holds a JSON array of SHA-256 hex digests of the one-time
-- recovery codes (high-entropy, so we don't need argon2id here).
ALTER TABLE users ADD COLUMN totp_secret TEXT;
ALTER TABLE users ADD COLUMN totp_verified INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN totp_recovery TEXT;
