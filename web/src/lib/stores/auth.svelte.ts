const STORAGE_KEY = 'dockmesh_auth';
// Why the last session ended, shown once on the login page.
const SIGNOUT_REASON_KEY = 'dockmesh_signout_reason';
const CHANNEL_NAME = 'dockmesh_auth';
const LOCK_NAME = 'dockmesh_auth_refresh';
const LEASE_KEY = 'dockmesh_auth_refresh_lease';

// Refresh this long before the access token expires (capped at a quarter
// of its lifetime for short-lived tokens).
const REFRESH_MARGIN_MS = 60_000;
// Spreads the timers of several open tabs so they rarely contend for the lock.
const REFRESH_JITTER_MS = 5_000;
const FALLBACK_REFRESH_MS = 5 * 60_000;
const REFRESH_TIMEOUT_MS = 10_000;
// Retries inside one refresh() call; after that the call reports failure
// and a background retry keeps going without signing the user out.
const RETRY_DELAYS_MS = [1_000, 2_000, 4_000];
const BACKGROUND_RETRY_MS = 30_000;
const LEASE_MS = REFRESH_TIMEOUT_MS * 2;
const LEASE_WAIT_MS = 30_000;

export type SignOutReason = 'session_outdated';

export function consumeSignOutReason(): SignOutReason | null {
  try {
    const v = sessionStorage.getItem(SIGNOUT_REASON_KEY);
    sessionStorage.removeItem(SIGNOUT_REASON_KEY);
    return v === 'session_outdated' ? v : null;
  } catch {
    return null;
  }
}

interface MeScopes {
  stacks: string[];
  hosts: string[];
  host_tags: string[];
}

interface User {
  id: string;
  username: string;
  role: string;
  permissions?: string[];
  scopes?: MeScopes;
}

interface AuthState {
  user: User | null;
  accessToken: string | null;
  refreshToken: string | null;
  // Local-clock expiry of the access token, so server clock skew does not
  // shift the refresh schedule.
  accessExpiresAt?: number | null;
}

type SyncMessage = { type: 'sync'; reason?: SignOutReason };

type KV = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>;

export interface AuthEnv {
  storage: KV | null;
  fetch: typeof fetch;
  now: () => number;
  random: () => number;
  setTimeout: (fn: () => void, ms: number) => unknown;
  clearTimeout: (id: unknown) => void;
  // Cross-tab notifications; null disables them (single-tab behaviour).
  channel: {
    post: (msg: SyncMessage) => void;
    subscribe: (fn: (msg: SyncMessage) => void) => void;
  } | null;
  // Serialises refresh attempts across tabs.
  lock: <T>(fn: () => Promise<T>) => Promise<T>;
  rememberSignOutReason: (reason: SignOutReason) => void;
}

const empty = (): AuthState => ({ user: null, accessToken: null, refreshToken: null, accessExpiresAt: null });

function decodeClaims(token: string | null): { exp?: number; iat?: number } {
  if (!token) return {};
  try {
    const part = token.split('.')[1];
    const json = atob(part.replace(/-/g, '+').replace(/_/g, '/'));
    const c = JSON.parse(json);
    return {
      exp: typeof c.exp === 'number' ? c.exp : undefined,
      iat: typeof c.iat === 'number' ? c.iat : undefined
    };
  } catch {
    return {};
  }
}

function localExpiry(token: string, now: number): number | null {
  const { exp, iat } = decodeClaims(token);
  if (exp === undefined) return null;
  if (iat !== undefined && exp > iat) return now + (exp - iat) * 1000;
  return exp * 1000;
}

const sleep = (env: AuthEnv, ms: number) => new Promise<void>((r) => env.setTimeout(r, ms));

