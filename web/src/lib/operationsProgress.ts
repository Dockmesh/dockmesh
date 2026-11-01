// Pure helpers for showing an operation's progress; shared by the
// operations store and its components.

import type { Operation } from '$lib/api';

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

// The step currently running, or the one that failed.
export function currentStep(op: Operation) {
  return (
    op.steps.find((s) => s.status === 'running') ??
    op.steps.find((s) => s.status === 'failed') ??
    null
  );
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
