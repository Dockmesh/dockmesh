import { m } from '$lib/paraglide/messages.js';
import type { Fallback, SiteHeader, SiteOptions, TLSMode, UpstreamTLS } from './_site';

// Editable form of a site's options. Lists that are plain names or
// addresses are edited one per line; rows with a value of their own stay
// rows. Secrets are write-only: the server returns them empty with a
// "set" flag and keeps the stored value when they come back empty.

export interface UserRow { username: string; password: string; stored: boolean }
export interface HeaderRow { name: string; value: string; secret: boolean; valueSet: boolean }
export type SizeUnit = 'B' | 'MB' | 'GB';
export type CertCheck = 'public' | 'ca' | 'skip';
export interface TLSForm { tls: boolean; serverName: string; hostHeader: 'keep' | 'upstream'; check: CertCheck; caPEM: string }
export const REDIRECT_CODES = [301, 302, 307, 308] as const;
export type RedirectCode = (typeof REDIRECT_CODES)[number];
export interface FallbackForm { mode: 'proxy' | 'redirect' | '404'; to: string; code: RedirectCode }

export interface OptionsForm {
  routing: {
    extraUpstreams: string;
    lbPolicy: string;
    healthPath: string;
    wwwRedirect: boolean;
    upstreamTLS: TLSForm;
    fallback: FallbackForm;
  };
  https: { allowHTTP: boolean; hsts: boolean };
  performance: {
    flushImmediately: boolean;
    bufferRequests: boolean;
    dialTimeout: string;
    readTimeout: string;
    writeTimeout: string;
    maxBody: string;
    maxBodyUnit: SizeUnit;
    streamCloseDelay: string;
    compression: boolean;
  };
  access: {
    users: UserRow[];
    ipMode: '' | 'allow' | 'deny';
    ipRanges: string;
    forwardAuth: boolean;
    forwardAuthURL: string;
    copyHeaders: string;
    querySet: boolean;
    clearQuery: boolean;
  };
  headers: {
    security: boolean;
    forwardedPort: boolean;
    requestSet: HeaderRow[];
    requestRemove: string;
    responseSet: HeaderRow[];
    responseRemove: string;
  };
  logs: { accessLog: boolean };
}

export type GroupKey = keyof OptionsForm;

const MB = 1 << 20;
const GB = 1 << 30;
const UNIT: Record<SizeUnit, number> = { B: 1, MB, GB };

export const lines = (l?: string[] | null) => (l ?? []).join('\n');
export const fromLines = (s: string) => [...new Set(s.split(/[\n,]/).map((x) => x.trim()).filter(Boolean))];

export function headerRows(l?: SiteHeader[]): HeaderRow[] {
  return (l ?? []).map((h) => ({ name: h.name, value: h.value ?? '', secret: !!h.secret, valueSet: !!h.value_set }));
}

function sizeOf(bytes?: number): { maxBody: string; maxBodyUnit: SizeUnit } {
  if (!bytes) return { maxBody: '', maxBodyUnit: 'MB' };
  if (bytes % GB === 0) return { maxBody: String(bytes / GB), maxBodyUnit: 'GB' };
  if (bytes % MB === 0) return { maxBody: String(bytes / MB), maxBodyUnit: 'MB' };
  return { maxBody: String(bytes), maxBodyUnit: 'B' };
}

export function toTLSForm(t?: UpstreamTLS | null): TLSForm {
  return {
    tls: !!t?.tls,
    serverName: t?.server_name ?? '',
    hostHeader: t?.host_header === 'upstream' ? 'upstream' : 'keep',
    check: t?.skip_verify ? 'skip' : t?.ca_pem ? 'ca' : 'public',
    caPEM: t?.ca_pem ?? ''
  };
}

// toTLSPayload drops the HTTPS-only fields while HTTPS is off, so a
// switched-off block doesn't fail validation over leftovers.
export function toTLSPayload(f: TLSForm): UpstreamTLS {
  const on = f.tls;
  return {
    tls: on,
    server_name: on ? f.serverName.trim() : '',
    host_header: f.hostHeader === 'upstream' ? 'upstream' : '',
    ca_pem: on && f.check === 'ca' ? f.caPEM.trim() : '',
    skip_verify: on && f.check === 'skip'
  };
}

