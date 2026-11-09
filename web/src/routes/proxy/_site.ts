import { fmtBytes } from '$lib/format';
import { m } from '$lib/paraglide/messages.js';
export type TLSMode = 'auto' | 'internal' | 'none';

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
  foreign_ports?: number[];
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
  internal: m.proxy_tls_internal(),
  none: m.proxy_tls_none()
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
  if (r.extra_upstreams?.length) out.push(`${m.proxy_opt_load_balanced({ n: r.extra_upstreams.length + 1 })}${r.lb_policy ? ` (${r.lb_policy})` : ''}`);
  if (r.health_path) out.push(m.proxy_opt_health_check({ path: r.health_path }));
  if (r.www_redirect) out.push(m.proxy_opt_www_redirect());
  const h = o.https ?? {};
  if (h.allow_http) out.push(m.proxy_opt_allow_http());
  if (h.hsts) out.push('HSTS');
  const p = o.performance ?? {};
  if (p.flush_immediately) out.push(m.proxy_opt_streaming());
  if (p.buffer_requests) out.push(m.proxy_opt_buffered());
  if (p.compression) out.push(m.proxy_opt_compression());
  if (p.dial_timeout || p.read_timeout || p.write_timeout) out.push(m.proxy_opt_timeouts());
  if (p.max_body_bytes) out.push(m.proxy_opt_max_body({ size: fmtBytes(p.max_body_bytes) }));
  if (p.stream_close_delay) out.push(m.proxy_opt_stream_close({ delay: p.stream_close_delay }));
  const a = o.access ?? {};
  if (a.basic_auth?.length) out.push(m.proxy_opt_password({ count: a.basic_auth.length }));
  if (a.ip_mode === 'allow') out.push(m.proxy_opt_ip_allow({ count: a.ip_ranges?.length ?? 0 }));
  if (a.ip_mode === 'deny') out.push(m.proxy_opt_ip_deny({ count: a.ip_ranges?.length ?? 0 }));
  if (a.forward_auth?.url) out.push(m.proxy_opt_forward_auth());
  const hd = o.headers ?? {};
  if (hd.security) out.push(m.proxy_opt_security_headers());
  const set = (hd.request_set?.length ?? 0) + (hd.response_set?.length ?? 0);
  const rm = (hd.request_remove?.length ?? 0) + (hd.response_remove?.length ?? 0);
  if (set || rm) out.push(m.proxy_opt_header_rules({ count: set + rm }));
  if (o.logs?.access_log) out.push(m.proxy_opt_access_log());
  return out;
}

