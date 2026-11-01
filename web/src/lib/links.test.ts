import { describe, expect, it } from 'vitest';
import { inAppLink } from './links';

describe('inAppLink', () => {
  const cases: [string | undefined, string | undefined][] = [
    ['/stacks/web', '/stacks/web'],
    ['//evil.example', undefined],
    ['javascript:alert(1)', undefined],
    ['https://evil.example', undefined],
    ['/\\evil.example', undefined],
    ['', undefined],
    [undefined, undefined]
  ];
  for (const [inp, want] of cases) it(String(inp), () => expect(inAppLink(inp)).toBe(want));
});
