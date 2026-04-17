-- 024_default_alerts.sql
-- P.11.5: ship with sensible default alert rules so new installs have
-- coverage from day one. Rules can be disabled and edited, but the UI
-- prevents deletion (builtin=1 is checked server-side as well).
--
-- Why only CPU/memory defaults: the evaluator in internal/alerts today
-- supports `cpu_percent` and `mem_percent` per container. Host-level
-- disk, agent-offline, and backup-job-failed metrics aren't emitted yet
-- — when they are, follow-up migrations can seed additional builtins.
--
-- channel_ids is seeded as "[]" (no channels yet). Admins attach
-- channels via the edit dialog after initial setup. The rules still
-- evaluate — they just won't notify anywhere until a channel is added.

ALTER TABLE alert_rules ADD COLUMN builtin INTEGER NOT NULL DEFAULT 0;

-- Container CPU saturation, sustained → warning.
INSERT INTO alert_rules
    (name, container_filter, metric, operator, threshold, duration_seconds,
     channel_ids, enabled, severity, cooldown_seconds, builtin)
VALUES
    ('Container CPU > 90% (sustained)',
     '*', 'cpu_percent', 'gt', 90.0, 300,
     '[]', 1, 'warning', 600, 1);

-- Container CPU saturation, heavy + longer → critical.
INSERT INTO alert_rules
    (name, container_filter, metric, operator, threshold, duration_seconds,
     channel_ids, enabled, severity, cooldown_seconds, builtin)
VALUES
    ('Container CPU > 95% (critical)',
     '*', 'cpu_percent', 'gt', 95.0, 900,
     '[]', 1, 'critical', 600, 1);

-- Container memory pressure, sustained → warning.
INSERT INTO alert_rules
    (name, container_filter, metric, operator, threshold, duration_seconds,
     channel_ids, enabled, severity, cooldown_seconds, builtin)
VALUES
    ('Container memory > 90%',
     '*', 'mem_percent', 'gt', 90.0, 300,
     '[]', 1, 'warning', 600, 1);

-- Container memory approaching OOM → critical, fast.
INSERT INTO alert_rules
    (name, container_filter, metric, operator, threshold, duration_seconds,
     channel_ids, enabled, severity, cooldown_seconds, builtin)
VALUES
    ('Container memory > 98% (near-OOM)',
     '*', 'mem_percent', 'gt', 98.0, 60,
     '[]', 1, 'critical', 300, 1);
