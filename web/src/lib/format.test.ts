import { describe, expect, it } from 'vitest';
import { fmtAgo, fmtBytes } from './format';

describe('fmtAgo', () => {
  it('formats recent times', () => {
    const now = Date.now();
    expect(fmtAgo(now - 10_000)).toBe('just now');
    expect(fmtAgo(now - 5 * 60_000)).toBe('5 min ago');
    expect(fmtAgo(new Date(now - 3 * 3600_000).toISOString())).toBe('3 h ago');
    expect(fmtAgo(now - 2 * 86400_000)).toBe('2 d ago');
  });
  it('uses the empty text for missing values', () => {
    expect(fmtAgo(undefined)).toBe('—');
    expect(fmtAgo('', 'never')).toBe('never');
    expect(fmtAgo('not a date')).toBe('—');
  });
});

describe('fmtBytes', () => {
  it('uses SI units', () => {
    expect(fmtBytes(999)).toBe('999 B');
    expect(fmtBytes(1500)).toBe('1.5 kB');
    expect(fmtBytes(2_300_000_000)).toBe('2.3 GB');
  });
  it('uses the empty text for zero', () => {
    expect(fmtBytes(0)).toBe('0 B');
    expect(fmtBytes(0, '—')).toBe('—');
  });
});
