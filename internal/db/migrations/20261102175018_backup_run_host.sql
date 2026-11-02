-- backup_run_host
-- A run keeps the host it was taken on, so moving a job to another host
-- doesn't change who may read or restore its older archives, nor where a
-- restore goes by default. Existing runs get their job's current host,
-- the only record there is.
ALTER TABLE backup_runs ADD COLUMN host_id TEXT NOT NULL DEFAULT '';

UPDATE backup_runs
   SET host_id = COALESCE(
         (SELECT CASE WHEN j.host_id = '' THEN 'local' ELSE j.host_id END
            FROM backup_jobs j WHERE j.id = backup_runs.job_id),
         'local');
