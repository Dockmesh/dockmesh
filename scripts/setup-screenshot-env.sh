#!/usr/bin/env bash
# Marketing/GitHub screenshot environment setup. Runs on .90.
#
# Wipes everything except the admin user, deploys six realistic stacks
# via the dockmesh API (so they show up in stack_deployments + the UI),
# seeds users + channels + alerts + backup jobs/targets/runs + audit log
# with backdated timestamps, then restarts dockmesh so any in-memory
# cache picks up the changes.
#
# Designed to be idempotent — re-run any time to rebuild the demo set.
#
# Usage: ssh root@192.168.10.90 'bash -s' < scripts/setup-screenshot-env.sh

set -uo pipefail

DB=/data/data/dockmesh.db
BASE=http://localhost:8080
USER=admin
PASS=admin123#

echo "==> step 1: stop + remove all containers + stack files"
docker ps -a --format '{{.Names}}' | xargs -r docker rm -f 2>/dev/null || true
rm -rf /data/stacks/*

echo "==> step 2: wipe DB rows (keep admin + standard roles)"
sqlite3 "$DB" <<'SQL'
DELETE FROM stack_dependencies;
DELETE FROM stack_deploy_history;
DELETE FROM stack_deployments;
DELETE FROM stack_git_sources;
DELETE FROM update_history;
DELETE FROM scan_results;
DELETE FROM backup_runs;
DELETE FROM backup_jobs;
DELETE FROM backup_targets;
DELETE FROM alert_history;
DELETE FROM alert_rules;
DELETE FROM notification_channels;
DELETE FROM notifications;
DELETE FROM registries;
DELETE FROM api_tokens;
DELETE FROM drains;
DELETE FROM agents;
DELETE FROM host_tags;
DELETE FROM global_env;
DELETE FROM proxy_routes;
DELETE FROM oidc_provider_group_mappings;
DELETE FROM oidc_providers;
DELETE FROM saml_provider_group_mappings;
DELETE FROM saml_providers;
DELETE FROM saml_sp_keys;
DELETE FROM ldap_provider_group_mappings;
DELETE FROM ldap_providers;
DELETE FROM oauth2_provider_group_mappings;
DELETE FROM oauth2_providers;
DELETE FROM audit_log;
DELETE FROM metrics_raw;
DELETE FROM metrics_1m;
DELETE FROM metrics_1h;
DELETE FROM roles WHERE builtin = 0;
DELETE FROM sessions WHERE user_id NOT IN (SELECT id FROM users WHERE username='admin');
DELETE FROM users WHERE username != 'admin';
SQL

echo "==> step 3: restart dockmesh so it sees the clean state"
systemctl restart dockmesh
sleep 4

echo "==> step 4: login + grab access token"
TOKEN=$(curl -s -X POST -H 'Content-Type: application/json' \
  -d "{\"username\":\"$USER\",\"password\":\"$PASS\"}" \
  "$BASE/api/v1/auth/login" | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')
if [ -z "$TOKEN" ]; then echo "login failed"; exit 1; fi
echo "  token len=${#TOKEN}"

H=(-H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json")

api() { curl -s "${H[@]}" "$@"; }

create_stack() {
  local name="$1"
  local compose="$2"
  local deploy="$3"
  api -X POST "$BASE/api/v1/stacks" -d "$(jq -n --arg n "$name" --arg c "$compose" '{name:$n, compose:$c, env:""}')" >/dev/null
  if [ "$deploy" = "yes" ]; then
    api -X POST "$BASE/api/v1/stacks/$name/deploy" >/dev/null &
    echo "  deploying $name (background)"
  else
    echo "  created $name (not deployed)"
  fi
}

echo "==> step 5: create + deploy stacks"

create_stack monitoring 'services:
  prometheus:
    image: prom/prometheus:latest
    ports: ["9090:9090"]
    networks: [monitoring]
  grafana:
    image: grafana/grafana:latest
    ports: ["3000:3000"]
    environment:
      - GF_AUTH_ANONYMOUS_ENABLED=true
    networks: [monitoring]
  node-exporter:
    image: prom/node-exporter:latest
    networks: [monitoring]
networks:
  monitoring:
' yes

create_stack gitea 'services:
  gitea:
    image: gitea/gitea:latest-rootless
    ports: ["3001:3000"]
    networks: [gitea]
  postgres:
    image: postgres:16-alpine
    environment:
      - POSTGRES_USER=gitea
      - POSTGRES_PASSWORD=gitea
      - POSTGRES_DB=gitea
    networks: [gitea]
networks:
  gitea:
' yes

create_stack paperless 'services:
  paperless:
    image: nginx:alpine
    ports: ["8000:80"]
    networks: [paperless]
  redis:
    image: redis:7-alpine
    networks: [paperless]
  tika:
    image: nginx:alpine
    networks: [paperless]
  gotenberg:
    image: nginx:alpine
    networks: [paperless]
networks:
  paperless:
' yes

create_stack vault 'services:
  vaultwarden:
    image: vaultwarden/server:latest
    ports: ["8080:80"]
    environment:
      - SIGNUPS_ALLOWED=false
' no

create_stack media 'services:
  jellyfin:
    image: nginx:alpine
    ports: ["8096:80"]
    networks: [media]
  sonarr:
    image: nginx:alpine
    ports: ["8989:80"]
    networks: [media]
  radarr:
    image: nginx:alpine
    ports: ["7878:80"]
    networks: [media]
  transmission:
    image: nginx:alpine
    ports: ["9091:80"]
    networks: [media]
networks:
  media:
' yes

create_stack web-platform 'services:
  nginx:
    image: nginx:alpine
    ports: ["8090:80"]
    networks: [web]
  app:
    image: node:20-alpine
    command: ["sh","-c","echo platform app running && tail -f /dev/null"]
    networks: [web]
  redis:
    image: redis:7-alpine
    networks: [web]
networks:
  web:
' yes

echo "==> step 6: wait for deploys (30s should be enough for nginx:alpine pulls)"
sleep 30

echo "==> step 7: users"
for u in 'julia.becker|operator|julia.becker@dockmesh.dev' \
         'tim.weber|deployer|tim.weber@dockmesh.dev' \
         'anna.schulz|viewer|anna.schulz@dockmesh.dev' \
         'max.kovac|host-admin|max.kovac@contractor.com'; do
  IFS='|' read -r username role email <<<"$u"
  api -X POST "$BASE/api/v1/users" -d "$(jq -n --arg u "$username" --arg p TempPass123# --arg r "$role" --arg e "$email" '{username:$u, password:$p, role:$r, email:$e}')" >/dev/null
  echo "  user $username"
done

echo "==> step 8: scope tags for the scoped users"
UID_TIM=$(api "$BASE/api/v1/users" | jq -r '.[] | select(.username=="tim.weber") | .id')
UID_MAX=$(api "$BASE/api/v1/users" | jq -r '.[] | select(.username=="max.kovac") | .id')
api -X PUT "$BASE/api/v1/users/$UID_TIM" -d '{"email":"tim.weber@dockmesh.dev","role":"deployer","scope_tags":["stack:web-platform"]}' >/dev/null
api -X PUT "$BASE/api/v1/users/$UID_MAX" -d '{"email":"max.kovac@contractor.com","role":"host-admin","scope_tags":["host:local"]}' >/dev/null

echo "==> step 9: custom role"
api -X POST "$BASE/api/v1/roles" -d '{"name":"Junior Operator","description":"Stack deploy + container restart only, no destructive actions.","permissions":["containers.view","containers.update","containers.logs","stacks.view","stacks.update","images.view","volumes.view","networks.view"]}' >/dev/null

echo "==> step 10: notification channels"
# notification_channels schema uses "type" not "kind".
api -X POST "$BASE/api/v1/notifications/channels" -d '{"type":"webhook","name":"ops-slack","config":{"url":"https://hooks.slack.com/services/T0000/B0000/xxxx"}}' >/dev/null
api -X POST "$BASE/api/v1/notifications/channels" -d '{"type":"email","name":"oncall-email","config":{"to":"oncall@dockmesh.dev"}}' >/dev/null
api -X POST "$BASE/api/v1/notifications/channels" -d '{"type":"webhook","name":"dev-discord","config":{"url":"https://discord.com/api/webhooks/000/aaaa"}}' >/dev/null

CH_SLACK=$(api "$BASE/api/v1/notifications/channels" | jq -r '.[] | select(.name=="ops-slack") | .id')
CH_EMAIL=$(api "$BASE/api/v1/notifications/channels" | jq -r '.[] | select(.name=="oncall-email") | .id')
CH_DISCORD=$(api "$BASE/api/v1/notifications/channels" | jq -r '.[] | select(.name=="dev-discord") | .id')
echo "  channels: slack=$CH_SLACK email=$CH_EMAIL discord=$CH_DISCORD"

echo "==> step 11: alert rules (wired to real channel ids)"
api -X POST "$BASE/api/v1/alerts/rules" -d "$(jq -n --argjson s "$CH_SLACK" --argjson e "$CH_EMAIL" --argjson d "$CH_DISCORD" '{
  name: "Container memory > 90% (sustained)",
  container_filter: "*", metric: "mem_percent", operator: ">", threshold: 90, duration_seconds: 300,
  channel_ids: [$s, $e], severity: "warning", enabled: true
}')" >/dev/null
api -X POST "$BASE/api/v1/alerts/rules" -d "$(jq -n --argjson s "$CH_SLACK" --argjson e "$CH_EMAIL" --argjson d "$CH_DISCORD" '{
  name: "Container CPU > 95% (critical)",
  container_filter: "*", metric: "cpu_percent", operator: ">", threshold: 95, duration_seconds: 180,
  channel_ids: [$s, $e, $d], severity: "critical", enabled: true
}')" >/dev/null
api -X POST "$BASE/api/v1/alerts/rules" -d "$(jq -n --argjson s "$CH_SLACK" '{
  name: "paperless-* memory > 75%",
  container_filter: "paperless-*", metric: "mem_percent", operator: ">", threshold: 75, duration_seconds: 600,
  channel_ids: [$s], severity: "warning", enabled: true
}')" >/dev/null
api -X POST "$BASE/api/v1/alerts/rules" -d "$(jq -n --argjson e "$CH_EMAIL" --argjson d "$CH_DISCORD" '{
  name: "media-* CPU > 85% (long-running)",
  container_filter: "media-*", metric: "cpu_percent", operator: ">", threshold: 85, duration_seconds: 900,
  channel_ids: [$e, $d], severity: "warning", enabled: true
}')" >/dev/null

echo "==> step 12: backup targets + jobs"
api -X POST "$BASE/api/v1/backups/targets" -d '{"name":"local-backups","type":"local","config":{"path":"/var/lib/dockmesh/backups"}}' >/dev/null
api -X POST "$BASE/api/v1/backups/targets" -d '{"name":"s3-prod","type":"s3","config":{"endpoint":"s3.amazonaws.com","region":"eu-central-1","bucket":"dockmesh-prod-backups","access_key":"AKIA…","secret_key":"****"}}' >/dev/null
api -X POST "$BASE/api/v1/backups/targets" -d '{"name":"sftp-cold-storage","type":"sftp","config":{"host":"cold.storage.internal","port":22,"username":"backup","path":"/data/dockmesh"}}' >/dev/null

# Inline configs — avoids jq --argjson edge cases with quoted secrets.
api -X POST "$BASE/api/v1/backups/jobs" -d '{
  "name":"monitoring-daily", "target_type":"local",
  "target_config":{"path":"/var/lib/dockmesh/backups"},
  "sources":[{"kind":"stack","name":"monitoring"}],
  "schedule":"0 3 * * *", "retention_count":7, "retention_days":30, "encrypt":false, "enabled":true
}' >/dev/null
api -X POST "$BASE/api/v1/backups/jobs" -d '{
  "name":"web-platform-weekly", "target_type":"s3",
  "target_config":{"endpoint":"s3.amazonaws.com","region":"eu-central-1","bucket":"dockmesh-prod-backups","access_key":"AKIA…","secret_key":"****"},
  "sources":[{"kind":"stack","name":"web-platform"}],
  "schedule":"0 4 * * 0", "retention_count":4, "retention_days":90, "encrypt":true, "enabled":true
}' >/dev/null
api -X POST "$BASE/api/v1/backups/jobs" -d '{
  "name":"gitea-hourly", "target_type":"sftp",
  "target_config":{"host":"cold.storage.internal","port":22,"username":"backup","path":"/data/dockmesh"},
  "sources":[{"kind":"stack","name":"gitea"}],
  "schedule":"0 */6 * * *", "retention_count":12, "retention_days":14, "encrypt":true, "enabled":true
}' >/dev/null

