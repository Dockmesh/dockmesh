import { describe, expect, it } from 'vitest';
import { cleanPath, newPathForm, pathError, sortForms, toPathForm, toPathPayload, parsePathError, detailChips } from './_paths';
import type { PathRoute } from './_site';

const stored: PathRoute = {
  match: { type: 'prefix', path: '/api' },
  action: 'proxy',
  target: { kind: 'static', upstreams: ['10.0.0.30:443', '10.0.0.31:443'], lb_policy: 'first', health_path: '/h', tls: { tls: true, server_name: '', host_header: '', ca_pem: '', skip_verify: true } },
  strip_prefix: true,
  headers: { request_set: [{ name: 'Authorization', value: '', secret: true, value_set: true }], request_remove: ['Cookie'], response_set: [], response_remove: ['Server'] },
  transport: { dial_timeout: '3s', read_timeout: '', write_timeout: '', stream_close_delay: '', flush_immediately: null, buffer_requests: true }
};

describe('path form', () => {
  it('sends back what it got, secrets empty', () => {
    expect(toPathPayload(toPathForm(stored))).toEqual({
      ...stored,
      headers: { ...stored.headers, request_set: [{ name: 'Authorization', value: '', secret: true }] }
    });
  });

  it('sends only the fields of the action', () => {
    const f = newPathForm();
    Object.assign(f, { path: 'old/', action: 'redirect', to: '/new', code: 308, stripPrefix: true });
    expect(toPathPayload(f)).toEqual({ match: { type: 'prefix', path: '/old' }, action: 'redirect', to: '/new', code: 308 });
    f.action = 'respond';
    expect(toPathPayload(f)).toEqual({ match: { type: 'prefix', path: '/old' }, action: 'respond', status: 404 });
  });

  it('cleans paths like the server', () => {
    expect(cleanPath(' ords// ')).toBe('/ords');
    expect(cleanPath('/')).toBe('/');
  });

  it('checks paths', () => {
    const a = { ...newPathForm(), path: '/ORDS' };
    const b = { ...newPathForm(), path: '/ords/' };
    expect(pathError(a, [a, b])).not.toBe('');
    expect(pathError({ ...a, path: '/' }, [])).not.toBe('');
    expect(pathError({ ...a, path: '/a*' }, [])).not.toBe('');
    expect(pathError({ ...a, path: '/[a]' }, [])).not.toBe('');
    expect(pathError({ ...a, path: '/a/../b' }, [])).not.toBe('');
    expect(pathError({ ...a, path: '/ok' }, [])).toBe('');
  });

  it('orders like Caddy applies the routes', () => {
    const mk = (type: 'prefix' | 'exact', path: string) => ({ form: { ...newPathForm(), type, path } });
    const got = sortForms([mk('prefix', '/a'), mk('prefix', '/ords/r'), mk('exact', '/zz'), mk('prefix', '/ords'), mk('exact', '/a')]);
    expect(got.map((r) => `${r.form.type} ${r.form.path}`)).toEqual(['exact /zz', 'exact /a', 'prefix /ords/r', 'prefix /ords', 'prefix /a']);
  });

  it('reads server field errors', () => {
    expect(parsePathError('invalid proxy options: paths[2].target.tls.ca_pem: bad')).toEqual({ index: 2, field: 'target.tls.ca_pem', message: 'bad' });
    expect(parsePathError('options.routing.lb_policy: x')).toBeNull();
  });

  it('marks skip verify', () => {
    expect(detailChips(toPathForm(stored)).some((c) => c.warn)).toBe(true);
  });
});
