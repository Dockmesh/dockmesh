import { describe, expect, it } from 'vitest';
import type { Operation, OperationStep } from '$lib/api';
import { currentStep, isActive, kindLabel, overallProgress } from './operationsProgress';

function op(steps: Partial<OperationStep>[], status: Operation['status'] = 'running'): Operation {
  return {
    id: 'x',
    kind: 'backup.run',
    target: 'nightly',
    status,
    created_at: '2026-11-01T00:00:00Z',
    steps: steps.map((s, i) => ({ key: `s${i}`, label: `Step ${i}`, status: 'pending', ...s }))
  };
}

describe('overallProgress', () => {
  it('counts finished and skipped steps plus the running share', () => {
    const o = op([{ status: 'succeeded' }, { status: 'skipped' }, { status: 'running', progress: 0.5 }, {}]);
    expect(overallProgress(o)).toBeCloseTo(2.5 / 4);
  });
  it('does not guess for a running step without progress', () => {
    expect(overallProgress(op([{ status: 'running' }, {}]))).toBe(0);
  });
  it('is null without steps', () => {
    expect(overallProgress(op([]))).toBeNull();
  });
});

describe('currentStep', () => {
  it('prefers the running step', () => {
    expect(currentStep(op([{ status: 'succeeded' }, { status: 'running' }]))?.key).toBe('s1');
  });
  it('falls back to the failed step', () => {
    expect(currentStep(op([{ status: 'succeeded' }, { status: 'failed' }, { status: 'skipped' }], 'failed'))?.key).toBe('s1');
  });
});

describe('labels and state', () => {
  it('names known kinds and passes unknown ones through', () => {
    expect(kindLabel('image.update')).toBe('Image update');
    expect(kindLabel('other.kind')).toBe('other.kind');
  });
  it('treats queued and running as active', () => {
    expect(isActive({ status: 'queued' })).toBe(true);
    expect(isActive({ status: 'running' })).toBe(true);
    expect(isActive({ status: 'cancelled' })).toBe(false);
  });
});
