import { m } from '$lib/paraglide/messages.js';

const CONTAINER_STATE: Record<string, () => string> = {
  created: m.containers_status_created,
  running: m.containers_status_running,
  paused: m.containers_status_paused,
  restarting: m.containers_status_restarting,
  removing: m.containers_status_removing,
  exited: m.containers_status_exited,
  dead: m.containers_status_dead
};

const ALERT_SEVERITY: Record<string, () => string> = {
  critical: m.label_alert_sev_critical,
  warning: m.label_alert_sev_warning,
  info: m.label_alert_sev_info
};

const VULN_SEVERITY: Record<string, () => string> = {
  critical: m.label_vuln_critical,
  high: m.label_vuln_high,
  medium: m.label_vuln_medium,
  low: m.label_vuln_low,
  negligible: m.label_vuln_negligible,
  unknown: m.label_vuln_unknown
};

const HOST_STATUS: Record<string, () => string> = {
  online: m.label_host_online,
  offline: m.label_host_offline,
  pending: m.label_host_pending
};

// Values without a translation are shown as the server sent them.
const lookup = (table: Record<string, () => string>, v: string): string =>
  Object.hasOwn(table, v) ? table[v]() : v;

export const containerStateLabel = (state: string): string => lookup(CONTAINER_STATE, state);
export const alertSeverityLabel = (sev: string): string => lookup(ALERT_SEVERITY, sev);
export const vulnSeverityLabel = (sev: string): string => lookup(VULN_SEVERITY, sev);
export const hostStatusLabel = (status: string): string => lookup(HOST_STATUS, status);
