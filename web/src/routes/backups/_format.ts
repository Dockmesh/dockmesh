import type { BackupJob, BackupRun } from '$lib/api';
import type { Tone } from '$lib/components/ui/StatusChip.svelte';

export function fmtBytes(n: number): string {
  if (!n) return '—';
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`;
}

export function fmtAgo(ts?: string): string {
  if (!ts) return '—';
  const d = (Date.now() - new Date(ts).getTime()) / 1000;
  if (d < 60) return 'just now';
  if (d < 3600) return `${Math.floor(d / 60)} min ago`;
  if (d < 86400) return `${Math.floor(d / 3600)} h ago`;
  if (d < 7 * 86400) return `${Math.floor(d / 86400)} d ago`;
  return new Date(ts).toLocaleDateString();
}

export function fmtUntil(ts?: string): string {
  if (!ts) return '—';
  const t = Date.parse(ts);
  if (!t || isNaN(t)) return '—';
  const secs = Math.floor((t - Date.now()) / 1000);
  if (secs < 0) return 'overdue';
  if (secs < 60) return 'in <1 min';
  if (secs < 3600) return `in ${Math.floor(secs / 60)} min`;
  if (secs < 86400) {
    const h = Math.floor(secs / 3600);
    const m = Math.floor((secs % 3600) / 60);
    return m > 0 ? `in ${h} h ${m} min` : `in ${h} h`;
  }
  const d = Math.floor(secs / 86400);
  const h = Math.floor((secs % 86400) / 3600);
  return h > 0 ? `in ${d} d ${h} h` : `in ${d} d`;
}

export function fmtDuration(start: string, end?: string): string {
  if (!end) return 'running';
  const secs = Math.floor((new Date(end).getTime() - new Date(start).getTime()) / 1000);
  if (secs < 60) return `${secs} s`;
  if (secs < 3600) return `${Math.floor(secs / 60)} min ${secs % 60} s`;
  return `${Math.floor(secs / 3600)} h ${Math.floor((secs % 3600) / 60)} min`;
}

const CRON_LABELS: Record<string, string> = {
  '0 3 * * *': 'Daily at 03:00',
  '0 0 * * *': 'Daily at midnight',
  '0 */6 * * *': 'Every 6 hours',
  '0 */12 * * *': 'Every 12 hours',
  '0 2 * * 0': 'Weekly, Sunday 02:00',
  '0 3 1 * *': 'Monthly, 1st at 03:00'
};

export function cronHuman(cron: string): string {
  return CRON_LABELS[cron] ?? cron;
}

export function targetTypeLabel(type: string): string {
  if (type === 's3') return 'S3';
  if (type === 'sftp') return 'SFTP';
  if (type === 'smb') return 'SMB';
  if (type === 'webdav') return 'WebDAV';
  return 'Local';
}

export function jobSourceName(j: Pick<BackupJob, 'sources'>): string {
  const s = j.sources[0];
  if (!s) return 'no source';
  return s.type === 'system' ? 'Dockmesh server' : s.name;
}

export function jobSourceKind(j: Pick<BackupJob, 'sources'>): string {
  const s = j.sources[0];
  if (!s) return '';
  if (s.type === 'system') return 'database, stacks, settings';
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
  if (status === 'success') return 'succeeded';
  if (status === 'success_with_warnings') return 'warnings';
  return status;
}

export function isSystemRun(r: Pick<BackupRun, 'sources'>): boolean {
  return r.sources?.some((s) => s.type === 'system') ?? false;
}
