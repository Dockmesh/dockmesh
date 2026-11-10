-- stack_file_sums_backfill
-- Stacks tracked before .dockmesh.meta.json and the compose.<env>.yaml
-- override files were get a marker; the next start records those files
-- as they are and removes it. Stacks created later never get one.
INSERT OR IGNORE INTO stack_file_sums (stack, file, sum)
SELECT DISTINCT stack, '@backfill', '' FROM stack_file_sums WHERE file <> '@backfill';
