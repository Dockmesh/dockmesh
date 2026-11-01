import { fmtBytes } from '$lib/format';
export type TLSMode = 'auto' | 'internal' | 'none';

// A secret header comes back with an empty value and value_set.
export interface SiteHeader { name: string; value?: string; secret?: boolean; value_set?: boolean }

export interface SiteOptions {
  routing?: { extra_upstreams?: string[]; lb_policy?: string; health_path?: string; www_redirect?: boolean };
  https?: { allow_http?: boolean; hsts?: boolean };
  performance?: {
    flush_immediately?: boolean;
    buffer_requests?: boolean;
    dial_timeout?: string;
    read_timeout?: string;
    write_timeout?: string;
    max_body_bytes?: number;
    stream_close_delay?: string;
    compression?: boolean;
  };
  access?: {
    basic_auth?: Array<{ username: string; password?: string }>;
    ip_mode?: string;
    ip_ranges?: string[];
    forward_auth?: { url: string; copy_headers?: string[]; query_set?: boolean; clear_query?: boolean } | null;
  };
  headers?: {
    security?: boolean;
    request_set?: SiteHeader[];
    request_remove?: string[];
    response_set?: SiteHeader[];
    response_remove?: string[];
  };
  logs?: { access_log?: boolean };
}

export interface Site {
  id: number;
  host: string;
  upstream: string;
  tls_mode: TLSMode;
  enabled: boolean;
  options?: SiteOptions;
  created_at?: string;
  updated_at?: string;
  cert_issuer?: string;
  cert_valid_from?: string;
  cert_valid_to?: string;
  cert_days_left?: number;
}

export interface ProxyStatus {
  enabled: boolean;
  running: boolean;
  admin_ok: boolean;
  version?: string;
  container?: string;
}

export interface ACMEEvent {
  ts: string;
  kind: 'obtain' | 'renew' | 'failure';
  host?: string;
  issuer?: string;
  message: string;
  successful: boolean;
}

export const TLS_LABEL: Record<TLSMode, string> = {
  auto: "Let's Encrypt",
  internal: 'Internal CA',
  none: 'HTTP only'
};

// Certificates renew about 30 days before expiry; below 14 days a
// renewal has failed at least once.
export const RENEW_WARN_DAYS = 14;

export function certExpiresSoon(r: Site): boolean {
  return r.cert_days_left != null && r.cert_days_left < RENEW_WARN_DAYS;
}

// An HTTPS site whose certificate is missing or close to expiry.
export function certNeedsAttention(r: Site): boolean {
  return r.tls_mode !== 'none' && (r.cert_days_left == null || r.cert_days_left < RENEW_WARN_DAYS);
}

// The options a site sets beyond Caddy's defaults, as short phrases.
export function optionSummary(o?: SiteOptions): string[] {
  if (!o) return [];
  const out: string[] = [];
  const r = o.routing ?? {};
  if (r.extra_upstreams?.length) out.push(`load balanced over ${r.extra_upstreams.length + 1}${r.lb_policy ? ` (${r.lb_policy})` : ''}`);
  if (r.health_path) out.push(`health check ${r.health_path}`);
  if (r.www_redirect) out.push('www redirect');
  const h = o.https ?? {};
  if (h.allow_http) out.push('also plain HTTP');
  if (h.hsts) out.push('HSTS');
  const p = o.performance ?? {};
  if (p.flush_immediately) out.push('streaming');
  if (p.buffer_requests) out.push('buffered uploads');
  if (p.compression) out.push('compression');
  if (p.dial_timeout || p.read_timeout || p.write_timeout) out.push('timeouts');
  if (p.max_body_bytes) out.push(`max body ${fmtBytes(p.max_body_bytes)}`);
  if (p.stream_close_delay) out.push(`stream close delay ${p.stream_close_delay}`);
  const a = o.access ?? {};
  if (a.basic_auth?.length) out.push(`password (${a.basic_auth.length} user${a.basic_auth.length === 1 ? '' : 's'})`);
  if (a.ip_mode === 'allow') out.push(`only ${a.ip_ranges?.length ?? 0} IP range${a.ip_ranges?.length === 1 ? '' : 's'}`);
  if (a.ip_mode === 'deny') out.push(`blocks ${a.ip_ranges?.length ?? 0} IP range${a.ip_ranges?.length === 1 ? '' : 's'}`);
  if (a.forward_auth?.url) out.push('forward auth');
  const hd = o.headers ?? {};
  if (hd.security) out.push('security headers');
  const set = (hd.request_set?.length ?? 0) + (hd.response_set?.length ?? 0);
  const rm = (hd.request_remove?.length ?? 0) + (hd.response_remove?.length ?? 0);
  if (set || rm) out.push(`${set + rm} header rule${set + rm === 1 ? '' : 's'}`);
  if (o.logs?.access_log) out.push('access log');
  return out;
}

