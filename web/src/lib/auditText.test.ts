import { describe, expect, it } from 'vitest';
import { actionPhrase, targetLabel } from './auditText';

describe('actionPhrase', () => {
  const cases: [string, string][] = [
    ['stack.create', 'created stack'],
    ['backup.job_create', 'created backup job'],
    ['audit.retention_run', 'audit retention run']
  ];
  for (const [code, want] of cases) it(code, () => expect(actionPhrase(code)).toBe(want));
});

describe('targetLabel', () => {
  const names = {
    users: new Map([['bef3332d-1', 'anna']]),
    hosts: new Map([['03cc141a-2', 'dm-agent1']])
  };
  const cases: [string, string, string][] = [
    ['user.update', 'bef3332d-1', 'anna'],
    ['host.tags_set', '03cc141a-2', 'dm-agent1'],
    ['agent.delete', 'agent:03cc141a-2', 'dm-agent1'],
    ['backup.job_create', 'backup:nightly', 'nightly'],
    ['backup.download', 'backup-run:12', 'run 12'],
    ['user.update', 'unknown-id', 'unknown-id'],
    ['stack.deploy', 'web', 'web']
  ];
  for (const [action, target, want] of cases) {
    it(`${action} ${target}`, () => expect(targetLabel(action, target, names)).toBe(want));
  }
});