echo "==> step 13: registries"
api -X POST "$BASE/api/v1/settings/registries" -d '{"name":"ghcr","url":"ghcr.io","username":"dockmesh-bot","password":"****"}' >/dev/null
api -X POST "$BASE/api/v1/settings/registries" -d '{"name":"harbor.internal","url":"harbor.dockmesh.dev","username":"robot$ci","password":"****"}' >/dev/null

echo "==> step 14: SSO provider (disabled)"
api -X POST "$BASE/api/v1/oidc/providers" -d '{"slug":"authentik","display_name":"Authentik","issuer_url":"https://authentik.dockmesh.dev/application/o/dockmesh/","client_id":"dockmesh","client_secret":"****","enabled":false,"default_role":"viewer"}' >/dev/null

echo "==> step 15: git sources (web-platform + vault) — visual coverage of the Git-connected stack card"
# Both wire to docker/awesome-compose (real, public, no auth needed) so the
# UI's connected-state cards render with a real commit sha + sync history.
# auto_deploy = 0 on both because the awesome-compose contents don't match
# our seeded compose.yaml — leaving auto_deploy on would let the next poll
# overwrite our compose and break the running containers. Operator can
# still click "Sync now" to refresh the sha, just shouldn't deploy after.
sqlite3 "$DB" <<'SQL'
INSERT OR REPLACE INTO stack_git_sources (stack_name, repo_url, branch, path_in_repo, auth_kind, auto_deploy, poll_interval_sec, last_sync_sha, last_sync_at)
VALUES ('web-platform', 'https://github.com/docker/awesome-compose.git', 'master', 'nginx-golang-postgres', 'none', 0, 86400,
        '18f59bdb09ecf520dd5758fbf90dec314baec545', datetime('now','-12 minutes'));
