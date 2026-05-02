-- P.13.2 follow-up: split out so installs that already applied 034
-- pick this column up. review_acked is sticky once set — the boot-time
-- migration uses it to decide "this job has already been reviewed by
-- the operator, leave it alone forever". Without this column the
-- migration would re-flag every job after every restart.
ALTER TABLE backup_jobs ADD COLUMN review_acked INTEGER NOT NULL DEFAULT 0;
