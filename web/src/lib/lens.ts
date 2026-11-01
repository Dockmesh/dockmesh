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
