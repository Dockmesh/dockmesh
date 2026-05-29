-- Post-seed timestamp backdating. Makes the demo data look like it's
-- been running for weeks rather than "just now". Run via:
--   ssh root@192.168.10.90 'sqlite3 /data/data/dockmesh.db' < scripts/backdate-demo.sql
--
-- All datetimes use SQLite's `datetime('now', '-N seconds')` arithmetic
-- so re-running tomorrow shifts everything forward correctly.

-- ── stack_deployments ────────────────────────────────────────────────
-- monitoring, gitea = 2 weeks old (the "veteran" stacks)
-- paperless, media   = 5–7 days old
-- web-platform       = 2 days old (the "newest production rollout")
-- vault              = 1 day old (stopped recently)
UPDATE stack_deployments SET deployed_at = datetime('now', '-14 days'), updated_at = datetime('now', '-3 hours')      WHERE stack_name = 'monitoring';
UPDATE stack_deployments SET deployed_at = datetime('now', '-12 days'), updated_at = datetime('now', '-1 day')        WHERE stack_name = 'gitea';
UPDATE stack_deployments SET deployed_at = datetime('now', '-7 days'),  updated_at = datetime('now', '-6 hours')      WHERE stack_name = 'paperless';
UPDATE stack_deployments SET deployed_at = datetime('now', '-5 days'),  updated_at = datetime('now', '-12 hours')     WHERE stack_name = 'media';
UPDATE stack_deployments SET deployed_at = datetime('now', '-2 days'),  updated_at = datetime('now', '-30 minutes')   WHERE stack_name = 'web-platform';
UPDATE stack_deployments SET deployed_at = datetime('now', '-9 days'),  updated_at = datetime('now', '-1 day'),  status = 'stopped' WHERE stack_name = 'vault';

-- ── users ────────────────────────────────────────────────────────────
-- admin = "joined 30 days ago" (the OG), seeded users between 14 and 3 days
UPDATE users SET created_at = datetime('now', '-30 days'), last_login_at = datetime('now', '-15 minutes')   WHERE username = 'admin';
UPDATE users SET created_at = datetime('now', '-14 days'), last_login_at = datetime('now', '-2 hours')      WHERE username = 'julia.becker';
UPDATE users SET created_at = datetime('now', '-10 days'), last_login_at = datetime('now', '-5 hours')      WHERE username = 'tim.weber';
UPDATE users SET created_at = datetime('now', '-7 days'),  last_login_at = datetime('now', '-1 day')        WHERE username = 'anna.schulz';
UPDATE users SET created_at = datetime('now', '-3 days'),  last_login_at = datetime('now', '-6 hours')      WHERE username = 'max.kovac';

-- ── sessions ─────────────────────────────────────────────────────────
-- Spread last_seen_at so the Sessions card shows "12m / 2h / yesterday"
-- variety instead of all-just-now. Apply per-user — first match wins.
UPDATE sessions SET created_at = datetime('now', '-12 hours'), last_seen_at = datetime('now', '-15 minutes')
   WHERE user_id = (SELECT id FROM users WHERE username = 'admin') AND revoked_at IS NULL
   AND family_id = (SELECT family_id FROM sessions WHERE user_id = (SELECT id FROM users WHERE username = 'admin') AND revoked_at IS NULL ORDER BY last_seen_at DESC LIMIT 1);

-- ── alert_rules ──────────────────────────────────────────────────────
UPDATE alert_rules SET created_at = datetime('now', '-45 days'), updated_at = datetime('now', '-12 days');
-- One rule "firing" right now for the visual variety
UPDATE alert_rules SET firing_since = datetime('now', '-23 minutes'), last_triggered_at = datetime('now', '-23 minutes')
   WHERE name LIKE 'Container memory > 90%%';

-- ── backup_jobs ──────────────────────────────────────────────────────
UPDATE backup_jobs SET created_at = datetime('now', '-21 days'), updated_at = datetime('now', '-2 days'),
                       last_run_at = datetime('now', '-3 hours'),
                       next_run_at = datetime('now', '+21 hours')
 WHERE name = 'monitoring-daily';
UPDATE backup_jobs SET created_at = datetime('now', '-18 days'), updated_at = datetime('now', '-4 days'),
                       last_run_at = datetime('now', '-3 days'),
                       next_run_at = datetime('now', '+4 days')
 WHERE name = 'web-platform-weekly';
UPDATE backup_jobs SET created_at = datetime('now', '-14 days'), updated_at = datetime('now', '-1 day'),
                       last_run_at = datetime('now', '-45 minutes'),
                       next_run_at = datetime('now', '+5 hours')
 WHERE name = 'gitea-hourly';

-- ── backup_runs ──────────────────────────────────────────────────────
-- Wipe whatever the seed kicked off (timestamps are all "just now") and
-- replace with a realistic 7-day run history per job.
DELETE FROM backup_runs;

