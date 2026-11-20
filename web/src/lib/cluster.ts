import { m } from '$lib/paraglide/messages.js';
import { fmtAgo } from '$lib/format';
import type { ClusterState, ClusterStatus, ClusterMembership, ClusterDistribution, ClusterDistNode, SwarmTask } from '$lib/api';
import type { Tone } from '$lib/components/ui/StatusChip.svelte';

export function clusterTone(state: ClusterState | undefined): Tone {
  switch (state) {
    case 'healthy':
      return 'ok';
    case 'degraded':
      return 'warn';
    case 'no_quorum':
      return 'bad';
    default:
      return 'off';
  }
}

export function clusterStateLabel(state: ClusterState | undefined): string {
  switch (state) {
    case 'healthy':
      return m.cluster_state_healthy();
    case 'degraded':
      return m.cluster_state_degraded();
    case 'no_quorum':
      return m.cluster_state_no_quorum();
    case 'unreachable':
      return m.cluster_state_unreachable();
    default:
      return m.cluster_state_unknown();
  }
}

/** "Swarm · 3 managers, 1 worker · 1 node without agent", from the last
 * node list the managing node returned. */
export function clusterSummary(st: ClusterStatus | undefined): string {
  if (!st?.known_at) return m.cluster_swarm();
  const shape = [
    st.managers === 1 ? m.cluster_managers_one() : m.cluster_managers({ n: st.managers }),
    st.workers === 1 ? m.cluster_workers_one() : m.cluster_workers({ n: st.workers })
  ].join(', ');
  const parts = [m.cluster_swarm(), shape];
  if (st.nodes_without_agent > 0) {
    parts.push(st.nodes_without_agent === 1 ? m.cluster_no_agent_one() : m.cluster_no_agent({ n: st.nodes_without_agent }));
  }
  return parts.join(' · ');
}

/** The group row's second line: what the managing node last said, and
 *  which host it was read through when readName is given. */
export function clusterLine(st: ClusterStatus | undefined, readName?: string): string {
  if (!st || st.state === 'unknown') return m.cluster_line_not_checked();
  if (st.state === 'unreachable') return st.known_at ? m.cluster_line_last_known({ ago: fmtAgo(st.known_at) }) : clusterStateLabel(st.state);
  if (st.state === 'no_quorum') return m.cluster_line_no_quorum();
  const base = st.leader ? m.cluster_line_leader({ leader: st.leader, ago: fmtAgo(st.checked_at) }) : m.cluster_line_checked({ ago: fmtAgo(st.checked_at) });
  if (!readName) return base;
  return `${base} · ${st.failover ? m.cluster_line_read_failover({ host: readName }) : m.cluster_line_read({ host: readName })}`;
}

/** "Manager · reachable · leader" for a host page. */
export function memberRoleLine(ms: ClusterMembership): string {
  const parts = [ms.role === 'manager' ? m.hosts_member_role_manager() : m.hosts_member_role_worker()];
  const n = ms.node;
  if (n?.leader) parts.push(m.hosts_member_leader());
  else if (ms.role === 'manager' && n?.reachability) parts.push(n.reachability === 'reachable' ? m.hosts_member_reachable() : m.hosts_member_unreachable());
  if (n?.availability === 'drain') parts.push(m.hosts_member_drain());
  if (n?.availability === 'pause') parts.push(m.hosts_member_pause());
  return parts.join(' · ');
}

/** Task states after which Swarm starts a replacement because the task broke. */
export const FAILED_STATES = new Set(['failed', 'rejected', 'orphaned']);

/** Window in which repeated failures of one slot count as restarting. */
export const RESTART_WINDOW_MS = 60 * 60 * 1000;

export interface TaskProblem {
  kind: 'failed' | 'restarting';
  /** Failed tasks of the slot within RESTART_WINDOW_MS. */
  failures: number;
  /** The error of the slot's newest failed task. */
  lastError?: string;
}

