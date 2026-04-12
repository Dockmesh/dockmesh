const STORAGE_KEY = 'dockmesh_auth';

interface User {
  id: string;
  username: string;
  role: string;
}

interface AuthState {
  user: User | null;
  accessToken: string | null;
  refreshToken: string | null;
}

function loadFromStorage(): AuthState {
  if (typeof window === 'undefined') return { user: null, accessToken: null, refreshToken: null };
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return { user: null, accessToken: null, refreshToken: null };
    return JSON.parse(raw);
  } catch {
    return { user: null, accessToken: null, refreshToken: null };
  }
}

function saveToStorage(state: AuthState) {
  if (typeof window === 'undefined') return;
  if (state.user) {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(state));
  } else {
    localStorage.removeItem(STORAGE_KEY);
  }
}

function createAuthStore() {
  let state = $state<AuthState>(loadFromStorage());
  let refreshTimer: ReturnType<typeof setTimeout> | null = null;

  function scheduleRefresh() {
    if (refreshTimer) clearTimeout(refreshTimer);
    if (!state.refreshToken) return;
    // Refresh 2 minutes before the 15-min access token expires.
    refreshTimer = setTimeout(async () => {
      await doRefresh();
    }, 13 * 60 * 1000);
  }

  async function doRefresh(): Promise<boolean> {
    if (!state.refreshToken) return false;
    try {
      const res = await fetch('/api/v1/auth/refresh', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ refresh_token: state.refreshToken })
      });
      if (!res.ok) {
        clear();
        return false;
      }
      const data = await res.json();
      state.user = data.user;
      state.accessToken = data.access_token;
      state.refreshToken = data.refresh_token;
      saveToStorage(state);
      scheduleRefresh();
      return true;
    } catch {
      clear();
      return false;
    }
  }

  function setSession(user: User, accessToken: string, refreshToken: string) {
    state.user = user;
    state.accessToken = accessToken;
    state.refreshToken = refreshToken;
    saveToStorage(state);
    scheduleRefresh();
  }

  function clear() {
    if (refreshTimer) clearTimeout(refreshTimer);
    state.user = null;
    state.accessToken = null;
    state.refreshToken = null;
    saveToStorage(state);
  }

  // Start refresh timer if already logged in on load.
  if (state.accessToken) scheduleRefresh();

  return {
    get user() { return state.user; },
    get accessToken() { return state.accessToken; },
    get refreshToken() { return state.refreshToken; },
    get isAuthenticated() { return state.user !== null; },
    setSession,
    clear,
    refresh: doRefresh
  };
}

export const auth = createAuthStore();
