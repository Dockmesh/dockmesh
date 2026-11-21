-- backup_run_archive_missing
-- A run whose archive was looked for on a reachable target and wasn't
-- there. It stays listed, with the reason, until the archive is found
-- again or someone removes it from the list.
ALTER TABLE backup_runs ADD COLUMN archive_missing_at DATETIME;
ALTER TABLE backup_runs ADD COLUMN archive_missing_reason TEXT;