INSERT OR REPLACE INTO stack_git_sources (stack_name, repo_url, branch, path_in_repo, auth_kind, auto_deploy, poll_interval_sec, last_sync_sha, last_sync_at)
VALUES ('vault', 'https://github.com/docker/awesome-compose.git', 'master', 'react-express-mongodb', 'none', 0, 86400,
        '18f59bdb09ecf520dd5758fbf90dec314baec545', datetime('now','-2 days'));
SQL

echo "==> step 16: backdate everything + insert realistic activity"
JID_MON=$(sqlite3 "$DB" "SELECT id FROM backup_jobs WHERE name='monitoring-daily'")
JID_WEB=$(sqlite3 "$DB" "SELECT id FROM backup_jobs WHERE name='web-platform-weekly'")
JID_GIT=$(sqlite3 "$DB" "SELECT id FROM backup_jobs WHERE name='gitea-hourly'")
ALERT_MEM_ID=$(sqlite3 "$DB" "SELECT id FROM alert_rules WHERE name LIKE 'Container memory > 90%%'")
ALERT_CPU_ID=$(sqlite3 "$DB" "SELECT id FROM alert_rules WHERE name LIKE 'Container CPU > 95%%'")

sqlite3 "$DB" <<SQL
-- Stack deployments: spread across last 14 days
UPDATE stack_deployments SET deployed_at = datetime('now', '-14 days'), updated_at = datetime('now', '-3 hours')    WHERE stack_name = 'monitoring';
UPDATE stack_deployments SET deployed_at = datetime('now', '-12 days'), updated_at = datetime('now', '-1 day')      WHERE stack_name = 'gitea';
UPDATE stack_deployments SET deployed_at = datetime('now', '-7 days'),  updated_at = datetime('now', '-6 hours')    WHERE stack_name = 'paperless';
UPDATE stack_deployments SET deployed_at = datetime('now', '-5 days'),  updated_at = datetime('now', '-12 hours')   WHERE stack_name = 'media';
UPDATE stack_deployments SET deployed_at = datetime('now', '-2 days'),  updated_at = datetime('now', '-30 minutes') WHERE stack_name = 'web-platform';
UPDATE stack_deployments SET deployed_at = datetime('now', '-9 days'),  updated_at = datetime('now', '-1 day'), status = 'stopped' WHERE stack_name = 'vault';

