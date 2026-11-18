import { describe, expect, it } from 'vitest';
import {
  agentURLError,
  defaultTrustedProxies,
  stacksOutside,
  trustedProxiesError,
  agentURL,
  baseURLError,
  codeFormatOK,
  codeFromFragment,
  emailError,
  fingerprintLines,
  generatePassword,
  httpAddr,
  normalizeCode,
  passwordError,
  railClickable,
  usernameError
} from './setupWizard';

describe('codeFromFragment', () => {
  const cases: [string, string][] = [
    ['#K7QD-3MXA-9PRT', 'K7QD-3MXA-9PRT'],
    ['#k7qd3mxa9prt', 'K7QD3MXA9PRT'],
    ['#K7QD%203MXA%209PRT', 'K7QD 3MXA 9PRT'],
    ['', ''],
    ['#', ''],
    ['#<img src=x onerror=alert(1)>', ''],
    ['#%3Cscript%3E', ''],
    ['#%E0%A4%A', ''],
    ['#' + 'A'.repeat(40), ''],
    ['#javascript:alert(1)', '']
  ];
  for (const [hash, want] of cases) it(JSON.stringify(hash), () => expect(codeFromFragment(hash)).toBe(want));
});

describe('setup code format', () => {
  it('accepts lower case, spaces and no dashes', () => {
    expect(normalizeCode(' k7qd 3mxa-9prt ')).toBe('K7QD3MXA9PRT');
    expect(codeFormatOK('k7qd3mxa9prt')).toBe(true);
    expect(codeFormatOK('K7QD-3MXA-9PR')).toBe(false);
    expect(codeFormatOK('K7QD-3MXA-9PR!')).toBe(false);
  });
});

describe('admin fields', () => {
  it('username', () => {
    expect(usernameError('admin')).toBeNull();
    expect(usernameError('Ad')).toBe('username');
    expect(usernameError('a'.repeat(33))).toBe('username');
    expect(usernameError('ops.team-1_x')).toBeNull();
  });
  it('email', () => {
    expect(emailError('')).toBeNull();
    expect(emailError('you@example.com')).toBeNull();
    expect(emailError('marcel@')).toBe('email');
  });
  it('password', () => {
    expect(passwordError('short', 'admin', 12)).toBe('password_short');
    expect(passwordError('adminadmin12', 'adminadmin12', 12)).toBe('password_is_username');
    expect(passwordError('äöüäöüäöüäöü', 'admin', 12)).toBeNull();
  });
  it('generated passwords are 20 characters', () => {
    for (let i = 0; i < 20; i++) expect(generatePassword()).toHaveLength(20);
  });
});

describe('address', () => {
  it('base URL per TLS choice', () => {
    expect(baseURLError('https://10.0.0.5:8080', 'internal')).toBeNull();
    expect(baseURLError('http://10.0.0.5:8080', 'internal')).toBe('https_required');
    expect(baseURLError('http://dm.example.org', 'proxy')).toBe('proxy_url');
    expect(baseURLError('nonsense', 'file')).toBe('invalid');
  });
  it('agent URL', () => {
    expect(agentURL('https://dm.example.org', '8443')).toBe('wss://dm.example.org:8443/connect');
    expect(agentURL('https://[fd00::1]:8080', '8443')).toBe('wss://[fd00::1]:8443/connect');
  });
  it('listen address', () => {
    expect(httpAddr('internal', '127.0.0.1', '8080')).toBe(':8080');
    expect(httpAddr('proxy', '127.0.0.1', '8080')).toBe('127.0.0.1:8080');
    expect(httpAddr('proxy', '::1', '8080')).toBe('[::1]:8080');
  });
  it('fingerprint lines', () => {
    const fp = Array.from({ length: 32 }, (_, i) => i.toString(16).padStart(2, '0').toUpperCase()).join(':');
    const [a, b] = fingerprintLines(fp);
    expect(a.endsWith(':')).toBe(true);
    expect((a + b).replace(/:/g, '')).toHaveLength(64);
  });
});

describe('rail', () => {
  it('done steps link back, except the code and during apply', () => {
    expect(railClickable('N1', 'N3', 'new')).toBe(true);
    expect(railClickable('W1', 'N3', 'new')).toBe(false);
    expect(railClickable('N4', 'N3', 'new')).toBe(false);
    expect(railClickable('N1', 'N7', 'new')).toBe(false);
    expect(railClickable('R1', 'R2', 'restore')).toBe(true);
    expect(railClickable('R2', 'R3', 'restore')).toBe(false);
  });
});

describe('network fields', () => {
  it('checks agent URLs', () => {
    expect(agentURLError('wss://dm.lan:8443/connect')).toBe(false);
    expect(agentURLError('https://dm.lan/connect')).toBe(true);
    expect(agentURLError('wss://')).toBe(true);
    expect(agentURLError('nonsense')).toBe(true);
  });
  it('checks trusted proxies', () => {
    expect(trustedProxiesError('127.0.0.1/32')).toBeNull();
    expect(trustedProxiesError('127.0.0.1, 10.0.0.0/24;fd00::1')).toBeNull();
    expect(trustedProxiesError('')).toEqual({ kind: 'invalid' });
    expect(trustedProxiesError('proxy.lan')).toEqual({ kind: 'invalid' });
    expect(trustedProxiesError('10.0.0.0/33')).toEqual({ kind: 'invalid' });
    expect(trustedProxiesError('10.0.0.1,0.0.0.0/0')).toEqual({ kind: 'all', net: '0.0.0.0/0' });
    expect(trustedProxiesError('::/0')).toEqual({ kind: 'all', net: '::/0' });
    expect(trustedProxiesError('128.0.0.0/1')).toEqual({ kind: 'all', net: '128.0.0.0/1' });
    expect(trustedProxiesError('10.0.0.0/8')).toBeNull();
  });
  it('derives the default trusted proxy', () => {
    expect(defaultTrustedProxies('127.0.0.1')).toBe('127.0.0.1/32');
    expect(defaultTrustedProxies('::1')).toBe('::1/128');
    expect(defaultTrustedProxies('0.0.0.0')).toBe('');
  });
  it('tells a stacks dir outside the data dir', () => {
    expect(stacksOutside('/var/lib/dockmesh', '/var/lib/dockmesh/stacks')).toBe(false);
    expect(stacksOutside('/var/lib/dockmesh/', '/srv/stacks')).toBe(true);
    expect(stacksOutside('/var/lib/dockmesh', '/var/lib/dockmesh-stacks')).toBe(true);
  });
});
