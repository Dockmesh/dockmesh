<script lang="ts">
  import { RefreshCw } from 'lucide-svelte';

  // "Live · updated 4 s ago" with a refresh button. Replaces per-page
  // re-poll buttons: pages keep polling and pass the time of the last
  // successful load.
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
    if (!updatedAt) return 'loading';
    const s = Math.max(0, Math.round((now - updatedAt) / 1000));
    if (s < 60) return `updated ${s} s ago`;
    const m = Math.round(s / 60);
    if (m < 60) return `updated ${m} min ago`;
    return `updated ${Math.round(m / 60)} h ago`;
  });
  const stale = $derived(!!updatedAt && now - updatedAt > 90_000);
</script>

<span class="live" class:stale class:paused>
  <span class="d" aria-hidden="true"></span>
  <span>{paused ? 'Paused' : 'Live'} · {ago}</span>
  {#if onrefresh}
    <button type="button" onclick={onrefresh} aria-label="Refresh now" title="Refresh now" disabled={busy}>
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
