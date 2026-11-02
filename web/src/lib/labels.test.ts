import { describe, expect, it } from 'vitest';
import { alertSeverityLabel, containerStateLabel, hostStatusLabel, vulnSeverityLabel } from './labels';

describe('labels', () => {
  const cases: { fn: (v: string) => string; in: string; want: string }[] = [
    { fn: containerStateLabel, in: 'restarting', want: 'restarting' },
    { fn: containerStateLabel, in: 'zombie', want: 'zombie' },
    { fn: containerStateLabel, in: 'toString', want: 'toString' },
    { fn: alertSeverityLabel, in: 'critical', want: 'critical' },
    { fn: alertSeverityLabel, in: 'fatal', want: 'fatal' },
    { fn: vulnSeverityLabel, in: 'negligible', want: 'negligible' },
    { fn: hostStatusLabel, in: 'pending', want: 'pending' },
    { fn: hostStatusLabel, in: '', want: '' }
  ];
  it.each(cases)('$in', ({ fn, in: v, want }) => expect(fn(v)).toBe(want));
});
