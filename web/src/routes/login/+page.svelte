<script lang="ts">
  import { goto } from '$app/navigation';
  import { auth } from '$lib/stores/auth.svelte';
  import { api, ApiError } from '$lib/api';
  import { toast } from '$lib/stores/toast.svelte';
  import { Eyebrow, Field } from '$lib/components/editorial';
  import { ArrowRight, Sun, Moon, ShieldCheck } from 'lucide-svelte';

  let username = $state('admin');
  let password = $state('');
  let remember = $state(true);
  let error = $state('');
  let loading = $state(false);

  // MFA step state
  let mfaToken = $state<string | null>(null);
  let mfaCode = $state('');

  // SSO providers
  let providers = $state<Array<{ slug: string; display_name: string }>>([]);

  // Theme — the unauth login page renders without the dashboard chrome,
  // so it owns its own toggle. data-theme is the source of truth that
  // app.css reads; we mirror it into localStorage so the post-login
  // dashboard inherits the choice.
  let theme = $state<'light' | 'dark'>('dark');
  $effect(() => {
    if (typeof window === 'undefined') return;
    const stored = localStorage.getItem('dockmesh.theme');
    if (stored === 'light' || stored === 'dark') theme = stored;
  });
  $effect(() => {
    if (typeof document === 'undefined') return;
    document.documentElement.dataset.theme = theme;
    localStorage.setItem('dockmesh.theme', theme);
  });

  async function loadProviders() {
    try {
      providers = await api.oidc.listPublic();
    } catch {
      /* ignore */
    }
  }

  async function handleSSOHash() {
    if (typeof window === 'undefined') return;
    const hash = window.location.hash.slice(1);
    if (!hash) return;
    const params = new URLSearchParams(hash);
    const access = params.get('sso_access');
    const refresh = params.get('sso_refresh');
    if (!access || !refresh) return;

    history.replaceState(null, '', window.location.pathname + window.location.search);
    auth.setSession({ id: '', username: '', role: '' } as any, access, refresh);
    try {
      const me = await api.users.me();
      auth.setSession(me as any, access, refresh);
      toast.success('Signed in via SSO', me.username);
      goto('/');
    } catch {
      auth.clear();
      error = 'SSO callback failed to load profile';
    }
  }

  function handleSSOErrorParam() {
    if (typeof window === 'undefined') return;
    const p = new URLSearchParams(window.location.search);
    const e = p.get('sso_error');
    if (e) {
      error = 'SSO failed: ' + decodeURIComponent(e);
      history.replaceState(null, '', window.location.pathname);
    }
  }

  $effect(() => {
    handleSSOHash();
    handleSSOErrorParam();
    loadProviders();
  });

  async function submit(e: Event) {
    e.preventDefault();
    error = '';
    loading = true;
    try {
      const res = await api.auth.login(username, password);
      if (res.mfa_required && res.mfa_token) {
        mfaToken = res.mfa_token;
        mfaCode = '';
        return;
      }
      if (res.access_token && res.refresh_token && res.user) {
        auth.setSession(res.user, res.access_token, res.refresh_token);
        goto('/');
      }
    } catch (err) {
      error =
        err instanceof ApiError && err.status === 401
          ? 'Invalid username or password'
          : 'Login failed';
    } finally {
      loading = false;
    }
  }

  async function submitMFA(e: Event) {
    e.preventDefault();
    if (!mfaToken) return;
    error = '';
    loading = true;
    try {
      const res = await api.auth.verifyMFA(mfaToken, mfaCode.trim());
      auth.setSession(res.user, res.access_token, res.refresh_token);
      goto('/');
    } catch (err) {
      error =
        err instanceof ApiError && err.status === 401
          ? 'Invalid code'
          : 'Verification failed';
    } finally {
      loading = false;
    }
  }

  function cancelMFA() {
    mfaToken = null;
    mfaCode = '';
    error = '';
  }

  function ssoLogin(slug: string) {
    window.location.href = `/api/v1/auth/oidc/${slug}/login`;
  }

  let canSubmit = $derived(!loading && username.length > 0 && password.length > 0);
  let canVerify = $derived(!loading && mfaCode.trim().length >= 6);