// Cross-tab mutex on a localStorage lease, for insecure origins (plain-HTTP
// LAN installs) where navigator.locks does not exist. Not strictly atomic;
// the re-read of the stored token inside the lock covers the rare overlap.
export function leaseLock(env: Pick<AuthEnv, 'now' | 'setTimeout'> & { storage: KV }, owner: string) {
  const read = (): { owner: string; until: number } | null => {
    try {
      return JSON.parse(env.storage.getItem(LEASE_KEY) ?? 'null');
    } catch {
      return null;
    }
  };
  const wait = (ms: number) => new Promise<void>((r) => env.setTimeout(r, ms));
  return async <T>(fn: () => Promise<T>): Promise<T> => {
    const giveUpAt = env.now() + LEASE_WAIT_MS;
    while (env.now() < giveUpAt) {
      const cur = read();
      if (!cur || cur.until < env.now() || cur.owner === owner) {
        env.storage.setItem(LEASE_KEY, JSON.stringify({ owner, until: env.now() + LEASE_MS }));
        await wait(25);
        if (read()?.owner === owner) break;
      }
      await wait(100);
    }
    try {
      return await fn();
    } finally {
      if (read()?.owner === owner) env.storage.removeItem(LEASE_KEY);
    }
  };
}

function browserEnv(): AuthEnv | null {
  if (typeof window === 'undefined') return null;
  let storage: KV | null = null;
  try {
    storage = window.localStorage;
  } catch { /* storage disabled: sessions stay per tab */ }

  let channel: AuthEnv['channel'] = null;
  if (typeof BroadcastChannel !== 'undefined') {
    const bc = new BroadcastChannel(CHANNEL_NAME);
    channel = {
      post: (msg) => bc.postMessage(msg),
      subscribe: (fn) => { bc.onmessage = (e) => fn(e.data as SyncMessage); }
    };
  } else if (storage) {
    channel = {
      post: () => { /* the localStorage write itself fires the storage event */ },
      subscribe: (fn) => window.addEventListener('storage', (e) => {
        if (e.key === STORAGE_KEY || e.key === null) fn({ type: 'sync' });
      })
    };
  }

  const env: AuthEnv = {
    storage,
    fetch: (...args) => window.fetch(...args),
    now: () => Date.now(),
    random: Math.random,
    setTimeout: (fn, ms) => window.setTimeout(fn, ms),
    clearTimeout: (id) => window.clearTimeout(id as number),
    channel,
    lock: (fn) => fn(),
    rememberSignOutReason: (reason) => {
      try {
        sessionStorage.setItem(SIGNOUT_REASON_KEY, reason);
      } catch { /* storage unavailable: the login page just shows no reason */ }
    }
  };
  if (typeof navigator !== 'undefined' && navigator.locks) {
    env.lock = <T>(fn: () => Promise<T>) => navigator.locks.request(LOCK_NAME, fn) as Promise<T>;
  } else if (storage) {
    env.lock = leaseLock({ ...env, storage }, Math.random().toString(36).slice(2));
  }
  return env;
}