// A replicated task keeps its slot across replacements; a global task is
// replaced on the same node.
function slotKey(t: SwarmTask): string {
  return `${t.service_id}/${t.slot ? `s${t.slot}` : `n${t.node_id ?? ''}`}`;
}

/** Problems by the id of each slot's newest task: the newest failed, or
 * the slot failed at least twice within the window. */
export function taskProblems(tasks: SwarmTask[], now = Date.now()): Map<string, TaskProblem> {
  const slots = new Map<string, SwarmTask[]>();
  for (const t of tasks) {
    const k = slotKey(t);
    const list = slots.get(k);
    if (list) list.push(t);
    else slots.set(k, [t]);
  }
  const out = new Map<string, TaskProblem>();
  for (const list of slots.values()) {
    list.sort((a, b) => Date.parse(b.updated_at) - Date.parse(a.updated_at));
    const newest = list[0];
    const failed = list.filter((t) => FAILED_STATES.has(t.state));
    const failures = failed.filter((t) => now - Date.parse(t.updated_at) <= RESTART_WINDOW_MS).length;
    const lastError = failed[0]?.error || failed[0]?.message || undefined;
    if (FAILED_STATES.has(newest.state) && newest.desired_state !== 'shutdown') out.set(newest.id, { kind: 'failed', failures, lastError });
    else if (failures >= 2) out.set(newest.id, { kind: 'restarting', failures, lastError });
  }
  return out;
}

/** Services grouped by their stack label, stacks by name, services
 * without a stack last. */
export function servicesByStack<T extends { name: string; stack?: string }>(services: T[]): { stack: string; services: T[] }[] {
  const by = new Map<string, T[]>();
  for (const s of services) {
    const k = s.stack ?? '';
    const list = by.get(k);
    if (list) list.push(s);
    else by.set(k, [s]);
  }
  return [...by.entries()]
    .sort(([a], [b]) => (a === '' ? 1 : b === '' ? -1 : a.localeCompare(b)))
    .map(([stack, list]) => ({ stack, services: [...list].sort((a, b) => a.name.localeCompare(b.name)) }));
}

/** A node's host name, or its short id when the manager reported none. */
export function nodeLabel(n: { id: string; hostname?: string }): string {
  return n.hostname || n.id.slice(0, 12);
}

/** The placement constraint that ties a service to one node, as the
 * server's pinned rule reads it, or null. */
export function pinningConstraint(constraints: string[] | undefined): string | null {
  return constraints?.find((c) => /^\s*node\.(hostname|id)\s*==\s*\S+\s*$/.test(c)) ?? null;
}

/** The busiest heavy node, or null when the spread is even. */
export function heaviest(d: ClusterDistribution | null | undefined): ClusterDistNode | null {
  if (!d || d.stale) return null;
  let top: ClusterDistNode | null = null;
  for (const n of d.nodes) if (n.heavy && (!top || n.tasks > top.tasks)) top = n;
  return top;
}

/** The group row's load line: "Load spread: uneven · m1 runs 14 of 31 tasks". */
export function spreadLine(d: ClusterDistribution | null | undefined): { text: string; uneven: boolean } | null {
  if (!d || d.stale || d.tasks === 0) return null;
  const h = heaviest(d);
  if (!h) return { text: m.cluster_spread_even(), uneven: false };
  return { text: m.cluster_spread_uneven({ node: nodeLabel(h), n: h.tasks, total: d.tasks }), uneven: true };
}

export function nodeStateLabel(state: string | undefined): string {
  switch (state) {
    case 'ready':
      return m.cluster_node_ready();
    case 'down':
      return m.cluster_node_down();
    case 'disconnected':
      return m.cluster_node_disconnected();
    default:
      return m.cluster_node_unknown();
  }
}

export function availabilityLabel(a: string | undefined): string {
  switch (a) {
    case 'active':
      return m.cluster_avail_active();
    case 'pause':
      return m.cluster_avail_pause();
    case 'drain':
      return m.cluster_avail_drain();
    default:
      return '—';
  }
}
