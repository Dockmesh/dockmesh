import { describe, expect, it } from 'vitest';
import type { NotificationItem, Operation } from './api';
import { badgeText, buildFeed, categoryOf } from './activity';

describe('categoryOf', () => {
  const cases: [string, string][] = [
    ['alert.fire', 'alerts'],
    ['alert_firing', 'alerts'],
    ['cert_expiring', 'alerts'],
    ['backup.fail', 'backups'],
    ['backup_failed', 'backups'],
    ['image.update', 'updates'],
    ['system.upgrade', 'updates'],
    ['agent.offline', 'hosts'],
    ['host_offline', 'hosts'],
    ['migration.failed', 'migrations'],
    ['deploy.ok', 'deploys'],
    ['notice', 'notices'],
    ['invite.accepted', 'other']
  ];
  for (const [kind, want] of cases) {
    it(kind, () => expect(categoryOf(kind)).toBe(want));
  }
});

describe('badgeText', () => {
  it('hides zero and caps at 9+', () => {
    expect(badgeText(0)).toBe('');
    expect(badgeText(3)).toBe('3');
    expect(badgeText(9)).toBe('9');
    expect(badgeText(85)).toBe('9+');
  });
});

describe('buildFeed', () => {
  it('merges notifications and finished operations, newest first', () => {
    const n: NotificationItem = { id: 1, kind: 'backup.fail', severity: 'error', title: 'Backup failed', created_at: '2026-11-01T10:00:00Z' };
    const done = { id: 'a', kind: 'image.update', target: 'web', status: 'succeeded', steps: [], created_at: '2026-11-01T10:59:00Z', finished_at: '2026-11-01T11:00:00Z' } as Operation;
    const running = { ...done, id: 'b', status: 'running', finished_at: undefined } as Operation;
    const feed = buildFeed([n], [done, running], []);
    expect(feed.map((e) => e.key)).toEqual(['o:a', 'n:1']);
    expect(feed[1]).toMatchObject({ category: 'backups', tone: 'bad', unread: true });
    expect(feed[0]).toMatchObject({ category: 'operations', tone: 'ok', unread: false });
  });
});
