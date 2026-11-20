import { m } from '$lib/paraglide/messages.js';
import { fmtAgo } from '$lib/format';
import type { ClusterState, ClusterStatus, ClusterMembership } from '$lib/api';
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

/** The group row's second line: what the managing node last said. */
export function clusterLine(st: ClusterStatus | undefined): string {
  if (!st || st.state === 'unknown') return m.cluster_line_not_checked();
  if (st.state === 'unreachable') return st.known_at ? m.cluster_line_last_known({ ago: fmtAgo(st.known_at) }) : clusterStateLabel(st.state);
  if (st.state === 'no_quorum') return m.cluster_line_no_quorum();
  return st.leader ? m.cluster_line_leader({ leader: st.leader, ago: fmtAgo(st.checked_at) }) : m.cluster_line_checked({ ago: fmtAgo(st.checked_at) });
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
