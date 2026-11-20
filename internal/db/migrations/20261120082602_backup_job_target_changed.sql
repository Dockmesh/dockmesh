-- backup_job_target_changed
-- When the job last moved to another target, set from this version on.
-- Runs without a recorded location that started before it were written
-- somewhere else, so they are never stamped with the target's location.
ALTER TABLE backup_jobs ADD COLUMN target_changed_at DATETIME;
