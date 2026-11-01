// Rule kinds the alert engine evaluates. Event kinds reuse threshold,
// duration_seconds and container_filter for their own parameters.
import type { AlertRule, AlertRuleInput } from '$lib/api';
import type { Tone } from '$lib/components/ui/StatusChip.svelte';

export type RuleKind =
  | 'metric'
  | 'restart_loop'
  | 'container_stopped'
  | 'unhealthy'
  | 'oom'
  | 'host_offline'
  | 'disk_usage'
  | 'backup_failed'
  | 'backup_missed'
  | 'cert_expiry';

const CONTAINER_HINT = '* matches all, or exact container name.';

export const KINDS: { id: RuleKind; label: string; target: string; targetHint: string }[] = [
  { id: 'metric', label: 'Container CPU / memory', target: 'Container filter', targetHint: CONTAINER_HINT },
  { id: 'restart_loop', label: 'Container restart loop', target: 'Container filter', targetHint: CONTAINER_HINT },
  { id: 'container_stopped', label: 'Container stopped unexpectedly', target: 'Container filter', targetHint: CONTAINER_HINT },
  { id: 'unhealthy', label: 'Container unhealthy', target: 'Container filter', targetHint: CONTAINER_HINT },
  { id: 'oom', label: 'Container killed by OOM', target: 'Container filter', targetHint: CONTAINER_HINT },
  { id: 'host_offline', label: 'Host offline', target: 'Agent filter', targetHint: '* matches every agent, or an exact agent name.' },
  { id: 'disk_usage', label: 'Disk usage', target: 'Mount filter', targetHint: '* matches mounts holding Docker, containerd, stacks or Dockmesh data; or an exact mount point.' },
  { id: 'backup_failed', label: 'Backup run failed', target: 'Backup job filter', targetHint: '* matches all jobs, or an exact job name.' },
  { id: 'backup_missed', label: 'Backup overdue', target: 'Backup job filter', targetHint: '* matches all jobs, or an exact job name.' },
  { id: 'cert_expiry', label: 'TLS certificate expiring', target: 'Domain filter', targetHint: '* matches every proxy route, or an exact domain.' },
];

export function kindOf(r: { kind?: string }): RuleKind {
  return (r.kind || 'metric') as RuleKind;
}

export function isContainerKind(k: RuleKind): boolean {
  return k === 'restart_loop' || k === 'container_stopped' || k === 'unhealthy' || k === 'oom';
}

// Mirrors alerts.HostBound: backup and certificate rules are global.
export function isHostBoundKind(k: RuleKind): boolean {
  return k !== 'metric' && k !== 'backup_failed' && k !== 'backup_missed' && k !== 'cert_expiry';
}

export function fmtDuration(secs: number): string {
  if (secs < 60) return `${secs}s`;
  if (secs < 3600) return `${Math.round(secs / 60)}m`;
  return `${Math.round(secs / 3600)}h`;
}

// describeRule renders an event rule's condition as one sentence.
export function describeRule(r: AlertRule): string {
  switch (kindOf(r)) {
    case 'restart_loop':
      return `a container restarts ${r.threshold} times within ${fmtDuration(r.duration_seconds)}`;
    case 'container_stopped':
      return 'a container exits with an error without being stopped';
    case 'unhealthy':
      return 'a container health check reports unhealthy';
    case 'oom':
      return 'a container is killed for running out of memory';
    case 'host_offline':
      return `an agent is offline for ${fmtDuration(r.duration_seconds)}`;
    case 'disk_usage':
      return `a disk is at least ${r.threshold}% full`;
    case 'backup_failed':
      return 'a backup run fails';
    case 'backup_missed':
      return 'a backup job has no successful run within its schedule';
    case 'cert_expiry':
      return `a proxy certificate expires within ${r.threshold} days`;
    default:
      return '';
  }
}

export function ruleToInput(r: AlertRule): AlertRuleInput {
  return {
    name: r.name,
    kind: kindOf(r),
    container_filter: r.container_filter,
    scope_host: r.scope_host ?? '',
    scope_tag: r.scope_tag ?? '',
    scope_stack: r.scope_stack ?? '',
    metric: r.metric,
    operator: r.operator,
    threshold: r.threshold,
    duration_seconds: r.duration_seconds,
    channel_ids: r.channel_ids ?? [],
    enabled: r.enabled,
    severity: r.severity || 'warning',
    cooldown_seconds: r.cooldown_seconds || 300,
    muted_until: r.muted_until ?? '',
    mute_schedule: r.mute_schedule ?? '',
  };
}

type RuleShape = Pick<AlertRuleInput, 'kind' | 'container_filter' | 'scope_host' | 'scope_tag' | 'scope_stack' | 'metric' | 'operator' | 'threshold' | 'duration_seconds'>;

