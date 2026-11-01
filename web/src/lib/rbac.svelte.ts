// Frontend permission gate. /login and /me return every permission the
// caller holds on at least one scope; the backend still checks each
// request against the concrete host or stack. This file is purely UX:
// don't show buttons the user can't use anywhere.

import { auth } from './stores/auth.svelte';

export type Perm =
  | 'containers.view' | 'containers.update' | 'containers.delete' | 'containers.upgrade'
  | 'containers.exec' | 'containers.files' | 'containers.env' | 'containers.logs'
  | 'stacks.view' | 'stacks.operate' | 'stacks.deploy' | 'stacks.update' | 'stacks.create'
  | 'stacks.delete' | 'stacks.migrate' | 'stacks.adopt' | 'stacks.git'
  | 'volumes.view' | 'volumes.create' | 'volumes.delete' | 'volumes.browse'
  | 'networks.view' | 'networks.create' | 'networks.delete'
  | 'images.view' | 'images.create' | 'images.delete' | 'images.scan'
  | 'registries.view' | 'registries.create' | 'registries.update' | 'registries.delete'
  | 'hosts.view' | 'hosts.create' | 'hosts.update' | 'hosts.delete' | 'hosts.tag'
  | 'users.view' | 'users.create' | 'users.update' | 'users.delete'
  | 'roles.view' | 'roles.create' | 'roles.update' | 'roles.delete'
  | 'tokens.view' | 'tokens.create' | 'tokens.delete' | 'tokens.manage_others'
  | 'backups.view' | 'backups.run' | 'backups.restore' | 'backups.create' | 'backups.update'
  | 'backups.delete' | 'backups.download' | 'backups.key_export'
  | 'proxy.view' | 'proxy.create' | 'proxy.update' | 'proxy.delete'
  | 'alerts.view' | 'alerts.history' | 'alerts.create' | 'alerts.update' | 'alerts.delete'
  | 'templates.view' | 'templates.create' | 'templates.update' | 'templates.delete'
  | 'audit.view' | 'audit.export' | 'audit.write'
  | 'system.view' | 'system.update' | 'system.upgrade'
  | 'metrics.view';

// Every signed-in user holds these, whatever their assignments.
const BASE: Perm[] = ['tokens.view', 'tokens.create', 'tokens.delete', 'system.view'];

export interface AllowOpts {
  stack?: string;
  host?: string;
  hostTags?: string[];
}

function userPerms(): string[] {
  const u = auth.user;
  if (!u) return [];
  return [...BASE, ...(u.permissions ?? [])];
}

// opts are accepted for call sites that name the resource; whether the
// permission holds on that exact host or stack is decided by the backend.
export function allowed(perm: Perm, _opts?: AllowOpts): boolean {
  return userPerms().includes(perm);
}

/** system.update is only granted by an assignment on everything. */
export function isAdmin(): boolean {
  return userPerms().includes('system.update');
}

export function can(perm: Perm, opts?: AllowOpts): boolean {
  return allowed(perm, opts);
}
