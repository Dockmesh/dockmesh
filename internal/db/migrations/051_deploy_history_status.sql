-- Extended deploy-history metadata so the UI can show success vs
-- failed, how long the deploy took, and (for git-triggered deploys)
-- the commit SHA the images came from.
--
-- success NULL on existing rows = "deployed via the old code path
-- before this column existed" — the UI treats it as legacy/unknown.
-- duration_ms / git_commit_sha / error_message similarly nullable so
-- the migration can run online without backfilling.
ALTER TABLE stack_deploy_history ADD COLUMN success INTEGER;
ALTER TABLE stack_deploy_history ADD COLUMN duration_ms INTEGER;
ALTER TABLE stack_deploy_history ADD COLUMN git_commit_sha TEXT;
ALTER TABLE stack_deploy_history ADD COLUMN error_message TEXT;
