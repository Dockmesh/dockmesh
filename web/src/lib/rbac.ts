// Mirror of internal/rbac/rbac.go. Keep in sync!
// This is the frontend-side gate for showing/hiding UI affordances.
// The backend still enforces every permission authoritatively — this is
// purely for UX (don't show a button the user can't use).

import { auth } from './stores/auth.svelte';

export type Perm =
  | 'read'
  | 'container.control'
  | 'container.exec'
  | 'stack.write'
  | 'stack.deploy'
  | 'stack.adopt'
  | 'image.write'
  | 'image.scan'
  | 'network.write'
  | 'volume.write'
  | 'user.manage'
  | 'audit.read';

const rolePerms: Record<string, Set<Perm>> = {
  admin: new Set<Perm>([
    'read',
    'container.control',
    'container.exec',
    'stack.write',
    'stack.deploy',
    'stack.adopt',
    'image.write',
    'image.scan',
    'network.write',
    'volume.write',
    'user.manage',
    'audit.read'
  ]),
  operator: new Set<Perm>([
    'read',
    'container.control',
    'container.exec',
    'stack.deploy',
    'image.scan',
    'audit.read'
  ]),
  viewer: new Set<Perm>(['read'])
};

export function allowed(perm: Perm, role?: string): boolean {
  const r = role ?? auth.user?.role ?? '';
  return rolePerms[r]?.has(perm) ?? false;
}

/**
 * Reactive helper: use inside $derived so the check re-runs when the user
 * changes. Example: `const canDeploy = $derived(can('stack.deploy'));`
 */
export function can(perm: Perm): boolean {
  return allowed(perm);
}
