-- alert_history_retention
-- repeats counts one-shot alerts (OOM, failed backup) that recurred inside
-- the cooldown and were folded into the earlier row instead of a new one.
ALTER TABLE alert_history ADD COLUMN repeats INTEGER NOT NULL DEFAULT 0;

-- Serves the latest-row-per-target lookups (firing state, active alerts,
-- retention), which group by rule, host and target.
CREATE INDEX idx_alert_history_key ON alert_history(rule_id, host_id, container_name, id);
-- Scoped history lists entries of some hosts, newest first.
CREATE INDEX idx_alert_history_host ON alert_history(host_id, occurred_at);

-- CPU unit of metric rules: 'core' is 100 per busy core (docker stats),
-- 'host' is the share of the whole host the charts show. Existing CPU
-- rules keep the per-core behaviour they were written for.
ALTER TABLE alert_rules ADD COLUMN cpu_unit TEXT NOT NULL DEFAULT '';
UPDATE alert_rules SET cpu_unit = 'core' WHERE kind = 'metric' AND metric = 'cpu_percent';

INSERT INTO notices (key, audience, audience_value, severity, title, body, link)
SELECT 'alerts.cpu-unit', 'admins', '', 'info',
       'CPU thresholds now name their unit',
       'Charts and lists show CPU as a share of the whole host. Existing CPU alert rules and autoscaling rules keep working as before and are now labelled "% of one core" (100 per busy core). New rules use "% of host". On a 4-core host, 80% of one core is 20% of the host. Edit a rule to switch its unit.',
       '/alerts'
 WHERE EXISTS (SELECT 1 FROM users);
