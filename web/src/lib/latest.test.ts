import { afterEach, describe, expect, it, vi } from 'vitest';
import { latest } from './latest';
import { autoRefresh } from './autorefresh';

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

describe('autoRefresh', () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it('skips ticks while the previous call is still pending', async () => {
    vi.useFakeTimers();
    let calls = 0;
    const stop = autoRefresh(() => {
      calls++;
      return new Promise<void>((r) => setTimeout(r, 2_500));
    }, 1_000);
    await vi.advanceTimersByTimeAsync(3_000);
    expect(calls).toBe(1);
    await vi.advanceTimersByTimeAsync(1_000);
    expect(calls).toBe(2);
    stop();
  });
});
