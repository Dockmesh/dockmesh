import { test } from 'node:test';
import assert from 'node:assert/strict';
import { stepValid, type TargetType } from '../src/lib/backupTargetForm.ts';

const filled: Record<TargetType, Record<string, string>> = {
  local: { path: '/srv/backups' },
  sftp: { host: 'h', username: 'u' },
  smb: { host: 'h', share: 's' },
  webdav: { url: 'http://h', username: 'u' },
  s3: { endpoint: 'e', bucket: 'b', access_key: 'k' }
};

for (const type of Object.keys(filled) as TargetType[]) {
  test(`${type}: connection step is invalid until its required fields are set`, () => {
    assert.equal(stepValid(1, 'n', type, {}), false);
    assert.equal(stepValid(1, 'n', type, filled[type]), true);
  });
}

test('name step needs a non-blank name', () => {
  assert.equal(stepValid(0, '   ', 'local', {}), false);
  assert.equal(stepValid(0, 'x', 'local', {}), true);
});

test('test step is always valid', () => {
  assert.equal(stepValid(2, '', 'local', {}), true);
});
