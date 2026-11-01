import { describe, expect, it } from 'vitest';
import { toForm, toPayload, changedGroups, isDefault, parseFieldError, groupOfField, warnings, GROUPS } from './_options';
import type { SiteOptions } from './_site';

// What the server returns for a site with every group set; secrets come
// back empty with their "set" flags.
const stored: SiteOptions = {
  routing: { extra_upstreams: ['app2:8080'], lb_policy: 'least_conn', health_path: '/health', www_redirect: true },
  https: { allow_http: false, hsts: true },
  performance: {
    flush_immediately: true,
    buffer_requests: false,
    dial_timeout: '10s',
    read_timeout: '1h',
    write_timeout: '',
    max_body_bytes: 2 << 30,
    stream_close_delay: '5m',
    compression: true
  },
  access: {
    basic_auth: [{ username: 'marcel' }],
    ip_mode: 'allow',
    ip_ranges: ['192.168.10.0/24'],
    forward_auth: { url: 'http://authelia:9091/api/verify', copy_headers: ['Remote-User'], query_set: true }
  },
  headers: {
    security: true,
    request_set: [{ name: 'Authorization', value: '', secret: true, value_set: true }, { name: 'X-Plain', value: 'v' }],
    request_remove: ['X-Debug'],
    response_set: [],
    response_remove: ['Server']
  },
  logs: { access_log: true }
};

describe('round trip', () => {
  it('sends back what it got, without inventing secrets', () => {
    const p = toPayload(toForm(stored));
    expect(p.routing).toEqual(stored.routing);
    expect(p.https).toEqual(stored.https);
    expect(p.performance).toEqual(stored.performance);
    expect(p.logs).toEqual(stored.logs);
    expect(p.access?.basic_auth).toEqual([{ username: 'marcel' }]);
    expect(p.access?.forward_auth).toEqual({ url: 'http://authelia:9091/api/verify', copy_headers: ['Remote-User'] });
    expect(p.headers?.request_set).toEqual([{ name: 'Authorization', value: '', secret: true }, { name: 'X-Plain', value: 'v' }]);
    expect(changedGroups(toForm(stored), toForm(stored))).toEqual([]);
  });

  it('treats a missing options object as Caddy defaults', () => {
    for (const g of GROUPS) expect(isDefault(toForm(null), g.key)).toBe(true);
    for (const g of GROUPS) expect(isDefault(toForm(stored), g.key)).toBe(false);
  });
});

describe('each group', () => {
  it('routing: one upstream per line or comma, deduplicated', () => {
    const f = toForm(null);
    f.routing.extraUpstreams = ' a:1\nb:2, a:1 \n\n';
    expect(toPayload(f).routing?.extra_upstreams).toEqual(['a:1', 'b:2']);
    expect(changedGroups(f, toForm(null))).toEqual(['routing']);
  });

  it('https: flags pass through', () => {
    const f = toForm(null);
    f.https.allowHTTP = true;
    expect(toPayload(f).https).toEqual({ allow_http: true, hsts: false });
  });

  it('performance: sizes in MB and GB become bytes, garbage is sent as invalid', () => {
    const f = toForm(null);
    f.performance.maxBody = '1.5';
    f.performance.maxBodyUnit = 'GB';
    expect(toPayload(f).performance?.max_body_bytes).toBe(1.5 * 2 ** 30);
    f.performance.maxBody = '';
    expect(toPayload(f).performance?.max_body_bytes).toBe(0);
    f.performance.maxBody = 'lots';
    expect(toPayload(f).performance?.max_body_bytes).toBe(-1);
    expect(toForm({ performance: { max_body_bytes: 300 * 2 ** 20 } }).performance).toMatchObject({ maxBody: '300', maxBodyUnit: 'MB' });
    expect(toForm({ performance: { max_body_bytes: 1000 } }).performance).toMatchObject({ maxBody: '1000', maxBodyUnit: 'B' });
  });

  it('access: a stored user without a typed password keeps it, a typed one is sent', () => {
    const f = toForm(stored);
    f.access.users.push({ username: 'new', password: 'password-1', stored: false }, { username: ' ', password: '', stored: false });
    expect(toPayload(f).access?.basic_auth).toEqual([{ username: 'marcel' }, { username: 'new', password: 'password-1' }]);
    expect(changedGroups(f, toForm(stored))).toEqual(['access']);
  });

  it('access: forward auth off sends null, clearing the query is explicit', () => {
    const f = toForm(stored);
    f.access.clearQuery = true;
    expect(toPayload(f).access?.forward_auth).toMatchObject({ clear_query: true });
    f.access.forwardAuth = false;
    expect(toPayload(f).access?.forward_auth).toBeNull();
  });

  it('headers: empty rows are dropped, a typed secret counts as a change', () => {
    const f = toForm(stored);
    f.headers.responseSet.push({ name: '  ', value: 'x', secret: false, valueSet: false });
    expect(changedGroups(f, toForm(stored))).toEqual([]);
    f.headers.requestSet[0].value = 'Bearer new';
    expect(changedGroups(f, toForm(stored))).toEqual(['headers']);
    expect(toPayload(f).headers?.request_set?.[0]).toEqual({ name: 'Authorization', value: 'Bearer new', secret: true });
  });

  it('logs: access log flag', () => {
    const f = toForm(null);
    f.logs.accessLog = true;
    expect(changedGroups(f, toForm(null))).toEqual(['logs']);
  });
});

describe('server errors', () => {
  it.each([
    ['options.performance.read_timeout: must be a duration from 1s to 168h, like 30s, 5m or 1h', 'performance.read_timeout'],
    ['invalid proxy options: options.access.forward_auth.url: must be an http(s) URL', 'access.forward_auth.url'],
    ['the www. name of this host is a site of its own', 'routing.www_redirect']
  ])('%s → %s', (msg, field) => {
    expect(parseFieldError(msg)?.field).toBe(field);
  });

  it('ignores other errors and maps fields to groups', () => {
    expect(parseFieldError('host already has a route')).toBeNull();
    expect(groupOfField('access.forward_auth.url')).toBe('access');
    expect(groupOfField('nope.x')).toBeNull();
  });
});

describe('warnings', () => {
  it('marks risky settings with their consequence', () => {
    const f = toForm(null);
    expect(warnings(f, 'auto')).toEqual([]);
    f.https.allowHTTP = true;
    f.access.ipMode = 'allow';
    f.access.users.push({ username: 'u', password: '', stored: false });
    const fields = warnings(f, 'auto').map((w) => w.field);
    expect(fields).toEqual(['https.allow_http', 'access.ip_mode', 'access.basic_auth']);
  });

  it('warns about passwords on an HTTP-only site', () => {
    const f = toForm(null);
    f.access.users.push({ username: 'u', password: '', stored: false });
    expect(warnings(f, 'none').map((w) => w.field)).toEqual(['access.basic_auth']);
  });
});