-- Users
UPDATE users SET created_at = datetime('now', '-30 days'), last_login_at = datetime('now', '-2 minutes')  WHERE username = 'admin';
UPDATE users SET created_at = datetime('now', '-14 days'), last_login_at = datetime('now', '-2 hours')    WHERE username = 'julia.becker';
UPDATE users SET created_at = datetime('now', '-10 days'), last_login_at = datetime('now', '-5 hours')    WHERE username = 'tim.weber';
UPDATE users SET created_at = datetime('now', '-7 days'),  last_login_at = datetime('now', '-1 day')      WHERE username = 'anna.schulz';
UPDATE users SET created_at = datetime('now', '-3 days'),  last_login_at = datetime('now', '-6 hours')    WHERE username = 'max.kovac';

-- Alert rules: aged + one firing right now
UPDATE alert_rules SET created_at = datetime('now', '-45 days'), updated_at = datetime('now', '-12 days');
UPDATE alert_rules SET firing_since = datetime('now', '-23 minutes'), last_triggered_at = datetime('now', '-23 minutes') WHERE id = $ALERT_MEM_ID;

-- Backup jobs metadata
UPDATE backup_jobs SET created_at=datetime('now','-21 days'), updated_at=datetime('now','-2 days'),
                       last_run_at=datetime('now','-3 hours'), next_run_at=datetime('now','+21 hours')
 WHERE id = $JID_MON;