export function toFallbackForm(f?: Fallback | null): FallbackForm {
  const mode = f?.mode === 'redirect' || f?.mode === '404' ? f.mode : 'proxy';
  const code = (REDIRECT_CODES as readonly number[]).includes(f?.code ?? 0) ? (f!.code as RedirectCode) : 301;
  return { mode, to: f?.to ?? '', code };
}

export function toFallbackPayload(f: FallbackForm): Fallback {
  if (f.mode === 'redirect') return { mode: 'redirect', to: f.to.trim(), code: f.code };
  return { mode: f.mode === '404' ? '404' : '', to: '', code: 0 };
}

// pemCount is the number of certificate blocks in a pasted PEM.
export function pemCount(pem: string): number {
  return (pem.match(/-----BEGIN CERTIFICATE-----/g) ?? []).length;
}

export function toForm(o?: SiteOptions | null): OptionsForm {
  const r = o?.routing ?? {};
  const h = o?.https ?? {};
  const p = o?.performance ?? {};
  const a = o?.access ?? {};
  const hd = o?.headers ?? {};
  const fa = a.forward_auth ?? null;
  return {
    routing: {
      extraUpstreams: lines(r.extra_upstreams),
      lbPolicy: r.lb_policy ?? '',
      healthPath: r.health_path ?? '',
      wwwRedirect: !!r.www_redirect,
      upstreamTLS: toTLSForm(r.upstream_tls),
      fallback: toFallbackForm(r.fallback)
    },
    https: { allowHTTP: !!h.allow_http, hsts: !!h.hsts },
    performance: {
      flushImmediately: !!p.flush_immediately,
      bufferRequests: !!p.buffer_requests,
      dialTimeout: p.dial_timeout ?? '',
      readTimeout: p.read_timeout ?? '',
      writeTimeout: p.write_timeout ?? '',
      ...sizeOf(p.max_body_bytes),
      streamCloseDelay: p.stream_close_delay ?? '',
      compression: !!p.compression
    },
    access: {
      users: (a.basic_auth ?? []).map((u) => ({ username: u.username, password: '', stored: true })),
      ipMode: (a.ip_mode ?? '') as OptionsForm['access']['ipMode'],
      ipRanges: lines(a.ip_ranges),
      forwardAuth: !!fa,
      forwardAuthURL: fa?.url ?? '',
      copyHeaders: (fa?.copy_headers ?? []).join(', '),
      querySet: !!fa?.query_set,
      clearQuery: false
    },
    headers: {
      security: !!hd.security,
      forwardedPort: !!hd.forwarded_port,
      requestSet: headerRows(hd.request_set),
      requestRemove: lines(hd.request_remove),
      responseSet: headerRows(hd.response_set),
      responseRemove: lines(hd.response_remove)
    },
    logs: { accessLog: !!o?.logs?.access_log }
  };
}

// toPayload builds the options body for create/update. Empty rows are
// dropped; the server validates everything else.
export const sendRows = (rows: HeaderRow[]) =>
  rows.filter((r) => r.name.trim()).map((r) => ({ name: r.name.trim(), value: r.value, ...(r.secret ? { secret: true } : {}) }));

export function toPayload(f: OptionsForm): SiteOptions {
  const size = Number(f.performance.maxBody.trim() || '0');
  return {
    routing: {
      extra_upstreams: fromLines(f.routing.extraUpstreams),
      lb_policy: f.routing.lbPolicy,
      health_path: f.routing.healthPath.trim(),
      www_redirect: f.routing.wwwRedirect,
      upstream_tls: toTLSPayload(f.routing.upstreamTLS),
      fallback: toFallbackPayload(f.routing.fallback)
    },
    https: { allow_http: f.https.allowHTTP, hsts: f.https.hsts },
    performance: {
      flush_immediately: f.performance.flushImmediately,
      buffer_requests: f.performance.bufferRequests,
      dial_timeout: f.performance.dialTimeout.trim(),
      read_timeout: f.performance.readTimeout.trim(),
      write_timeout: f.performance.writeTimeout.trim(),
      max_body_bytes: Number.isFinite(size) ? Math.round(size * UNIT[f.performance.maxBodyUnit]) : -1,
      stream_close_delay: f.performance.streamCloseDelay.trim(),
      compression: f.performance.compression
    },
    access: {
      basic_auth: f.access.users
        .filter((u) => u.username.trim())
        .map((u) => ({ username: u.username.trim(), ...(u.password ? { password: u.password } : {}) })),
      ip_mode: f.access.ipMode,
      ip_ranges: fromLines(f.access.ipRanges),
      forward_auth: f.access.forwardAuth
        ? {
            url: f.access.forwardAuthURL.trim(),
            copy_headers: fromLines(f.access.copyHeaders),
            ...(f.access.clearQuery ? { clear_query: true } : {})
          }
        : null
    },
    headers: {
      security: f.headers.security,
      forwarded_port: f.headers.forwardedPort,
      request_set: sendRows(f.headers.requestSet),
      request_remove: fromLines(f.headers.requestRemove),
      response_set: sendRows(f.headers.responseSet),
      response_remove: fromLines(f.headers.responseRemove)
    },
    logs: { access_log: f.logs.accessLog }
  };
}

