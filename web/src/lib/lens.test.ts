import { describe, expect, it } from 'vitest';
import type { HostInfo } from './api';
import { filterHosts, stackInLens } from './lens';

const h = (name: string, tags: string[] = []): HostInfo => ({ id: name, name, kind: 'agent', status: 'online', tags });
const list = [h('edge-fra-1', ['edge', 'prod']), h('gpu-box', ['gpu']), h('Local', [])];

describe('filterHosts', () => {
  const cases: [string, string[]][] = [
    ['', ['edge-fra-1', 'gpu-box', 'Local']],
    ['fra', ['edge-fra-1']],
    ['GPU', ['gpu-box']],
    ['pro', ['edge-fra-1']],
    ['#prod', ['edge-fra-1']],
    ['#pro', []],
    ['nothing', []]
  ];
  for (const [q, want] of cases) {
    it(JSON.stringify(q), () => expect(filterHosts(list, q).map((x) => x.name)).toEqual(want));
  }
});

describe('stackInLens', () => {
  const on = (host_id: string) => ({ deployment: { host_id } });
  const cases: [string, Parameters<typeof stackInLens>, boolean][] = [
    ['all hosts shows everything', [on('a1'), 'all', false], true],
    ['deployed on the lens host', [on('local'), 'local', false], true],
    ['deployed on another host', [on('a1'), 'local', false], false],
    ['deployed elsewhere but has containers here', [on('a1'), 'local', true], true],
    ['never deployed', [{}, 'a1', false], true],
    ['null deployment', [{ deployment: null }, 'local', false], true]
  ];
  for (const [name, args, want] of cases) {
    it(name, () => expect(stackInLens(...args)).toBe(want));
  }
});