UPDATE backup_jobs SET created_at=datetime('now','-18 days'), updated_at=datetime('now','-4 days'),
                       last_run_at=datetime('now','-3 days'),  next_run_at=datetime('now','+4 days')
 WHERE id = $JID_WEB;
UPDATE backup_jobs SET created_at=datetime('now','-14 days'), updated_at=datetime('now','-1 day'),
                       last_run_at=datetime('now','-45 minutes'), next_run_at=datetime('now','+5 hours')
 WHERE id = $JID_GIT;

-- Realistic backup_runs history
DELETE FROM backup_runs;
INSERT INTO backup_runs (job_id, job_name, status, started_at, finished_at, size_bytes, sources_json, encrypted)
  SELECT $JID_MON, 'monitoring-daily', 'success',
         datetime('now','-'||day||' days','+3 hours'),
         datetime('now','-'||day||' days','+3 hours','+47 seconds'),
         314572800 + ABS(RANDOM() % 50000000),
         '[{"kind":"stack","name":"monitoring"}]', 0
    FROM (SELECT 0 day UNION SELECT 1 UNION SELECT 2 UNION SELECT 3 UNION SELECT 4 UNION SELECT 5 UNION SELECT 6);

INSERT INTO backup_runs (job_id, job_name, status, started_at, finished_at, size_bytes, sources_json, encrypted)
  SELECT $JID_WEB, 'web-platform-weekly', 'success',
         datetime('now','-'||(week*7)||' days','+4 hours'),
         datetime('now','-'||(week*7)||' days','+4 hours','+3 minutes'),
         1610612736 + ABS(RANDOM() % 200000000),
         '[{"kind":"stack","name":"web-platform"},{"kind":"stack","name":"gitea"}]', 1
    FROM (SELECT 1 week UNION SELECT 2 UNION SELECT 3 UNION SELECT 4);

INSERT INTO backup_runs (job_id, job_name, status, started_at, finished_at, size_bytes, sources_json, encrypted)
  SELECT $JID_GIT, 'gitea-hourly', 'success',
         datetime('now','-'||(h*6)||' hours'),
         datetime('now','-'||(h*6)||' hours','+18 seconds'),
         89128960 + ABS(RANDOM() % 10000000),
         '[{"kind":"stack","name":"gitea"}]', 1
    FROM (SELECT 1 h UNION SELECT 2 UNION SELECT 3 UNION SELECT 4 UNION SELECT 5 UNION SELECT 6 UNION SELECT 7 UNION SELECT 8 UNION SELECT 9 UNION SELECT 10 UNION SELECT 11);

