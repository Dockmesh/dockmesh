// Frontend RBAC v2.1 gate. The backend's /login and /me responses
// include the caller's effective permissions + scopes; we read them
// straight from auth.user. The hardcoded built-in role map below is
// kept ONLY as a fallback for very old backends that don't return
// permissions yet — once we cut over completely, it can be deleted.
//
// Backend enforces every permission authoritatively. This file is
// purely UX: don't show buttons the user can't use.

import { auth } from './stores/auth.svelte';

export type Perm =
  // Containers
  | 'containers.view' | 'containers.update' | 'containers.delete'
  | 'containers.exec' | 'containers.logs'
  // Stacks
  | 'stacks.view' | 'stacks.create' | 'stacks.update' | 'stacks.delete'
  | 'stacks.deploy' | 'stacks.migrate' | 'stacks.adopt'
  // Volumes
  | 'volumes.view' | 'volumes.create' | 'volumes.delete'
  | 'volumes.browse' | 'volumes.read_file'
  // Networks
  | 'networks.view' | 'networks.create' | 'networks.delete'
  // Images
  | 'images.view' | 'images.create' | 'images.delete' | 'images.scan'
  // Registries
  | 'registries.view' | 'registries.create' | 'registries.update' | 'registries.delete'
  // Hosts
  | 'hosts.view' | 'hosts.create' | 'hosts.update' | 'hosts.delete' | 'hosts.tag'
  // Users
  | 'users.view' | 'users.create' | 'users.update' | 'users.delete'
  | 'users.password_reset' | 'users.suspend'
  // Roles
  | 'roles.view' | 'roles.create' | 'roles.update' | 'roles.delete'
  // Tokens
  | 'tokens.view' | 'tokens.create' | 'tokens.delete' | 'tokens.manage_others'
  // Backups
  | 'backups.view' | 'backups.create' | 'backups.update' | 'backups.delete' | 'backups.restore'
  // Proxy
  | 'proxy.view' | 'proxy.create' | 'proxy.update' | 'proxy.delete'
  // Alerts
  | 'alerts.view' | 'alerts.create' | 'alerts.update' | 'alerts.delete'
  // Templates
  | 'templates.view' | 'templates.create' | 'templates.update' | 'templates.delete'
  // Audit
  | 'audit.view' | 'audit.export' | 'audit.write'
  // System
  | 'system.view' | 'system.update' | 'system.upgrade'
  // Metrics
  | 'metrics.view';

const ALL_VIEW: Perm[] = [
  'containers.view', 'stacks.view', 'volumes.view', 'networks.view',
  'images.view', 'registries.view', 'hosts.view', 'users.view',
  'roles.view', 'tokens.view', 'backups.view', 'proxy.view',
  'alerts.view', 'templates.view', 'audit.view', 'system.view',
  'metrics.view',
];

// Fallback only — used when the backend hasn't populated user.permissions.
// Mirrors internal/rbac/rbac.go rolePerms map for the five built-ins.
const FALLBACK_ROLE_PERMS: Record<string, Perm[]> = {
  admin: [
    'containers.view', 'containers.update', 'containers.delete', 'containers.exec', 'containers.logs',
    'stacks.view', 'stacks.create', 'stacks.update', 'stacks.delete', 'stacks.deploy',
    'stacks.migrate', 'stacks.adopt',
    'volumes.view', 'volumes.create', 'volumes.delete', 'volumes.browse', 'volumes.read_file',
    'networks.view', 'networks.create', 'networks.delete',
    'images.view', 'images.create', 'images.delete', 'images.scan',
    'registries.view', 'registries.create', 'registries.update', 'registries.delete',
    'hosts.view', 'hosts.create', 'hosts.update', 'hosts.delete', 'hosts.tag',
    'users.view', 'users.create', 'users.update', 'users.delete', 'users.password_reset', 'users.suspend',
    'roles.view', 'roles.create', 'roles.update', 'roles.delete',
    'tokens.view', 'tokens.create', 'tokens.delete', 'tokens.manage_others',
    'backups.view', 'backups.create', 'backups.update', 'backups.delete', 'backups.restore',
    'proxy.view', 'proxy.create', 'proxy.update', 'proxy.delete',
    'alerts.view', 'alerts.create', 'alerts.update', 'alerts.delete',
    'templates.view', 'templates.create', 'templates.update', 'templates.delete',
    'audit.view', 'audit.export', 'audit.write',
    'system.view', 'system.update', 'system.upgrade',
    'metrics.view',
  ],
  'host-admin': [
    'containers.view', 'containers.update', 'containers.delete', 'containers.exec', 'containers.logs',
    'stacks.view', 'stacks.create', 'stacks.update', 'stacks.delete', 'stacks.deploy',
    'stacks.migrate', 'stacks.adopt',
    'volumes.view', 'volumes.create', 'volumes.delete', 'volumes.browse', 'volumes.read_file',
    'networks.view', 'networks.create', 'networks.delete',
    'images.view', 'images.create', 'images.delete', 'images.scan',
    'registries.view', 'registries.create', 'registries.update', 'registries.delete',
    'hosts.view', 'hosts.create', 'hosts.update', 'hosts.delete', 'hosts.tag',
    'users.view', 'roles.view',
    'tokens.view', 'tokens.create', 'tokens.delete',
    'backups.view', 'backups.create', 'backups.update', 'backups.delete', 'backups.restore',
    'proxy.view', 'proxy.create', 'proxy.update', 'proxy.delete',
    'alerts.view', 'alerts.create', 'alerts.update', 'alerts.delete',
    'templates.view', 'templates.create', 'templates.update', 'templates.delete',
    'audit.view', 'audit.export',
    'system.view', 'system.update',
    'metrics.view',
  ],
  deployer: [
    'containers.view', 'containers.update', 'containers.delete', 'containers.exec', 'containers.logs',
    'stacks.view', 'stacks.create', 'stacks.update', 'stacks.delete', 'stacks.deploy', 'stacks.adopt',
    'volumes.view', 'volumes.create',
    'networks.view', 'networks.create',
    'images.view', 'images.create', 'images.scan',
    'registries.view', 'hosts.view',
    'users.view', 'roles.view',
    'tokens.view', 'tokens.create', 'tokens.delete',
    'backups.view',
    'proxy.view', 'alerts.view',
    'templates.view', 'templates.create', 'templates.update',
    'audit.view',
    'system.view', 'metrics.view',
  ],
  operator: [
    'containers.view', 'containers.update', 'containers.exec', 'containers.logs',
    'stacks.view', 'stacks.deploy',
    'volumes.view', 'networks.view',
    'images.view', 'images.scan',
    'registries.view', 'hosts.view',
    'users.view', 'roles.view',
    'tokens.view', 'tokens.create', 'tokens.delete',
    'backups.view', 'proxy.view', 'alerts.view',
    'templates.view', 'audit.view',
    'system.view', 'metrics.view',
  ],
  viewer: ALL_VIEW,
};

