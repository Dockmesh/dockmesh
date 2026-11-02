import { describe, expect, it } from 'vitest';
import { HOOK_PRESETS, hookToRow, inlineTargetConfig, rowToHook, rowsToHooks } from './backupHooks';

describe('backup hooks', () => {
  it('runs edited and new commands through sh -c', () => {
    expect(rowToHook({ container: 'db-1', command: 'mysqldump -u root > /var/lib/mysql/d.sql' })).toEqual({
      container: 'db-1',
      cmd: ['sh', '-c', 'mysqldump -u root > /var/lib/mysql/d.sql']
    });
  });

  it('keeps an untouched stored hook exactly as it was', () => {
    const argv = { container: 'app', cmd: ['/usr/local/bin/flush', '--path', 'a b'] };
    expect(rowToHook(hookToRow(argv))).toEqual(argv);
    const shell = { container: 'app', cmd: ['sh', '-c', 'echo hi > /tmp/x'] };
    expect(hookToRow(shell).command).toBe('echo hi > /tmp/x');
    expect(rowToHook(hookToRow(shell))).toEqual(shell);
  });

  it('drops rows without a container or command', () => {
    expect(rowsToHooks([{ container: '', command: 'x' }, { container: 'a', command: ' ' }, { container: 'a', command: 'b' }])).toEqual([
      { container: 'a', cmd: ['sh', '-c', 'b'] }
    ]);
  });

  it('presets never dump to /tmp', () => {
    for (const p of HOOK_PRESETS) expect(p.command).not.toContain('/tmp');
  });

  it('sends ports as numbers and use_ssl as a boolean', () => {
    expect(inlineTargetConfig('sftp', { host: 'h', port: '2222' })).toEqual({ host: 'h', port: 2222 });
    expect(inlineTargetConfig('sftp', { host: 'h', port: '' })).toEqual({ host: 'h' });
    expect(inlineTargetConfig('s3', { bucket: 'b' })).toEqual({ bucket: 'b', use_ssl: false });
  });
});
