<script lang="ts">
  import { goto } from '$app/navigation';
  import { auth } from '$lib/stores/auth.svelte';
  import { api, ApiError } from '$lib/api';

  let username = $state('admin');
  let password = $state('');
  let error = $state('');
  let loading = $state(false);

  async function submit(e: Event) {
    e.preventDefault();
    error = '';
    loading = true;
    try {
      const res = await api.auth.login(username, password);
      auth.setSession(res.user, res.access_token, res.refresh_token);
      goto('/');
    } catch (err) {
      if (err instanceof ApiError) {
        error = err.status === 401 ? 'Invalid credentials' : err.message;
      } else {
        error = 'Login failed';
      }
    } finally {
      loading = false;
    }
  }
</script>

<div class="min-h-screen flex items-center justify-center bg-[var(--bg)]">
  <div class="w-full max-w-sm p-6 rounded border border-[var(--border)] bg-[var(--panel)]">
    <h2 class="text-2xl font-bold mb-1">Dockmesh</h2>
    <p class="text-sm text-[var(--muted)] mb-6">Sign in to continue</p>
    <form onsubmit={submit} class="space-y-3">
      <input
        class="w-full px-3 py-2 rounded border border-[var(--border)] bg-[var(--bg)]"
        placeholder="Username"
        bind:value={username}
        disabled={loading}
        autocomplete="username"
      />
      <input
        class="w-full px-3 py-2 rounded border border-[var(--border)] bg-[var(--bg)]"
        type="password"
        placeholder="Password"
        bind:value={password}
        disabled={loading}
        autocomplete="current-password"
      />
      {#if error}
        <div class="text-sm text-red-500">{error}</div>
      {/if}
      <button
        type="submit"
        class="w-full py-2 rounded bg-brand-500 text-white font-semibold disabled:opacity-50"
        disabled={loading || !username || !password}
      >
        {loading ? 'Signing in…' : 'Sign in'}
      </button>
    </form>
  </div>
</div>
