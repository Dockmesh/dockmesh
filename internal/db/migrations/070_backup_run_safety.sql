-- sha256_plain: 1 when sha256 covers the plaintext archive. Older runs
-- hashed the bytes written to the target, which for encrypted runs is
-- the ciphertext, so verify keeps comparing those against the raw file.
ALTER TABLE backup_runs ADD COLUMN sha256_plain INTEGER NOT NULL DEFAULT 0;
UPDATE backup_runs SET sha256_plain = 1 WHERE encrypted = 0;

-- pruned_at: the archive was deleted (retention or failed-run cleanup);
-- the row stays as history but can no longer be restored.
ALTER TABLE backup_runs ADD COLUMN pruned_at DATETIME;

-- A failed post-hook used to mark an intact archive as failed. Those
-- runs carry a hash (only written once the archive was complete).
UPDATE backup_runs SET status = 'success_with_warnings'
 WHERE status = 'failed' AND error LIKE 'post-hooks:%' AND COALESCE(sha256, '') != '';

-- Deleted jobs are kept (hidden) so their runs stay restorable.
ALTER TABLE backup_jobs ADD COLUMN deleted_at DATETIME;
