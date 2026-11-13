-- git_pending_deploy
-- A commit whose auto-deploy found the stack busy waits here until the
-- stack is free; only the newest one is kept.
ALTER TABLE stack_git_sources ADD COLUMN pending_sha TEXT;
ALTER TABLE stack_git_sources ADD COLUMN pending_operation TEXT;
ALTER TABLE stack_git_sources ADD COLUMN pending_since TIMESTAMP;
