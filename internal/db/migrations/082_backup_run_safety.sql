-- pruned_at: the archive was deleted (retention or failed-run cleanup);
-- the row stays as history but can no longer be restored.
ALTER TABLE backup_runs ADD COLUMN pruned_at DATETIME;

-- Runs failed only by a post-hook have an intact archive (the hash is
-- written only once it is complete).
UPDATE backup_runs SET status = 'success_with_warnings'
 WHERE status = 'failed' AND error LIKE 'post-hooks:%' AND COALESCE(sha256, '') != '';

-- Deleted jobs are kept (hidden) so their runs stay restorable.
ALTER TABLE backup_jobs ADD COLUMN deleted_at DATETIME;
