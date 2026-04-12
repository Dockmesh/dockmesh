import { auth } from './stores/auth.svelte';

const BASE = '/api/v1';

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
      request<void>(`/containers/${id}${force ? '?force=true' : ''}`, { method: 'DELETE' })
  },

  images: {
    list: (all = false) => request<any[]>(`/images${all ? '?all=true' : ''}`),
    pull: (image: string) => request<any>('/images/pull', { method: 'POST', body: JSON.stringify({ image }) }),
    remove: (id: string, force = false) =>
      request<any>(`/images/${encodeURIComponent(id)}${force ? '?force=true' : ''}`, { method: 'DELETE' }),
    prune: () => request<{ ImagesDeleted: any[]; SpaceReclaimed: number }>('/images/prune', { method: 'POST' })
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

  ws: {
    ticket: () => request<{ ticket: string }>('/ws/ticket', { method: 'POST' })
  }
};
