-- Database written by v0.5.3: five jobs with a system source, each created through
-- the job API on its own saved target (local absolute, local relative, local
-- without a path, SMB, S3).
PRAGMA foreign_keys=OFF;
BEGIN TRANSACTION;
CREATE TABLE schema_migrations (version TEXT PRIMARY KEY, applied_at DATETIME DEFAULT CURRENT_TIMESTAMP);
INSERT INTO "schema_migrations" VALUES('001_initial.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('002_sessions_token_family.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('003_mfa.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('004_audit_chain.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('005_scan_results.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('006_proxy_routes.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('007_update_history.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('008_oidc.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('009_metrics.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('010_alerts.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('011_backups.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('012_agents.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('013_stack_deployments.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('014_migrations.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('015_drains.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('016_custom_roles.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('017_settings.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('018_global_env.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('019_alert_enhancements.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('020_backup_targets.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('021_api_tokens.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('022_host_tags.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('023_scope_tags.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('024_default_alerts.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('025_registries.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('026_stack_git_sources.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('027_stack_templates.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('028_auth_hardening.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('029_stack_deploy_history.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('030_stack_dependencies.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('031_backup_host_id.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('032_update_check.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('033_rbac_stack_adopt.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('034_backup_review.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('035_backup_review_acked.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('036_role_descriptions.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('037_rbac_v2_catalog.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('038_role_scopes.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('039_rbac_v2_drop_legacy_perms.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('040_rbac_v21_explicit_create.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('041_agent_online_since.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('042_user_profile_fields.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('043_sessions_last_seen.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('044_template_metadata.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('045_user_avatar.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('046_oidc_extensions.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('047_saml_providers.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('048_ldap_providers.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('049_oauth2_providers.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('050_git_env_drift.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('051_deploy_history_status.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('052_notifications.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('053_backup_run_logs.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('054_proxy_route_enabled.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('055_alert_mute_schedule.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('056_invite_tokens.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('057_notifications_per_user.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('058_notices.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('059_api_token_owner.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('060_stack_file_sums.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('061_backup_download_permission.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('062_sso_linking.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('063_totp_last_step.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('064_agent_enroll_token_expiry.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('072_image_update_checks.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('073_login_limit.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('074_saml_subject.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('076_rbac_seed_drift.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('079_alert_engine.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('080_event_alerts.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('081_agent_cert_renewal.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('082_backup_run_safety.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('083_user_preferences.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('084_proxy_route_options.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('085_operations.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('20261101151453_rbac_assignments.sql','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('20261101171127_metrics_host_id.sql','2026-11-14 19:45:37 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('20261102095418_alert_history_retention.sql','2026-11-14 19:45:37 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('20261102111652_api_token_parent.sql','2026-11-14 19:45:37 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('20261102111923_stack_targets.sql','2026-11-14 19:45:37 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('20261102125620_deploy_history_environment.sql','2026-11-14 19:45:37 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('20261102135820_sso_immutable_subjects.sql','2026-11-14 19:45:37 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('20261102175018_backup_run_host.sql','2026-11-14 19:45:37 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('20261102193312_notice_blocking.sql','2026-11-14 19:45:37 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('20261103221016_proxy_enabled_unsaved_default.sql','2026-11-14 19:45:37 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('20261109133233_scanner_enabled_env_default.sql','2026-11-14 19:45:37 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('20261109133528_saml_provider_options.sql','2026-11-14 19:45:37 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('20261109150513_notice_alert_text_keys.sql','2026-11-14 19:45:37 +0000 UTC');
INSERT INTO "schema_migrations" VALUES('20261110124842_stack_file_sums_backfill.sql','2026-11-14 19:45:37 +0000 UTC');
CREATE TABLE users (
    id          TEXT PRIMARY KEY,
    username    TEXT NOT NULL UNIQUE,
    email       TEXT UNIQUE,
    password    TEXT NOT NULL,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
, totp_secret TEXT, totp_verified INTEGER NOT NULL DEFAULT 0, totp_recovery TEXT, oidc_provider TEXT, oidc_subject  TEXT, failed_login_attempts INTEGER NOT NULL DEFAULT 0, locked_until          DATETIME, password_changed_at   DATETIME, display_name TEXT NOT NULL DEFAULT '', last_login_at DATETIME, avatar_blob BLOB, avatar_mime TEXT, saml_provider TEXT, saml_subject  TEXT, ldap_provider TEXT, ldap_subject  TEXT, oauth2_provider TEXT, oauth2_subject  TEXT, role_source TEXT NOT NULL DEFAULT 'manual', totp_last_step INTEGER NOT NULL DEFAULT 0, login_limit_count INTEGER NOT NULL DEFAULT 0, login_limit_since INTEGER NOT NULL DEFAULT 0, login_limit_until INTEGER NOT NULL DEFAULT 0, saml_rekey_until DATETIME, oauth2_legacy_subject TEXT, oauth2_rekey_until DATETIME, ldap_legacy_subject TEXT, ldap_rekey_until DATETIME);
CREATE TABLE audit_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    ts          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    user_id     TEXT,
    action      TEXT NOT NULL,
    target      TEXT,
    details     TEXT
, prev_hash TEXT, row_hash  TEXT);
CREATE TABLE settings (
    key         TEXT PRIMARY KEY,
    value       TEXT NOT NULL,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO "settings" VALUES('proxy_enabled','','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "settings" VALUES('scanner_enabled','','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "settings" VALUES('base_url','http://localhost:8080','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "settings" VALUES('agent_public_url','','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "settings" VALUES('update_check_enabled','true','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "settings" VALUES('update_check_interval_minutes','120','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "settings" VALUES('update_last_check','','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "settings" VALUES('update_latest_version','','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "settings" VALUES('update_release_url','','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "settings" VALUES('update_release_notes','','2026-11-14 19:45:36 +0000 UTC');
INSERT INTO "settings" VALUES('update_published_at','','2026-11-14 19:45:36 +0000 UTC');
CREATE TABLE sessions (
    family_id    TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    current_seq  INTEGER NOT NULL DEFAULT 0,
    user_agent   TEXT,
    ip           TEXT,
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at   DATETIME NOT NULL,
    revoked_at   DATETIME
, last_seen_at DATETIME);
CREATE TABLE scan_results (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    image_ref       TEXT NOT NULL UNIQUE,
    scanner         TEXT NOT NULL,
    scanner_version TEXT,
    findings_json   TEXT NOT NULL,
    critical        INTEGER NOT NULL DEFAULT 0,
    high            INTEGER NOT NULL DEFAULT 0,
    medium          INTEGER NOT NULL DEFAULT 0,
    low             INTEGER NOT NULL DEFAULT 0,
    negligible      INTEGER NOT NULL DEFAULT 0,
    unknown_sev     INTEGER NOT NULL DEFAULT 0,
    scanned_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE proxy_routes (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    host         TEXT NOT NULL UNIQUE,
    upstream     TEXT NOT NULL,                 -- e.g. 127.0.0.1:8080
    tls_mode     TEXT NOT NULL DEFAULT 'auto',  -- auto | internal | none
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
, enabled INTEGER NOT NULL DEFAULT 1, options TEXT NOT NULL DEFAULT '{}');
CREATE TABLE update_history (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    container_name  TEXT NOT NULL,
    image_ref       TEXT NOT NULL,
    old_digest      TEXT NOT NULL,
    new_digest      TEXT NOT NULL,
    rollback_tag    TEXT NOT NULL,
    applied_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    rolled_back_at  DATETIME
);
CREATE TABLE oidc_providers (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    slug           TEXT NOT NULL UNIQUE,         -- /auth/oidc/{slug}/login
    display_name   TEXT NOT NULL,
    issuer_url     TEXT NOT NULL,
    client_id      TEXT NOT NULL,
    client_secret  TEXT NOT NULL,                -- ciphertext when secrets enabled
    scopes         TEXT NOT NULL DEFAULT 'openid,profile,email',
    group_claim    TEXT,                         -- claim name holding group memberships, e.g. "groups"
    default_role   TEXT NOT NULL DEFAULT 'viewer',
    enabled        INTEGER NOT NULL DEFAULT 1,
    created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
, is_default INTEGER NOT NULL DEFAULT 0, last_tested_at DATETIME, last_test_ok INTEGER, last_test_error TEXT, allow_email_link INTEGER NOT NULL DEFAULT 0);
CREATE TABLE notification_channels (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    type        TEXT NOT NULL,
    name        TEXT NOT NULL,
    config      TEXT NOT NULL DEFAULT '{}',
    enabled     INTEGER NOT NULL DEFAULT 1,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
, mute_schedule TEXT NOT NULL DEFAULT '', last_sent_at DATETIME, last_error TEXT NOT NULL DEFAULT '', last_error_at DATETIME);
CREATE TABLE alert_rules (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    name              TEXT NOT NULL,
    container_filter  TEXT NOT NULL DEFAULT '*',
    metric            TEXT NOT NULL,
    operator          TEXT NOT NULL,
    threshold         REAL NOT NULL,
    duration_seconds  INTEGER NOT NULL DEFAULT 60,
    channel_ids       TEXT NOT NULL DEFAULT '[]',
    enabled           INTEGER NOT NULL DEFAULT 1,
    firing_since      DATETIME,
    last_triggered_at DATETIME,
    last_resolved_at  DATETIME,
    created_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
, severity TEXT NOT NULL DEFAULT 'warning', cooldown_seconds INTEGER NOT NULL DEFAULT 300, muted_until DATETIME, builtin INTEGER NOT NULL DEFAULT 0, mute_schedule TEXT NOT NULL DEFAULT '', kind TEXT NOT NULL DEFAULT 'metric', scope_host TEXT NOT NULL DEFAULT '', scope_tag TEXT NOT NULL DEFAULT '', scope_stack TEXT NOT NULL DEFAULT '', cpu_unit TEXT NOT NULL DEFAULT '');
INSERT INTO "alert_rules" VALUES(1,'Container CPU > 90% (sustained)','*','cpu_percent','gt',90,300,'[]',1,NULL,NULL,NULL,'2026-11-14 19:45:36 +0000 UTC','2026-11-14 19:45:36 +0000 UTC','warning',600,NULL,1,'','metric','','','','core');
INSERT INTO "alert_rules" VALUES(2,'Container CPU > 95% (critical)','*','cpu_percent','gt',95,900,'[]',1,NULL,NULL,NULL,'2026-11-14 19:45:36 +0000 UTC','2026-11-14 19:45:36 +0000 UTC','critical',600,NULL,1,'','metric','','','','core');
INSERT INTO "alert_rules" VALUES(3,'Container memory > 90%','*','mem_percent','gt',90,300,'[]',1,NULL,NULL,NULL,'2026-11-14 19:45:36 +0000 UTC','2026-11-14 19:45:36 +0000 UTC','warning',600,NULL,1,'','metric','','','','');
INSERT INTO "alert_rules" VALUES(4,'Container memory > 98% (near-OOM)','*','mem_percent','gt',98,60,'[]',1,NULL,NULL,NULL,'2026-11-14 19:45:36 +0000 UTC','2026-11-14 19:45:36 +0000 UTC','critical',300,NULL,1,'','metric','','','','');
INSERT INTO "alert_rules" VALUES(5,'Container restart loop','*','','',3,600,'[]',1,NULL,NULL,NULL,'2026-11-14 19:45:36 +0000 UTC','2026-11-14 19:45:36 +0000 UTC','warning',1800,NULL,1,'','restart_loop','','','','');
INSERT INTO "alert_rules" VALUES(6,'Container killed by OOM','*','','',0,0,'[]',1,NULL,NULL,NULL,'2026-11-14 19:45:36 +0000 UTC','2026-11-14 19:45:36 +0000 UTC','critical',600,NULL,1,'','oom','','','','');
INSERT INTO "alert_rules" VALUES(7,'Container stopped unexpectedly','*','','',0,0,'[]',0,NULL,NULL,NULL,'2026-11-14 19:45:36 +0000 UTC','2026-11-14 19:45:36 +0000 UTC','warning',600,NULL,1,'','container_stopped','','','','');
INSERT INTO "alert_rules" VALUES(8,'Container unhealthy','*','','',0,0,'[]',0,NULL,NULL,NULL,'2026-11-14 19:45:36 +0000 UTC','2026-11-14 19:45:36 +0000 UTC','warning',600,NULL,1,'','unhealthy','','','','');
INSERT INTO "alert_rules" VALUES(9,'Host offline','*','','',0,300,'[]',1,NULL,NULL,NULL,'2026-11-14 19:45:36 +0000 UTC','2026-11-14 19:45:36 +0000 UTC','critical',1800,NULL,1,'','host_offline','','','','');
INSERT INTO "alert_rules" VALUES(10,'Disk usage above 90%','*','','',90,0,'[]',1,NULL,NULL,NULL,'2026-11-14 19:45:36 +0000 UTC','2026-11-14 19:45:36 +0000 UTC','warning',21600,NULL,1,'','disk_usage','','','','');
INSERT INTO "alert_rules" VALUES(11,'Backup run failed','*','','',0,0,'[]',1,NULL,NULL,NULL,'2026-11-14 19:45:36 +0000 UTC','2026-11-14 19:45:36 +0000 UTC','critical',300,NULL,1,'','backup_failed','','','','');
INSERT INTO "alert_rules" VALUES(12,'Backup overdue','*','','',0,0,'[]',1,NULL,NULL,NULL,'2026-11-14 19:45:36 +0000 UTC','2026-11-14 19:45:36 +0000 UTC','warning',21600,NULL,1,'','backup_missed','','','','');
INSERT INTO "alert_rules" VALUES(13,'TLS certificate expiring','*','','',14,0,'[]',1,NULL,NULL,NULL,'2026-11-14 19:45:36 +0000 UTC','2026-11-14 19:45:36 +0000 UTC','warning',86400,NULL,1,'','cert_expiry','','','','');
CREATE TABLE alert_history (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    rule_id         INTEGER NOT NULL,
    rule_name       TEXT NOT NULL,
    container_name  TEXT NOT NULL,
    status          TEXT NOT NULL,        -- "fired" | "resolved"
    message         TEXT NOT NULL,
    value           REAL,
    threshold       REAL,
    occurred_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
, notified INTEGER NOT NULL DEFAULT 1, host_id TEXT NOT NULL DEFAULT 'local', repeats INTEGER NOT NULL DEFAULT 0, message_i18n TEXT NOT NULL DEFAULT '');
CREATE TABLE backup_jobs (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    name            TEXT NOT NULL UNIQUE,
    target_type     TEXT NOT NULL,
    target_config   TEXT NOT NULL DEFAULT '{}',
    sources         TEXT NOT NULL DEFAULT '[]',
    schedule        TEXT NOT NULL DEFAULT '',
    retention_count INTEGER NOT NULL DEFAULT 0,
    retention_days  INTEGER NOT NULL DEFAULT 0,
    encrypt         INTEGER NOT NULL DEFAULT 1,
    pre_hooks       TEXT NOT NULL DEFAULT '[]',
    post_hooks      TEXT NOT NULL DEFAULT '[]',
    enabled         INTEGER NOT NULL DEFAULT 1,
    last_run_at     DATETIME,
    next_run_at     DATETIME,
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
, target_id INTEGER REFERENCES backup_targets(id), host_id TEXT NOT NULL DEFAULT '', needs_review INTEGER NOT NULL DEFAULT 0, review_reason TEXT NOT NULL DEFAULT '', review_acked INTEGER NOT NULL DEFAULT 0, deleted_at DATETIME);
INSERT INTO "backup_jobs" VALUES(1,'sys-abs','local','{}','[{"type":"system","name":"dockmesh"}]','0 3 * * *',0,14,0,'null','null',1,NULL,NULL,'2026-11-14 19:45:37 +0000 UTC','2026-11-14 19:45:37 +0000 UTC',1,'',0,'',0,NULL);
INSERT INTO "backup_jobs" VALUES(2,'sys-rel','local','{}','[{"type":"system","name":"dockmesh"}]','0 3 * * *',0,14,0,'null','null',1,NULL,NULL,'2026-11-14 19:45:37 +0000 UTC','2026-11-14 19:45:37 +0000 UTC',2,'',0,'',0,NULL);
INSERT INTO "backup_jobs" VALUES(3,'sys-default','local','{}','[{"type":"system","name":"dockmesh"}]','0 3 * * *',0,14,0,'null','null',1,NULL,NULL,'2026-11-14 19:45:37 +0000 UTC','2026-11-14 19:45:37 +0000 UTC',3,'',0,'',0,NULL);
INSERT INTO "backup_jobs" VALUES(4,'sys-smb','smb','{}','[{"type":"system","name":"dockmesh"}]','0 3 * * *',0,14,0,'null','null',1,NULL,NULL,'2026-11-14 19:45:37 +0000 UTC','2026-11-14 19:45:37 +0000 UTC',4,'',0,'',0,NULL);
INSERT INTO "backup_jobs" VALUES(5,'sys-s3','s3','{}','[{"type":"system","name":"dockmesh"}]','0 3 * * *',0,14,0,'null','null',1,NULL,NULL,'2026-11-14 19:45:37 +0000 UTC','2026-11-14 19:45:37 +0000 UTC',5,'',0,'',0,NULL);
CREATE TABLE backup_runs (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id       INTEGER NOT NULL,
    job_name     TEXT NOT NULL,
    status       TEXT NOT NULL,                         -- "running" | "success" | "failed"
    started_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finished_at  DATETIME,
    size_bytes   INTEGER NOT NULL DEFAULT 0,
    target_path  TEXT,
    sha256       TEXT,
    encrypted    INTEGER NOT NULL DEFAULT 0,
    error        TEXT,
    sources_json TEXT NOT NULL DEFAULT '[]'
, pruned_at DATETIME, host_id TEXT NOT NULL DEFAULT '');
CREATE TABLE agents (
    id                    TEXT PRIMARY KEY,
    name                  TEXT NOT NULL UNIQUE,
    enrollment_token_hash TEXT,                -- SHA-256 hex; NULL once enrolled
    cert_fingerprint      TEXT,                -- SHA-256 hex of the client cert SPKI
    status                TEXT NOT NULL DEFAULT 'pending', -- pending | online | offline | revoked
    version               TEXT,
    os                    TEXT,
    arch                  TEXT,
    hostname              TEXT,
    docker_version        TEXT,
    last_seen_at          DATETIME,
    created_at            DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at            DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
, online_since DATETIME, enrollment_token_expires_at DATETIME, pending_cert_fingerprint TEXT, cert_expires_at DATETIME);
CREATE TABLE stack_deployments (
    stack_name  TEXT    NOT NULL PRIMARY KEY,
    host_id     TEXT    NOT NULL,
    status      TEXT    NOT NULL DEFAULT 'deployed',  -- deployed | stopped | migrating | migrated_away
    deployed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE migrations (
    id              TEXT    NOT NULL PRIMARY KEY,
    stack_name      TEXT    NOT NULL,
    source_host_id  TEXT    NOT NULL,
    target_host_id  TEXT    NOT NULL,
    status          TEXT    NOT NULL DEFAULT 'pending',
    phase           TEXT,
    progress_json   TEXT,
    started_at      DATETIME,
    completed_at    DATETIME,
    error_message   TEXT,
    initiated_by    TEXT    NOT NULL,
    drain_id        TEXT,
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE drains (
    id              TEXT    NOT NULL PRIMARY KEY,
    source_host_id  TEXT    NOT NULL,
    status          TEXT    NOT NULL DEFAULT 'planning',  -- planning | executing | paused | completed | aborted
    plan_json       TEXT    NOT NULL DEFAULT '[]',
    started_at      DATETIME,
    completed_at    DATETIME,
    initiated_by    TEXT    NOT NULL,
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE roles (
    name        TEXT NOT NULL PRIMARY KEY,
    display     TEXT NOT NULL,
    builtin     INTEGER NOT NULL DEFAULT 0,  -- 1 for admin/operator/viewer
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
, description TEXT NOT NULL DEFAULT '');
INSERT INTO "roles" VALUES('admin','Admin',1,'2026-11-14 19:45:36 +0000 UTC','Everything, including users, roles, settings and updates.');
INSERT INTO "roles" VALUES('operator','Operator',1,'2026-11-14 19:45:36 +0000 UTC','Day-to-day operations: start, stop, restart, logs and terminal. No deploys.');
INSERT INTO "roles" VALUES('viewer','Viewer',1,'2026-11-14 19:45:36 +0000 UTC','Read-only: sees everything it is assigned to, changes nothing.');
INSERT INTO "roles" VALUES('deployer','Deployer',1,'2026-11-14 19:45:36 +0000 UTC','Ships changes: deploys stacks and edits compose. Cannot delete stacks or open terminals.');
INSERT INTO "roles" VALUES('host-admin','Host Admin',1,'2026-11-14 19:45:36 +0000 UTC','Runs hosts end to end: stacks, containers, proxy, backups, alerts. No users or instance settings.');
CREATE TABLE global_env (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    key         TEXT NOT NULL,
    value       TEXT NOT NULL DEFAULT '',
    group_name  TEXT NOT NULL DEFAULT '',  -- e.g. "database", "smtp", "" for ungrouped
    encrypted   INTEGER NOT NULL DEFAULT 0,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(key)
);
CREATE TABLE backup_targets (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT    NOT NULL UNIQUE,
    type        TEXT    NOT NULL,  -- local | s3 | sftp | smb | webdav
    config_json TEXT    NOT NULL DEFAULT '{}',
    status      TEXT    NOT NULL DEFAULT 'unknown',  -- connected | error | unknown
    total_bytes INTEGER NOT NULL DEFAULT 0,
    used_bytes  INTEGER NOT NULL DEFAULT 0,
    last_checked_at DATETIME,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO "backup_targets" VALUES(1,'sys-abs','local','{"path":"/srv/nas-backups"}','unknown',0,0,NULL,'2026-11-14 19:45:37 +0000 UTC','2026-11-14 19:45:37 +0000 UTC');
INSERT INTO "backup_targets" VALUES(2,'sys-rel','local','{"path":"./rel-backups"}','unknown',0,0,NULL,'2026-11-14 19:45:37 +0000 UTC','2026-11-14 19:45:37 +0000 UTC');
INSERT INTO "backup_targets" VALUES(3,'sys-default','local','{}','unknown',0,0,NULL,'2026-11-14 19:45:37 +0000 UTC','2026-11-14 19:45:37 +0000 UTC');
INSERT INTO "backup_targets" VALUES(4,'sys-smb','smb','{"host":"nas.example.test","password":"pw","path":"dockmesh","share":"backups","username":"dm"}','unknown',0,0,NULL,'2026-11-14 19:45:37 +0000 UTC','2026-11-14 19:45:37 +0000 UTC');
INSERT INTO "backup_targets" VALUES(5,'sys-s3','s3','{"access_key":"AK","bucket":"dm-backups","endpoint":"s3.example.test","prefix":"srv1","secret_key":"SK"}','unknown',0,0,NULL,'2026-11-14 19:45:37 +0000 UTC','2026-11-14 19:45:37 +0000 UTC');
CREATE TABLE host_tags (
    host_id     TEXT    NOT NULL,
    tag         TEXT    NOT NULL,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (host_id, tag),
    CHECK (tag GLOB '[a-z0-9]*' AND length(tag) BETWEEN 1 AND 32)
);
CREATE TABLE registries (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    name               TEXT NOT NULL,
    url                TEXT NOT NULL,
    username           TEXT,
    password_encrypted BLOB,
    scope_tags         TEXT,
    last_tested_at     DATETIME,
    last_test_ok       INTEGER,
    last_test_error    TEXT,
    created_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (url)
);
CREATE TABLE stack_git_sources (
    stack_name         TEXT PRIMARY KEY,
    repo_url           TEXT NOT NULL,
    branch             TEXT NOT NULL DEFAULT 'main',
    path_in_repo       TEXT NOT NULL DEFAULT '.',      -- dir containing compose.yaml (and optional .env)
    auth_kind          TEXT NOT NULL DEFAULT 'none',    -- 'none' | 'http' | 'ssh'
    username           TEXT,
    password_encrypted BLOB,                            -- age-encrypted HTTP password / PAT
    ssh_key_encrypted  BLOB,                            -- age-encrypted OpenSSH private key
    auto_deploy        INTEGER NOT NULL DEFAULT 0,
    poll_interval_sec  INTEGER NOT NULL DEFAULT 300,
    webhook_secret     TEXT,                            -- shared secret for HMAC webhook verification
    last_sync_sha      TEXT,
    last_sync_at       DATETIME,
    last_sync_error    TEXT,
    created_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
, last_env_drift TEXT);
CREATE TABLE stack_templates (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    slug        TEXT NOT NULL UNIQUE,        -- stable identifier, e.g. "postgres-16"
    name        TEXT NOT NULL,               -- human label, e.g. "PostgreSQL 16"
    description TEXT,
    icon_url    TEXT,                         -- optional image URL (external or /static/…)
    compose     TEXT NOT NULL,               -- compose.yaml with {{param}} placeholders
    env_tmpl    TEXT,                         -- optional .env template
    parameters  TEXT NOT NULL DEFAULT '[]',   -- JSON array of ParamDef
    author      TEXT,
    version     TEXT,
    builtin     INTEGER NOT NULL DEFAULT 0,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
, category TEXT NOT NULL DEFAULT '', featured INTEGER NOT NULL DEFAULT 0, deploys_count INTEGER NOT NULL DEFAULT 0);
CREATE TABLE stack_deploy_history (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    stack_name    TEXT     NOT NULL,
    host_id       TEXT     NOT NULL,
    compose_yaml  TEXT     NOT NULL,
    services_json TEXT,                    -- JSON: [{"service":"web","image":"…"}]
    note          TEXT,
    deployed_by   TEXT,                    -- users.id (TEXT, UUID-style), nullable for system/api-token-no-user
    deployed_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
, success INTEGER, duration_ms INTEGER, git_commit_sha TEXT, error_message TEXT, environment TEXT NOT NULL DEFAULT '');
CREATE TABLE stack_dependencies (
    stack_name  TEXT NOT NULL,
    depends_on  TEXT NOT NULL,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (stack_name, depends_on),
    CHECK (stack_name <> depends_on)
);
CREATE TABLE saml_providers (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    slug                TEXT NOT NULL UNIQUE,
    display_name        TEXT NOT NULL,
    entity_id           TEXT NOT NULL,
    sso_url             TEXT NOT NULL,
    slo_url             TEXT,
    idp_metadata_xml    TEXT NOT NULL,
    idp_cert_pem        TEXT NOT NULL,
    nameid_format       TEXT NOT NULL DEFAULT 'urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress',
    username_attribute  TEXT NOT NULL DEFAULT 'NameID',
    email_attribute     TEXT NOT NULL DEFAULT 'email',
    groups_attribute    TEXT NOT NULL DEFAULT 'groups',
    default_role        TEXT NOT NULL DEFAULT 'viewer',
    enabled             INTEGER NOT NULL DEFAULT 1,
    is_default          INTEGER NOT NULL DEFAULT 0,
    last_tested_at      DATETIME,
    last_test_ok        INTEGER,
    last_test_error     TEXT,
    created_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
, allow_email_link INTEGER NOT NULL DEFAULT 0, subject_attribute TEXT NOT NULL DEFAULT '', idp_metadata_url TEXT NOT NULL DEFAULT '', metadata_fetched_at TIMESTAMP, metadata_error TEXT NOT NULL DEFAULT '', sp_entity_id TEXT NOT NULL DEFAULT '', signing_cert_pem TEXT NOT NULL DEFAULT '', encryption_cert_pem TEXT NOT NULL DEFAULT '', encryption_key TEXT NOT NULL DEFAULT '');
CREATE TABLE saml_sp_keys (
    id          INTEGER PRIMARY KEY CHECK (id = 1),
    key_pem     TEXT NOT NULL,
    cert_pem    TEXT NOT NULL,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE ldap_providers (
    id                          INTEGER PRIMARY KEY AUTOINCREMENT,
    slug                        TEXT NOT NULL UNIQUE,
    display_name                TEXT NOT NULL,
    host                        TEXT NOT NULL,
    port                        INTEGER NOT NULL DEFAULT 636,
    tls_mode                    TEXT NOT NULL DEFAULT 'ldaps',  -- ldaps | starttls | none
    skip_verify                 INTEGER NOT NULL DEFAULT 0,
    bind_dn                     TEXT,
    bind_password               TEXT,  -- encrypted at rest when secrets enabled
    user_search_base            TEXT NOT NULL,
    user_search_filter          TEXT NOT NULL DEFAULT '(&(objectClass=person)(uid=%s))',
    username_attribute          TEXT NOT NULL DEFAULT 'uid',
    email_attribute             TEXT NOT NULL DEFAULT 'mail',
    group_search_base           TEXT,
    group_membership_attribute  TEXT NOT NULL DEFAULT 'memberOf',
    default_role                TEXT NOT NULL DEFAULT 'viewer',
    enabled                     INTEGER NOT NULL DEFAULT 1,
    is_default                  INTEGER NOT NULL DEFAULT 0,
    last_tested_at              DATETIME,
    last_test_ok                INTEGER,
    last_test_error             TEXT,
    created_at                  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at                  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
, allow_email_link INTEGER NOT NULL DEFAULT 0, subject_attribute TEXT NOT NULL DEFAULT '');
CREATE TABLE oauth2_providers (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    slug                TEXT NOT NULL UNIQUE,
    display_name        TEXT NOT NULL,
    authorization_url   TEXT NOT NULL,
    token_url           TEXT NOT NULL,
    userinfo_url        TEXT NOT NULL,
    client_id           TEXT NOT NULL,
    client_secret       TEXT NOT NULL,
    scopes              TEXT NOT NULL DEFAULT '',
    username_field      TEXT NOT NULL DEFAULT 'username',
    email_field         TEXT NOT NULL DEFAULT 'email',
    groups_field        TEXT NOT NULL DEFAULT 'groups',
    default_role        TEXT NOT NULL DEFAULT 'viewer',
    enabled             INTEGER NOT NULL DEFAULT 1,
    is_default          INTEGER NOT NULL DEFAULT 0,
    last_tested_at      DATETIME,
    last_test_ok        INTEGER,
    last_test_error     TEXT,
    created_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
, allow_email_link INTEGER NOT NULL DEFAULT 0, subject_field TEXT NOT NULL DEFAULT '');
CREATE TABLE backup_run_logs (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id  INTEGER NOT NULL,
    ts      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    -- stream is one of: info | warn | error | hook_stdout | hook_stderr.
    -- "info" covers normal phase narration; "warn" is non-fatal issues
    -- (e.g. post-hook failed but backup succeeded); "error" is fatal.
    -- hook_stdout/hook_stderr carry verbatim subprocess output, capped
    -- per hook to avoid swamping the table on noisy hooks.
    stream  TEXT NOT NULL,
    line    TEXT NOT NULL,
    FOREIGN KEY (run_id) REFERENCES backup_runs(id) ON DELETE CASCADE
);
CREATE TABLE invite_tokens (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    token_hash      TEXT    NOT NULL UNIQUE,
    role            TEXT    NOT NULL,
    -- JSON array of scope tags, e.g. ["team-platform","host:prod-eu"].
    -- Empty list = unscoped (no host filtering applied).
    email_hint      TEXT    NOT NULL DEFAULT '',
    expires_at      DATETIME NOT NULL,
    used_at         DATETIME,
    -- created_by stores the inviting user's id so the audit log can
    -- attribute the invite. We keep the row even after used_at is set
    -- so the admin can see "alice@… accepted on Y, invited by X".
    created_by      TEXT    NOT NULL,
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
, scopes_json TEXT NOT NULL DEFAULT '[{"type":"global"}]');
CREATE TABLE "notifications" (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     TEXT NOT NULL,
    kind        TEXT NOT NULL,
    severity    TEXT NOT NULL DEFAULT 'info',
    title       TEXT NOT NULL,
    body        TEXT,
    link        TEXT,
    read_at     DATETIME,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE TABLE notices (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    key             TEXT NOT NULL UNIQUE,
    audience        TEXT NOT NULL,
    audience_value  TEXT NOT NULL DEFAULT '',
    severity        TEXT NOT NULL,
    title           TEXT NOT NULL,
    body            TEXT NOT NULL DEFAULT '',
    link            TEXT NOT NULL DEFAULT '',
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    resolved_at     DATETIME
, blocking INTEGER NOT NULL DEFAULT 0, title_i18n TEXT NOT NULL DEFAULT '', body_i18n TEXT NOT NULL DEFAULT '');
CREATE TABLE notice_dismissals (
    notice_id     INTEGER NOT NULL REFERENCES notices(id) ON DELETE CASCADE,
    user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    dismissed_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, hidden_until DATETIME,
    PRIMARY KEY (notice_id, user_id)
);
CREATE TABLE "api_tokens" (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    token_prefix       TEXT    NOT NULL UNIQUE,
    token_hash         TEXT    NOT NULL,
    name               TEXT    NOT NULL,
    role               TEXT    NOT NULL,
    created_by_user_id TEXT    REFERENCES users(id) ON DELETE SET NULL,
    legacy             INTEGER NOT NULL DEFAULT 0,
    created_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at         DATETIME,
    last_used_at       DATETIME,
    last_used_ip       TEXT,
    revoked_at         DATETIME, scope_type  TEXT NOT NULL DEFAULT 'global', scope_host  TEXT NOT NULL DEFAULT '', scope_value TEXT NOT NULL DEFAULT '', parent_token_id INTEGER,
    CHECK (token_prefix LIKE 'dmt_%')
);
CREATE TABLE stack_file_sums (
    stack      TEXT NOT NULL,
    file       TEXT NOT NULL,
    sum        TEXT NOT NULL,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (stack, file)
);
CREATE TABLE image_update_checks (
    image            TEXT PRIMARY KEY,
    update_available INTEGER NOT NULL DEFAULT 0,
    local_digest     TEXT NOT NULL DEFAULT '',
    remote_digest    TEXT NOT NULL DEFAULT '',
    error            TEXT NOT NULL DEFAULT '',
    notified_digest  TEXT NOT NULL DEFAULT '',
    checked_at       DATETIME NOT NULL
);
CREATE TABLE user_preferences (
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    key        TEXT NOT NULL,
    value      TEXT NOT NULL,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (user_id, key)
);
CREATE TABLE operations (
    id          TEXT PRIMARY KEY,
    kind        TEXT NOT NULL,
    target      TEXT NOT NULL DEFAULT '',
    ref         TEXT NOT NULL DEFAULT '',
    group_id    TEXT NOT NULL DEFAULT '',
    host_id     TEXT NOT NULL DEFAULT '',
    stack       TEXT NOT NULL DEFAULT '',
    admin_only  INTEGER NOT NULL DEFAULT 0,
    actor       TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL,
    error       TEXT NOT NULL DEFAULT '',
    steps       TEXT NOT NULL DEFAULT '[]',
    log         TEXT NOT NULL DEFAULT '[]',
    created_at  INTEGER NOT NULL,
    started_at  INTEGER,
    finished_at INTEGER
);
CREATE TABLE role_levels (
    role_name TEXT NOT NULL REFERENCES roles(name) ON DELETE CASCADE,
    area      TEXT NOT NULL,
    level     TEXT NOT NULL,
    PRIMARY KEY (role_name, area)
);
INSERT INTO "role_levels" VALUES('admin','users','manage');
INSERT INTO "role_levels" VALUES('admin','settings','manage');
INSERT INTO "role_levels" VALUES('admin','stacks','manage');
INSERT INTO "role_levels" VALUES('admin','resources','manage');
INSERT INTO "role_levels" VALUES('admin','backups','manage');
INSERT INTO "role_levels" VALUES('admin','containers','manage');
INSERT INTO "role_levels" VALUES('admin','hosts','manage');
INSERT INTO "role_levels" VALUES('admin','proxy','manage');
INSERT INTO "role_levels" VALUES('admin','alerts','manage');
INSERT INTO "role_levels" VALUES('host-admin','hosts','manage');
INSERT INTO "role_levels" VALUES('host-admin','proxy','manage');
INSERT INTO "role_levels" VALUES('host-admin','backups','manage');
INSERT INTO "role_levels" VALUES('host-admin','alerts','manage');
INSERT INTO "role_levels" VALUES('host-admin','settings','view');
INSERT INTO "role_levels" VALUES('host-admin','stacks','manage');
INSERT INTO "role_levels" VALUES('host-admin','containers','manage');
INSERT INTO "role_levels" VALUES('host-admin','resources','manage');
INSERT INTO "role_levels" VALUES('deployer','alerts','view');
INSERT INTO "role_levels" VALUES('deployer','stacks','deploy');
INSERT INTO "role_levels" VALUES('deployer','containers','operate');
INSERT INTO "role_levels" VALUES('deployer','resources','view');
INSERT INTO "role_levels" VALUES('deployer','hosts','view');
INSERT INTO "role_levels" VALUES('deployer','proxy','view');
INSERT INTO "role_levels" VALUES('deployer','backups','view');
INSERT INTO "role_levels" VALUES('operator','hosts','view');
INSERT INTO "role_levels" VALUES('operator','backups','view');
INSERT INTO "role_levels" VALUES('operator','alerts','view');
INSERT INTO "role_levels" VALUES('operator','stacks','operate');
INSERT INTO "role_levels" VALUES('operator','containers','operate');
INSERT INTO "role_levels" VALUES('operator','resources','view');
INSERT INTO "role_levels" VALUES('viewer','resources','view');
INSERT INTO "role_levels" VALUES('viewer','hosts','view');
INSERT INTO "role_levels" VALUES('viewer','proxy','view');
INSERT INTO "role_levels" VALUES('viewer','backups','view');
INSERT INTO "role_levels" VALUES('viewer','alerts','view');
INSERT INTO "role_levels" VALUES('viewer','settings','view');
INSERT INTO "role_levels" VALUES('viewer','stacks','view');
INSERT INTO "role_levels" VALUES('viewer','containers','view');
CREATE TABLE role_extras (
    role_name TEXT NOT NULL REFERENCES roles(name) ON DELETE CASCADE,
    extra     TEXT NOT NULL,
    PRIMARY KEY (role_name, extra)
);
INSERT INTO "role_extras" VALUES('admin','terminal');
INSERT INTO "role_extras" VALUES('admin','files');
INSERT INTO "role_extras" VALUES('admin','env');
INSERT INTO "role_extras" VALUES('admin','backup_download');
INSERT INTO "role_extras" VALUES('admin','key_export');
INSERT INTO "role_extras" VALUES('host-admin','terminal');
INSERT INTO "role_extras" VALUES('host-admin','files');
INSERT INTO "role_extras" VALUES('host-admin','env');
INSERT INTO "role_extras" VALUES('host-admin','backup_download');
INSERT INTO "role_extras" VALUES('operator','terminal');
CREATE TABLE role_assignments (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role_name   TEXT NOT NULL REFERENCES roles(name),
    scope_type  TEXT NOT NULL CHECK (scope_type IN ('global', 'host', 'host_tag', 'stack')),
    scope_host  TEXT NOT NULL DEFAULT '',
    scope_value TEXT NOT NULL DEFAULT '',
    source      TEXT NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'idp')),
    via         TEXT NOT NULL DEFAULT '',
    created_by  TEXT,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (user_id, role_name, scope_type, scope_host, scope_value, source, via)
);
CREATE TABLE "oidc_provider_group_mappings" (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    provider_id INTEGER NOT NULL REFERENCES oidc_providers(id) ON DELETE CASCADE,
    group_value TEXT NOT NULL,
    role_name   TEXT NOT NULL,
    scope_type  TEXT NOT NULL DEFAULT 'global',
    scope_host  TEXT NOT NULL DEFAULT '',
    scope_value TEXT NOT NULL DEFAULT '',
    UNIQUE (provider_id, group_value, role_name, scope_type, scope_host, scope_value)
);
CREATE TABLE "saml_provider_group_mappings" (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    provider_id INTEGER NOT NULL REFERENCES saml_providers(id) ON DELETE CASCADE,
    group_value TEXT NOT NULL,
    role_name   TEXT NOT NULL,
    scope_type  TEXT NOT NULL DEFAULT 'global',
    scope_host  TEXT NOT NULL DEFAULT '',
    scope_value TEXT NOT NULL DEFAULT '',
    UNIQUE (provider_id, group_value, role_name, scope_type, scope_host, scope_value)
);
CREATE TABLE "ldap_provider_group_mappings" (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    provider_id INTEGER NOT NULL REFERENCES ldap_providers(id) ON DELETE CASCADE,
    group_value TEXT NOT NULL,
    role_name   TEXT NOT NULL,
    scope_type  TEXT NOT NULL DEFAULT 'global',
    scope_host  TEXT NOT NULL DEFAULT '',
    scope_value TEXT NOT NULL DEFAULT '',
    UNIQUE (provider_id, group_value, role_name, scope_type, scope_host, scope_value)
);
CREATE TABLE "oauth2_provider_group_mappings" (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    provider_id INTEGER NOT NULL REFERENCES oauth2_providers(id) ON DELETE CASCADE,
    group_value TEXT NOT NULL,
    role_name   TEXT NOT NULL,
    scope_type  TEXT NOT NULL DEFAULT 'global',
    scope_host  TEXT NOT NULL DEFAULT '',
    scope_value TEXT NOT NULL DEFAULT '',
    UNIQUE (provider_id, group_value, role_name, scope_type, scope_host, scope_value)
);
CREATE TABLE "metrics_raw" (
    host_id        TEXT    NOT NULL DEFAULT 'local',
    container_name TEXT    NOT NULL,
    ts             INTEGER NOT NULL,
    cpu_percent    REAL    NOT NULL,
    cpu_cores      INTEGER NOT NULL DEFAULT 0,
    mem_used       INTEGER NOT NULL,
    mem_limit      INTEGER NOT NULL,
    net_rx         INTEGER NOT NULL,
    net_tx         INTEGER NOT NULL,
    blk_read       INTEGER NOT NULL,
    blk_write      INTEGER NOT NULL,
    PRIMARY KEY (host_id, container_name, ts)
);
CREATE TABLE "metrics_1m" (
    host_id        TEXT    NOT NULL DEFAULT 'local',
    container_name TEXT    NOT NULL,
    ts             INTEGER NOT NULL,
    cpu_percent    REAL    NOT NULL,
    cpu_cores      INTEGER NOT NULL DEFAULT 0,
    mem_used       INTEGER NOT NULL,
    mem_limit      INTEGER NOT NULL,
    net_rx         INTEGER NOT NULL,
    net_tx         INTEGER NOT NULL,
    blk_read       INTEGER NOT NULL,
    blk_write      INTEGER NOT NULL,
    PRIMARY KEY (host_id, container_name, ts)
);
CREATE TABLE "metrics_1h" (
    host_id        TEXT    NOT NULL DEFAULT 'local',
    container_name TEXT    NOT NULL,
    ts             INTEGER NOT NULL,
    cpu_percent    REAL    NOT NULL,
    cpu_cores      INTEGER NOT NULL DEFAULT 0,
    mem_used       INTEGER NOT NULL,
    mem_limit      INTEGER NOT NULL,
    net_rx         INTEGER NOT NULL,
    net_tx         INTEGER NOT NULL,
    blk_read       INTEGER NOT NULL,
    blk_write      INTEGER NOT NULL,
    PRIMARY KEY (host_id, container_name, ts)
);
CREATE TABLE host_metrics_raw (
    host_id     TEXT    NOT NULL,
    ts          INTEGER NOT NULL,
    cpu_percent REAL    NOT NULL,
    cpu_cores   INTEGER NOT NULL,
    mem_used    INTEGER NOT NULL,
    mem_total   INTEGER NOT NULL,
    disk_used   INTEGER NOT NULL,
    disk_total  INTEGER NOT NULL,
    PRIMARY KEY (host_id, ts)
);
CREATE TABLE host_metrics_1m (
    host_id     TEXT    NOT NULL,
    ts          INTEGER NOT NULL,
    cpu_percent REAL    NOT NULL,
    cpu_cores   INTEGER NOT NULL,
    mem_used    INTEGER NOT NULL,
    mem_total   INTEGER NOT NULL,
    disk_used   INTEGER NOT NULL,
    disk_total  INTEGER NOT NULL,
    PRIMARY KEY (host_id, ts)
);
CREATE TABLE host_metrics_1h (
    host_id     TEXT    NOT NULL,
    ts          INTEGER NOT NULL,
    cpu_percent REAL    NOT NULL,
    cpu_cores   INTEGER NOT NULL,
    mem_used    INTEGER NOT NULL,
    mem_total   INTEGER NOT NULL,
    disk_used   INTEGER NOT NULL,
    disk_total  INTEGER NOT NULL,
    PRIMARY KEY (host_id, ts)
);
CREATE TABLE stack_targets (
    stack_name TEXT     NOT NULL PRIMARY KEY,
    host_id    TEXT     NOT NULL,
    created_by TEXT     NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_audit_ts ON audit_log(ts);
CREATE INDEX idx_sessions_user ON sessions(user_id);
CREATE INDEX idx_scan_scanned_at ON scan_results(scanned_at);
CREATE INDEX idx_proxy_host ON proxy_routes(host);
CREATE INDEX idx_update_history_container ON update_history(container_name);
CREATE INDEX idx_users_oidc ON users(oidc_provider, oidc_subject);
CREATE INDEX idx_alert_history_rule ON alert_history(rule_id);
CREATE INDEX idx_alert_history_ts ON alert_history(occurred_at);
CREATE INDEX idx_backup_runs_job ON backup_runs(job_id);
CREATE INDEX idx_backup_runs_started ON backup_runs(started_at);
CREATE INDEX idx_agents_fingerprint ON agents(cert_fingerprint);
CREATE INDEX idx_agents_status ON agents(status);
CREATE INDEX idx_stack_deployments_host ON stack_deployments(host_id);
CREATE INDEX idx_stack_deployments_status ON stack_deployments(status);
CREATE INDEX idx_migrations_stack ON migrations(stack_name, status);
CREATE INDEX idx_migrations_active ON migrations(status)
    WHERE status NOT IN ('completed', 'failed', 'rolled_back');
CREATE INDEX idx_drains_host ON drains(source_host_id);
CREATE INDEX idx_drains_status ON drains(status);
CREATE INDEX idx_host_tags_tag ON host_tags(tag);
CREATE INDEX idx_registries_url ON registries(url);
CREATE INDEX idx_stack_templates_slug ON stack_templates(slug);
CREATE INDEX idx_stack_deploy_history_stack
    ON stack_deploy_history(stack_name, deployed_at DESC);
CREATE INDEX idx_stack_dependencies_depends_on
    ON stack_dependencies(depends_on);
CREATE INDEX idx_sessions_last_seen ON sessions(user_id, last_seen_at);
CREATE INDEX idx_stack_templates_category ON stack_templates(category);
CREATE INDEX idx_stack_templates_featured ON stack_templates(featured) WHERE featured = 1;
CREATE INDEX idx_users_saml ON users(saml_provider, saml_subject);
CREATE INDEX idx_users_ldap ON users(ldap_provider, ldap_subject);
CREATE INDEX idx_users_oauth2 ON users(oauth2_provider, oauth2_subject);
CREATE INDEX idx_backup_run_logs_run_id
    ON backup_run_logs(run_id, id);
CREATE INDEX idx_invite_tokens_expires ON invite_tokens(expires_at);
CREATE INDEX idx_notifications_user_unread
    ON notifications(user_id, read_at) WHERE read_at IS NULL;
CREATE INDEX idx_notifications_user_created
    ON notifications(user_id, created_at);
CREATE INDEX idx_notices_open ON notices(resolved_at) WHERE resolved_at IS NULL;
CREATE INDEX idx_api_tokens_active ON api_tokens(revoked_at) WHERE revoked_at IS NULL;
CREATE INDEX idx_api_tokens_owner ON api_tokens(created_by_user_id);
CREATE INDEX idx_agents_pending_cert_fp ON agents(pending_cert_fingerprint);
CREATE INDEX operations_created ON operations(created_at);
CREATE INDEX operations_status ON operations(status);
CREATE INDEX operations_ref ON operations(kind, ref);
CREATE INDEX idx_role_assignments_role ON role_assignments(role_name);
CREATE INDEX idx_metrics_raw_ts ON metrics_raw(ts);
CREATE INDEX idx_metrics_1m_ts ON metrics_1m(ts);
CREATE INDEX idx_metrics_1h_ts ON metrics_1h(ts);
CREATE INDEX idx_host_metrics_raw_ts ON host_metrics_raw(ts);
CREATE INDEX idx_host_metrics_1m_ts ON host_metrics_1m(ts);
CREATE INDEX idx_host_metrics_1h_ts ON host_metrics_1h(ts);
CREATE INDEX idx_alert_history_key ON alert_history(rule_id, host_id, container_name, id);
CREATE INDEX idx_alert_history_host ON alert_history(host_id, occurred_at);
COMMIT;
