-- deploy_history_environment
-- Env-override deploys stored the merged compose with the stack's .env
-- values filled in, and rollbacks copied it into new rows. History now
-- keeps the base compose and the override name; the filled-in copies
-- are cleared so no secret values stay in the table.
ALTER TABLE stack_deploy_history ADD COLUMN environment TEXT NOT NULL DEFAULT '';

UPDATE stack_deploy_history SET compose_yaml = ''
 WHERE id IN (
   SELECT h.id FROM stack_deploy_history h
     JOIN stack_deploy_history e
       ON e.stack_name = h.stack_name AND e.compose_yaml = h.compose_yaml
    WHERE e.note LIKE 'env: %'
 );
