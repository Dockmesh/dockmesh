import { describe, expect, it } from 'vitest';
import catalogGo from '../../../internal/rbac/catalog.go?raw';
import { RBAC_CATALOG_TEXTS, rbacText } from './rbacText';

describe('rbacText', () => {
  it('has a message for every text of the server role catalog', () => {
    const texts = new Set<string>();
    for (const m of catalogGo.matchAll(/(?:Label|Warning|Description|Display):\s*"([^"]*)"/g)) texts.add(m[1]);
    for (const m of catalogGo.matchAll(/Items:\s*\[\]string\{([^}]*)\}/g)) {
      for (const s of m[1].matchAll(/"([^"]*)"/g)) texts.add(s[1]);
    }
    expect(texts.size).toBeGreaterThan(50);
    expect([...texts].filter((t) => !RBAC_CATALOG_TEXTS.includes(t))).toEqual([]);
  });

  const cases: { in: string | undefined; want: string }[] = [
    { in: 'Read logs', want: 'Read logs' },
    { in: 'A role someone typed', want: 'A role someone typed' },
    { in: undefined, want: '' },
    { in: '', want: '' }
  ];
  for (const c of cases) {
    it(`renders ${JSON.stringify(c.in)}`, () => {
      expect(rbacText(c.in)).toBe(c.want);
    });
  }
});
