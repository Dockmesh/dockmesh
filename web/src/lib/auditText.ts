// Audit entries carry neutral action codes ("stack.create"); the UI turns
// them into a short phrase for tables and a sentence with actor and target,
// whose word order differs between languages.
import { m } from '$lib/paraglide/messages.js';

type Phrase = () => string;
type Sentence = (inputs: { actor: string; target: string }) => string;

const ACTIONS: Record<string, [Phrase, Sentence]> = {
  'agent.create': [m.audit_act_agent_create, m.audit_sen_agent_create],
  'agent.delete': [m.audit_act_agent_delete, m.audit_sen_agent_delete],
  'agent.rotate_token': [m.audit_act_agent_rotate_token, m.audit_sen_agent_rotate_token],
  'agent.upgrade': [m.audit_act_agent_upgrade, m.audit_sen_agent_upgrade],
  'agent.upgrade_policy': [m.audit_act_agent_upgrade_policy, m.audit_sen_agent_upgrade_policy],
  'agent.upgrade_run': [m.audit_act_agent_upgrade_run, m.audit_sen_agent_upgrade_run],
  'alerts.rule_create': [m.audit_act_alerts_rule_create, m.audit_sen_alerts_rule_create],
  'alerts.rule_update': [m.audit_act_alerts_rule_update, m.audit_sen_alerts_rule_update],
  'alerts.rule_delete': [m.audit_act_alerts_rule_delete, m.audit_sen_alerts_rule_delete],
  'api_token.create': [m.audit_act_api_token_create, m.audit_sen_api_token_create],
  'api_token.revoke': [m.audit_act_api_token_revoke, m.audit_sen_api_token_revoke],
  'assignment.add': [m.audit_act_assignment_add, m.audit_sen_assignment_add],
  'assignment.remove': [m.audit_act_assignment_remove, m.audit_sen_assignment_remove],
  'audit.chain_bridge': [m.audit_act_audit_chain_bridge, m.audit_sen_audit_chain_bridge],
  'audit.genesis': [m.audit_act_audit_genesis, m.audit_sen_audit_genesis],
  'audit.retention_configure': [m.audit_act_audit_retention_configure, m.audit_sen_audit_retention_configure],
  'audit.retention_run': [m.audit_act_audit_retention_run, m.audit_sen_audit_retention_run],
  'audit.webhook_configure': [m.audit_act_audit_webhook_configure, m.audit_sen_audit_webhook_configure],
  'audit.webhook_test': [m.audit_act_audit_webhook_test, m.audit_sen_audit_webhook_test],
  'auth.lockout': [m.audit_act_auth_lockout, m.audit_sen_auth_lockout],
  'auth.login': [m.audit_act_auth_login, m.audit_sen_auth_login],
  'auth.login_failed': [m.audit_act_auth_login_failed, m.audit_sen_auth_login_failed],
  'auth.logout': [m.audit_act_auth_logout, m.audit_sen_auth_logout],
  'auth.policy_update': [m.audit_act_auth_policy_update, m.audit_sen_auth_policy_update],
  'auth.refresh': [m.audit_act_auth_refresh, m.audit_sen_auth_refresh],
  'auth.saml_rekey': [m.audit_act_auth_saml_rekey, m.audit_sen_auth_saml_rekey],
  'auth.saml_rekey_end': [m.audit_act_auth_saml_rekey_end, m.audit_sen_auth_saml_rekey_end],
  'auth.scope_denied': [m.audit_act_auth_scope_denied, m.audit_sen_auth_scope_denied],
  'auth.session_revoke': [m.audit_act_auth_session_revoke, m.audit_sen_auth_session_revoke],
  'auth.session_revoke_all': [m.audit_act_auth_session_revoke_all, m.audit_sen_auth_session_revoke_all],
  'auth.signin_config_update': [m.audit_act_auth_signin_config_update, m.audit_sen_auth_signin_config_update],
  'auth.sso_login': [m.audit_act_auth_sso_login, m.audit_sen_auth_sso_login],
  'auth.sso_rekey': [m.audit_act_auth_sso_rekey, m.audit_sen_auth_sso_rekey],
  'auth.sso_rekey_end': [m.audit_act_auth_sso_rekey_end, m.audit_sen_auth_sso_rekey_end],
  'backup.accept_host_key': [m.audit_act_backup_accept_host_key, m.audit_sen_backup_accept_host_key],
  'backup.accept_target_host_key': [m.audit_act_backup_accept_target_host_key, m.audit_sen_backup_accept_target_host_key],
  'backup.download': [m.audit_act_backup_download, m.audit_sen_backup_download],
  'backup.job_create': [m.audit_act_backup_job_create, m.audit_sen_backup_job_create],
  'backup.job_update': [m.audit_act_backup_job_update, m.audit_sen_backup_job_update],
  'backup.job_delete': [m.audit_act_backup_job_delete, m.audit_sen_backup_job_delete],
  'backup.job_repaired': [m.audit_act_backup_job_repaired, m.audit_sen_backup_job_repaired],
  'backup.key_export': [m.audit_act_backup_key_export, m.audit_sen_backup_key_export],
  'backup.restore': [m.audit_act_backup_restore, m.audit_sen_backup_restore],
  'backup.run': [m.audit_act_backup_run, m.audit_sen_backup_run],
  'backup.verify_run': [m.audit_act_backup_verify_run, m.audit_sen_backup_verify_run],
  'backup.verify_upload': [m.audit_act_backup_verify_upload, m.audit_sen_backup_verify_upload],
  'container.browse': [m.audit_act_container_browse, m.audit_sen_container_browse],
  'container.delete_file': [m.audit_act_container_delete_file, m.audit_sen_container_delete_file],
  'container.exec_end': [m.audit_act_container_exec_end, m.audit_sen_container_exec_end],
  'container.exec_start': [m.audit_act_container_exec_start, m.audit_sen_container_exec_start],
  'container.kill': [m.audit_act_container_kill, m.audit_sen_container_kill],
  'container.pause': [m.audit_act_container_pause, m.audit_sen_container_pause],
  'container.unpause': [m.audit_act_container_unpause, m.audit_sen_container_unpause],
  'container.read_file': [m.audit_act_container_read_file, m.audit_sen_container_read_file],
  'container.remove': [m.audit_act_container_remove, m.audit_sen_container_remove],
  'container.restart': [m.audit_act_container_restart, m.audit_sen_container_restart],
  'container.rollback': [m.audit_act_container_rollback, m.audit_sen_container_rollback],
  'container.start': [m.audit_act_container_start, m.audit_sen_container_start],
  'container.stop': [m.audit_act_container_stop, m.audit_sen_container_stop],
  'container.update': [m.audit_act_container_update, m.audit_sen_container_update],
  'container.write_file': [m.audit_act_container_write_file, m.audit_sen_container_write_file],
  'host.drain': [m.audit_act_host_drain, m.audit_sen_host_drain],
  'host.drain_abort': [m.audit_act_host_drain_abort, m.audit_sen_host_drain_abort],
  'host.tag_add': [m.audit_act_host_tag_add, m.audit_sen_host_tag_add],
  'host.tag_remove': [m.audit_act_host_tag_remove, m.audit_sen_host_tag_remove],
  'host.tags_set': [m.audit_act_host_tags_set, m.audit_sen_host_tags_set],
  'image.prune': [m.audit_act_image_prune, m.audit_sen_image_prune],
  'image.pull': [m.audit_act_image_pull, m.audit_sen_image_pull],
  'image.remove': [m.audit_act_image_remove, m.audit_sen_image_remove],
  'image.scan': [m.audit_act_image_scan, m.audit_sen_image_scan],
  'ldap.provider_create': [m.audit_act_ldap_provider_create, m.audit_sen_ldap_provider_create],
  'ldap.provider_update': [m.audit_act_ldap_provider_update, m.audit_sen_ldap_provider_update],
  'ldap.provider_delete': [m.audit_act_ldap_provider_delete, m.audit_sen_ldap_provider_delete],
  'network.create': [m.audit_act_network_create, m.audit_sen_network_create],
  'network.remove': [m.audit_act_network_remove, m.audit_sen_network_remove],
  'notify.channel_create': [m.audit_act_notify_channel_create, m.audit_sen_notify_channel_create],
  'notify.channel_update': [m.audit_act_notify_channel_update, m.audit_sen_notify_channel_update],
  'notify.channel_delete': [m.audit_act_notify_channel_delete, m.audit_sen_notify_channel_delete],
  'oidc.provider_create': [m.audit_act_oidc_provider_create, m.audit_sen_oidc_provider_create],
  'oidc.provider_update': [m.audit_act_oidc_provider_update, m.audit_sen_oidc_provider_update],
  'oidc.provider_delete': [m.audit_act_oidc_provider_delete, m.audit_sen_oidc_provider_delete],
  'oidc.reload': [m.audit_act_oidc_reload, m.audit_sen_oidc_reload],
  'operation.cancel': [m.audit_act_operation_cancel, m.audit_sen_operation_cancel],
  'proxy.enable': [m.audit_act_proxy_enable, m.audit_sen_proxy_enable],
  'proxy.disable': [m.audit_act_proxy_disable, m.audit_sen_proxy_disable],
  'proxy.import': [m.audit_act_proxy_import, m.audit_sen_proxy_import],
  'proxy.route_create': [m.audit_act_proxy_route_create, m.audit_sen_proxy_route_create],
  'proxy.route_update': [m.audit_act_proxy_route_update, m.audit_sen_proxy_route_update],
  'proxy.route_delete': [m.audit_act_proxy_route_delete, m.audit_sen_proxy_route_delete],
  'proxy.route_enabled': [m.audit_act_proxy_route_enabled, m.audit_sen_proxy_route_enabled],
  'proxy.route_disabled': [m.audit_act_proxy_route_disabled, m.audit_sen_proxy_route_disabled],
  'proxy.settings_update': [m.audit_act_proxy_settings_update, m.audit_sen_proxy_settings_update],
  'proxy.version_update': [m.audit_act_proxy_version_update, m.audit_sen_proxy_version_update],
  'proxy.version_rollback': [m.audit_act_proxy_version_rollback, m.audit_sen_proxy_version_rollback],
  'registry.create': [m.audit_act_registry_create, m.audit_sen_registry_create],
  'registry.update': [m.audit_act_registry_update, m.audit_sen_registry_update],
  'registry.delete': [m.audit_act_registry_delete, m.audit_sen_registry_delete],
  'registry.test': [m.audit_act_registry_test, m.audit_sen_registry_test],
  'role.create': [m.audit_act_role_create, m.audit_sen_role_create],
  'role.update': [m.audit_act_role_update, m.audit_sen_role_update],
  'role.delete': [m.audit_act_role_delete, m.audit_sen_role_delete],
  'saml.provider_create': [m.audit_act_saml_provider_create, m.audit_sen_saml_provider_create],
  'saml.provider_update': [m.audit_act_saml_provider_update, m.audit_sen_saml_provider_update],
  'saml.provider_delete': [m.audit_act_saml_provider_delete, m.audit_sen_saml_provider_delete],
  'secrets.rotate': [m.audit_act_secrets_rotate, m.audit_sen_secrets_rotate],
  'stack.accept_external_changes': [m.audit_act_stack_accept_external_changes, m.audit_sen_stack_accept_external_changes],
  'stack.adopt': [m.audit_act_stack_adopt, m.audit_sen_stack_adopt],
  'stack.create': [m.audit_act_stack_create, m.audit_sen_stack_create],
  'stack.delete': [m.audit_act_stack_delete, m.audit_sen_stack_delete],
  'stack.deploy': [m.audit_act_stack_deploy, m.audit_sen_stack_deploy],
  'stack.git_configure': [m.audit_act_stack_git_configure, m.audit_sen_stack_git_configure],
  'stack.git_disconnect': [m.audit_act_stack_git_disconnect, m.audit_sen_stack_git_disconnect],
  'stack.git_sync': [m.audit_act_stack_git_sync, m.audit_sen_stack_git_sync],
  'stack.git_webhook': [m.audit_act_stack_git_webhook, m.audit_sen_stack_git_webhook],
  'stack.migrate': [m.audit_act_stack_migrate, m.audit_sen_stack_migrate],
  'stack.stop': [m.audit_act_stack_stop, m.audit_sen_stack_stop],
  'stack.update': [m.audit_act_stack_update, m.audit_sen_stack_update],
  'system.restore_apply': [m.audit_act_system_restore_apply, m.audit_sen_system_restore_apply],
  'system.restore_cleanup': [m.audit_act_system_restore_cleanup, m.audit_sen_system_restore_cleanup],
  'system.restore_discard': [m.audit_act_system_restore_discard, m.audit_sen_system_restore_discard],
  'system.restore_stage': [m.audit_act_system_restore_stage, m.audit_sen_system_restore_stage],
  'system.update_check': [m.audit_act_system_update_check, m.audit_sen_system_update_check],
  'template.create': [m.audit_act_template_create, m.audit_sen_template_create],
  'template.update': [m.audit_act_template_update, m.audit_sen_template_update],
  'template.delete': [m.audit_act_template_delete, m.audit_sen_template_delete],
  'template.deploy': [m.audit_act_template_deploy, m.audit_sen_template_deploy],
  'user.avatar_update': [m.audit_act_user_avatar_update, m.audit_sen_user_avatar_update],
  'user.avatar_delete': [m.audit_act_user_avatar_delete, m.audit_sen_user_avatar_delete],
  'user.create': [m.audit_act_user_create, m.audit_sen_user_create],
  'user.update': [m.audit_act_user_update, m.audit_sen_user_update],
  'user.delete': [m.audit_act_user_delete, m.audit_sen_user_delete],
  'user.password': [m.audit_act_user_password, m.audit_sen_user_password],
  'user.unlock': [m.audit_act_user_unlock, m.audit_sen_user_unlock],
  'user.mfa_enroll': [m.audit_act_user_mfa_enroll, m.audit_sen_user_mfa_enroll],
  'user.mfa_disable': [m.audit_act_user_mfa_disable, m.audit_sen_user_mfa_disable],
  'user.mfa_disable_failed': [m.audit_act_user_mfa_disable_failed, m.audit_sen_user_mfa_disable_failed],
  'user.mfa_reset': [m.audit_act_user_mfa_reset, m.audit_sen_user_mfa_reset],
  'user.invite_created': [m.audit_act_user_invite_created, m.audit_sen_user_invite_created],
  'user.invite_revoked': [m.audit_act_user_invite_revoked, m.audit_sen_user_invite_revoked],
  'volume.browse': [m.audit_act_volume_browse, m.audit_sen_volume_browse],
  'volume.create': [m.audit_act_volume_create, m.audit_sen_volume_create],
  'volume.remove': [m.audit_act_volume_remove, m.audit_sen_volume_remove],
  'volume.prune': [m.audit_act_volume_prune, m.audit_sen_volume_prune],
  'volume.read_file': [m.audit_act_volume_read_file, m.audit_sen_volume_read_file]
};

