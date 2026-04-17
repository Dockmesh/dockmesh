-- P.11.11 — git-backed stacks.
--
-- Stacks are filesystem-based (no rows in a `stacks` table), so we key by
-- the stack's canonical name. ON DELETE semantics are driven by the
-- stacks.Manager deleting its FS tree — the service nulls / deletes these
-- rows when the stack directory disappears.
CREATE TABLE IF NOT EXISTS stack_git_sources (
    stack_name         TEXT PRIMARY KEY,
    repo_url           TEXT NOT NULL,
    branch             TEXT NOT NULL DEFAULT 'main',
    path_in_repo       TEXT NOT NULL DEFAULT '.',      -- dir containing compose.yaml (and optional .env)
    auth_kind          TEXT NOT NULL DEFAULT 'none',    -- 'none' | 'http' | 'ssh'
    username           TEXT,
    password_encrypted BLOB,                            -- age-encrypted HTTP password / PAT
    ssh_key_encrypted  BLOB,                            -- age-encrypted OpenSSH private key
    auto_deploy        INTEGER NOT NULL DEFAULT 0,
    poll_interval_sec  INTEGER NOT NULL DEFAULT 300,
    webhook_secret     TEXT,                            -- shared secret for HMAC webhook verification
    last_sync_sha      TEXT,
    last_sync_at       DATETIME,
    last_sync_error    TEXT,
    created_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
