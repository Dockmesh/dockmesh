// Audit entries carry neutral action codes ("stack.create"); the UI turns
// them into short phrases.

const PHRASES: Record<string, string> = {
  'stack.create': 'created stack',
  'stack.update': 'updated stack',
  'stack.delete': 'deleted stack',
  'stack.deploy': 'deployed stack',
  'stack.stop': 'stopped stack',
  'stack.adopt': 'adopted stack',
  'stack.migrate': 'migrated stack',
  'stack.git_configure': 'connected git for stack',
  'stack.git_disconnect': 'disconnected git from stack',
  'stack.git_sync': 'synced stack from git',
  'stack.git_webhook': 'received a git webhook for stack',
  'stack.accept_external_changes': 'accepted outside changes of stack',
  'backup.job_create': 'created backup job',
  'backup.job_update': 'updated backup job',
  'backup.job_delete': 'deleted backup job',
  'backup.run': 'ran backup job',
  'backup.restore': 'restored backup run',
  'backup.download': 'downloaded backup run',
  'backup.key_export': 'exported the backup key',
  'user.create': 'created user',
  'user.update': 'updated user',
  'user.delete': 'deleted user',
  'user.password': 'changed the password of',
  'user.unlock': 'unlocked user',
  'user.mfa_enroll': 'turned on two-factor for',
  'user.mfa_disable': 'turned off two-factor for',
  'user.mfa_reset': 'reset two-factor for',
  'user.invite_created': 'invited',
  'user.invite_revoked': 'revoked invite',
  'role.create': 'created role',
  'role.update': 'updated role',
  'role.delete': 'deleted role',
  'assignment.add': 'granted access to',
  'assignment.remove': 'removed access from',
  'api_token.create': 'created API token',
  'api_token.revoke': 'revoked API token',
  'agent.create': 'added host',
  'agent.delete': 'removed host',
  'agent.upgrade': 'upgraded the agent on',
  'agent.rotate_token': 'issued a new token for',
  'host.tags_set': 'set tags on',
  'host.tag_add': 'tagged',
  'host.tag_remove': 'untagged',
  'host.drain': 'drained',
  'host.drain_abort': 'stopped draining',
  'container.start': 'started container',
  'container.stop': 'stopped container',
  'container.restart': 'restarted container',
  'container.kill': 'killed container',
  'container.remove': 'removed container',
  'container.update': 'updated container',
  'container.rollback': 'rolled back container',
  'container.browse': 'browsed files of container',
  'container.read_file': 'read a file in container',
  'container.write_file': 'wrote a file in container',
  'container.delete_file': 'deleted a file in container',
  'container.exec_start': 'opened a terminal in',
  'container.exec_end': 'closed a terminal in',
  'image.scan': 'scanned image',
  'image.pull': 'pulled image',
  'image.remove': 'removed image',
  'image.prune': 'pruned images',
  'volume.create': 'created volume',
  'volume.remove': 'removed volume',
  'volume.prune': 'pruned volumes',
  'volume.browse': 'browsed files of volume',
  'volume.read_file': 'read a file in volume',
  'network.create': 'created network',
  'network.remove': 'removed network',
  'proxy.enable': 'turned on the proxy',
  'proxy.disable': 'turned off the proxy',
  'proxy.route_create': 'added proxy site',
  'proxy.route_update': 'updated proxy site',
  'proxy.route_delete': 'deleted proxy site',
  'registry.create': 'added registry',
  'registry.update': 'updated registry',
  'registry.delete': 'removed registry',
  'template.create': 'created template',
  'template.update': 'updated template',
  'template.delete': 'deleted template',
  'template.deploy': 'deployed a template as',
  'alerts.rule_create': 'created alert rule',
  'alerts.rule_update': 'updated alert rule',
  'alerts.rule_delete': 'deleted alert rule',
  'auth.login': 'signed in',
  'auth.logout': 'signed out',
  'auth.login_failed': 'failed to sign in as',
  'auth.lockout': 'locked out',
  'auth.refresh': 'renewed a session',
  'auth.scope_denied': 'was refused access to',
  'auth.sso_login': 'signed in through SSO as',
  'auth.sso_rekey': 're-keyed the sign-in of',
  'auth.saml_rekey': 're-keyed the SAML sign-in of',
  'auth.session_revoke': 'ended a session',
  'auth.session_revoke_all': 'ended all sessions of'
};

/** Phrase for an action code; unknown codes fall back to the code with its
 * underscores spelled out ("audit.retention_run" → "audit retention run"). */
export function actionPhrase(action: string): string {
  return PHRASES[action] ?? action.replace(/[._]/g, ' ');
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
  const m = PREFIXED.exec(target);
  const raw = m ? m[2] : target;
  if (area === 'user' || area === 'assignment' || action === 'auth.sso_login' || action === 'auth.sso_rekey' || action === 'auth.session_revoke_all') {
    return names.users?.get(raw) ?? raw;
  }
  if (area === 'host' || area === 'agent' || m?.[1] === 'agent') {
    return names.hosts?.get(raw) ?? raw;
  }
  if (m?.[1] === 'backup-run') return `run ${raw}`;
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
