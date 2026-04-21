<script lang="ts">
  import { api, type UpdateStatus } from '$lib/api';
  import { auth } from '$lib/stores/auth.svelte';
  import { Download, X, ExternalLink } from 'lucide-svelte';

  let status = $state<UpdateStatus | null>(null);
  let dismissedVersion = $state<string>('');

  // Per-version dismiss: storing the dismissed LatestVersion means a
  // newer release (e.g. v0.1.1 after dismissing v0.1.0) surfaces the
  // banner again. Cross-tab sync via the storage event is overkill for
  // this; a reload is fine.
  const STORAGE_KEY = 'dm_update_dismissed_version';

  async function fetchStatus() {
    try {
      status = await api.system.updateStatus();
    } catch {
      status = null;
    }
  }

  $effect(() => {
    if (!auth.isAuthenticated) return;
    if (typeof localStorage !== 'undefined') {
      dismissedVersion = localStorage.getItem(STORAGE_KEY) ?? '';
    }
    fetchStatus();
    // Hourly refresh — banner picks up fresh data between the backend's
    // 24h polls without a page reload. Still cheap (one request/hour).
    const iv = setInterval(fetchStatus, 3_600_000);
    return () => clearInterval(iv);
  });

  function dismiss() {
    if (!status?.latest_version) return;
    dismissedVersion = status.latest_version;
    try {
      localStorage.setItem(STORAGE_KEY, status.latest_version);
    } catch { /* ignore quota errors */ }
  }

  const show = $derived(
    status !== null &&
    status.enabled &&
    status.update_available &&
    status.latest_version !== '' &&
    status.latest_version !== dismissedVersion
  );
</script>

{#if show && status}
  <div
    class="border-b border-cyan-500/30 bg-gradient-to-r from-cyan-500/10 via-cyan-500/5 to-transparent"
    role="status"
    aria-live="polite"
  >
    <div class="max-w-7xl mx-auto px-5 md:px-8 py-2.5 flex items-center gap-3 text-sm">
      <div class="w-8 h-8 rounded-lg bg-cyan-500/15 border border-cyan-500/40 flex items-center justify-center flex-shrink-0">
        <Download class="w-4 h-4 text-cyan-400" />
      </div>
      <div class="flex-1 min-w-0">
        {#if status.is_dev_build}
          <span class="font-medium text-[var(--fg)]">
            Release available: {status.latest_version}
          </span>
          <span class="text-[var(--fg-muted)] ml-1">
            · you're on a dev build
          </span>
        {:else}
          <span class="font-medium text-[var(--fg)]">
            Update available: {status.latest_version}
          </span>
          <span class="text-[var(--fg-muted)] ml-1">
            · running {status.current_version}
          </span>
        {/if}
      </div>
      {#if status.release_url}
        <a
          href={status.release_url}
          target="_blank"
          rel="noopener"
          class="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-md bg-cyan-500 hover:bg-cyan-400 text-slate-950 font-semibold text-xs transition-colors"
        >
          Release notes
          <ExternalLink class="w-3 h-3" />
        </a>
      {/if}
      <button
        type="button"
        onclick={dismiss}
        class="p-1.5 rounded-md text-[var(--fg-muted)] hover:bg-[var(--surface-hover)] hover:text-[var(--fg)] transition-colors"
        aria-label="Dismiss update notice"
      >
        <X class="w-4 h-4" />
      </button>
    </div>
  </div>
{/if}
