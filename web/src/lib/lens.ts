import type { HostInfo } from '$lib/api';

/** Hosts whose name contains the query, or whose tags do; "#tag" matches
 * tags only and exactly. */
export function filterHosts(list: HostInfo[], query: string): HostInfo[] {
  const q = query.trim().toLowerCase();
  if (!q) return list;
  if (q.startsWith('#')) {
    const tag = q.slice(1);
    return list.filter((h) => (h.tags ?? []).some((t) => t.toLowerCase() === tag));
  }
  return list.filter((h) => h.name.toLowerCase().includes(q) || (h.tags ?? []).some((t) => t.toLowerCase().includes(q)));
}

/** Whether a stack belongs to a single-host lens. A stack that was never
 * deployed has no host yet and shows everywhere, so it can be deployed from
 * any lens; one deployed elsewhere stays out unless it has containers here. */
export function stackInLens(
  stack: { deployment?: { host_id: string } | null },
  lensId: string,
  hasContainersHere: boolean
): boolean {
  if (lensId === 'all' || hasContainersHere || !stack.deployment) return true;
  return stack.deployment.host_id === lensId;
}
