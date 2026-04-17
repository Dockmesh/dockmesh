-- P.12.1 — auth hardening.
--
-- failed_login_attempts + locked_until implement per-user brute-force
-- protection on top of the existing per-IP rate limiter. The IP
-- limiter stops distributed attackers hammering one account from many
-- IPs with junk passwords; the user limiter stops an attacker who's
-- already behind the IP limit (or changes IPs) from walking every
-- password in a dictionary against one specific username. Both are
-- cleared on any successful login.
--
-- password_changed_at drives the optional "force password rotation
-- every N days" reminder (Settings → Authentication → rotation_days).
-- Nullable so we can distinguish "never changed since this column
-- existed" (NULL) from "changed at time X". Existing users get NULL
-- on migration; we only flag them for rotation once they've voluntarily
-- changed once.
ALTER TABLE users ADD COLUMN failed_login_attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN locked_until          DATETIME;
ALTER TABLE users ADD COLUMN password_changed_at   DATETIME;
