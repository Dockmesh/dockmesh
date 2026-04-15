<script lang="ts">
  import { goto } from '$app/navigation';
  import { auth } from '$lib/stores/auth.svelte';
  import { api, ApiError } from '$lib/api';
  import { Button, Input } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { Lock, ShieldCheck } from 'lucide-svelte';

  let username = $state('admin');
  let password = $state('');
  let error = $state('');
  let loading = $state(false);

  // MFA step state
  let mfaToken = $state<string | null>(null);
  let mfaCode = $state('');

  // SSO providers
  let providers = $state<Array<{ slug: string; display_name: string }>>([]);

  async function loadProviders() {
    try {
      providers = await api.oidc.listPublic();
    } catch { /* ignore */ }
  }

  async function handleSSOHash() {
    if (typeof window === 'undefined') return;
    const hash = window.location.hash.slice(1);
    if (!hash) return;
    const params = new URLSearchParams(hash);
    const access = params.get('sso_access');
    const refresh = params.get('sso_refresh');
    if (!access || !refresh) return;

    // Clear hash so reloads don't re-trigger.
    history.replaceState(null, '', window.location.pathname + window.location.search);

    // Set tokens so we can fetch /me.
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
      error = err instanceof ApiError && err.status === 401 ? 'Invalid username or password' : 'Login failed';
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
      error = err instanceof ApiError && err.status === 401 ? 'Invalid code' : 'Verification failed';
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
    // Full redirect — backend sets the state cookie + redirects to provider.
    window.location.href = `/api/v1/auth/oidc/${slug}/login`;
  }
</script>

<div class="min-h-screen flex items-center justify-center p-6 relative overflow-hidden">
  <div class="absolute inset-0 bg-[var(--bg)]"></div>
  <div class="absolute inset-0 opacity-30"
       style="background: radial-gradient(ellipse 80% 50% at 50% -20%, var(--color-brand-500), transparent);"></div>
  <div class="absolute inset-0 opacity-20"
       style="background: radial-gradient(ellipse 60% 40% at 80% 100%, var(--color-brand-700), transparent);"></div>

  <div class="relative w-full max-w-sm dm-fade-in">
    <div class="flex flex-col items-center mb-8">
      <img src="/logo-mark.svg" alt="Dockmesh" class="w-14 h-14 mb-4 drop-shadow-xl" />
      <h1 class="text-2xl font-semibold tracking-tight">
        {mfaToken ? 'Two-factor authentication' : 'Welcome to Dockmesh'}
      </h1>
      <p class="text-sm text-[var(--fg-muted)] mt-1">
        {mfaToken ? 'Enter the code from your authenticator' : 'Sign in to manage your containers'}
      </p>
    </div>

    {#if !mfaToken}
      <form onsubmit={submit} class="dm-card p-6 space-y-4 shadow-2xl">
        <Input
          label="Username"
          placeholder="admin"
          bind:value={username}
          disabled={loading}
          autocomplete="username"
        />
        <Input
          label="Password"
          type="password"
          placeholder="••••••••"
          bind:value={password}
          disabled={loading}
          autocomplete="current-password"
        />

        {#if error}
          <div class="flex items-start gap-2 text-xs text-[var(--color-danger-400)] bg-[color-mix(in_srgb,var(--color-danger-500)_10%,transparent)] border border-[color-mix(in_srgb,var(--color-danger-500)_25%,transparent)] rounded-lg px-3 py-2">
            <Lock class="w-3.5 h-3.5 shrink-0 mt-0.5" />
            <span>{error}</span>
          </div>
        {/if}

        <Button type="submit" variant="primary" class="w-full" {loading} disabled={loading || !username || !password}>
          {loading ? 'Signing in…' : 'Sign in'}
        </Button>

        {#if providers.length > 0}
          <div class="relative my-3">
            <div class="absolute inset-0 flex items-center">
              <div class="w-full border-t border-[var(--border)]"></div>
            </div>
            <div class="relative flex justify-center">
              <span class="bg-[var(--surface)] px-2 text-xs text-[var(--fg-subtle)] uppercase tracking-wider">or</span>
            </div>
          </div>
          <div class="space-y-2">
            {#each providers as p}
              <button
                type="button"
                class="dm-btn dm-btn-secondary w-full"
                onclick={() => ssoLogin(p.slug)}
              >
                Sign in with {p.display_name}
              </button>
            {/each}
          </div>
        {/if}
      </form>
    {:else}
      <form onsubmit={submitMFA} class="dm-card p-6 space-y-4 shadow-2xl">
        <div class="flex items-center gap-2 text-sm text-[var(--fg-muted)]">
          <ShieldCheck class="w-4 h-4 text-[var(--color-brand-400)]" />
          Enter the 6-digit code or a recovery code
        </div>

        <Input
          label="Code"
          placeholder="000000"
          bind:value={mfaCode}
          disabled={loading}
          autocomplete="one-time-code"
          inputmode="text"
        />

        {#if error}
          <div class="flex items-start gap-2 text-xs text-[var(--color-danger-400)] bg-[color-mix(in_srgb,var(--color-danger-500)_10%,transparent)] border border-[color-mix(in_srgb,var(--color-danger-500)_25%,transparent)] rounded-lg px-3 py-2">
            <Lock class="w-3.5 h-3.5 shrink-0 mt-0.5" />
            <span>{error}</span>
          </div>
        {/if}

        <Button type="submit" variant="primary" class="w-full" {loading} disabled={loading || mfaCode.length < 6}>
          {loading ? 'Verifying…' : 'Verify'}
        </Button>
        <button
          type="button"
          class="text-xs text-[var(--fg-muted)] hover:text-[var(--fg)] w-full"
          onclick={cancelMFA}
        >
          Cancel and go back
        </button>
      </form>
    {/if}

    <p class="text-center text-xs text-[var(--fg-subtle)] mt-6">
      Filesystem is source of truth · 100% open source · AGPL-3.0
    </p>
  </div>
</div>
