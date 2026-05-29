-- 053_backup_run_logs.sql
-- Per-run log capture for backup runs (concept §13). The executor emits
-- one row per phase boundary (start/end), hook output line, and error.
-- The UI surfaces this in the run-detail Log section; ops can copy a
-- single run's log for support tickets without grepping journald.

CREATE TABLE IF NOT EXISTS backup_run_logs (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id  INTEGER NOT NULL,
    ts      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    -- stream is one of: info | warn | error | hook_stdout | hook_stderr.
    -- "info" covers normal phase narration; "warn" is non-fatal issues
    -- (e.g. post-hook failed but backup succeeded); "error" is fatal.
    -- hook_stdout/hook_stderr carry verbatim subprocess output, capped
    -- per hook to avoid swamping the table on noisy hooks.
    stream  TEXT NOT NULL,
    line    TEXT NOT NULL,
    FOREIGN KEY (run_id) REFERENCES backup_runs(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_backup_run_logs_run_id
    ON backup_run_logs(run_id, id);
