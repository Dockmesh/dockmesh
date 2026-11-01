import type { Notice, NotificationItem, Operation } from '$lib/api';
import type { Tone } from '$lib/components/ui/StatusChip.svelte';
import { kindLabel } from '$lib/operationsProgress';

export type FeedCategory = 'alerts' | 'operations' | 'deploys' | 'backups' | 'updates' | 'hosts' | 'migrations' | 'notices' | 'other';

export const CATEGORY_LABEL: Record<FeedCategory, string> = {
  alerts: 'Alerts',
  operations: 'Operations',
  deploys: 'Deploys',
  backups: 'Backups',
  updates: 'Updates',
  hosts: 'Hosts',
  migrations: 'Migrations',
  notices: 'Notices',
  other: 'Other'
};

/** Groups a notification kind ("alert.fire", "backup_failed") for the filter. */
export function categoryOf(kind: string): FeedCategory {
  const k = kind.toLowerCase();
  if (k.startsWith('alert') || k.startsWith('cert')) return 'alerts';
  if (k.startsWith('deploy')) return 'deploys';
  if (k.startsWith('backup')) return 'backups';
  if (k.startsWith('image') || k.startsWith('cve') || k.startsWith('system.upgrade')) return 'updates';
  if (k.startsWith('agent') || k.startsWith('host')) return 'hosts';
  if (k.startsWith('migration')) return 'migrations';
  if (k === 'notice') return 'notices';
  return 'other';
}

export interface FeedEntry {
  key: string;
  category: FeedCategory;
  tone: Tone;
  word: string;
  title: string;
  body?: string;
  link?: string;
  ts: string;
  unread: boolean;
  notification?: NotificationItem;
  operation?: Operation;
}

const NOTIF_TONE: Record<string, [Tone, string]> = {
  success: ['ok', 'Succeeded'],
  warning: ['warn', 'Warning'],
  error: ['bad', 'Failed'],
  critical: ['bad', 'Critical'],
  info: ['off', 'Info']
};

const OP_TONE: Record<string, [Tone, string]> = {
  succeeded: ['ok', 'Succeeded'],
  failed: ['bad', 'Failed'],
  cancelled: ['off', 'Cancelled']
};

/** One history list from notifications, finished operations and open
 * notices, newest first. Only notifications carry a read state. */
export function buildFeed(items: NotificationItem[], ops: Operation[], open: Notice[]): FeedEntry[] {
  const out: FeedEntry[] = [];
  for (const n of items) {
    const [tone, word] = NOTIF_TONE[n.severity] ?? NOTIF_TONE.info;
    out.push({
      key: `n:${n.id}`,
      category: categoryOf(n.kind),
      tone,
      word,
      title: n.title,
      body: n.body,
      link: n.link,
      ts: n.created_at,
      unread: !n.read_at,
      notification: n
    });
  }
  for (const o of ops) {
    const t = OP_TONE[o.status];
    if (!t || !o.finished_at) continue;
    out.push({
      key: `o:${o.id}`,
      category: 'operations',
      tone: t[0],
      word: t[1],
      title: `${kindLabel(o.kind)} · ${o.target}`,
      body: o.error,
      ts: o.finished_at,
      unread: false,
      operation: o
    });
  }
  for (const n of open) {
    out.push({
      key: `x:${n.id}`,
      category: 'notices',
      tone: n.severity === 'critical' ? 'bad' : n.severity === 'warning' ? 'warn' : 'off',
      word: n.severity === 'critical' ? 'Critical' : n.severity === 'warning' ? 'Action required' : 'Info',
      title: n.title,
      link: n.link,
      ts: n.created_at,
      unread: false
    });
  }
  return out.sort((a, b) => Date.parse(b.ts) - Date.parse(a.ts));
}

/** Badge text for the bell: nothing at zero, capped at "9+". */
export function badgeText(unread: number): string {
  if (unread <= 0) return '';
  return unread > 9 ? '9+' : String(unread);
}
