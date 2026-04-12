<script lang="ts">
  import { goto } from '$app/navigation';
  import { auth } from '$lib/stores/auth.svelte';
  import { api, ApiError } from '$lib/api';
  import { Button, Input } from '$lib/components/ui';
  import { Lock, ShieldCheck } from 'lucide-svelte';

  let username = $state('admin');
  let password = $state('');
  let error = $state('');
  let loading = $state(false);

  // MFA step state
  let mfaToken = $state<string | null>(null);
  let mfaCode = $state('');

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
</script>

<div class="min-h-screen flex items-center justify-center p-6 relative overflow-hidden">
  <div class="absolute inset-0 bg-[var(--bg)]"></div>
  <div class="absolute inset-0 opacity-30"
       style="background: radial-gradient(ellipse 80% 50% at 50% -20%, var(--color-brand-500), transparent);"></div>
  <div class="absolute inset-0 opacity-20"
       style="background: radial-gradient(ellipse 60% 40% at 80% 100%, var(--color-brand-700), transparent);"></div>

  <div class="relative w-full max-w-sm dm-fade-in">
    <div class="flex flex-col items-center mb-8">
      <div class="w-14 h-14 rounded-2xl bg-gradient-to-br from-brand-400 to-brand-600 flex items-center justify-center shadow-xl mb-4">
        <span class="text-white font-bold text-2xl">D</span>
      </div>
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
