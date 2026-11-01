import { describe, expect, it } from 'vitest';
import { auditTargetHref, csvLine } from './_links';

describe('auditTargetHref', () => {
  const cases: [string, string | undefined, string | undefined][] = [
    ['stack.deploy', 'immich', '/stacks/immich'],
    ['stack.delete', 'immich', undefined],
    ['stack.create', 'backup job 3', undefined],
    ['container.update', 'web-1', '/containers/web-1'],
    ['volume.remove', 'data', undefined],
    ['proxy.route_create', 'a.example.com', '/proxy/a.example.com'],
    ['proxy.route_update', '12', undefined],
    ['user.update', 'u-1', undefined],
    ['image.pull', 'nginx:1.27', undefined],
    ['stack.deploy', undefined, undefined]
  ];
  for (const [action, target, want] of cases) {
    it(`${action} ${target}`, () => expect(auditTargetHref(action, target)).toBe(want));
  }
});

describe('csvLine', () => {
  it('quotes cells, doubles quotes and defuses formulas', () => {
    expect(csvLine(['a', 'say "hi"', '=SUM(A1)', 3, undefined])).toBe('"a","say ""hi""","\'=SUM(A1)","3",""');
  });
});
