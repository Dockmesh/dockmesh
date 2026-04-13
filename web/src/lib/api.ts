import { auth } from './stores/auth.svelte';

const BASE = '/api/v1';

export type Severity = 'critical' | 'high' | 'medium' | 'low' | 'negligible' | 'unknown';

export interface ScanVuln {
  id: string;
  severity: Severity;
  package: string;
  version: string;
  fixed_in?: string;
  type?: string;
  url?: string;
}

export interface ScanReport {
  image: string;
  scanner: string;
  scanner_version?: string;
  scanned_at: string;
  summary: {
    critical: number;
    high: number;
    medium: number;
    low: number;
    negligible: number;
    unknown: number;
  };
  vulnerabilities: ScanVuln[];
}

export interface UpdateResult {
  container_id: string;
  container_name: string;
  image: string;
  old_digest: string;
  new_digest: string;
  updated: boolean;
  rollback_tag?: string;
  history_id?: number;
}

export interface MetricsSample {
  ts: number;
  cpu_percent: number;
  mem_used: number;
  mem_limit: number;
  net_rx: number;
  net_tx: number;
  blk_read: number;
  blk_write: number;
}

export interface OIDCProvider {
  id: number;
  slug: string;
  display_name: string;
  issuer_url: string;
  client_id: string;
  scopes: string;
  group_claim?: string;
  admin_group?: string;
  operator_group?: string;
  default_role: string;
  enabled: boolean;
  created_at: string;
  updated_at: string;
}

export interface OIDCProviderInput {
  slug: string;
  display_name: string;
  issuer_url: string;
  client_id: string;
  client_secret: string;
  scopes: string;
  group_claim?: string;
  admin_group?: string;
  operator_group?: string;
  default_role: string;
  enabled: boolean;
}

export interface UpdateHistoryEntry {
  id: number;
  container_name: string;
  image_ref: string;
  old_digest: string;
  new_digest: string;
  rollback_tag: string;
  applied_at: string;
  rolled_back_at?: string;
}

export class ApiError extends Error {
  status: number;
  constructor(message: string, status: number) {
    super(message);
    this.status = status;
  }
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    ...(init.headers as Record<string, string> ?? {})
  };
  if (auth.accessToken) {
    headers['Authorization'] = `Bearer ${auth.accessToken}`;
  }

  let res = await fetch(`${BASE}${path}`, { ...init, headers });

  // On 401, try one refresh then retry.
  if (res.status === 401 && auth.refreshToken) {
    const ok = await auth.refresh();
    if (ok && auth.accessToken) {
      headers['Authorization'] = `Bearer ${auth.accessToken}`;
      res = await fetch(`${BASE}${path}`, { ...init, headers });
    }
  }

  if (!res.ok) {
    let msg = `${res.status} ${res.statusText}`;
    try {
      const body = await res.json();
      if (body.error) msg = body.error;
    } catch { /* ignore */ }
    throw new ApiError(msg, res.status);
  }
  if (res.status === 204) return undefined as T;
  return res.json() as Promise<T>;
}

