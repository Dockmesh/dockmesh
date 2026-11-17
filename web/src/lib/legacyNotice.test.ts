import { describe, expect, it } from 'vitest';
import { LEGACY_NOTICE_HIDE_MS, hideLegacyNotice, legacyNoticeHidden } from './legacyNotice';

function memStore(): Pick<Storage, 'getItem' | 'setItem'> {
  const m = new Map<string, string>();
  return { getItem: (k) => m.get(k) ?? null, setItem: (k, v) => void m.set(k, v) };
}

describe('legacyNoticeHidden', () => {
  const t0 = Date.UTC(2026, 10, 1);
  const cases: { name: string; at: number; want: boolean }[] = [
    { name: 'just hidden', at: t0, want: true },
    { name: 'a day later', at: t0 + 24 * 3600 * 1000, want: true },
    { name: 'just before 30 days', at: t0 + LEGACY_NOTICE_HIDE_MS - 1, want: true },
    { name: 'after 30 days', at: t0 + LEGACY_NOTICE_HIDE_MS, want: false },
    { name: 'clock moved back', at: t0 - 1000, want: false }
  ];
  for (const c of cases) {
    it(c.name, () => {
      const s = memStore();
      hideLegacyNotice(s, 'u1', 'h1', t0);
      expect(legacyNoticeHidden(s, 'u1', 'h1', c.at)).toBe(c.want);
    });
  }

  it('is per user and per host', () => {
    const s = memStore();
    hideLegacyNotice(s, 'u1', 'h1', t0);
    expect(legacyNoticeHidden(s, 'u2', 'h1', t0)).toBe(false);
    expect(legacyNoticeHidden(s, 'u1', 'h2', t0)).toBe(false);
  });

  it('shows the notice without storage or with garbage in it', () => {
    expect(legacyNoticeHidden(undefined, 'u1', 'h1', t0)).toBe(false);
    const s = memStore();
    s.setItem('dm_legacy_layout_hidden:u1:h1', 'nope');
    expect(legacyNoticeHidden(s, 'u1', 'h1', t0)).toBe(false);
  });
});
