-- Event-based alert rules. kind selects the evaluator; existing rules are
-- the CPU/memory threshold kind. The scope columns restrict a rule to one
-- host id, host tag or compose stack; empty means everywhere.
ALTER TABLE alert_rules ADD COLUMN kind TEXT NOT NULL DEFAULT 'metric';
ALTER TABLE alert_rules ADD COLUMN scope_host TEXT NOT NULL DEFAULT '';
ALTER TABLE alert_rules ADD COLUMN scope_tag TEXT NOT NULL DEFAULT '';
ALTER TABLE alert_rules ADD COLUMN scope_stack TEXT NOT NULL DEFAULT '';

INSERT INTO alert_rules
    (name, kind, container_filter, metric, operator, threshold, duration_seconds,
     channel_ids, enabled, severity, cooldown_seconds, builtin)
VALUES
    ('Container restart loop',            'restart_loop',      '*', '', '', 3,  600,  '[]', 1, 'warning',  1800, 1),
    ('Container killed by OOM',           'oom',               '*', '', '', 0,  0,    '[]', 1, 'critical', 600,  1),
    ('Container stopped unexpectedly',    'container_stopped', '*', '', '', 0,  0,    '[]', 0, 'warning',  600,  1),
    ('Container unhealthy',               'unhealthy',         '*', '', '', 0,  0,    '[]', 0, 'warning',  600,  1),
    ('Host offline',                      'host_offline',      '*', '', '', 0,  300,  '[]', 1, 'critical', 1800, 1),
    ('Disk usage above 90%',              'disk_usage',        '*', '', '', 90, 0,    '[]', 1, 'warning',  21600, 1),
    ('Backup run failed',                 'backup_failed',     '*', '', '', 0,  0,    '[]', 1, 'critical', 300,  1),
    ('Backup overdue',                    'backup_missed',     '*', '', '', 0,  0,    '[]', 1, 'warning',  21600, 1),
    ('TLS certificate expiring',          'cert_expiry',       '*', '', '', 14, 0,    '[]', 1, 'warning',  86400, 1);

-- Existing installs (any user exists) get a one-time notice about the new
-- built-in rules; fresh installs see them in the rule list directly.
INSERT INTO notices (key, audience, audience_value, severity, title, body, link)
SELECT 'alerts.event-rules-added', 'admins', '', 'info',
       'New built-in alert rules',
       'Dockmesh now alerts on container restart loops, OOM kills, offline hosts, disk usage above 90% on Docker mounts, failed or overdue backups and TLS certificates expiring within 14 days. "Container stopped unexpectedly" and "Container unhealthy" are available but off. Attach a notification channel to the rules to get them outside Dockmesh.',
       '/alerts'
 WHERE EXISTS (SELECT 1 FROM users);
