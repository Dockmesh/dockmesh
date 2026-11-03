import { describe, expect, it, vi } from 'vitest';
import type { Operation } from '$lib/api';

const listed: Operation[] = [];
vi.mock('$lib/api', () => ({
  api: { operations: { list: vi.fn(async () => listed.slice()) } }
}));

const { operations } = await import('./operations.svelte');

const op = (id: string, status: Operation['status'], agoMs: number): Operation =>
  ({
    id,
    kind: 'backup.restore',
    ref: 'backup_run:7',
    status,
    created_at: new Date(Date.now() - agoMs).toISOString()
  }) as Operation;

describe('operations.find', () => {
  it('skips an earlier restore of the same run that is still in the time window', async () => {
    listed.push(op('old', 'failed', 20_000));
    const earlier = await operations.known('backup_run:7', 'backup.restore');
    const since = Date.now();
    listed.push(op('new', 'running', 0));
    const found = await operations.find('backup_run:7', 'backup.restore', since, 1000, earlier);
    expect(found?.id).toBe('new');
  });

  it('without the skip list the earlier one would match', async () => {
    const found = await operations.find('backup_run:7', 'backup.restore', Date.now(), 1000);
    expect(found).toBeDefined();
  });
});
