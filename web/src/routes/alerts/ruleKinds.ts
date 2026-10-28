// Rule kinds the alert engine evaluates. Event kinds reuse threshold,
// duration_seconds and container_filter for their own parameters.
import type { AlertRule, AlertRuleInput } from '$lib/api';

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

export const KINDS: { id: RuleKind; label: string; target: string; targetHint: string }[] = [
  { id: 'metric', label: 'Container CPU / memory', target: 'Container filter', targetHint: '* matches all, or exact container name.' },
  { id: 'restart_loop', label: 'Container restart loop', target: 'Container filter', targetHint: '* matches all, or exact container name.' },
  { id: 'container_stopped', label: 'Container stopped unexpectedly', target: 'Container filter', targetHint: '* matches all, or exact container name.' },
  { id: 'unhealthy', label: 'Container unhealthy', target: 'Container filter', targetHint: '* matches all, or exact container name.' },
  { id: 'oom', label: 'Container killed by OOM', target: 'Container filter', targetHint: '* matches all, or exact container name.' },
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
  return k === 'metric' || k === 'restart_loop' || k === 'container_stopped' || k === 'unhealthy' || k === 'oom';
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
      return 'a backup job misses its schedule';
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