export function createAuthStore(env: AuthEnv | null) {
  // Returns null when storage can't be read, which is not the same as
  // "signed out" and must not end a session.
  function readStored(): AuthState | null {
    if (!env?.storage) return null;
    try {
      const raw = env.storage.getItem(STORAGE_KEY);
      return raw ? { ...empty(), ...JSON.parse(raw) } : empty();
    } catch {
      return null;
    }
  }

  function persist() {
    if (!env?.storage) return;
    try {
      if (state.user) env.storage.setItem(STORAGE_KEY, JSON.stringify(state));
      else env.storage.removeItem(STORAGE_KEY);
    } catch { /* quota or disabled storage: keep the in-memory session */ }
  }

  let state = $state<AuthState>(readStored() ?? empty());
  let refreshTimer: unknown = null;
  let inflight: Promise<boolean> | null = null;

  function cancelTimer() {
    if (refreshTimer !== null) env?.clearTimeout(refreshTimer);
    refreshTimer = null;
  }

  function scheduleIn(ms: number) {
    cancelTimer();
    if (!env || !state.refreshToken) return;
    refreshTimer = env.setTimeout(() => {
      refreshTimer = null;
      void refresh();
    }, Math.max(1_000, ms));
  }

  function scheduleFromExpiry() {
    if (!env) return;
    const now = env.now();
    const expiresAt = state.accessExpiresAt ?? localExpiry(state.accessToken ?? '', now);
    if (!expiresAt) {
      scheduleIn(FALLBACK_REFRESH_MS);
      return;
    }
    const { exp, iat } = decodeClaims(state.accessToken);
    const lifetime = exp !== undefined && iat !== undefined ? (exp - iat) * 1000 : Infinity;
    const margin = Math.min(REFRESH_MARGIN_MS, lifetime / 4);
    scheduleIn(expiresAt - margin - env.random() * REFRESH_JITTER_MS - now);
  }

  function apply(next: AuthState) {
    state.user = next.user;
    state.accessToken = next.accessToken;
    state.refreshToken = next.refreshToken;
    state.accessExpiresAt = next.accessExpiresAt ?? null;
    if (state.refreshToken) scheduleFromExpiry();
    else cancelTimer();
  }

  function signOutLocal(reason?: SignOutReason) {
    if (reason) env?.rememberSignOutReason(reason);
    apply(empty());
  }

  function broadcast(reason?: SignOutReason) {
    env?.channel?.post(reason ? { type: 'sync', reason } : { type: 'sync' });
  }

  // Another tab changed the stored session: take it over as-is.
  function syncFromStorage(reason?: SignOutReason) {
    const stored = readStored();
    if (!stored) return;
    if (stored.refreshToken === state.refreshToken && stored.accessToken === state.accessToken) return;
    if (!stored.user || !stored.refreshToken) signOutLocal(reason);
    else apply(stored);
  }

  type Attempt = 'ok' | 'rejected' | 'transient';

  async function attemptRefresh(): Promise<Attempt> {
    // Inside the cross-tab lock: if another tab rotated the token while we
    // waited, adopt its result instead of replaying a spent refresh token,
    // which the server would treat as reuse and revoke the whole session.
    const stored = readStored();
    if (stored && stored.refreshToken !== state.refreshToken) {
      syncFromStorage();
      return state.refreshToken ? 'ok' : 'rejected';
    }
    const token = state.refreshToken;
    if (!token || !env) return 'rejected';

    const ctrl = new AbortController();
    const timeout = env.setTimeout(() => ctrl.abort(), REFRESH_TIMEOUT_MS);
    try {
      const res = await env.fetch('/api/v1/auth/refresh', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ refresh_token: token }),
        signal: ctrl.signal
      });
      if (res.status === 400 || res.status === 401) {
        const body = await res.json().catch(() => null);
        const reason: SignOutReason | undefined = body?.code === 'session_outdated' ? 'session_outdated' : undefined;
        signOutLocal(reason);
        persist();
        broadcast(reason);
        return 'rejected';
      }
      if (!res.ok) return 'transient';
      const data = await res.json();
      // A sign-out in this tab while the request was in flight wins.
      if (state.refreshToken !== token) return state.refreshToken ? 'ok' : 'rejected';
      apply({
        user: data.user,
        accessToken: data.access_token,
        refreshToken: data.refresh_token,
        accessExpiresAt: localExpiry(data.access_token, env.now())
      });
      persist();
      broadcast();
      return 'ok';
    } catch {
      return 'transient';
    } finally {
      env.clearTimeout(timeout);
    }
  }

  async function runRefresh(): Promise<boolean> {
    if (!env) return false;
    for (let i = 0; ; i++) {
      const result = await env.lock(attemptRefresh);
      if (result !== 'transient') return result === 'ok';
      if (i >= RETRY_DELAYS_MS.length) {
        scheduleIn(BACKGROUND_RETRY_MS);
        return false;
      }
      await sleep(env, RETRY_DELAYS_MS[i]);
      if (!state.refreshToken) return false;
    }
  }

  // Single-flight: concurrent callers share one refresh. Resolves false
  // when the session ended or the server stayed unreachable; only the
  // former clears the session.
  function refresh(): Promise<boolean> {
    if (!inflight) inflight = runRefresh().finally(() => { inflight = null; });
    return inflight;
  }

  function setSession(user: User, accessToken: string, refreshToken: string) {
    apply({
      user,
      accessToken,
      refreshToken,
      accessExpiresAt: env ? localExpiry(accessToken, env.now()) : null
    });
    persist();
    broadcast();
  }

  function clear() {
    signOutLocal();
    persist();
    broadcast();
  }

  env?.channel?.subscribe((msg) => {
    if (msg?.type === 'sync') syncFromStorage(msg.reason);
  });
  if (state.refreshToken) scheduleFromExpiry();

  return {
    get user() { return state.user; },
    get accessToken() { return state.accessToken; },
    get refreshToken() { return state.refreshToken; },
    get isAuthenticated() { return state.user !== null; },
    setSession,
    clear,
    refresh
  };
}

export const auth = createAuthStore(browserEnv());
