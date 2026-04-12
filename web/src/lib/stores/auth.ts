interface User {
  id: string;
  username: string;
  role: string;
}

interface AuthState {
  user: User | null;
  token: string | null;
}

function createAuthStore() {
  let state = $state<AuthState>({ user: null, token: null });

  return {
    get user() {
      return state.user;
    },
    get token() {
      return state.token;
    },
    get isAuthenticated() {
      return state.user !== null;
    },
    setSession(user: User, token: string) {
      state.user = user;
      state.token = token;
    },
    clear() {
      state.user = null;
      state.token = null;
    }
  };
}

export const auth = createAuthStore();
