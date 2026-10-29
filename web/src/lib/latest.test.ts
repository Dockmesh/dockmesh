import { describe, expect, it } from 'vitest';
import { latest } from './latest';

describe('latest', () => {
  it('only the newest request stays current', () => {
    const next = latest();
    const first = next();
    expect(first()).toBe(true);
    const second = next();
    expect(first()).toBe(false);
    expect(second()).toBe(true);
  });
});