INSERT INTO backup_runs (job_id, job_name, status, started_at, sources_json, encrypted)
  VALUES ($JID_GIT, 'gitea-hourly', 'running', datetime('now','-22 seconds'),
          '[{"kind":"stack","name":"gitea"}]', 1);

-- Alert history (correct schema this time: status, message, value, threshold, occurred_at)
DELETE FROM alert_history;
INSERT INTO alert_history (rule_id, rule_name, container_name, status, message, value, threshold, occurred_at) VALUES
  ($ALERT_MEM_ID, 'Container memory > 90% (sustained)', 'paperless-gotenberg-1', 'resolved', 'memory back below threshold', 87.2, 90, datetime('now','-2 days','+10 hours','+18 minutes')),
  ($ALERT_MEM_ID, 'Container memory > 90% (sustained)', 'paperless-gotenberg-1', 'fired',    'memory above 90% for 5min',     93.4, 90, datetime('now','-2 days','+10 hours')),
  ($ALERT_CPU_ID, 'Container CPU > 95% (critical)',    'media-transmission-1',    'resolved', 'cpu back below threshold',      85.1, 95, datetime('now','-4 days','+8 minutes')),
  ($ALERT_CPU_ID, 'Container CPU > 95% (critical)',    'media-transmission-1',    'fired',    'cpu above 95% for 3min',         96.7, 95, datetime('now','-4 days')),
  ($ALERT_MEM_ID, 'Container memory > 90% (sustained)', 'paperless-gotenberg-1', 'fired',    'memory above 90% for 5min',     94.1, 90, datetime('now','-23 minutes'));

-- Audit log: wipe + 24 realistic entries spread across 14 days
DELETE FROM audit_log;
INSERT INTO audit_log (ts, user_id, action, target, details) VALUES
  (datetime('now','-12 minutes'),               (SELECT id FROM users WHERE username='admin'),        'container.restart',      'paperless-gotenberg-1',   '{"reason":"oom"}'),
  (datetime('now','-45 minutes'),               (SELECT id FROM users WHERE username='julia.becker'), 'stack.update',           'web-platform',            '{"compose_changed":true}'),
  (datetime('now','-2 hours'),                  (SELECT id FROM users WHERE username='tim.weber'),    'stack.deploy',           'web-platform',            '{"services":3}'),
  (datetime('now','-3 hours'),                  (SELECT id FROM users WHERE username='admin'),        'backup.run',             'monitoring-daily',        '{"size_mb":312}'),
  (datetime('now','-5 hours'),                  (SELECT id FROM users WHERE username='max.kovac'),    'auth.login',             'max.kovac',               '{"ip":"10.0.4.12"}'),
  (datetime('now','-7 hours'),                  (SELECT id FROM users WHERE username='julia.becker'), 'image.pull',             'nginx:alpine',            NULL),
  (datetime('now','-1 day'),                    (SELECT id FROM users WHERE username='admin'),        'user.create',            'max.kovac',               '{"role":"host-admin"}'),
  (datetime('now','-1 day','+2 hours'),         (SELECT id FROM users WHERE username='admin'),        'auth.policy_update',     '',                        '{"min_length":12}'),
  (datetime('now','-1 day','+5 hours'),         (SELECT id FROM users WHERE username='tim.weber'),    'stack.deploy',           'web-platform',            '{"services":3}'),
  (datetime('now','-2 days'),                   (SELECT id FROM users WHERE username='julia.becker'), 'container.stop',         'vault-vaultwarden-1',     '{"reason":"maintenance"}'),
  (datetime('now','-2 days','+3 hours'),        (SELECT id FROM users WHERE username='julia.becker'), 'stack.stop',             'vault',                   NULL),
  (datetime('now','-3 days'),                   (SELECT id FROM users WHERE username='admin'),        'role.create',            'Junior Operator',         '{"permissions":8}'),
  (datetime('now','-4 days'),                   (SELECT id FROM users WHERE username='admin'),        'sso.provider_create',    'authentik',               '{"kind":"oidc"}'),
  (datetime('now','-5 days'),                   (SELECT id FROM users WHERE username='admin'),        'registry.create',        'ghcr',                    NULL),
  (datetime('now','-5 days','+4 hours'),        (SELECT id FROM users WHERE username='admin'),        'registry.create',        'harbor.internal',         NULL),
  (datetime('now','-6 days'),                   (SELECT id FROM users WHERE username='julia.becker'), 'image.scan',             'grafana/grafana:latest',  '{"vulnerabilities":3,"severity":"medium"}'),
  (datetime('now','-7 days'),                   (SELECT id FROM users WHERE username='admin'),        'backup.target_create',   's3-prod',                 NULL),
  (datetime('now','-8 days'),                   (SELECT id FROM users WHERE username='admin'),        'alert.rule_create',      'Container memory > 90%',  '{"severity":"warning"}'),
  (datetime('now','-9 days'),                   (SELECT id FROM users WHERE username='admin'),        'stack.create',           'media',                   NULL),
  (datetime('now','-10 days'),                  (SELECT id FROM users WHERE username='admin'),        'user.create',            'anna.schulz',             '{"role":"viewer"}'),
  (datetime('now','-12 days'),                  (SELECT id FROM users WHERE username='admin'),        'stack.create',           'gitea',                   NULL),
  (datetime('now','-14 days'),                  (SELECT id FROM users WHERE username='admin'),        'user.create',            'julia.becker',            '{"role":"operator"}'),
  (datetime('now','-14 days','+1 hour'),        (SELECT id FROM users WHERE username='admin'),        'stack.create',           'monitoring',              NULL),
  (datetime('now','-14 days','+2 hours'),       (SELECT id FROM users WHERE username='admin'),        'stack.deploy',           'monitoring',              '{"services":3}');
