import { describe, expect, it } from 'vitest';
import type { Notice } from './api';
import { bannerNotice, bannerNotices, withoutBanner } from './notices';

const n = (id: number, severity: Notice['severity'], dismissed = false, blocking = severity === 'critical'): Notice => ({
  id,
  key: `k${id}`,
  severity,
  title: `n${id}`,
  created_at: `2026-11-01T00:00:0${id}Z`,
  blocking,
  dismissible: blocking || severity !== 'critical',
  dismissed
});

describe('bannerNotices', () => {
  it('keeps only blocking notices the user has not hidden, oldest first', () => {
    const list = [n(1, 'warning'), n(2, 'info'), n(3, 'critical'), n(4, 'critical', true), n(5, 'critical', false, false), n(6, 'critical')];
    expect(bannerNotices(list).map((x) => x.id)).toEqual([3, 6]);
    expect(bannerNotice(list)?.id).toBe(3);
  });
});

describe('withoutBanner', () => {
  const att = (id: string, kind: string, objId: string) => ({ id, kind, object: { id: objId } });
  it('drops notices the banner shows and keeps hidden or non-blocking ones', () => {
    const list = [n(1, 'critical'), n(2, 'warning'), n(4, 'critical', true)];
    const items = [att('notice:1', 'notice', '1'), att('notice:2', 'notice', '2'), att('notice:4', 'notice', '4'), att('host:a', 'host', '1')];
    expect(withoutBanner(items, list).map((x) => x.id)).toEqual(['notice:2', 'notice:4', 'host:a']);
  });
});