export interface AllowOpts {
  /** When set, also requires the caller's role to include this stack in its scope (or be unscoped on stacks). */
  stack?: string;
  /** When set, also requires the caller's role to include this host in its scope (or be unscoped on hosts). */
  host?: string;
  /** When set, also requires the caller's role to include at least one of these tags in its host_tags scope. */
  hostTags?: string[];
}

function userPerms(): Perm[] {
  const u = auth.user;
  if (!u) return [];
  if (u.permissions && u.permissions.length > 0) return u.permissions as Perm[];
  return FALLBACK_ROLE_PERMS[u.role] ?? [];
}

function scopeOK(opts?: AllowOpts): boolean {
  if (!opts) return true;
  const u = auth.user;
  if (!u || !u.scopes) return true; // backend pre-v2.1 — open
  const { stacks, hosts, host_tags } = u.scopes;
  // Each scope dimension: empty list = unscoped on that dimension =
  // matches anything. Non-empty = caller's value must be in the list.
  if (opts.stack !== undefined && stacks.length > 0 && !stacks.includes(opts.stack)) {
    // Fall back to host/tag membership for the same role — InScope() on
    // the backend uses an OR across scope types, mirror that here.
    if (opts.host !== undefined && hosts.length > 0 && hosts.includes(opts.host)) return true;
    if (opts.hostTags && host_tags.length > 0 && opts.hostTags.some((t) => host_tags.includes(t))) return true;
    return false;
  }
  if (opts.host !== undefined && hosts.length > 0 && !hosts.includes(opts.host)) {
    if (opts.hostTags && host_tags.length > 0 && opts.hostTags.some((t) => host_tags.includes(t))) return true;
    if (opts.stack !== undefined && stacks.length > 0 && stacks.includes(opts.stack)) return true;
    return false;
  }
  if (opts.hostTags && host_tags.length > 0) {
    if (!opts.hostTags.some((t) => host_tags.includes(t))) {
      if (opts.stack !== undefined && stacks.length > 0 && stacks.includes(opts.stack)) return true;
      if (opts.host !== undefined && hosts.length > 0 && hosts.includes(opts.host)) return true;
      return false;
    }
  }
  return true;
}

export function allowed(perm: Perm, opts?: AllowOpts): boolean {
  if (!userPerms().includes(perm)) return false;
  return scopeOK(opts);
}

/**
 * Reactive helper: use inside $derived so the check re-runs when the user
 * changes. Example: `const canDeploy = $derived(can('stacks.deploy'));`
 */
export function can(perm: Perm, opts?: AllowOpts): boolean {
  return allowed(perm, opts);
}

// Legacy stubs — old call sites expected these. No-ops now that
// /login + /me return permissions directly.
export function ensureRolesLoaded(): Promise<void> { return Promise.resolve(); }
export function resetRolesCache() { /* no-op */ }
