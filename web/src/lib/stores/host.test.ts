import { describe, expect, it } from 'vitest';
import { createHostStore } from './host.svelte';
import type { HostInfo } from '$lib/api';

const h = (id: string, status: HostInfo['status'] = 'online'): HostInfo => ({ id, name: id, kind: id === 'local' ? 'local' : 'agent', status });

describe('host selection fallback', () => {
  const cases: { name: string; start: string; list: HostInfo[]; want: string }[] = [
    { name: 'keeps a visible host', start: 'a1', list: [h('local'), h('a1')], want: 'a1' },
    { name: 'offline agent falls back to local', start: 'a1', list: [h('local'), h('a1', 'offline')], want: 'local' },
    { name: 'scoped caller without local gets its first online host', start: 'local', list: [h('a2', 'offline'), h('a1')], want: 'a1' },
    { name: 'removed host without local or online hosts takes the first one', start: 'gone', list: [h('a2', 'offline')], want: 'a2' },
    { name: 'all stays all', start: 'all', list: [h('a1'), h('a2')], want: 'all' },
    { name: 'empty list keeps the selection', start: 'a1', list: [], want: 'a1' }
  ];
  for (const c of cases) {
    it(c.name, () => {
      const s = createHostStore();
      s.set(c.start);
      s.setAvailable(c.list);
      expect(s.id).toBe(c.want);
    });
  }
});

describe('host selection usable', () => {
  it('waits for the host list unless the caller cannot list hosts', () => {
    const s = createHostStore();
    s.set('local');
    expect(s.usable(true)).toBe(false);
    expect(s.usable(false)).toBe(true);
    s.setAvailable([h('a1')]);
    expect(s.id).toBe('a1');
    expect(s.usable(true)).toBe(true);
    s.set('all');
    expect(s.usable(true)).toBe(true);
  });
});