-- monitoring-daily — 7 successful daily runs
INSERT INTO backup_runs (job_id, job_name, status, started_at, finished_at, size_bytes, sources_json, encrypted)
  SELECT id, name, 'success',
         datetime('now', '-' || day || ' days', '+3 hours'),
         datetime('now', '-' || day || ' days', '+3 hours', '+47 seconds'),
         (314572800 + ABS(RANDOM() % 50000000)),
         '[{"kind":"stack","name":"monitoring"}]', 0
    FROM backup_jobs, (SELECT 0 AS day UNION SELECT 1 UNION SELECT 2 UNION SELECT 3 UNION SELECT 4 UNION SELECT 5 UNION SELECT 6)
   WHERE backup_jobs.name = 'monitoring-daily';

-- web-platform-weekly — 4 successful weekly runs + 1 failed
INSERT INTO backup_runs (job_id, job_name, status, started_at, finished_at, size_bytes, sources_json, encrypted)
  SELECT id, name, 'success',
         datetime('now', '-' || (week*7) || ' days', '+4 hours'),
         datetime('now', '-' || (week*7) || ' days', '+4 hours', '+3 minutes'),
         (1610612736 + ABS(RANDOM() % 200000000)),
         '[{"kind":"stack","name":"web-platform"},{"kind":"stack","name":"gitea"}]', 1
    FROM backup_jobs, (SELECT 1 AS week UNION SELECT 2 UNION SELECT 3 UNION SELECT 4)
   WHERE backup_jobs.name = 'web-platform-weekly';
INSERT INTO backup_runs (job_id, job_name, status, started_at, finished_at, size_bytes, sources_json, encrypted, error)
  SELECT id, name, 'failed',
         datetime('now', '-1 days', '+4 hours'),
         datetime('now', '-1 days', '+4 hours', '+12 seconds'),
         0, '[{"kind":"stack","name":"web-platform"}]', 1,
         'sftp: connection refused (target unreachable)'
    FROM backup_jobs WHERE name = 'web-platform-weekly';

-- gitea-hourly — last 12 runs, mostly successful, one currently running
INSERT INTO backup_runs (job_id, job_name, status, started_at, finished_at, size_bytes, sources_json, encrypted)
  SELECT id, name, 'success',
         datetime('now', '-' || (h*6) || ' hours'),
         datetime('now', '-' || (h*6) || ' hours', '+18 seconds'),
         (89128960 + ABS(RANDOM() % 10000000)),
         '[{"kind":"stack","name":"gitea"}]', 1
    FROM backup_jobs, (SELECT 1 AS h UNION SELECT 2 UNION SELECT 3 UNION SELECT 4 UNION SELECT 5 UNION SELECT 6 UNION SELECT 7 UNION SELECT 8 UNION SELECT 9 UNION SELECT 10 UNION SELECT 11)
   WHERE backup_jobs.name = 'gitea-hourly';
INSERT INTO backup_runs (job_id, job_name, status, started_at, sources_json, encrypted)
  SELECT id, name, 'running',
         datetime('now', '-22 seconds'),
         '[{"kind":"stack","name":"gitea"}]', 1
    FROM backup_jobs WHERE name = 'gitea-hourly';

-- ── audit_log ────────────────────────────────────────────────────────
-- Wipe the seed-induced entries and replace with a realistic 14-day
-- ops history. Mixed actions, mixed actors, mixed targets. Order
-- doesn't matter; the index on `ts` handles read order.
DELETE FROM audit_log;

