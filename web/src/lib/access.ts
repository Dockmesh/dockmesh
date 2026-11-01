import type { Assignment, CustomRole, RoleCatalog, Scope } from './api';

export const GLOBAL: Scope = { type: 'global' };

export type HostNames = Record<string, string>;

export function scopeLabel(s: Scope | undefined, hosts: HostNames = {}): string {
  const hn = (id?: string) => (id ? hosts[id] ?? id : '');
  switch (s?.type) {
    case 'host':
      return `host ${hn(s.host)}`;
    case 'host_tag':
      return `hosts tagged ${s.value}`;
    case 'stack':
      return s.host ? `stack ${s.value} on ${hn(s.host)}` : `stack ${s.value}`;
    default:
      return 'everything';
  }
}

export function isGlobal(s: Scope | undefined): boolean {
  return !s || s.type === 'global';
}

export function sameScope(a: Scope, b: Scope): boolean {
  return a.type === b.type && (a.host ?? '') === (b.host ?? '') && (a.value ?? '') === (b.value ?? '');
}

export function roleDisplay(roles: CustomRole[], name: string): string {
  return roles.find((r) => r.name === name)?.display ?? name;
}

export function assignmentLabel(a: Assignment, roles: CustomRole[], hosts: HostNames = {}): string {
  return `${roleDisplay(roles, a.role)} on ${scopeLabel(a.scope, hosts)}`;
}

function levelIndex(cat: RoleCatalog, area: string, level: string | undefined): number {
  const a = cat.areas.find((x) => x.key === area);
  if (!a) return 0;
  const i = a.levels.findIndex((l) => l.key === (level || 'none'));
  return i < 0 ? 0 : i;
}

export interface AreaSummary {
  key: string;
  label: string;
  scoped: boolean;
  level: string;
  levelLabel: string;
  can: string[];
  cannot: string[];
}

// Plain-words summary of what a role allows per area. With narrow = true
// the areas a non-global assignment never grants are left out.
export function areaSummaries(cat: RoleCatalog, levels: Record<string, string>, narrow = false): AreaSummary[] {
  return cat.areas
    .filter((a) => !narrow || a.scoped)
    .map((a) => {
      const i = levelIndex(cat, a.key, levels[a.key]);
      return {
        key: a.key,
        label: a.label,
        scoped: a.scoped,
        level: a.levels[i]?.key ?? 'none',
        levelLabel: a.levels[i]?.label ?? 'None',
        can: a.levels.slice(1, i + 1).flatMap((l) => l.items),
        cannot: a.levels.slice(i + 1).flatMap((l) => l.items)
      };
    });
}

const PHRASE: Record<string, Record<string, string>> = {
  stacks: { view: 'see stacks', operate: 'stop stacks', deploy: 'deploy stacks', manage: 'create and delete stacks' },
  containers: { view: 'see containers', operate: 'restart containers', manage: 'remove and update containers' },
  resources: { view: 'see images and volumes', manage: 'manage images and volumes' },
  hosts: { view: 'see hosts', manage: 'manage hosts' },
  proxy: { view: 'see proxy routes', manage: 'manage proxy routes' },
  backups: { view: 'see backups', operate: 'run and restore backups', manage: 'manage backup jobs' },
  alerts: { view: 'see alerts', manage: 'manage alerts' },
  users: { view: 'see users', manage: 'manage users and roles' },
  settings: { view: 'read the audit log', manage: 'change settings' }
};
const EXTRA_SHORT: Record<string, string> = {
  terminal: 'open terminals',
  files: 'browse files',
  env: 'see secrets in env',
  backup_download: 'download backups',
  key_export: 'export the backup key'
};

function list(xs: string[], n: number): string {
  return xs.length <= n ? xs.join(', ') : `${xs.slice(0, n).join(', ')} and ${xs.length - n} more`;
}

// One line: "Can deploy stacks, restart containers and 3 more. Cannot create and delete stacks."
export function roleOneLine(cat: RoleCatalog | null, r: Pick<CustomRole, 'levels' | 'extras'>): string {
  if (!cat) return '';
  const can: string[] = [];
  const cannot: string[] = [];
  let top = true;
  for (const a of cat.areas) {
    const i = levelIndex(cat, a.key, r.levels[a.key]);
    if (i < a.levels.length - 1) top = false;
    const lv = a.levels[i]?.key;
    if (lv && lv !== 'none' && PHRASE[a.key]?.[lv]) can.push(PHRASE[a.key][lv]);
    const next = a.levels[i + 1]?.key;
    if (next && (a.key === 'stacks' || a.key === 'containers') && PHRASE[a.key]?.[next]) cannot.push(PHRASE[a.key][next]);
  }
  for (const e of r.extras) if (EXTRA_SHORT[e]) can.push(EXTRA_SHORT[e]);
  for (const e of ['terminal', 'files']) if (!r.extras.includes(e)) cannot.push(EXTRA_SHORT[e]);
  if (top && cat.extras.every((e) => r.extras.includes(e.key))) return 'Can do everything.';
  const a = can.length ? `Can ${list(can, 3)}.` : 'No access.';
  return cannot.length ? `${a} Cannot ${list(cannot, 2)}.` : a;
}
