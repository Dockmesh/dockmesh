import { describe, expect, it, vi } from 'vitest';

const held = new Set<string>();
vi.mock('$lib/rbac.svelte', () => ({ allowed: (p: string) => held.has(p) }));

const { routeAllowed } = await import('./nav');

describe('routeAllowed', () => {
  const cases: [string, string[], boolean][] = [
    ['/proxy', [], false],
    ['/proxy', ['proxy.view'], true],
    ['/stacks/web', ['containers.view'], false],
    ['/stacks/web', ['stacks.view'], true],
    ['/volumes/data', ['images.view'], false],
    ['/volumes/data', ['volumes.view'], true],
    ['/resources', ['images.view'], true],
    ['/settings/updates', ['registries.view'], false],
    ['/registries', ['registries.view'], true],
    ['/account', [], true],
    ['/', [], true]
  ];
  for (const [path, perms, want] of cases) {
    it(`${path} with ${perms.join(',') || 'nothing'}`, () => {
      held.clear();
      perms.forEach((p) => held.add(p));
      expect(routeAllowed(path)).toBe(want);
    });
  }
});
