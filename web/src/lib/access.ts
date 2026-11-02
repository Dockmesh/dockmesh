import type { Assignment, CustomRole, RoleCatalog, Scope } from './api';
import { m } from '$lib/paraglide/messages.js';

export type HostNames = Record<string, string>;

export function scopeLabel(s: Scope | undefined, hosts: HostNames = {}): string {
  const hn = (id?: string) => (id ? hosts[id] ?? id : '');
  switch (s?.type) {
    case 'host':
      return m.lib_scope_host({ host: hn(s.host) });
    case 'host_tag':
      return m.lib_scope_host_tag({ tag: s.value ?? '' });
    case 'stack':
      return s.host ? m.lib_scope_stack_on({ stack: s.value ?? '', host: hn(s.host) }) : m.lib_scope_stack({ stack: s.value ?? '' });
    default:
      return m.lib_scope_everything();
  }
}

export function isGlobal(s: Scope | undefined): boolean {
  return !s || s.type === 'global';
}

export function roleDisplay(roles: CustomRole[], name: string): string {
  return roles.find((r) => r.name === name)?.display ?? name;
}

export function assignmentLabel(a: Assignment, roles: CustomRole[], hosts: HostNames = {}): string {
  return m.lib_assignment({ role: roleDisplay(roles, a.role), scope: scopeLabel(a.scope, hosts) });
}

export function levelIndex(cat: RoleCatalog, area: string, level: string | undefined): number {
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
        levelLabel: a.levels[i]?.label ?? m.common_none(),
        can: a.levels.slice(1, i + 1).flatMap((l) => l.items),
        cannot: a.levels.slice(i + 1).flatMap((l) => l.items)
      };
    });
}

const PHRASE: Record<string, Record<string, string>> = {
  stacks: { view: m.lib_can_stacks_view(), operate: m.lib_can_stacks_operate(), deploy: m.lib_can_stacks_deploy(), manage: m.lib_can_stacks_manage() },
  containers: { view: m.lib_can_containers_view(), operate: m.lib_can_containers_operate(), manage: m.lib_can_containers_manage() },
  resources: { view: m.lib_can_resources_view(), manage: m.lib_can_resources_manage() },
  hosts: { view: m.lib_can_hosts_view(), manage: m.lib_can_hosts_manage() },
  proxy: { view: m.lib_can_proxy_view(), manage: m.lib_can_proxy_manage() },
  backups: { view: m.lib_can_backups_view(), operate: m.lib_can_backups_operate(), manage: m.lib_can_backups_manage() },
  alerts: { view: m.lib_can_alerts_view(), manage: m.lib_can_alerts_manage() },
  users: { view: m.lib_can_users_view(), manage: m.lib_can_users_manage() },
  settings: { view: m.lib_can_settings_view(), manage: m.lib_can_settings_manage() }
};
const EXTRA_SHORT: Record<string, string> = {
  terminal: m.lib_can_terminal(),
  files: m.lib_can_files(),
  env: m.lib_can_env(),
  backup_download: m.lib_can_backup_download(),
  key_export: m.lib_can_key_export()
};

function list(xs: string[], n: number): string {
  return xs.length <= n ? xs.join(', ') : m.lib_list_more({ items: xs.slice(0, n).join(', '), count: xs.length - n });
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
  if (top && cat.extras.every((e) => r.extras.includes(e.key))) return m.lib_role_everything();
  const a = can.length ? m.lib_role_can({ list: list(can, 3) }) : m.lib_role_no_access();
  return cannot.length ? `${a} ${m.lib_role_cannot({ list: list(cannot, 2) })}` : a;
}
