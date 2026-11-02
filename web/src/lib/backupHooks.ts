import type { BackupHook } from '$lib/api';
import { m } from '$lib/paraglide/messages.js';

// A hook as the job wizard edits it: one container and one shell command
// line. `orig` keeps the stored argv so a hook the user didn't touch is
// saved exactly as it was, whatever form it was written in.
export interface HookRow {
  container: string;
  command: string;
  orig?: string[];
}

export interface HookPreset {
  id: string;
  label: string;
  command: string;
}

// Dump presets write into the database's own data directory of the
// official images, which is the volume the backup picks up. They run
// through `sh -c`, so redirects and the image's password variables work;
// MYSQL_PWD keeps the password out of the dump tool's arguments.
export const HOOK_PRESETS: HookPreset[] = [
  { id: 'pg', label: m.lib_hook_pg(), command: 'pg_dumpall -U "${POSTGRES_USER:-postgres}" -f /var/lib/postgresql/data/dockmesh-dump.sql' },
  { id: 'mysql', label: m.lib_hook_mysql(), command: 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysqldump -u root --all-databases > /var/lib/mysql/dockmesh-dump.sql' },
  { id: 'maria', label: m.lib_hook_maria(), command: 'MYSQL_PWD="$MARIADB_ROOT_PASSWORD" mariadb-dump -u root --all-databases > /var/lib/mysql/dockmesh-dump.sql' },
  { id: 'redis', label: m.lib_hook_redis(), command: 'redis-cli SAVE' },
  { id: 'mongo', label: m.lib_hook_mongo(), command: 'mongodump --archive=/data/db/dockmesh-dump.archive' }
];

export function hookToRow(h: BackupHook): HookRow {
  const cmd = h.cmd ?? [];
  if (cmd.length === 3 && (cmd[0] === 'sh' || cmd[0] === '/bin/sh') && cmd[1] === '-c') {
    return { container: h.container, command: cmd[2], orig: cmd };
  }
  return { container: h.container, command: cmd.join(' '), orig: cmd };
}

// rowToHook returns null for a row without a container or command.
export function rowToHook(r: HookRow): BackupHook | null {
  const container = r.container.trim();
  const command = r.command.trim();
  if (!container || !command) return null;
  if (r.orig && command === hookToRow({ container, cmd: r.orig }).command) {
    return { container, cmd: r.orig };
  }
  return { container, cmd: ['sh', '-c', command] };
}

export function rowsToHooks(rows: HookRow[]): BackupHook[] {
  return rows.map(rowToHook).filter((h): h is BackupHook => h !== null);
}

// inlineTargetConfig converts form values to the types the server expects.
export function inlineTargetConfig(type: string, cfg: Record<string, any>): Record<string, any> {
  const out = { ...cfg };
  if (type === 'sftp' && 'port' in out) {
    const port = parseInt(String(out.port), 10);
    if (Number.isFinite(port) && port > 0) out.port = port;
    else delete out.port;
  }
  if (type === 's3') out.use_ssl = !!out.use_ssl;
  return out;
}
