-- drop_migrated_away_source_rows
-- Older migrations recorded the source host as a pseudo stack
-- "<stack>__source" with status migrated_away. The migrations table keeps
-- the source host, and stack names can't contain "_", so these rows were
-- never a stack.
DELETE FROM stack_deployments
 WHERE status = 'migrated_away'
   AND stack_name LIKE '%\_\_source' ESCAPE '\';