/** Phrase for an action code; unknown codes fall back to the code with its
 * underscores spelled out ("legacy.some_thing" → "legacy some thing"). */
export function actionPhrase(action: string): string {
  return ACTIONS[action]?.[0]() ?? action.replace(/[._]/g, ' ');
}

export interface SentencePart {
  text: string;
  strong: boolean;
}

// Sentinels stand in for actor and target so the translated sentence can be
// split around them and the names rendered bold.
const MARK = '\u0001';

/** "admin created stack web" as parts, with actor and target marked strong.
 * An empty target is left out together with the gap it would leave. */
export function auditSentence(action: string, actor: string, target: string): SentencePart[] {
  const inputs = { actor: `${MARK}0${MARK}`, target: `${MARK}1${MARK}` };
  const sentence = ACTIONS[action]?.[1];
  const raw = sentence ? sentence(inputs) : m.audit_sentence_fallback({ ...inputs, action: actionPhrase(action) });
  const values = [actor, target];
  const parts: SentencePart[] = [];
  raw.split(MARK).forEach((seg, i) => {
    const text = i % 2 ? values[Number(seg)] : seg;
    if (!text) return;
    const last = parts[parts.length - 1];
    if (i % 2 === 0 && last && !last.strong) last.text += text;
    else parts.push({ text, strong: i % 2 === 1 });
  });
  for (const p of parts) if (!p.strong) p.text = p.text.replace(/\s{2,}/g, ' ');
  if (parts.length && !parts[parts.length - 1].strong) parts[parts.length - 1].text = parts[parts.length - 1].text.trimEnd();
  if (parts.length && !parts[0].strong) parts[0].text = parts[0].text.trimStart();
  return parts.filter((p) => p.text);
}

