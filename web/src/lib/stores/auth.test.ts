import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createAuthStore, leaseLock, type AuthEnv, type SignOutReason } from './auth.svelte';

const LIFETIME_S = 15 * 60;

function b64url(v: unknown) {
  return btoa(JSON.stringify(v)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

function jwt(serial: number, nowMs: number) {
  const iat = Math.floor(nowMs / 1000);
  return `${b64url({ alg: 'HS256' })}.${b64url({ iat, exp: iat + LIFETIME_S, n: serial })}.sig`;
}

const user = { id: 'u1', username: 'admin', role: 'admin' };

function memStorage() {
  const data = new Map<string, string>();
  const storage = {
    getItem: (k: string) => data.get(k) ?? null,
    setItem: (k: string, v: string) => void data.set(k, v),
    removeItem: (k: string) => void data.delete(k)
  };
  return { data, storage };
}

// Rotating refresh tokens like the backend: each token is single use and
// replaying a spent one revokes the session.
class FakeServer {
  serial = 0;
  valid = new Set<string>();
  revoked = false;
  calls = 0;
  mode: 'ok' | 'down' | 'error' | 'outdated' = 'ok';

  issue() {
    this.serial++;
    const refresh = `r${this.serial}`;
    this.valid.add(refresh);
    return { access_token: jwt(this.serial, Date.now()), refresh_token: refresh, user };
  }

  fetch: typeof fetch = async (_url, init) => {
    this.calls++;
    if (this.mode === 'down') throw new TypeError('Failed to fetch');
    if (this.mode === 'error') return new Response('{"error":"refresh failed"}', { status: 500 });
    if (this.mode === 'outdated') {
      return new Response('{"error":"signed out","code":"session_outdated"}', { status: 401 });
    }
    const token = JSON.parse(String(init?.body)).refresh_token;
    if (this.revoked || !this.valid.has(token)) {
      this.revoked = true;
      return new Response('{"error":"token reuse detected"}', { status: 401 });
    }
    this.valid.delete(token);
    return new Response(JSON.stringify(this.issue()), { status: 200 });
  };
}

// One browser profile: shared localStorage, broadcast bus and lock.
class Browser {
  mem = memStorage();
  data = this.mem.data;
  storage = this.mem.storage;
  subscribers: Array<{ tab: number; fn: (msg: any) => void }> = [];
  lockTail: Promise<unknown> = Promise.resolve();
  reasons: Array<{ tab: number; reason: SignOutReason }> = [];

  constructor(readonly server: FakeServer) {}

  env(tab: number): AuthEnv {
    return {
      storage: this.storage,
      fetch: this.server.fetch,
      now: () => Date.now(),
      random: () => 0,
      setTimeout: (fn, ms) => setTimeout(fn, ms),
      clearTimeout: (id) => clearTimeout(id as ReturnType<typeof setTimeout>),
      channel: {
        post: (msg) => {
          for (const s of this.subscribers) if (s.tab !== tab) queueMicrotask(() => s.fn(msg));
        },
        subscribe: (fn) => void this.subscribers.push({ tab, fn })
      },
      lock: <T>(fn: () => Promise<T>) => {
        const run = this.lockTail.then(fn, fn);
        this.lockTail = run.catch(() => undefined);
        return run;
      },
      rememberSignOutReason: (reason) => void this.reasons.push({ tab, reason })
    };
  }

  tab(n: number) {
    return createAuthStore(this.env(n));
  }

  signIn(store: ReturnType<typeof createAuthStore>) {
    const t = this.server.issue();
    store.setSession(t.user, t.access_token, t.refresh_token);
  }
}

describe('auth store', () => {
  let server: FakeServer;
  let browser: Browser;

  beforeEach(() => {
    vi.useFakeTimers({ now: new Date('2026-10-29T08:00:00Z') });
    server = new FakeServer();
    browser = new Browser(server);
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it('shares one refresh between concurrent callers', async () => {
    const tab = browser.tab(1);
    browser.signIn(tab);
    const results = await Promise.all([tab.refresh(), tab.refresh(), tab.refresh(), tab.refresh()]);
    expect(results).toEqual([true, true, true, true]);
    expect(server.calls).toBe(1);
    expect(server.revoked).toBe(false);
    expect(tab.refreshToken).toBe('r2');
  });

  it('lets only one tab refresh and the other adopts its tokens', async () => {
    const a = browser.tab(1);
    const b = browser.tab(2);
    browser.signIn(a);
    await vi.advanceTimersByTimeAsync(0);
    expect(b.refreshToken).toBe('r1');

    const [ra, rb] = await Promise.all([a.refresh(), b.refresh()]);
    expect([ra, rb]).toEqual([true, true]);
    expect(server.calls).toBe(1);
    expect(server.revoked).toBe(false);
    expect(a.refreshToken).toBe('r2');
    expect(b.refreshToken).toBe('r2');
    expect(b.accessToken).toBe(a.accessToken);
  });

  it('schedules the refresh from the token expiry, not a fixed interval', async () => {
    const tab = browser.tab(1);
    browser.signIn(tab);
    await vi.advanceTimersByTimeAsync((LIFETIME_S - 61) * 1000);
    expect(server.calls).toBe(0);
    await vi.advanceTimersByTimeAsync(2_000);
    expect(server.calls).toBe(1);
    expect(tab.refreshToken).toBe('r2');
  });

  it('only one of several open tabs refreshes on schedule', async () => {
    const tabs = [browser.tab(1), browser.tab(2), browser.tab(3)];
    browser.signIn(tabs[0]);
    await vi.advanceTimersByTimeAsync(LIFETIME_S * 1000);
    expect(server.calls).toBe(1);
    expect(server.revoked).toBe(false);
    for (const t of tabs) {
      expect(t.isAuthenticated).toBe(true);
      expect(t.refreshToken).toBe('r2');
    }
  });

  const transient: Array<{ name: string; mode: FakeServer['mode'] }> = [
    { name: 'network error', mode: 'down' },
    { name: 'server error', mode: 'error' }
  ];
  for (const tc of transient) {
    it(`keeps the session on ${tc.name} and recovers in the background`, async () => {
      const tab = browser.tab(1);
      browser.signIn(tab);
      server.mode = tc.mode;
      const pending = tab.refresh();
      await vi.advanceTimersByTimeAsync(10_000);
      expect(await pending).toBe(false);
      expect(server.calls).toBe(4);
      expect(tab.isAuthenticated).toBe(true);
      expect(tab.refreshToken).toBe('r1');

      server.mode = 'ok';
      await vi.advanceTimersByTimeAsync(30_000);
      expect(tab.refreshToken).toBe('r2');
      expect(tab.isAuthenticated).toBe(true);
    });
  }

  it('signs every tab out with the reason when the session is outdated', async () => {
    const a = browser.tab(1);
    const b = browser.tab(2);
    browser.signIn(a);
    await vi.advanceTimersByTimeAsync(0);
    server.mode = 'outdated';
    expect(await a.refresh()).toBe(false);
    await vi.advanceTimersByTimeAsync(0);
    expect(a.isAuthenticated).toBe(false);
    expect(b.isAuthenticated).toBe(false);
    expect(browser.data.has('dockmesh_auth')).toBe(false);
    expect(browser.reasons).toEqual([
      { tab: 1, reason: 'session_outdated' },
      { tab: 2, reason: 'session_outdated' }
    ]);
  });

  it('signs out on a rejected refresh token without a reason', async () => {
    const tab = browser.tab(1);
    browser.signIn(tab);
    server.valid.clear();
    expect(await tab.refresh()).toBe(false);
    expect(tab.isAuthenticated).toBe(false);
    expect(browser.reasons).toEqual([]);
  });

  it('propagates sign-in and sign-out to other tabs', async () => {
    const a = browser.tab(1);
    const b = browser.tab(2);
    browser.signIn(a);
    await vi.advanceTimersByTimeAsync(0);
    expect(b.user?.username).toBe('admin');
    b.clear();
    await vi.advanceTimersByTimeAsync(0);
    expect(a.isAuthenticated).toBe(false);
  });

  it('refreshes soon after load when the stored token is about to expire', async () => {
    const t = server.issue();
    browser.storage.setItem('dockmesh_auth', JSON.stringify({ user, accessToken: t.access_token, refreshToken: t.refresh_token }));
    vi.setSystemTime(Date.now() + (LIFETIME_S - 30) * 1000);
    const tab = browser.tab(1);
    await vi.advanceTimersByTimeAsync(1_500);
    expect(server.calls).toBe(1);
    expect(tab.refreshToken).toBe('r2');
  });

  it('restores the session from storage on load', () => {
    const a = browser.tab(1);
    browser.signIn(a);
    const b = browser.tab(2);
    expect(b.refreshToken).toBe('r1');
    expect(b.isAuthenticated).toBe(true);
  });
});

describe('leaseLock', () => {
  beforeEach(() => {
    vi.useFakeTimers({ now: new Date('2026-10-29T08:00:00Z') });
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it('runs one holder at a time across owners', async () => {
    const { data, storage } = memStorage();
    const env = { storage, now: () => Date.now(), setTimeout: (fn: () => void, ms: number) => setTimeout(fn, ms) };
    const lockA = leaseLock(env, 'a');
    const lockB = leaseLock(env, 'b');
    let active = 0;
    let maxActive = 0;
    const work = async () => {
      active++;
      maxActive = Math.max(maxActive, active);
      await new Promise((r) => setTimeout(r, 500));
      active--;
    };
    const done = Promise.all([lockA(work), lockB(work)]);
    await vi.advanceTimersByTimeAsync(5_000);
    await done;
    expect(maxActive).toBe(1);
    expect(data.has('dockmesh_auth_refresh_lease')).toBe(false);
  });
});