export const api = {
  health: () => request<{ status: string; version: string; docker: boolean }>('/health'),

  auth: {
    login: (username: string, password: string) =>
      request<{
        access_token?: string;
        refresh_token?: string;
        user?: { id: string; username: string; role: string; mfa_enabled?: boolean };
        mfa_required?: boolean;
        mfa_token?: string;
      }>('/auth/login', { method: 'POST', body: JSON.stringify({ username, password }) }),
    verifyMFA: (mfaToken: string, code: string) =>
      request<{
        access_token: string;
        refresh_token: string;
        user: { id: string; username: string; role: string; mfa_enabled?: boolean };
      }>('/auth/mfa', { method: 'POST', body: JSON.stringify({ mfa_token: mfaToken, code }) }),
    logout: () => {
      const body = auth.refreshToken ? JSON.stringify({ refresh_token: auth.refreshToken }) : '{}';
      return request<void>('/auth/logout', { method: 'POST', body }).finally(() => auth.clear());
    }
  },

  mfa: {
    enrollStart: () =>
      request<{ secret: string; url: string; qr_data_url: string }>('/mfa/enroll/start', { method: 'POST' }),
    enrollVerify: (code: string) =>
      request<{ recovery_codes: string[] }>('/mfa/enroll/verify', {
        method: 'POST',
        body: JSON.stringify({ code })
      }),
    disable: () => request<void>('/mfa', { method: 'DELETE' }),
    reset: (userId: string) => request<void>(`/users/${userId}/mfa`, { method: 'DELETE' })
  },

  stacks: {
    list: () => request<Array<{ name: string; compose_path: string }>>('/stacks'),
    get: (name: string) => request<{ name: string; compose: string; env: string }>(`/stacks/${encodeURIComponent(name)}`),
    create: (name: string, compose: string, env?: string) =>
      request<{ name: string }>('/stacks', { method: 'POST', body: JSON.stringify({ name, compose, env }) }),
    update: (name: string, compose: string, env?: string) =>
      request<{ name: string }>(`/stacks/${encodeURIComponent(name)}`, { method: 'PUT', body: JSON.stringify({ compose, env }) }),
    delete: (name: string) =>
      request<void>(`/stacks/${encodeURIComponent(name)}`, { method: 'DELETE' }),
    deploy: (name: string) =>
      request<{ stack: string; services: Array<{ name: string; container_id: string; image: string }> }>(
        `/stacks/${encodeURIComponent(name)}/deploy`, { method: 'POST' }
      ),
    stop: (name: string) =>
      request<void>(`/stacks/${encodeURIComponent(name)}/stop`, { method: 'POST' }),
    status: (name: string) =>
      request<Array<{ service: string; container_id: string; state: string; status: string; image: string }>>(
        `/stacks/${encodeURIComponent(name)}/status`
      )
  },

  containers: {
    list: (all = false) => request<any[]>(`/containers${all ? '?all=true' : ''}`),
    inspect: (id: string) => request<any>(`/containers/${id}`),
    start: (id: string) => request<void>(`/containers/${id}/start`, { method: 'POST' }),
    stop: (id: string) => request<void>(`/containers/${id}/stop`, { method: 'POST' }),
    restart: (id: string) => request<void>(`/containers/${id}/restart`, { method: 'POST' }),
    remove: (id: string, force = false) =>
      request<void>(`/containers/${id}${force ? '?force=true' : ''}`, { method: 'DELETE' }),
    updateInfo: (id: string) =>
      request<UpdatePreview>(`/containers/${id}/update-info`),
    doUpdate: (id: string) =>
      request<UpdateResult>(`/containers/${id}/update`, { method: 'POST' }),
    rollback: (id: string, historyId: number) =>
      request<UpdateResult>(`/containers/${id}/rollback`, {
        method: 'POST',
        body: JSON.stringify({ history_id: historyId })
      }),
    updateHistory: (id: string) =>
      request<UpdateHistoryEntry[]>(`/containers/${id}/update-history`),
    metrics: (id: string, from: number, to: number, resolution: 'raw' | '1m' | '1h' = 'raw') =>
      request<MetricsSample[]>(
        `/containers/${id}/metrics?from=${from}&to=${to}&resolution=${resolution}`
      )
  },

  images: {
    list: (all = false) => request<any[]>(`/images${all ? '?all=true' : ''}`),
    pull: (image: string) => request<any>('/images/pull', { method: 'POST', body: JSON.stringify({ image }) }),
    remove: (id: string, force = false) =>
      request<any>(`/images/${encodeURIComponent(id)}${force ? '?force=true' : ''}`, { method: 'DELETE' }),
    prune: () => request<{ ImagesDeleted: any[]; SpaceReclaimed: number }>('/images/prune', { method: 'POST' }),
    scan: (id: string) =>
      request<ScanReport>(`/images/${encodeURIComponent(id)}/scan`, { method: 'POST' }),
    getScan: (id: string) =>
      request<ScanReport>(`/images/${encodeURIComponent(id)}/scan`)
  },

  networks: {
    list: () => request<any[]>('/networks'),
    inspect: (id: string) => request<any>(`/networks/${id}`),
    create: (name: string, driver?: string) =>
      request<{ Id: string }>('/networks', { method: 'POST', body: JSON.stringify({ name, driver }) }),
    remove: (id: string) => request<void>(`/networks/${id}`, { method: 'DELETE' })
  },

  volumes: {
    list: () => request<any[]>('/volumes'),
    inspect: (name: string) => request<any>(`/volumes/${encodeURIComponent(name)}`),
    create: (name: string, driver?: string) =>
      request<any>('/volumes', { method: 'POST', body: JSON.stringify({ name, driver }) }),
    remove: (name: string, force = false) =>
      request<void>(`/volumes/${encodeURIComponent(name)}${force ? '?force=true' : ''}`, { method: 'DELETE' }),
    prune: () => request<any>('/volumes/prune', { method: 'POST' })
  },

  users: {
    me: () => request<{ id: string; username: string; email?: string; role: string }>('/me'),
    list: () => request<Array<{ id: string; username: string; email?: string; role: string }>>('/users'),
    create: (username: string, password: string, role: string, email?: string) =>
      request<{ id: string; username: string; role: string }>('/users', {
        method: 'POST',
        body: JSON.stringify({ username, password, role, email })
      }),
    update: (id: string, email: string, role: string) =>
      request<{ id: string; username: string; role: string }>(`/users/${id}`, {
        method: 'PUT',
        body: JSON.stringify({ email, role })
      }),
    delete: (id: string) => request<void>(`/users/${id}`, { method: 'DELETE' }),
    changePassword: (id: string, password: string) =>
      request<void>(`/users/${id}/password`, {
        method: 'PUT',
        body: JSON.stringify({ password })
      })
  },

  audit: {
    list: (limit = 100) =>
      request<Array<{ id: number; ts: string; user_id?: string; action: string; target?: string; details?: string; prev_hash?: string; row_hash?: string }>>(
        `/audit?limit=${limit}`
      ),
    verify: () =>
      request<{
        verified: number;
        broken: number;
        first_break?: number;
        break_reason?: string;
        genesis: string;
        warnings?: string[];
      }>('/audit/verify')
  },

  convert: {
    runToCompose: (command: string) =>
      request<{ yaml: string; warnings?: string[] }>('/convert/run-to-compose', {
        method: 'POST',
        body: JSON.stringify({ command })
      })
  },

  oidc: {
    listPublic: () =>
      request<Array<{ slug: string; display_name: string }>>('/auth/oidc/providers'),
    listAdmin: () =>
      request<Array<OIDCProvider>>('/oidc/providers'),
    create: (input: OIDCProviderInput) =>
      request<OIDCProvider>('/oidc/providers', { method: 'POST', body: JSON.stringify(input) }),
    update: (id: number, input: OIDCProviderInput) =>
      request<OIDCProvider>(`/oidc/providers/${id}`, { method: 'PUT', body: JSON.stringify(input) }),
    delete: (id: number) =>
      request<void>(`/oidc/providers/${id}`, { method: 'DELETE' })
  },

  proxy: {
    status: () =>
      request<{ enabled: boolean; running: boolean; admin_ok: boolean; version?: string; container?: string }>('/proxy/status'),
    enable: () => request<any>('/proxy/enable', { method: 'POST' }),
    disable: () => request<void>('/proxy/disable', { method: 'POST' }),
    listRoutes: () =>
      request<Array<{ id: number; host: string; upstream: string; tls_mode: string; created_at: string; updated_at: string }>>('/proxy/routes'),
    createRoute: (host: string, upstream: string, tls_mode: string) =>
      request<any>('/proxy/routes', { method: 'POST', body: JSON.stringify({ host, upstream, tls_mode }) }),
    updateRoute: (id: number, upstream: string, tls_mode: string) =>
      request<void>(`/proxy/routes/${id}`, { method: 'PUT', body: JSON.stringify({ upstream, tls_mode }) }),
    deleteRoute: (id: number) => request<void>(`/proxy/routes/${id}`, { method: 'DELETE' })
  },

  ws: {
    ticket: () => request<{ ticket: string }>('/ws/ticket', { method: 'POST' })
  }
};