/** Names the UI knows for ids that audit targets record. */
export interface AuditNames {
  users?: Map<string, string>;
  hosts?: Map<string, string>;
}

const PREFIXED = /^(agent|backup|backup-job|backup-run|backup-target):(.+)$/;

/** Display name of a target: user and host ids become their names, and the
 * internal "agent:" / "backup:" prefixes are dropped. Unknown ids stay. */
export function targetLabel(action: string, target: string | undefined, names: AuditNames): string {
  if (!target) return '';
  const area = action.split('.')[0];
  const pre = PREFIXED.exec(target);
  const raw = pre ? pre[2] : target;
  if (area === 'user' || area === 'assignment' || action === 'auth.sso_login' || action === 'auth.sso_rekey' || action === 'auth.session_revoke_all') {
    return names.users?.get(raw) ?? raw;
  }
  if (area === 'host' || area === 'agent' || pre?.[1] === 'agent') {
    return names.hosts?.get(raw) ?? raw;
  }
  if (pre?.[1] === 'backup-run') return m.audit_target_run({ id: raw });
  return raw;
}

/** Host id → name for the hosts the caller can see. */
export function hostNames(list: { id: string; name: string; kind?: string }[]): Map<string, string> {
  return new Map(list.filter((h) => h.kind !== 'all').map((h) => [h.id, h.name]));
}

/** User id → username, or an empty map without users.view. */
export async function loadUserNames(canView: boolean, list: () => Promise<{ id: string; username: string }[]>): Promise<Map<string, string>> {
  if (!canView) return new Map();
  try {
    return new Map((await list()).map((u) => [u.id, u.username]));
  } catch {
    return new Map();
  }
}
