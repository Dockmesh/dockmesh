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
      { key: 'overview', href: '/', label: 'Overview', icon: LayoutDashboard, show: () => true },
      { key: 'activity', href: '/activity', label: 'Activity', icon: Inbox, show: () => true }
    ]
  },
  {
    title: 'Workloads',
    items: [
      { key: 'stacks', href: '/stacks', label: 'Stacks', icon: Layers, show: () => allowed('stacks.view') },
      { key: 'containers', href: '/containers', label: 'Containers', icon: Box, show: () => allowed('containers.view') },
      { key: 'templates', href: '/templates', label: 'Templates', icon: LayoutTemplate, show: () => allowed('templates.view') }
    ]
  },
  {
    title: 'Infrastructure',
    items: [
      { key: 'hosts', href: '/hosts', label: 'Hosts', icon: Server, show: () => allowed('hosts.view'), also: ['/agents'] },
      {
        key: 'resources',
        href: '/resources',
        label: 'Resources',
        icon: Boxes,
        show: () => allowed('images.view') || allowed('volumes.view') || allowed('networks.view'),
        also: ['/images', '/volumes', '/networks']
      },
      { key: 'topology', href: '/topology', label: 'Topology', icon: Waypoints, show: () => allowed('containers.view') }
    ]
  },
  {
    title: 'Operations',
    items: [
      { key: 'updates', href: '/updates', label: 'Updates', icon: RefreshCw, show: () => allowed('containers.view') },
      { key: 'backups', href: '/backups', label: 'Backups', icon: Archive, show: () => allowed('backups.view'), also: ['/migrations'] },
      { key: 'proxy', href: '/proxy', label: 'Reverse proxy', icon: Globe, show: () => allowed('proxy.view') },
      { key: 'alerts', href: '/alerts', label: 'Alerts', icon: Bell, show: () => allowed('alerts.view') }
    ]
  },
  {
    title: 'Admin',
    items: [
      { key: 'users', href: '/users', label: 'Users & roles', icon: Users, show: () => allowed('users.view') },
      { key: 'audit', href: '/audit', label: 'Audit log', icon: Activity, show: () => allowed('audit.view') },
      {
        key: 'settings',
        href: '/settings',
        label: 'Settings',
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
  { href: '/settings', label: 'General', group: 'Instance', hint: 'Base URL, agent URL, trusted proxies, image scanner', show: () => allowed('system.update') },
  { href: '/settings/updates', label: 'Updates', group: 'Instance', hint: 'Dockmesh version and update checks', show: () => allowed('system.update') },
  { href: '/settings/encryption', label: 'Encryption key', group: 'Instance', hint: 'Key backup and rotation', show: () => allowed('system.update') },
  { href: '/authentication', label: 'Authentication', group: 'Access', hint: 'Identity providers: OIDC, SAML, LDAP, OAuth2', show: () => allowed('system.update') },
  { href: '/registries', label: 'Registries', group: 'Integrations', hint: 'Private registries and credentials', show: () => allowed('registries.view') },
  { href: '/environment', label: 'Global environment', group: 'Integrations', hint: 'Variables shared by stacks', show: () => allowed('system.update') }
];
