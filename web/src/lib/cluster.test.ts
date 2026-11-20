import { describe, expect, it } from 'vitest';
import { heaviest, servicesByStack, spreadLine, taskProblems, RESTART_WINDOW_MS } from './cluster';
import type { ClusterDistribution, SwarmTask } from './api';

const now = Date.parse('2026-11-20T12:00:00Z');
const ago = (min: number) => new Date(now - min * 60_000).toISOString();
let seq = 0;
function task(p: Partial<SwarmTask>): SwarmTask {
  seq++;
  return { id: `t${seq}`, service_id: 'web', desired_state: 'running', state: 'running', updated_at: ago(1), ...p };
}

describe('taskProblems', () => {
  it('marks the newest task of a failed slot', () => {
    const failed = task({ slot: 1, state: 'failed', desired_state: 'running', updated_at: ago(1), error: 'exit 1' });
    const got = taskProblems([task({ slot: 1, state: 'shutdown', desired_state: 'shutdown', updated_at: ago(90) }), failed], now);
    expect([...got.entries()]).toEqual([[failed.id, { kind: 'failed', failures: 1, lastError: 'exit 1' }]]);
  });
  it('marks a slot that failed twice in the window, even while it runs again', () => {
    const running = task({ slot: 2, updated_at: ago(1) });
    const got = taskProblems([running, task({ slot: 2, state: 'failed', updated_at: ago(5), error: 'task: non-zero exit (1)' }), task({ slot: 2, state: 'rejected', updated_at: ago(30) })], now);
    expect(got.get(running.id)).toEqual({ kind: 'restarting', failures: 2, lastError: 'task: non-zero exit (1)' });
  });
  it('ignores failures older than the window and updates that shut tasks down', () => {
    const old = ago(RESTART_WINDOW_MS / 60_000 + 5);
    const tasks = [
      task({ slot: 3, updated_at: ago(1) }),
      task({ slot: 3, state: 'failed', updated_at: ago(10) }),
      task({ slot: 3, state: 'failed', updated_at: old }),
      task({ slot: 4, updated_at: ago(1) }),
      task({ slot: 4, state: 'shutdown', desired_state: 'shutdown', updated_at: ago(2) }),
      task({ slot: 4, state: 'shutdown', desired_state: 'shutdown', updated_at: ago(3) })
    ];
    expect(taskProblems(tasks, now).size).toBe(0);
  });
  it('keys global tasks by node', () => {
    const a = task({ service_id: 'agent', node_id: 'n1', state: 'failed' });
    const b = task({ service_id: 'agent', node_id: 'n2' });
    const got = taskProblems([a, b], now);
    expect([...got.keys()]).toEqual([a.id]);
  });
  it('does not flag a failed task the scheduler already wants shut down', () => {
    expect(taskProblems([task({ slot: 5, state: 'failed', desired_state: 'shutdown' })], now).size).toBe(0);
  });
});

describe('servicesByStack', () => {
  it('groups by stack, stacks by name, loose services last', () => {
    const got = servicesByStack([{ name: 'solo' }, { name: 'shop_web', stack: 'shop' }, { name: 'mon_graf', stack: 'mon' }, { name: 'shop_api', stack: 'shop' }]);
    expect(got.map((g) => [g.stack, g.services.map((s) => s.name)])).toEqual([
      ['mon', ['mon_graf']],
      ['shop', ['shop_api', 'shop_web']],
      ['', ['solo']]
    ]);
  });
});

describe('spread', () => {
  const d = (heavy: boolean[], tasks: number[], stale = false): ClusterDistribution => ({
    nodes: tasks.map((t, i) => ({ id: `n${i}`, hostname: `m${i}`, role: 'manager', tasks: t, counted: true, heavy: heavy[i] })),
    tasks: tasks.reduce((a, b) => a + b, 0),
    counted_nodes: tasks.length,
    fair_share: 0,
    stale
  });
  it('names the busiest heavy node', () => {
    const x = d([true, false, true], [12, 2, 14]);
    expect(heaviest(x)?.id).toBe('n2');
    expect(spreadLine(x)).toEqual({ text: 'Load spread: uneven · m2 runs 14 of 28 tasks', uneven: true });
  });
  it('says even when no node is heavy, nothing for no tasks or a stale list', () => {
    expect(spreadLine(d([false, false], [3, 3]))?.uneven).toBe(false);
    expect(spreadLine(d([false], [0]))).toBeNull();
    expect(spreadLine(d([true, false], [9, 1], true))).toBeNull();
    expect(heaviest(d([true, false], [9, 1], true))).toBeNull();
  });
});