// changedGroups lists the groups whose payload differs between a and b.
// A typed password or secret value is part of the payload, so it counts.
export function changedGroups(a: OptionsForm, b: OptionsForm): GroupKey[] {
  const pa = toPayload(a) as Record<string, unknown>;
  const pb = toPayload(b) as Record<string, unknown>;
  return GROUPS.map((g) => g.key).filter((g) => JSON.stringify(pa[g]) !== JSON.stringify(pb[g]));
}

export function isDefault(f: OptionsForm, g: GroupKey): boolean {
  return changedGroups(f, toForm(null)).indexOf(g) < 0;
}

export interface FieldError { field: string; message: string }

// parseFieldError reads the field out of a server validation message
// ("options.performance.read_timeout: must be …").
export function parseFieldError(msg: string): FieldError | null {
  const mt = /options\.([a-z_]+(?:\.[a-z_]+)*): (.+)$/s.exec(msg);
  if (mt) return { field: mt[1], message: mt[2] };
  if (/www\. name/.test(msg)) return { field: 'routing.www_redirect', message: msg };
  return null;
}

export function groupOfField(field: string): GroupKey | null {
  const g = field.split('.')[0];
  return GROUPS.some((x) => x.key === g) ? (g as GroupKey) : null;
}

export interface GroupInfo { key: GroupKey; label: string; hint: string }

export const GROUPS: GroupInfo[] = [
  { key: 'routing', label: m.proxy_grp_routing(), hint: m.proxy_grp_routing_hint() },
  { key: 'https', label: 'HTTPS', hint: m.proxy_grp_https_hint() },
  { key: 'performance', label: m.proxy_grp_performance(), hint: m.proxy_grp_performance_hint() },
  { key: 'access', label: m.proxy_grp_access(), hint: m.proxy_grp_access_hint() },
  { key: 'headers', label: m.proxy_grp_headers(), hint: m.proxy_grp_headers_hint() },
  { key: 'logs', label: m.common_logs(), hint: m.proxy_grp_logs_hint() }
];

export interface Warning { field: string; text: string }

// warnings lists the consequences of risky settings in the form, shown
// next to the field with a ⚠.
export function warnings(f: OptionsForm, tls: TLSMode): Warning[] {
  const out: Warning[] = [];
  if (f.https.allowHTTP && tls !== 'none')
    out.push({ field: 'https.allow_http', text: m.proxy_warn_allow_http() });
  if (f.https.hsts)
    out.push({ field: 'https.hsts', text: m.proxy_warn_hsts() });
  if (f.routing.healthPath.trim())
    out.push({ field: 'routing.health_path', text: m.proxy_warn_health() });
  if (f.performance.bufferRequests)
    out.push({ field: 'performance.buffer_requests', text: m.proxy_warn_buffer() });
  if (f.access.ipMode === 'allow')
    out.push({ field: 'access.ip_mode', text: m.proxy_warn_ip_allow() });
  if (f.access.forwardAuth)
    out.push({ field: 'access.forward_auth', text: m.proxy_warn_forward_auth() });
  if (f.access.users.some((u) => u.username.trim()) && (tls === 'none' || f.https.allowHTTP))
    out.push({ field: 'access.basic_auth', text: m.proxy_warn_basic_auth_http() });
  if (f.routing.fallback.mode === 'redirect' && f.routing.fallback.to.trim().startsWith('/'))
    out.push({ field: 'routing.fallback', text: m.proxy_warn_fallback_loop() });
  return out;
}
