// Rule kinds the alert engine evaluates. Event kinds reuse threshold,
// duration_seconds and container_filter for their own parameters.
import type { AlertRule, AlertRuleInput } from '$lib/api';
import type { Tone } from '$lib/components/ui/StatusChip.svelte';
import { m } from '$lib/paraglide/messages.js';

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

const CONTAINER_HINT = m.alerts_hint_container();
const CONTAINER_FILTER = m.alerts_filter_container();

export const KINDS: { id: RuleKind; label: string; target: string; targetHint: string }[] = [
  { id: 'metric', label: m.alerts_kind_metric(), target: CONTAINER_FILTER, targetHint: CONTAINER_HINT },
  { id: 'restart_loop', label: m.alerts_kind_restart_loop(), target: CONTAINER_FILTER, targetHint: CONTAINER_HINT },
  { id: 'container_stopped', label: m.alerts_kind_container_stopped(), target: CONTAINER_FILTER, targetHint: CONTAINER_HINT },
  { id: 'unhealthy', label: m.alerts_kind_unhealthy(), target: CONTAINER_FILTER, targetHint: CONTAINER_HINT },
  { id: 'oom', label: m.alerts_kind_oom(), target: CONTAINER_FILTER, targetHint: CONTAINER_HINT },
  { id: 'host_offline', label: m.alerts_kind_host_offline(), target: m.alerts_filter_agent(), targetHint: m.alerts_hint_agent() },
  { id: 'disk_usage', label: m.alerts_kind_disk_usage(), target: m.alerts_filter_mount(), targetHint: m.alerts_hint_mount() },
  { id: 'backup_failed', label: m.alerts_kind_backup_failed(), target: m.alerts_filter_backup_job(), targetHint: m.alerts_hint_backup_job() },
  { id: 'backup_missed', label: m.alerts_kind_backup_missed(), target: m.alerts_filter_backup_job(), targetHint: m.alerts_hint_backup_job() },
  { id: 'cert_expiry', label: m.alerts_kind_cert_expiry(), target: m.alerts_filter_domain(), targetHint: m.alerts_hint_domain() },
];

export function kindOf(r: { kind?: string }): RuleKind {
  return (r.kind || 'metric') as RuleKind;
}

export function isContainerKind(k: RuleKind): boolean {
  return k === 'restart_loop' || k === 'container_stopped' || k === 'unhealthy' || k === 'oom';
}

