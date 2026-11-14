import { m } from '$lib/paraglide/messages.js';
import type { PathRoute } from './_site';
import { REDIRECT_CODES, toTLSForm, toTLSPayload, type HeaderRow, type RedirectCode, type TLSForm } from './_options';

// Editable form of one path route. Upstreams are one per line like the
// site's extra upstreams; header rows keep their write-only secrets.

export const MAX_PATHS = 50;

export interface PathForm {
  type: 'prefix' | 'exact';
  path: string;
  action: 'proxy' | 'redirect' | 'respond';
  backend: 'site' | 'static';
  upstreams: string;
  lbPolicy: string;
  healthPath: string;
  tls: TLSForm;
  stripPrefix: boolean;
  requestSet: HeaderRow[];
  requestRemove: string;
  responseSet: HeaderRow[];
  responseRemove: string;
  dialTimeout: string;
  readTimeout: string;
  writeTimeout: string;
  streamCloseDelay: string;
  flush: '' | 'on' | 'off';
  buffer: '' | 'on' | 'off';
  to: string;
  code: RedirectCode;
  status: 403 | 404;
}

const lines = (l?: string[] | null) => (l ?? []).join('\n');
const fromLines = (s: string) => [...new Set(s.split(/[\n,]/).map((x) => x.trim()).filter(Boolean))];
const tri = (b?: boolean | null): '' | 'on' | 'off' => (b == null ? '' : b ? 'on' : 'off');
const fromTri = (t: '' | 'on' | 'off') => (t === '' ? null : t === 'on');

function rows(l?: { name: string; value?: string; secret?: boolean; value_set?: boolean }[]): HeaderRow[] {
  return (l ?? []).map((h) => ({ name: h.name, value: h.value ?? '', secret: !!h.secret, valueSet: !!h.value_set }));
}

export function newPathForm(): PathForm {
  return toPathForm({ match: { type: 'prefix', path: '' }, action: 'proxy', target: { kind: 'site' } });
}

export function toPathForm(r: PathRoute): PathForm {
  const t = r.target ?? {};
  const tr = r.transport ?? {};
  const h = r.headers ?? {};
  return {
    type: r.match.type === 'exact' ? 'exact' : 'prefix',
    path: r.match.path,
    action: r.action === 'redirect' || r.action === 'respond' ? r.action : 'proxy',
    backend: t.kind === 'static' ? 'static' : 'site',
    upstreams: lines(t.upstreams),
    lbPolicy: t.lb_policy ?? '',
    healthPath: t.health_path ?? '',
    tls: toTLSForm(t.tls),
    stripPrefix: !!r.strip_prefix,
    requestSet: rows(h.request_set),
    requestRemove: lines(h.request_remove),
    responseSet: rows(h.response_set),
    responseRemove: lines(h.response_remove),
    dialTimeout: tr.dial_timeout ?? '',
    readTimeout: tr.read_timeout ?? '',
    writeTimeout: tr.write_timeout ?? '',
    streamCloseDelay: tr.stream_close_delay ?? '',
    flush: tri(tr.flush_immediately),
    buffer: tri(tr.buffer_requests),
    to: r.to ?? '',
    code: (REDIRECT_CODES as readonly number[]).includes(r.code ?? 0) ? (r.code as RedirectCode) : 301,
    status: r.status === 403 ? 403 : 404
  };
}

// cleanPath is what the server stores: a leading / and no trailing one.
export function cleanPath(p: string): string {
  let s = p.trim();
  if (s && !s.startsWith('/')) s = '/' + s;
  while (s.length > 1 && s.endsWith('/')) s = s.slice(0, -1);
  return s;
}

