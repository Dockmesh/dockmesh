-- Per-account sign-in limit: failures in the current window, window start
-- and block end (Unix seconds). Kept on the row so an unlock from the CLI
-- clears it too.
ALTER TABLE users ADD COLUMN login_limit_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN login_limit_since INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN login_limit_until INTEGER NOT NULL DEFAULT 0;
