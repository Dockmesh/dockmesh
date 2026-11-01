import { describe, expect, it } from 'vitest';
import { fmtVersion, semverBump } from './version';

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

describe('semverBump', () => {
  it('names the first differing part', () => {
    expect(semverBump('1.2.3', '2.0.0')).toBe('major');
    expect(semverBump('1.2.3', '1.3.0')).toBe('minor');
    expect(semverBump('1.2.3', '1.2.4')).toBe('patch');
  });
  it('is null for equal or non-version input', () => {
    expect(semverBump('1.2.3', '1.2.3')).toBeNull();
    expect(semverBump('latest', '1.2.3')).toBeNull();
  });
});
