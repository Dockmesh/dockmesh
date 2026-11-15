-- backup_run_location
-- A run records the target it wrote to and that target's settings at the
-- time (credentials excluded), so editing the target or switching the
-- job to another one leaves older archives readable. Existing runs stay
-- without a location: nothing recorded where they went.
ALTER TABLE backup_runs ADD COLUMN target_id INTEGER;
ALTER TABLE backup_runs ADD COLUMN target_location TEXT;
