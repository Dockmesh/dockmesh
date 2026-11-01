import { describe, expect, it } from 'vitest';
import type { Operation, OperationStep } from '$lib/api';
import { currentStep, elapsed, failedStep, hasWarning, isActive, kindLabel, opRef, overallProgress, shortMessage } from './operationsProgress';

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

describe('step predicates', () => {
  it('finds warnings and the failed step', () => {
    expect(hasWarning(op([{ status: 'succeeded' }, { status: 'warning' }]))).toBe(true);
    expect(hasWarning(op([{ status: 'succeeded' }]))).toBe(false);
    expect(failedStep(op([{ status: 'succeeded' }, { status: 'failed' }], 'failed'))?.key).toBe('s1');
    expect(failedStep(op([{ status: 'succeeded' }]))).toBeUndefined();
  });
});

describe('opRef', () => {
  it('builds the refs the server uses', () => {
    expect(opRef.backupRun(42)).toBe('backup-run:42');
    expect(opRef.container('abc')).toBe('container:abc');
    expect(opRef.proxySite('a.example.com')).toBe('proxy-site:a.example.com');
  });
});

describe('elapsed', () => {
  it('formats seconds and minutes', () => {
    expect(elapsed('2026-11-01T00:00:00Z', '2026-11-01T00:00:12Z')).toBe('12 s');
    expect(elapsed('2026-11-01T00:00:00Z', '2026-11-01T00:03:04Z')).toBe('3 min 4 s');
  });
  it('is empty without a start', () => {
    expect(elapsed(undefined)).toBe('');
  });
});

describe('shortMessage', () => {
  const acme = 'Caddy could not get a certificate yet and keeps retrying: could not get certificate from issuer: HTTP 400 urn:ietf:params:acme:error:rejectedIdentifier - Error creating new order :: Cannot issue for "x.example.com": forbidden by policy';
  it.each([
    ['short text stays', 'Pull failed: rate limit', 'Pull failed: rate limit'],
    ['raw error after the first colon is dropped', acme, 'Caddy could not get a certificate yet and keeps retrying.'],
    ['long text without a sentence prefix is cut', 'x'.repeat(200), 'x'.repeat(139) + '…'],
    ['empty', '', '']
  ])('%s', (_n, input, want) => {
    expect(shortMessage(input)).toBe(want);
  });
});