SQL

echo "==> step 17: restart dockmesh once more so all caches pick up the backdates"
systemctl restart dockmesh
sleep 4

echo "==> step 18: final timestamp pass — stacks that finished deploying late"
# paperless + media each ship 4 services so their async deploy may have
# finished after step 16, overwriting our backdated rows. Re-stamp them
# now that the restart has settled and no more deploy callbacks fire.
sqlite3 "$DB" <<'SQL'
UPDATE stack_deployments SET deployed_at = datetime('now', '-14 days'), updated_at = datetime('now', '-3 hours')    WHERE stack_name = 'monitoring';
UPDATE stack_deployments SET deployed_at = datetime('now', '-12 days'), updated_at = datetime('now', '-1 day')      WHERE stack_name = 'gitea';
UPDATE stack_deployments SET deployed_at = datetime('now', '-7 days'),  updated_at = datetime('now', '-6 hours')    WHERE stack_name = 'paperless';
UPDATE stack_deployments SET deployed_at = datetime('now', '-5 days'),  updated_at = datetime('now', '-12 hours')   WHERE stack_name = 'media';
UPDATE stack_deployments SET deployed_at = datetime('now', '-2 days'),  updated_at = datetime('now', '-30 minutes') WHERE stack_name = 'web-platform';

-- Audit log: wipe everything created since step 16 (login events, etc.)
-- and re-insert the canonical 24-entry history.
DELETE FROM audit_log;
INSERT INTO audit_log (ts, user_id, action, target, details) VALUES
  (datetime('now','-12 minutes'),               (SELECT id FROM users WHERE username='admin'),        'container.restart',      'paperless-gotenberg-1',   '{"reason":"oom"}'),
  (datetime('now','-45 minutes'),               (SELECT id FROM users WHERE username='julia.becker'), 'stack.update',           'web-platform',            '{"compose_changed":true}'),
  (datetime('now','-2 hours'),                  (SELECT id FROM users WHERE username='tim.weber'),    'stack.deploy',           'web-platform',            '{"services":3}'),
  (datetime('now','-3 hours'),                  (SELECT id FROM users WHERE username='admin'),        'backup.run',             'monitoring-daily',        '{"size_mb":312}'),
  (datetime('now','-5 hours'),                  (SELECT id FROM users WHERE username='max.kovac'),    'auth.login',             'max.kovac',               '{"ip":"10.0.4.12"}'),
  (datetime('now','-7 hours'),                  (SELECT id FROM users WHERE username='julia.becker'), 'image.pull',             'nginx:alpine',            NULL),
  (datetime('now','-1 day'),                    (SELECT id FROM users WHERE username='admin'),        'user.create',            'max.kovac',               '{"role":"host-admin"}'),
  (datetime('now','-1 day','+2 hours'),         (SELECT id FROM users WHERE username='admin'),        'auth.policy_update',     '',                        '{"min_length":12}'),
  (datetime('now','-1 day','+5 hours'),         (SELECT id FROM users WHERE username='tim.weber'),    'stack.deploy',           'web-platform',            '{"services":3}'),
  (datetime('now','-2 days'),                   (SELECT id FROM users WHERE username='julia.becker'), 'container.stop',         'vault-vaultwarden-1',     '{"reason":"maintenance"}'),
  (datetime('now','-2 days','+3 hours'),        (SELECT id FROM users WHERE username='julia.becker'), 'stack.stop',             'vault',                   NULL),
  (datetime('now','-3 days'),                   (SELECT id FROM users WHERE username='admin'),        'role.create',            'Junior Operator',         '{"permissions":8}'),
  (datetime('now','-4 days'),                   (SELECT id FROM users WHERE username='admin'),        'sso.provider_create',    'authentik',               '{"kind":"oidc"}'),
  (datetime('now','-5 days'),                   (SELECT id FROM users WHERE username='admin'),        'registry.create',        'ghcr',                    NULL),
  (datetime('now','-5 days','+4 hours'),        (SELECT id FROM users WHERE username='admin'),        'registry.create',        'harbor.internal',         NULL),
  (datetime('now','-6 days'),                   (SELECT id FROM users WHERE username='julia.becker'), 'image.scan',             'grafana/grafana:latest',  '{"vulnerabilities":3,"severity":"medium"}'),
  (datetime('now','-7 days'),                   (SELECT id FROM users WHERE username='admin'),        'backup.target_create',   's3-prod',                 NULL),
  (datetime('now','-8 days'),                   (SELECT id FROM users WHERE username='admin'),        'alert.rule_create',      'Container memory > 90%',  '{"severity":"warning"}'),
  (datetime('now','-9 days'),                   (SELECT id FROM users WHERE username='admin'),        'stack.create',           'media',                   NULL),
  (datetime('now','-10 days'),                  (SELECT id FROM users WHERE username='admin'),        'user.create',            'anna.schulz',             '{"role":"viewer"}'),
  (datetime('now','-12 days'),                  (SELECT id FROM users WHERE username='admin'),        'stack.create',           'gitea',                   NULL),
  (datetime('now','-14 days'),                  (SELECT id FROM users WHERE username='admin'),        'user.create',            'julia.becker',            '{"role":"operator"}'),
  (datetime('now','-14 days','+1 hour'),        (SELECT id FROM users WHERE username='admin'),        'stack.create',           'monitoring',              NULL),
  (datetime('now','-14 days','+2 hours'),       (SELECT id FROM users WHERE username='admin'),        'stack.deploy',           'monitoring',              '{"services":3}');
SQL

echo "==> done. Final state:"
sqlite3 "$DB" "SELECT 'stacks:', count(*) FROM stack_deployments;
SELECT 'users:', count(*) FROM users;
SELECT 'channels:', count(*) FROM notification_channels;
SELECT 'rules:', count(*) FROM alert_rules;
SELECT 'targets:', count(*) FROM backup_targets;
SELECT 'jobs:', count(*) FROM backup_jobs;
SELECT 'runs:', count(*) FROM backup_runs;
SELECT 'audit:', count(*) FROM audit_log;
SELECT 'alert_history:', count(*) FROM alert_history;
SELECT 'registries:', count(*) FROM registries;
SELECT 'oidc_providers:', count(*) FROM oidc_providers;
SELECT 'git sources:', count(*) FROM stack_git_sources;"
docker ps --format '{{.Names}}: {{.Status}}' | wc -l
echo "containers running ^"
