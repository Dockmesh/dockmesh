export type AuditCategory = 'changes' | 'access' | 'auth' | 'system';

export const CATEGORY_LABEL: Record<AuditCategory, string> = {
  changes: 'Changes',
  access: 'Access',
  auth: 'Sign-in & security',
  system: 'System'
};

const PAGES: Record<string, (t: string) => string> = {
  stack: (t) => `/stacks/${encodeURIComponent(t)}`,
  container: (t) => `/containers/${encodeURIComponent(t)}`,
  volume: (t) => `/volumes/${encodeURIComponent(t)}`,
  network: (t) => `/networks/${encodeURIComponent(t)}`,
  proxy: (t) => `/proxy/${encodeURIComponent(t)}`
};

/** In-app page of an audit target, when the action names one object by a
 * plain name or id. Free-text targets ("backup job 3", "agent web-1") and
 * removed objects get no link. */
export function auditTargetHref(action: string, target: string | undefined): string | undefined {
  if (!target || /\s/.test(target)) return undefined;
  const area = action.split('.')[0];
  const verb = action.split('.')[1] ?? '';
  if (/(^|_)(delete|remove|prune)$/.test(verb)) return undefined;
  // Proxy actions other than create record the route id, not its name.
  if (area === 'proxy' && verb !== 'route_create') return undefined;
  return PAGES[area]?.(target);
}

/** Quoted CSV line; inner quotes are doubled and leading formula
 * characters are neutralised so a spreadsheet does not evaluate them. */
export function csvLine(cells: (string | number | undefined)[]): string {
  return cells
    .map((c) => {
      let s = String(c ?? '');
      if (/^[=+\-@\t\r]/.test(s)) s = `'${s}`;
      return `"${s.replace(/"/g, '""')}"`;
    })
    .join(',');
}
