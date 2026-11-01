// Pure helpers for showing an operation's progress; shared by the
// operations store and its components.

import type { Operation } from '$lib/api';
import type { Perm } from '$lib/rbac.svelte';

export const KIND_LABEL: Record<string, string> = {
  'image.update': 'Image update',
  'backup.run': 'Backup',
  'backup.restore': 'Restore',
  'proxy.site': 'New site'
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
  if (op.status === 'queued') return 'It is taken out of the queue and does not start.';
  switch (op.kind) {
    case 'backup.run':
      return 'The run stops and is recorded as failed. Post-backup hooks still run, and the unfinished archive is deleted from the target.';
    case 'backup.restore':
      return 'The restore stops before anything is written. The volumes and stack files keep their current contents.';
    case 'image.update':
      return 'The image pull stops. The container keeps running on its current image.';
    case 'proxy.site':
      return 'Dockmesh stops waiting for the certificate. The site stays, and Caddy keeps trying to get one.';
  }
  return 'The work stops.';
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
  const m = msg.trim();
  if (m.length <= max) return m;
  const colon = m.indexOf(': ');
  if (colon >= 20 && colon <= max) return m.slice(0, colon) + '.';
  return m.slice(0, max - 1).trimEnd() + '…';
}
