import { describe, expect, it } from 'vitest';
import { actionPhrase, auditSentence, targetLabel } from './auditText';

describe('actionPhrase', () => {
  const cases: [string, string][] = [
    ['stack.create', 'created stack'],
    ['backup.job_create', 'created backup job'],
    ['container.pause', 'paused container'],
    ['proxy.route_enabled', 'turned on proxy site'],
    ['system.update_check', 'checked for updates'],
    ['legacy.some_thing', 'legacy some thing']
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

describe('auditSentence', () => {
  const text = (parts: { text: string; strong: boolean }[]) => parts.map((p) => (p.strong ? `[${p.text}]` : p.text)).join('');
  const cases: [string, string, string, string][] = [
    ['stack.create', 'admin', 'r42-new', '[admin] created stack [r42-new]'],
    ['user.password', 'admin', 'anna', '[admin] changed the password of [anna]'],
    ['audit.retention_run', 'System', 'days', '[System] ran audit retention ([days])'],
    ['image.prune', 'admin', '', '[admin] pruned images'],
    ['network.remove', 'admin', '', '[admin] removed network'],
    ['legacy.some_thing', 'admin', 'x', '[admin] legacy some thing [x]']
  ];
  for (const [action, actor, target, want] of cases) {
    it(action, () => expect(text(auditSentence(action, actor, target))).toBe(want));
  }
});
