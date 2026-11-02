import {
  LayoutDashboard,
  Layers,
  Box,
  LayoutTemplate,
  Server,
  Boxes,
  Waypoints,
  Archive,
  RefreshCw,
  Globe,
  Bell,
  Users,
  Activity,
  Inbox,
  Settings
} from 'lucide-svelte';
import { allowed } from '$lib/rbac.svelte';
import { m } from '$lib/paraglide/messages.js';

export interface NavItem {
  key: string;
  href: string;
  label: string;
  icon: any;
  show: () => boolean;
  // Other path prefixes that belong to this entry (detail pages, sub-pages).
  also?: string[];
}
export interface NavGroup {
  title: string | null;
  items: NavItem[];
}

// Settings pages that live outside /settings but are reached through its
// sub-navigation.
export const SETTINGS_PATHS = ['/settings', '/authentication', '/registries', '/environment'];

export const NAV: NavGroup[] = [
  {
    title: null,
    items: [
      { key: 'overview', href: '/', label: m.common_overview(), icon: LayoutDashboard, show: () => true },
      { key: 'activity', href: '/activity', label: m.shell_nav_activity(), icon: Inbox, show: () => true }
    ]
  },
  {
    title: m.shell_nav_workloads(),
    items: [
      { key: 'stacks', href: '/stacks', label: m.common_stacks(), icon: Layers, show: () => allowed('stacks.view') },
      { key: 'containers', href: '/containers', label: m.common_containers(), icon: Box, show: () => allowed('containers.view') },
      { key: 'templates', href: '/templates', label: m.shell_nav_templates(), icon: LayoutTemplate, show: () => allowed('templates.view') }
    ]
  },
  {
    title: m.shell_nav_infrastructure(),
    items: [
      { key: 'hosts', href: '/hosts', label: m.common_hosts(), icon: Server, show: () => allowed('hosts.view'), also: ['/agents'] },
      {
        key: 'resources',
        href: '/resources',
        label: m.shell_nav_resources(),
        icon: Boxes,
        show: () => allowed('images.view') || allowed('volumes.view') || allowed('networks.view'),
        also: ['/images', '/volumes', '/networks']
      },
      { key: 'topology', href: '/topology', label: m.shell_nav_topology(), icon: Waypoints, show: () => allowed('networks.view') }
    ]
  },
  {
    title: m.shell_nav_operations(),
    items: [
      { key: 'updates', href: '/updates', label: m.shell_nav_updates(), icon: RefreshCw, show: () => allowed('containers.view') },
      { key: 'backups', href: '/backups', label: m.shell_nav_backups(), icon: Archive, show: () => allowed('backups.view'), also: ['/migrations'] },
      { key: 'proxy', href: '/proxy', label: m.shell_nav_proxy(), icon: Globe, show: () => allowed('proxy.view') },
      { key: 'alerts', href: '/alerts', label: m.shell_nav_alerts(), icon: Bell, show: () => allowed('alerts.view') || allowed('alerts.history') }
    ]
  },
  {
    title: m.shell_nav_admin(),
    items: [
      { key: 'users', href: '/users', label: m.shell_nav_users(), icon: Users, show: () => allowed('users.view') },
      { key: 'audit', href: '/audit', label: m.shell_nav_audit(), icon: Activity, show: () => allowed('audit.view') },
      {
        key: 'settings',
        href: '/settings',
        label: m.common_settings(),
        icon: Settings,
        show: () => allowed('system.update') || allowed('registries.view'),
        also: SETTINGS_PATHS
      }
    ]
  }
];

export function matches(path: string, prefix: string): boolean {
  if (prefix === '/') return path === '/';
  return path === prefix || path.startsWith(prefix + '/');
}

export function activeItem(path: string): { group: NavGroup; item: NavItem } | null {
  for (const group of NAV) {
    for (const item of group.items) {
      if (matches(path, item.href) || item.also?.some((p) => matches(path, p))) return { group, item };
    }
  }
  return null;
}

export interface SettingsLink {
  href: string;
  label: string;
  group: string;
  hint: string;
  show: () => boolean;
}

export const SETTINGS_NAV: SettingsLink[] = [
  { href: '/settings', label: m.shell_settings_general(), group: m.shell_settings_group_instance(), hint: m.shell_settings_general_hint(), show: () => allowed('system.update') },
  { href: '/settings/updates', label: m.shell_nav_updates(), group: m.shell_settings_group_instance(), hint: m.shell_settings_updates_hint(), show: () => allowed('system.update') },
  { href: '/settings/encryption', label: m.shell_settings_encryption(), group: m.shell_settings_group_instance(), hint: m.shell_settings_encryption_hint(), show: () => allowed('system.update') },
  { href: '/authentication', label: m.shell_settings_auth(), group: m.shell_settings_group_access(), hint: m.shell_settings_auth_hint(), show: () => allowed('system.update') },
  { href: '/registries', label: m.shell_settings_registries(), group: m.shell_settings_group_integrations(), hint: m.shell_settings_registries_hint(), show: () => allowed('registries.view') },
  { href: '/environment', label: m.shell_settings_env(), group: m.shell_settings_group_integrations(), hint: m.shell_settings_env_hint(), show: () => allowed('system.update') }
];

// Pages under a nav entry that need a narrower permission than the entry.
const PAGE_PERMS: Array<{ prefix: string; show: () => boolean }> = [
  { prefix: '/images', show: () => allowed('images.view') },
  { prefix: '/volumes', show: () => allowed('volumes.view') },
  { prefix: '/networks', show: () => allowed('networks.view') },
  { prefix: '/migrations', show: () => true }
];

/**
 * routeAllowed reports whether the caller holds what the page at path
 * needs anywhere. The layout shows a no-access state instead of pages
 * that fail it, so they never start loading data they would get 403s for.
 */
export function routeAllowed(path: string): boolean {
  const longest = <T extends { prefix: string }>(xs: T[]) =>
    xs.filter((x) => matches(path, x.prefix)).sort((a, b) => b.prefix.length - a.prefix.length)[0];
  const page = longest(PAGE_PERMS);
  if (page) return page.show();
  const link = longest(SETTINGS_NAV.map((l) => ({ prefix: l.href, show: l.show })));
  if (link) return link.show();
  const hit = activeItem(path);
  return hit ? hit.item.show() : true;
}
