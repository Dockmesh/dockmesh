const BASE = '/api/v1';

export class ApiError extends Error {
  status: number;
  constructor(message: string, status: number) {
    super(message);
    this.status = status;
  }
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const res = await fetch(`${BASE}${path}`, {
    ...init,
    headers: {
      'Content-Type': 'application/json',
      ...(init.headers ?? {})
    },
    credentials: 'include'
  });
  if (!res.ok) {
    throw new ApiError(`${res.status} ${res.statusText}`, res.status);
  }
  if (res.status === 204) return undefined as T;
  return res.json() as Promise<T>;
}

export const api = {
  health: () => request<{ status: string; version: string }>('/health'),
  stacks: {
    list: () => request<Array<{ name: string }>>('/stacks'),
    get: (name: string) => request<{ name: string }>(`/stacks/${encodeURIComponent(name)}`),
    deploy: (name: string) =>
      request<void>(`/stacks/${encodeURIComponent(name)}/deploy`, { method: 'POST' }),
    stop: (name: string) =>
      request<void>(`/stacks/${encodeURIComponent(name)}/stop`, { method: 'POST' })
  }
};