// Mirrors alerts.HostBound: backup and certificate rules are global.
export function isHostBoundKind(k: RuleKind): boolean {
  return k !== 'backup_failed' && k !== 'backup_missed' && k !== 'cert_expiry';
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
      return m.alerts_desc_restart_loop({ n: r.threshold, d: fmtDuration(r.duration_seconds) });
    case 'container_stopped':
      return m.alerts_desc_container_stopped();
    case 'unhealthy':
      return m.alerts_desc_unhealthy();
    case 'oom':
      return m.alerts_desc_oom();
    case 'host_offline':
      return m.alerts_desc_host_offline({ d: fmtDuration(r.duration_seconds) });
    case 'disk_usage':
      return m.alerts_desc_disk_usage({ n: r.threshold });
    case 'backup_failed':
      return m.alerts_desc_backup_failed();
    case 'backup_missed':
      return m.alerts_desc_backup_missed();
    case 'cert_expiry':
      return m.alerts_desc_cert_expiry({ n: r.threshold });
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
    cpu_unit: r.cpu_unit ?? '',
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

type RuleShape = Pick<AlertRuleInput, 'kind' | 'container_filter' | 'scope_host' | 'scope_tag' | 'scope_stack' | 'metric' | 'cpu_unit' | 'operator' | 'threshold' | 'duration_seconds'>;

// Groups for the "Watch" picker, in the order the dialog lists them.
export const KIND_GROUPS: { group: string; kinds: RuleKind[] }[] = [
  { group: m.common_containers(), kinds: ['metric', 'restart_loop', 'container_stopped', 'unhealthy', 'oom'] },
  { group: m.common_hosts(), kinds: ['host_offline', 'disk_usage'] },
  { group: m.alerts_group_backups(), kinds: ['backup_failed', 'backup_missed'] },
  { group: m.alerts_group_proxy(), kinds: ['cert_expiry'] }
];

function filtered(f: string | undefined, any: string, one: (v: string) => string): string {
  return !f || f === '*' ? any : one(f);
}

// Where a rule looks, as a phrase: "a container of stack web on nas-01".
// dat picks the form German needs after "von"; English has only one.
export function subjectOf(r: RuleShape, hostName: (id: string) => string = (id) => id, dat = false): string {
  const k = kindOf(r);
  const where = r.scope_host
    ? ` ${m.alerts_subj_on_host({ host: hostName(r.scope_host) })}`
    : r.scope_tag ? ` ${m.alerts_subj_on_tag({ tag: r.scope_tag })}` : '';
  switch (k) {
    case 'metric':
    case 'restart_loop':
    case 'container_stopped':
    case 'unhealthy':
    case 'oom': {
      const base = filtered(r.container_filter, dat ? m.alerts_subj_any_container_dat() : m.alerts_subj_any_container(), (v) => m.alerts_subj_container({ name: v }));
      const stack = r.scope_stack ? ` ${m.alerts_subj_of_stack({ stack: r.scope_stack })}` : '';
      return base + stack + where;
    }
    case 'host_offline':
      return r.scope_host ? hostName(r.scope_host) : filtered(r.container_filter, m.alerts_subj_any_agent(), (v) => m.alerts_subj_agent({ name: v })) + where;
    case 'disk_usage':
      return filtered(r.container_filter, m.alerts_subj_any_disk(), (v) => m.alerts_subj_disk({ mount: v })) + where;
    case 'backup_failed':
    case 'backup_missed':
      return filtered(r.container_filter, dat ? m.alerts_subj_any_job_dat() : m.alerts_subj_any_job(), (v) => m.alerts_subj_job({ name: v }));
    case 'cert_expiry':
      return filtered(r.container_filter, dat ? m.alerts_subj_any_site_dat() : m.alerts_subj_any_site(), (v) => (dat ? m.alerts_subj_site_dat({ name: v }) : m.alerts_subj_site({ name: v })));
  }
}

// The whole condition in words, used in the list and as the dialog's
// preview: "CPU of a container of stack web is above 80 % for 1m".
export function ruleSentence(r: RuleShape, hostName?: (id: string) => string): string {
  const s = subjectOf(r, hostName);
  const sd = subjectOf(r, hostName, true);
  const d = fmtDuration(r.duration_seconds || 0);
  switch (kindOf(r)) {
    case 'metric': {
      const metric = r.metric === 'mem_percent' ? m.alerts_metric_memory() : 'CPU';
      const op = r.operator === 'lt' ? m.alerts_op_below() : m.alerts_op_above();
      const dur = r.duration_seconds > 0 ? ` ${m.alerts_for_duration({ d })}` : '';
      return m.alerts_sentence_metric({ metric, subject: sd, op, n: r.threshold }) + cpuUnitSuffix(r) + dur;
    }
    case 'restart_loop':
      return m.alerts_sentence_restart_loop({ subject: s, n: r.threshold, d });
    case 'container_stopped':
      return m.alerts_sentence_container_stopped({ subject: s });
    case 'unhealthy':
      return m.alerts_sentence_unhealthy({ subject: sd });
    case 'oom':
      return m.alerts_sentence_oom({ subject: s });
    case 'host_offline':
      return m.alerts_sentence_host_offline({ subject: s, d });
    case 'disk_usage':
      return m.alerts_sentence_disk_usage({ subject: s, n: r.threshold });
    case 'backup_failed':
      return m.alerts_sentence_backup_failed({ subject: sd });
    case 'backup_missed':
      return m.alerts_sentence_backup_missed({ subject: s });
    case 'cert_expiry':
      return m.alerts_sentence_cert_expiry({ subject: sd, n: r.threshold });
  }
}

// Short "applies to" summary for the rules list.
export function appliesTo(r: RuleShape, hostName: (id: string) => string = (id) => id): string {
  const k = kindOf(r);
  const parts: string[] = [];
  const f = r.container_filter && r.container_filter !== '*' ? r.container_filter : '';
  if (k === 'backup_failed' || k === 'backup_missed') return f ? m.alerts_applies_job({ name: f }) : m.alerts_applies_all_jobs();
  if (k === 'cert_expiry') return f || m.alerts_applies_all_sites();
  if (k === 'host_offline') parts.push(f ? m.alerts_subj_agent({ name: f }) : m.alerts_applies_all_agents());
  else if (k === 'disk_usage') parts.push(f ? m.alerts_applies_mount({ mount: f }) : m.alerts_applies_data_disks());
  else {
    if (r.scope_stack) parts.push(m.alerts_applies_stack({ stack: r.scope_stack }));
    parts.push(f ? m.alerts_subj_container({ name: f }) : r.scope_stack ? '' : m.alerts_applies_all_containers());
  }
  if (r.scope_host) parts.push(m.alerts_subj_on_host({ host: hostName(r.scope_host) }));
  else if (r.scope_tag) parts.push(m.alerts_applies_hosts_tagged({ tag: r.scope_tag }));
  return parts.filter(Boolean).join(' · ');
}

export function kindLabel(k: RuleKind): string {
  return KINDS.find((x) => x.id === k)?.label ?? k;
}

export function sevTone(s: string | undefined): Tone {
  return s === 'critical' ? 'bad' : s === 'warning' ? 'warn' : 'off';
}

export function sevLabel(s: string): string {
  if (s === 'critical') return m.alerts_sev_critical();
  if (s === 'warning') return m.alerts_sev_warning();
  if (s === 'info') return m.alerts_sev_info();
  return s;
}

// CPU rules name their unit; rules stored without one were written
// against per-core values.
function cpuUnitOf(r: Pick<AlertRuleInput, 'metric' | 'cpu_unit'>): 'host' | 'core' | '' {
  if (r.metric !== 'cpu_percent') return '';
  return r.cpu_unit === 'host' ? 'host' : 'core';
}

export function cpuUnitSuffix(r: Pick<AlertRuleInput, 'metric' | 'cpu_unit'>): string {
  const u = cpuUnitOf(r);
  return u === 'host' ? ` ${m.alerts_cpu_of_host()}` : u === 'core' ? ` ${m.alerts_cpu_of_core()}` : '';
}
