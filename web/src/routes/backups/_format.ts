import type { BackupJob, BackupRun } from '$lib/api';
import { hosts } from '$lib/stores/host.svelte';
import { allowed, isAdmin, type Perm } from '$lib/rbac.svelte';
import { fmtAgo as baseAgo, fmtBytes as baseBytes } from '$lib/format';
import type { Tone } from '$lib/components/ui/StatusChip.svelte';
import { m } from '$lib/paraglide/messages.js';

export const fmtAgo = (ts?: string) => baseAgo(ts);
export const fmtBytes = (n: number) => baseBytes(n, '—');

export function fmtUntil(ts?: string): string {
  if (!ts) return '—';
  const t = Date.parse(ts);
  if (!t || isNaN(t)) return '—';
  const secs = Math.floor((t - Date.now()) / 1000);
  if (secs < 0) return m.backups_until_overdue();
  if (secs < 60) return m.backups_until_lt_min();
  if (secs < 3600) return m.backups_until_min({ n: Math.floor(secs / 60) });
  if (secs < 86400) {
    const h = Math.floor(secs / 3600);
    const min = Math.floor((secs % 3600) / 60);
    return min > 0 ? m.backups_until_h_min({ h, min }) : m.backups_until_h({ h });
  }
  const d = Math.floor(secs / 86400);
  const h = Math.floor((secs % 86400) / 3600);
  return h > 0 ? m.backups_until_d_h({ d, h }) : m.backups_until_d({ d });
}

export function fmtDuration(start: string, end?: string): string {
  if (!end) return m.backups_duration_running();
  const secs = Math.floor((new Date(end).getTime() - new Date(start).getTime()) / 1000);
  if (secs < 60) return m.backups_duration_s({ s: secs });
  if (secs < 3600) return m.backups_duration_min_s({ min: Math.floor(secs / 60), s: secs % 60 });
  return m.backups_duration_h_min({ h: Math.floor(secs / 3600), min: Math.floor((secs % 3600) / 60) });
}

const CRON_LABELS: Record<string, () => string> = {
  '0 3 * * *': m.backups_cron_daily_0300,
  '0 0 * * *': m.backups_cron_daily_midnight,
  '0 */6 * * *': m.backups_cron_every_6h,
  '0 */12 * * *': m.backups_cron_every_12h,
  '0 2 * * 0': m.backups_cron_weekly_sun_0200,
  '0 3 1 * *': m.backups_cron_monthly_1st_0300
};

export function cronHuman(cron: string): string {
  return CRON_LABELS[cron]?.() ?? cron;
}

export function targetTypeLabel(type: string): string {
  if (type === 's3') return 'S3';
  if (type === 'sftp') return 'SFTP';
  if (type === 'smb') return 'SMB';
  if (type === 'webdav') return 'WebDAV';
  return m.common_local();
}

export function jobSourceName(j: Pick<BackupJob, 'sources'>): string {
  const s = j.sources[0];
  if (!s) return m.backups_no_source();
  return s.type === 'system' ? m.backups_dockmesh_server() : s.name;
}

export function jobSourceKind(j: Pick<BackupJob, 'sources'>): string {
  const s = j.sources[0];
  if (!s) return '';
  if (s.type === 'system') return m.backups_system_source_kind();
  return s.type;
}

export function runTone(status: string): Tone {
  if (status === 'success') return 'ok';
  if (status === 'success_with_warnings') return 'warn';
  if (status === 'failed') return 'bad';
  if (status === 'running') return 'acc';
  return 'off';
}

export function runLabel(status: string): string {
  if (status === 'success') return m.backups_run_succeeded();
  if (status === 'success_with_warnings') return m.backups_run_warnings();
  if (status === 'failed') return m.backups_run_failed();
  if (status === 'running') return m.backups_run_running();
  return status;
}

export function isSystemRun(r: Pick<BackupRun, 'sources'>): boolean {
  return r.sources?.some((s) => s.type === 'system') ?? false;
}

// The server allows changes to system jobs and access to their runs only
// to admins; perm is the permission the action needs otherwise.
export function mayOnJob(perm: Perm, j: Pick<BackupRun, 'sources'>): boolean {
  return allowed(perm) && (!isSystemRun(j) || isAdmin());
}

// Name of the host a job runs on; jobs without a host run on the server.
export function hostLabel(id?: string): string {
  const hid = id || 'local';
  return hosts.available.find((h) => h.id === hid)?.name ?? hid;
}
