import { describe, expect, it } from 'vitest';
import { inAppLink, releaseLink } from './links';

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

describe('releaseLink', () => {
  const cases: [string | undefined, string | undefined][] = [
    ['https://github.com/Dockmesh/dockmesh/releases/tag/v0.6.0', 'https://github.com/Dockmesh/dockmesh/releases/tag/v0.6.0'],
    ['https://github.com/Dockmesh/dockmesh/releases/tag/v0.6.0-rc.1', 'https://github.com/Dockmesh/dockmesh/releases/tag/v0.6.0-rc.1'],
    ['javascript:alert(1)', undefined],
    ['JavaScript://github.com/Dockmesh/dockmesh/releases/%0aalert(1)', undefined],
    ['http://github.com/Dockmesh/dockmesh/releases/tag/v0.6.0', undefined],
    ['https://github.com.evil.example/Dockmesh/dockmesh/releases/tag/v1', undefined],
    ['https://user@github.com/Dockmesh/dockmesh/releases/tag/v1', undefined],
    ['https://github.com/Dockmesh/dockmesh/releases/../../evil/repo', undefined],
    ['https://github.com/evil/dockmesh/releases/tag/v1', undefined],
    ['https://github.com/Dockmesh/dockmesh/pulls', undefined],
    ['/settings', undefined],
    ['', undefined],
    [undefined, undefined]
  ];
  for (const [inp, want] of cases) it(String(inp), () => expect(releaseLink(inp)).toBe(want));
});