-- Helper trick: we use a sub-select to look up each user's UUID by
-- username inline so the script stays portable.
INSERT INTO audit_log (ts, user_id, action, target, details) VALUES
  -- yesterday → today
  (datetime('now', '-12 minutes'),     (SELECT id FROM users WHERE username='admin'),         'container.restart',      'paperless-gotenberg-1',   '{"reason":"oom"}'),
  (datetime('now', '-45 minutes'),     (SELECT id FROM users WHERE username='julia.becker'),  'stack.update',           'web-platform',            '{"compose_changed":true}'),
  (datetime('now', '-2 hours'),        (SELECT id FROM users WHERE username='tim.weber'),     'stack.deploy',           'web-platform',            '{"services":3}'),
  (datetime('now', '-3 hours'),        (SELECT id FROM users WHERE username='admin'),         'backup.run',             'monitoring-daily',        '{"size_mb":312}'),
  (datetime('now', '-5 hours'),        (SELECT id FROM users WHERE username='max.kovac'),     'auth.login',             'max.kovac',               '{"ip":"10.0.4.12"}'),
  (datetime('now', '-7 hours'),        (SELECT id FROM users WHERE username='julia.becker'),  'image.pull',             'nginx:alpine',            NULL),
  -- 1–3 days
  (datetime('now', '-1 day'),          (SELECT id FROM users WHERE username='admin'),         'user.create',            'max.kovac',               '{"role":"host-admin"}'),
  (datetime('now', '-1 day', '+2 hours'), (SELECT id FROM users WHERE username='admin'),      'auth.policy_update',     '',                        '{"min_length":12}'),
  (datetime('now', '-1 day', '+5 hours'), (SELECT id FROM users WHERE username='tim.weber'),  'stack.deploy',           'web-platform',            '{"services":3}'),
  (datetime('now', '-2 days'),         (SELECT id FROM users WHERE username='julia.becker'),  'container.stop',         'vault-vaultwarden-1',     '{"reason":"maintenance"}'),
  (datetime('now', '-2 days', '+3 hours'), (SELECT id FROM users WHERE username='julia.becker'), 'stack.stop',          'vault',                   NULL),
  (datetime('now', '-3 days'),         (SELECT id FROM users WHERE username='admin'),         'role.create',            'Junior Operator',         '{"permissions":8}'),
  -- 4–7 days
  (datetime('now', '-4 days'),         (SELECT id FROM users WHERE username='admin'),         'sso.provider_create',    'authentik',               '{"kind":"oidc"}'),
  (datetime('now', '-5 days'),         (SELECT id FROM users WHERE username='admin'),         'registry.create',        'ghcr',                    NULL),
  (datetime('now', '-5 days', '+4 hours'), (SELECT id FROM users WHERE username='admin'),     'registry.create',        'harbor.internal',         NULL),
  (datetime('now', '-6 days'),         (SELECT id FROM users WHERE username='julia.becker'),  'image.scan',             'grafana/grafana:latest',  '{"vulnerabilities":3,"severity":"medium"}'),
  (datetime('now', '-7 days'),         (SELECT id FROM users WHERE username='admin'),         'backup.target_create',   's3-prod',                 NULL),
  -- 8–14 days (foundational setup)
  (datetime('now', '-8 days'),         (SELECT id FROM users WHERE username='admin'),         'alert.rule_create',      'Container memory > 90%',  '{"severity":"warning"}'),
  (datetime('now', '-9 days'),         (SELECT id FROM users WHERE username='admin'),         'stack.create',           'media',                   NULL),
  (datetime('now', '-10 days'),        (SELECT id FROM users WHERE username='admin'),         'user.create',            'anna.schulz',             '{"role":"viewer"}'),
  (datetime('now', '-12 days'),        (SELECT id FROM users WHERE username='admin'),         'stack.create',           'gitea',                   NULL),
  (datetime('now', '-14 days'),        (SELECT id FROM users WHERE username='admin'),         'user.create',            'julia.becker',            '{"role":"operator"}'),
  (datetime('now', '-14 days', '+1 hour'), (SELECT id FROM users WHERE username='admin'),     'stack.create',           'monitoring',              NULL),
  (datetime('now', '-14 days', '+2 hours'), (SELECT id FROM users WHERE username='admin'),    'stack.deploy',           'monitoring',              '{"services":3}');

-- ── alert_history — recent firing events ────────────────────────────
-- Backfill a few resolved fires so the History tab has data.
INSERT INTO alert_history (rule_id, rule_name, severity, fired_at, resolved_at, container_name, value, threshold)
  SELECT id, name, severity,
         datetime('now', '-2 days', '+10 hours'),
         datetime('now', '-2 days', '+10 hours', '+18 minutes'),
         'paperless-gotenberg-1', 93.4, threshold
    FROM alert_rules WHERE name LIKE 'Container memory > 90%%';
INSERT INTO alert_history (rule_id, rule_name, severity, fired_at, resolved_at, container_name, value, threshold)
  SELECT id, name, severity,
         datetime('now', '-4 days'),
         datetime('now', '-4 days', '+8 minutes'),
         'media-transmission-1', 96.7, threshold
    FROM alert_rules WHERE name LIKE 'Container CPU > 95%%';
INSERT INTO alert_history (rule_id, rule_name, severity, fired_at, resolved_at, container_name, value, threshold)
  SELECT id, name, severity,
         datetime('now', '-23 minutes'),
         NULL,
         'paperless-gotenberg-1', 94.1, threshold
    FROM alert_rules WHERE name LIKE 'Container memory > 90%%';

SELECT '=== final state ===';
SELECT 'audit entries:', count(*) FROM audit_log;
SELECT 'backup runs:', count(*) FROM backup_runs;
SELECT 'alert history:', count(*) FROM alert_history;
SELECT 'oldest stack:', stack_name, deployed_at FROM stack_deployments ORDER BY deployed_at ASC LIMIT 1;
SELECT 'newest stack:', stack_name, deployed_at FROM stack_deployments ORDER BY deployed_at DESC LIMIT 1;
