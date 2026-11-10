-- stack_file_sums_backfill
-- Marks every stack that already has checksums, so the next start records
-- its .dockmesh.meta.json and compose.<env>.yaml override files as they
-- are and removes the marker. Stacks created later never get one.
INSERT OR IGNORE INTO stack_file_sums (stack, file, sum)
SELECT DISTINCT stack, '@backfill', '' FROM stack_file_sums WHERE file <> '@backfill';
