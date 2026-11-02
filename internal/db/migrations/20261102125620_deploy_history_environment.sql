-- deploy_history_environment
-- Env-override deploys stored the merged compose with the stack's .env
-- values filled in. History now keeps the base compose and the override
-- name; the filled-in snapshots are cleared below.
ALTER TABLE stack_deploy_history ADD COLUMN environment TEXT NOT NULL DEFAULT '';

-- A rollback to such a snapshot also wrote it into the stack's
-- compose.yaml, so every later deploy of that stack may carry the values.
-- Admins are told which stacks to check before those rows are cleared.
INSERT INTO notices (key, audience, audience_value, severity, title, body, link)
SELECT 'stacks.compose-env-values', 'admins', '', 'warning',
       'Check compose.yaml for .env values',
       'An earlier rollback of an environment-override deploy wrote the stack''s .env values into compose.yaml of: ' ||
       group_concat(stack_name, ', ') ||
       '. Their deploy history from that rollback on was cleared because it may hold those values. Replace the values in compose.yaml with ${VARIABLE} references.',
       '/stacks'
  FROM (SELECT DISTINCT r.stack_name
          FROM stack_deploy_history r
          JOIN stack_deploy_history e
            ON e.stack_name = r.stack_name AND e.compose_yaml = r.compose_yaml AND e.id < r.id
         WHERE e.note LIKE 'env: %' AND r.note LIKE 'rollback to #%')
HAVING count(*) > 0;

UPDATE stack_deploy_history SET compose_yaml = ''
 WHERE id IN (
   SELECT h.id FROM stack_deploy_history h
     JOIN stack_deploy_history e
       ON e.stack_name = h.stack_name AND e.compose_yaml = h.compose_yaml
    WHERE e.note LIKE 'env: %'
 )
    OR id IN (
   SELECT later.id FROM stack_deploy_history later
     JOIN (SELECT r.stack_name, MIN(r.id) AS first_id
             FROM stack_deploy_history r
             JOIN stack_deploy_history e
               ON e.stack_name = r.stack_name AND e.compose_yaml = r.compose_yaml AND e.id < r.id
            WHERE e.note LIKE 'env: %' AND r.note LIKE 'rollback to #%'
            GROUP BY r.stack_name) p
       ON p.stack_name = later.stack_name AND later.id >= p.first_id
 );
