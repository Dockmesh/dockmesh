<script lang="ts">
  import { m } from '$lib/paraglide/messages.js';
  import { RefreshCw } from 'lucide-svelte';

  // Pages keep polling and pass the time of their last successful load.
  let {
    updatedAt,
    onrefresh,
    busy = false,
    paused = false
  }: { updatedAt: number | null; onrefresh?: () => void; busy?: boolean; paused?: boolean } = $props();

  let now = $state(Date.now());
  $effect(() => {
    const t = setInterval(() => (now = Date.now()), 1000);
    return () => clearInterval(t);
  });

  const ago = $derived.by(() => {
    if (!updatedAt) return m.ui_live_loading();
    const s = Math.max(0, Math.round((now - updatedAt) / 1000));
    if (s < 60) return m.ui_live_updated_s({ n: s });
    const mins = Math.round(s / 60);
    if (mins < 60) return m.ui_live_updated_min({ n: mins });
    return m.ui_live_updated_h({ n: Math.round(mins / 60) });
  });
  const stale = $derived(!!updatedAt && now - updatedAt > 90_000);
</script>

<span class="live" class:stale class:paused>
  <span class="d" aria-hidden="true"></span>
  <span>{paused ? m.ui_live_paused() : m.ui_live_live()} · {ago}</span>
  {#if onrefresh}
    <button type="button" onclick={onrefresh} aria-label={m.ui_live_refresh_now()} title={m.ui_live_refresh_now()} disabled={busy}>
      <RefreshCw size={13} strokeWidth={1.8} class={busy ? 'spin' : ''} />
    </button>
  {/if}
</span>

<style>
  .live {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: var(--fs-xs);
    color: var(--subtle);
    white-space: nowrap;
  }
  .d { width: 7px; height: 7px; border-radius: 4px; background: var(--ok); }
  .stale .d { background: var(--warn); }
  .paused .d { background: var(--off); }
  button {
    width: 26px;
    height: 26px;
    border-radius: var(--r-sm);
    border: 1px solid transparent;
    background: none;
    color: var(--muted);
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    justify-content: center;
  }
  button:hover:not(:disabled) { border-color: var(--border); color: var(--fg); }
  button:disabled { opacity: 0.6; cursor: default; }
  .live :global(.spin) { animation: lv-spin 0.9s linear infinite; }
  @keyframes lv-spin { to { transform: rotate(360deg); } }
</style>