export function toPathPayload(f: PathForm): PathRoute {
  const out: PathRoute = { match: { type: f.type, path: cleanPath(f.path) }, action: f.action };
  if (f.action === 'redirect') return { ...out, to: f.to.trim(), code: f.code };
  if (f.action === 'respond') return { ...out, status: f.status };
  const sendRows = (rs: HeaderRow[]) =>
    rs.filter((r) => r.name.trim()).map((r) => ({ name: r.name.trim(), value: r.value, ...(r.secret ? { secret: true } : {}) }));
  return {
    ...out,
    target:
      f.backend === 'static'
        ? { kind: 'static', upstreams: fromLines(f.upstreams), lb_policy: f.lbPolicy, health_path: f.healthPath.trim(), tls: toTLSPayload(f.tls) }
        : { kind: 'site' },
    strip_prefix: f.type === 'prefix' && f.stripPrefix,
    headers: {
      request_set: sendRows(f.requestSet),
      request_remove: fromLines(f.requestRemove),
      response_set: sendRows(f.responseSet),
      response_remove: fromLines(f.responseRemove)
    },
    transport: {
      dial_timeout: f.dialTimeout.trim(),
      read_timeout: f.readTimeout.trim(),
      write_timeout: f.writeTimeout.trim(),
      stream_close_delay: f.streamCloseDelay.trim(),
      flush_immediately: fromTri(f.flush),
      buffer_requests: fromTri(f.buffer)
    }
  };
}

// sortForms orders the forms like the server stores them, so the table
// shows the order Caddy applies before saving.
export function sortForms<T extends { form: PathForm }>(list: T[]): T[] {
  return [...list].sort((a, b) => {
    const pa = cleanPath(a.form.path), pb = cleanPath(b.form.path);
    if ((a.form.type === 'exact') !== (b.form.type === 'exact')) return a.form.type === 'exact' ? -1 : 1;
    if (pa.length !== pb.length) return pb.length - pa.length;
    return pa < pb ? -1 : pa > pb ? 1 : 0;
  });
}

// pathError is the client-side check of a path, the same rules as the
// server; the server's answer still decides.
export function pathError(f: PathForm, all: PathForm[]): string {
  const p = cleanPath(f.path);
  if (!p || !p.startsWith('/')) return m.proxy_path_err_slash();
  if (p === '/') return m.proxy_path_err_root();
  if (/[*{}[\]\\]|\/\.\.?(\/|$)/.test(p)) return m.proxy_path_err_chars();
  if (p.length > 256) return m.proxy_path_err_long();
  const dup = all.filter((o) => o.type === f.type && cleanPath(o.path).toLowerCase() === p.toLowerCase());
  if (dup.length > 1) return m.proxy_path_err_dup();
  return '';
}

export function actionLabel(f: PathForm, siteUpstream: string): string {
  switch (f.action) {
    case 'redirect':
      return m.proxy_path_act_redirect({ code: f.code, to: f.to });
    case 'respond':
      return m.proxy_path_act_respond({ status: f.status });
  }
  if (f.backend === 'site') return m.proxy_path_act_site();
  const ups = fromLines(f.upstreams);
  const first = ups[0] ?? siteUpstream;
  return ups.length > 1 ? m.proxy_path_act_proxy_more({ to: first, n: ups.length - 1 }) : m.proxy_path_act_proxy({ to: first });
}

export interface Chip { text: string; warn?: boolean }

export function detailChips(f: PathForm): Chip[] {
  if (f.action !== 'proxy') return [];
  const out: Chip[] = [];
  if (f.type === 'prefix' && f.stripPrefix) out.push({ text: m.proxy_path_chip_strip() });
  const rq = f.requestSet.filter((r) => r.name.trim()).length + fromLines(f.requestRemove).length;
  const rs = f.responseSet.filter((r) => r.name.trim()).length + fromLines(f.responseRemove).length;
  if (rq) out.push({ text: m.proxy_path_chip_req({ n: rq }) });
  if (rs) out.push({ text: m.proxy_path_chip_resp({ n: rs }) });
  if (f.dialTimeout || f.readTimeout || f.writeTimeout || f.streamCloseDelay || f.flush || f.buffer) out.push({ text: m.proxy_path_chip_timeouts() });
  if (f.backend === 'static' && f.tls.tls) {
    out.push({ text: 'TLS' });
    if (f.tls.check === 'skip') out.push({ text: m.proxy_path_chip_skip(), warn: true });
  }
  return out;
}

// parsePathError maps a server error "paths[<i>].<field>: <msg>" to the index
// in the stored (sorted) list and the field.
export function parsePathError(msg: string): { index: number; field: string; message: string } | null {
  const mt = /paths\[(\d+)\]\.([a-z_.]+): (.+)$/s.exec(msg);
  return mt ? { index: Number(mt[1]), field: mt[2], message: mt[3] } : null;
}
