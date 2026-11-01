import { describe, expect, it } from 'vitest';
import { fmtAgo, fmtBytes, fmtMem } from './format';

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

describe('byte formatters', () => {
  const cases: { fn: typeof fmtBytes; n: number | null | undefined; empty?: string; want: string }[] = [
    { fn: fmtBytes, n: 999, want: '999 B' },
    { fn: fmtBytes, n: 1000, want: '1.0 kB' },
    { fn: fmtBytes, n: 1500, want: '1.5 kB' },
    { fn: fmtBytes, n: 2_300_000_000, want: '2.3 GB' },
    { fn: fmtBytes, n: 5e15, want: '5.0 PB' },
    { fn: fmtBytes, n: 0, want: '0 B' },
    { fn: fmtBytes, n: 0, empty: '—', want: '—' },
    { fn: fmtBytes, n: null, want: '0 B' },
    { fn: fmtBytes, n: 0.5, want: '0 B' },
    { fn: fmtMem, n: 1023, want: '1023 B' },
    { fn: fmtMem, n: 1024, want: '1.0 KiB' },
    { fn: fmtMem, n: 1000, want: '1000 B' },
    { fn: fmtMem, n: 512 * 1024 ** 2, want: '512.0 MiB' },
    { fn: fmtMem, n: 3.8 * 1024 ** 3, want: '3.8 GiB' },
    { fn: fmtMem, n: 2 * 1024 ** 5, want: '2.0 PiB' },
    { fn: fmtMem, n: undefined, empty: '—', want: '—' },
    { fn: fmtMem, n: 0.2, want: '0 B' }
  ];
  for (const c of cases) {
    it(`${c.fn.name}(${c.n}) = ${c.want}`, () => {
      expect(c.empty === undefined ? c.fn(c.n) : c.fn(c.n, c.empty)).toBe(c.want);
    });
  }
});