// Groups for the "Watch" picker, in the order the dialog lists them.
export const KIND_GROUPS: { group: string; kinds: RuleKind[] }[] = [
  { group: 'Containers', kinds: ['metric', 'restart_loop', 'container_stopped', 'unhealthy', 'oom'] },
  { group: 'Hosts', kinds: ['host_offline', 'disk_usage'] },
  { group: 'Backups', kinds: ['backup_failed', 'backup_missed'] },
  { group: 'Reverse proxy', kinds: ['cert_expiry'] }
];

function filtered(f: string | undefined, any: string, one: (v: string) => string): string {
  return !f || f === '*' ? any : one(f);
}

// Where a rule looks, as a phrase: "a container of stack web on nas-01".
export function subjectOf(r: RuleShape, hostName: (id: string) => string = (id) => id): string {
  const k = kindOf(r);
  const where = r.scope_host ? ` on ${hostName(r.scope_host)}` : r.scope_tag ? ` on hosts tagged ${r.scope_tag}` : '';
  switch (k) {
    case 'metric':
    case 'restart_loop':
    case 'container_stopped':
    case 'unhealthy':
    case 'oom': {
      const base = filtered(r.container_filter, 'a container', (v) => `container ${v}`);
      const stack = r.scope_stack ? ` of stack ${r.scope_stack}` : '';
      return base + stack + where;
    }
    case 'host_offline':
      return r.scope_host ? hostName(r.scope_host) : filtered(r.container_filter, 'an agent', (v) => `agent ${v}`) + where;
    case 'disk_usage':
      return filtered(r.container_filter, 'a data disk', (v) => `the disk at ${v}`) + where;
    case 'backup_failed':
    case 'backup_missed':
      return filtered(r.container_filter, 'any backup job', (v) => `backup job ${v}`);
    case 'cert_expiry':
      return filtered(r.container_filter, 'any proxy site', (v) => `the site ${v}`);
  }
}

// The whole condition in words, used in the list and as the dialog's
// preview: "CPU of a container of stack web is above 80 % for 1m".
export function ruleSentence(r: RuleShape, hostName?: (id: string) => string): string {
  const s = subjectOf(r, hostName);
  const d = fmtDuration(r.duration_seconds || 0);
  switch (kindOf(r)) {
    case 'metric': {
      const m = r.metric === 'mem_percent' ? 'memory' : 'CPU';
      const op = r.operator === 'lt' ? 'below' : 'above';
      return `${m} of ${s} is ${op} ${r.threshold} %${r.duration_seconds > 0 ? ` for ${d}` : ''}`;
    }
    case 'restart_loop':
      return `${s} restarts ${r.threshold} times within ${d}`;
    case 'container_stopped':
      return `${s} exits with an error without being stopped`;
    case 'unhealthy':
      return `the health check of ${s} reports unhealthy`;
    case 'oom':
      return `${s} is killed for running out of memory`;
    case 'host_offline':
      return `${s} is offline for ${d}`;
    case 'disk_usage':
      return `${s} is at least ${r.threshold} % full`;
    case 'backup_failed':
      return `a run of ${s} fails`;
    case 'backup_missed':
      return `${s} has no successful run within its schedule`;
    case 'cert_expiry':
      return `the certificate of ${s} expires within ${r.threshold} days`;
  }
}

// Short "applies to" summary for the rules list.
export function appliesTo(r: RuleShape, hostName: (id: string) => string = (id) => id): string {
  const k = kindOf(r);
  const parts: string[] = [];
  const f = r.container_filter && r.container_filter !== '*' ? r.container_filter : '';
  if (k === 'backup_failed' || k === 'backup_missed') return f ? `job ${f}` : 'all backup jobs';
  if (k === 'cert_expiry') return f || 'all proxy sites';
  if (k === 'host_offline') parts.push(f ? `agent ${f}` : 'all agents');
  else if (k === 'disk_usage') parts.push(f ? `mount ${f}` : 'data disks');
  else {
    if (r.scope_stack) parts.push(`stack ${r.scope_stack}`);
    parts.push(f ? `container ${f}` : r.scope_stack ? '' : 'all containers');
  }
  if (r.scope_host) parts.push(`on ${hostName(r.scope_host)}`);
  else if (r.scope_tag) parts.push(`hosts tagged ${r.scope_tag}`);
  return parts.filter(Boolean).join(' · ');
}

export function kindLabel(k: RuleKind): string {
  return KINDS.find((x) => x.id === k)?.label ?? k;
}

export function sevTone(s: string | undefined): Tone {
  return s === 'critical' ? 'bad' : s === 'warning' ? 'warn' : 'off';
}
