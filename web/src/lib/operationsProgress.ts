// Pure helpers for showing an operation's progress; shared by the
// operations store and its components.

import type { Operation } from '$lib/api';
import type { Perm } from '$lib/rbac.svelte';
import { m } from '$lib/paraglide/messages.js';

export const KIND_LABEL: Record<string, string> = {
  'image.update': m.lib_op_image_update(),
  'backup.run': m.lib_op_backup(),
  'backup.restore': m.lib_op_restore(),
  'proxy.site': m.lib_op_new_site()
};

export function kindLabel(kind: string): string {
  return KIND_LABEL[kind] ?? kind;
}

export function isActive(op: Pick<Operation, 'status'>): boolean {
  return op.status === 'queued' || op.status === 'running';
}

// The step currently running, or the one that failed or was cancelled.
export function currentStep(op: Operation) {
  return (
    op.steps.find((s) => s.status === 'running') ??
    op.steps.find((s) => s.status === 'failed') ??
    op.steps.find((s) => s.status === 'cancelled') ??
    null
  );
}

// Cancelling needs the permission of the action that started the work.
const CANCEL_PERM: Record<string, Perm> = {
  'image.update': 'containers.upgrade',
  'backup.run': 'backups.run',
  'backup.restore': 'backups.restore',
  'proxy.site': 'proxy.create'
};

export function cancelPerm(kind: string): Perm | undefined {
  return CANCEL_PERM[kind];
}

// The server marks when the work may be stopped now: queued, or in a step
// that only reads or waits (never while files or containers are swapped).
export function canCancel(op: Operation): boolean {
  return isActive(op) && !!op.cancellable && !op.cancel_requested;
}

// What cancelling does, for the confirmation.
export function cancelConsequence(op: Operation): string {
  if (op.status === 'queued') return m.lib_cancel_queued();
  switch (op.kind) {
    case 'backup.run':
      return m.lib_cancel_backup_run();
    case 'backup.restore':
      return m.lib_cancel_backup_restore();
    case 'image.update':
      return m.lib_cancel_image_update();
    case 'proxy.site':
      return m.lib_cancel_proxy_site();
  }
  return m.lib_cancel_default();
}

// Share of finished steps plus the running step's own progress; skipped
// steps count as done.
export function overallProgress(op: Operation): number | null {
  const steps = op.steps;
  if (!steps.length) return null;
  let done = 0;
  for (const s of steps) {
    if (s.status === 'succeeded' || s.status === 'skipped' || s.status === 'warning') done += 1;
    else if (s.status === 'running' && s.progress != null) done += s.progress;
  }
  return done / steps.length;
}

// What an operation belongs to, as the server names it in `ref`.
export const opRef = {
  backupRun: (runId: number | string) => `backup-run:${runId}`,
  container: (id: string) => `container:${id}`,
  proxySite: (host: string) => `proxy-site:${host}`
};

export function hasWarning(op: Operation): boolean {
  return op.steps.some((s) => s.status === 'warning');
}

export function failedStep(op: Operation) {
  return op.steps.find((s) => s.status === 'failed');
}

// "12 s" or "3 min 4 s" from start to end (or now); '' without a start.
export function elapsed(start?: string, end?: string): string {
  if (!start) return '';
  const stop = end ? Date.parse(end) : Date.now();
  const sec = Math.max(0, Math.round((stop - Date.parse(start)) / 1000));
  return sec < 60 ? `${sec} s` : `${Math.floor(sec / 60)} min ${sec % 60} s`;
}

// Toasts carry a short form of a step's message: the sentence before the
// first colon when that is where a raw upstream error starts, else the
// first 140 characters. The progress dialog shows the full text.
export function shortMessage(msg: string | null | undefined, max = 140): string {
  if (!msg) return '';
  const t = msg.trim();
  if (t.length <= max) return t;
  const colon = t.indexOf(': ');
  if (colon >= 20 && colon <= max) return t.slice(0, colon) + '.';
  return t.slice(0, max - 1).trimEnd() + '…';
}

const HINTS: [RegExp, string][] = [
  [/permission denied|operation not permitted|read-only file system|EACCES/i, m.lib_hint_permission()],
  [/no space left on device|disk quota exceeded|ENOSPC/i, m.lib_hint_disk_full()],
  [/connection refused|ECONNREFUSED/i, m.lib_hint_refused()],
  [/no such host|server misbehaving|name resolution|ENOTFOUND/i, m.lib_hint_dns()],
  [/i\/o timeout|deadline exceeded|timed out|ETIMEDOUT/i, m.lib_hint_timeout()],
  [/authentication failed|unable to authenticate|unauthorized/i, m.lib_hint_auth()],
  [/pull access denied|manifest unknown|repository does not exist/i, m.lib_hint_pull()],
  [/address already in use|port is already allocated/i, m.lib_hint_port()]
];

/** A plain-language next step for a common low-level error, if one fits. */
export function errorHint(msg: string | null | undefined): string {
  if (!msg) return '';
  for (const [re, hint] of HINTS) if (re.test(msg)) return hint;
  return '';
}
