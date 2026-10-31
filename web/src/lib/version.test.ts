import { describe, expect, it } from 'vitest';
import { fmtVersion } from './version';

describe('fmtVersion', () => {
  it.each([
    ['v0.4.2', 'v0.4.2'],
    ['0.4.2', 'v0.4.2'],
    ['v0.4.2+7b7449d', 'v0.4.2+7b7449d'],
    ['dev', 'dev'],
    ['branch-feat/x', 'branch-feat/x'],
    ['', ''],
    [undefined, '']
  ])('%s → %s', (input, want) => {
    expect(fmtVersion(input)).toBe(want);
  });
});
