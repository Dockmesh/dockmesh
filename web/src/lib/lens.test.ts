import { describe, expect, it } from 'vitest';
import type { HostInfo } from './api';
import { filterHosts } from './lens';

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