</script>

<div class="login-shell">
  <button
    type="button"
    class="theme-toggle login-chrome-tr"
    onclick={() => (theme = theme === 'dark' ? 'light' : 'dark')}
    aria-label="Toggle theme"
    title={theme === 'dark' ? 'Switch to light' : 'Switch to dark'}
  >
    {#if theme === 'dark'}
      <Sun size={13} strokeWidth={1.5} />
    {:else}
      <Moon size={13} strokeWidth={1.5} />
    {/if}
  </button>

  <div class="login-stack">
    <div class="login-brand">
      <img src="/logo-mark.svg" alt="" width="28" height="28" />
      <span>Dockmesh</span>
    </div>

  {#if !mfaToken}
    <form class="login-card" onsubmit={submit}>
      <Eyebrow active>Sign in</Eyebrow>

      <div class="login-fields">
        <Field label="Username">
          <input
            class="ed-input ed-input-mono"
            bind:value={username}
            disabled={loading}
            autocomplete="username"
            autocapitalize="off"
            spellcheck="false"
          />
        </Field>

        <Field label="Password">
          {#snippet right()}
            <a class="login-aside" href="#forgot" onclick={(e) => e.preventDefault()}>Forgot?</a>
          {/snippet}
          <input
            class="ed-input ed-input-mono"
            type="password"
            bind:value={password}
            disabled={loading}
            autocomplete="current-password"
          />
        </Field>

        <label class="login-remember">
          <span class="login-check" class:checked={remember} aria-hidden="true">
            {#if remember}
              <svg viewBox="0 0 24 24" width="10" height="10" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round">
                <path d="m5 12 5 5L20 7" />
              </svg>
            {/if}
          </span>
          <input
            type="checkbox"
            bind:checked={remember}
            class="visually-hidden"
          />
          Remember this device
        </label>

        {#if error}
          <p class="login-error" role="alert">{error}</p>
        {/if}

        <button
          type="submit"
          class="dm-btn dm-btn-primary login-submit"
          disabled={!canSubmit}
        >
          {loading ? 'Signing in…' : 'Sign in'}
          <ArrowRight size={14} strokeWidth={1.5} />
        </button>

        {#if providers.length > 0}
          <div class="login-divider">
            <span class="login-divider-line"></span>
            <span class="login-divider-label">or</span>
            <span class="login-divider-line"></span>
          </div>

          <div class="login-sso">
            {#each providers as p (p.slug)}
              <button
                type="button"
                class="dm-btn dm-btn-secondary"
                onclick={() => ssoLogin(p.slug)}
              >
                Continue with {p.display_name}
              </button>
            {/each}
          </div>
        {/if}
      </div>
    </form>
  {:else}
    <form class="login-card" onsubmit={submitMFA}>
      <Eyebrow active>Two-factor</Eyebrow>
      <h1 class="ed-title login-title">
        Enter the <em>code</em>.
      </h1>
      <p class="ed-subtitle login-subtitle">
        Six digits from your authenticator app, or one of the recovery codes you saved.
      </p>

      <div class="login-fields">
        <Field label="Code">
          {#snippet right()}
            <span class="login-aside" style="display: inline-flex; align-items: center; gap: 4px;">
              <ShieldCheck size={11} strokeWidth={1.5} /> 6-digit
            </span>
          {/snippet}
          <input
            class="ed-input ed-input-mono"
            bind:value={mfaCode}
            disabled={loading}
            autocomplete="one-time-code"
            inputmode="text"
            maxlength="64"
          />
        </Field>

        {#if error}
          <p class="login-error" role="alert">{error}</p>
        {/if}

        <button
          type="submit"
          class="dm-btn dm-btn-primary login-submit"
          disabled={!canVerify}
        >
          {loading ? 'Verifying…' : 'Verify'}
          <ArrowRight size={14} strokeWidth={1.5} />
        </button>

        <button
          type="button"
          class="dm-btn dm-btn-ghost login-cancel"
          onclick={cancelMFA}
        >
          Cancel and go back
        </button>
      </div>
    </form>
  {/if}

    <p class="login-footer">
      Dockmesh · self-hosted · {new Date().getFullYear()} · AGPL-3.0
    </p>
  </div>
</div>

<style>
  .login-stack {
    position: relative;
    z-index: 2;
    width: 380px;
    max-width: calc(100vw - 32px);
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 20px;
  }

  .login-brand {
    display: inline-flex;
    align-items: center;
    gap: 10px;
    color: var(--fg);
    font-size: 17px;
    font-weight: 600;
    letter-spacing: -0.005em;
  }
  .login-brand img {
    color: var(--color-brand-400);
    display: inline-block;
  }

  .login-card {
    width: 100%;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 28px 32px 32px;
    box-shadow: 0 1px 2px rgba(0, 0, 0, 0.04), 0 8px 24px -12px rgba(0, 0, 0, 0.12);
  }

  .login-chrome-tr {
    position: absolute;
    z-index: 4;
    top: 28px;
    right: 32px;
  }

  .login-title {
    font-size: 22px;
    margin-top: 14px;
    line-height: 1.2;
    max-width: 20ch;
  }
  .login-subtitle {
    font-size: 13.5px;
    margin-top: 8px;
    color: var(--fg-muted);
  }

  .login-fields {
    margin-top: 20px;
    display: flex;
    flex-direction: column;
    gap: 18px;
  }

  .login-aside {
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: var(--fg-subtle);
    text-decoration: none;
    letter-spacing: 0.04em;
  }
  .login-aside:hover { color: var(--fg-muted); }

  .login-remember {
    display: inline-flex;
    align-items: center;
    gap: 9px;
    cursor: pointer;
    font-size: 13px;
    color: var(--fg-muted);
    user-select: none;
  }
  .login-check {
    width: 14px;
    height: 14px;
    border: 1px solid var(--border-strong);
    border-radius: 3px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    color: transparent;
    transition: background 0.12s, border-color 0.12s, color 0.12s;
  }
  .login-check.checked {
    background: var(--color-brand-500);
    border-color: var(--color-brand-500);
    color: #02181f;
  }
  :global(:root[data-theme='light']) .login-check.checked { color: #fff; }
  .visually-hidden {
    position: absolute;
    width: 1px;
    height: 1px;
    padding: 0;
    margin: -1px;
    overflow: hidden;
    clip: rect(0, 0, 0, 0);
    white-space: nowrap;
    border: 0;
  }

  .login-error {
    margin: 0;
    padding: 10px 12px;
    border: 1px solid color-mix(in srgb, var(--color-danger-500) 40%, var(--border));
    background: color-mix(in srgb, var(--color-danger-500) 8%, transparent);
    border-radius: 5px;
    color: var(--color-danger-400);
    font-size: 12.5px;
    line-height: 1.5;
  }

  .login-submit {
    margin-top: 6px;
    padding: 0.65rem 1rem;
  }

  .login-divider {
    display: flex;
    align-items: center;
    gap: 12px;
    margin-top: 6px;
    color: var(--fg-subtle);
    font-size: 11.5px;
    font-family: var(--font-mono);
    letter-spacing: 0.14em;
    text-transform: uppercase;
  }
  .login-divider-line {
    height: 1px;
    background: var(--border);
    flex: 1;
  }
  .login-divider-label { line-height: 1; }

  .login-sso {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }

  .login-cancel {
    margin-top: 4px;
    align-self: flex-start;
    padding-left: 0;
    color: var(--fg-subtle);
  }
  .login-cancel:hover { color: var(--fg); }

  .login-footer {
    margin: 8px 0 0;
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
    letter-spacing: 0.14em;
    text-transform: uppercase;
    text-align: center;
  }
</style>
