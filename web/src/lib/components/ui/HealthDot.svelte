<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { api, type HealthResponse } from '$lib/api';
  import { goto } from '$app/navigation';

  let health = $state<HealthResponse | null>(null);
  let open = $state(false);
  let error = $state<string | null>(null);
  let timer: ReturnType<typeof setInterval> | null = null;

  // HealthDot lives in the sidebar footer; the sidebar has overflow
  // clipping rules that would chop a popover positioned inside it.
  // We portal the popover via `position: fixed` and compute its
  // anchor from the button's bounding box each time we open.
  let popoverLeft = $state(0);
  let popoverBottom = $state(0);

  async function load() {
    try {
      health = await api.system.health();
      error = null;
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }

  onMount(() => {
    load();
    timer = setInterval(load, 30_000);
    document.addEventListener('click', onOutside);
  });
  onDestroy(() => {
    if (timer) clearInterval(timer);
    document.removeEventListener('click', onOutside);
  });

  let root: HTMLDivElement;
  let trigger: HTMLButtonElement;
  function onOutside(e: MouseEvent) {
    if (!open) return;
    if (root && !root.contains(e.target as Node)) open = false;
  }

  function toggle() {
    if (!open) {
      // Anchor above+aligned-to-left-edge of the trigger, just
      // outside the sidebar so we don't fight its overflow rules.
      const r = trigger.getBoundingClientRect();
      popoverLeft = Math.max(r.left, 12);
      popoverBottom = Math.max(window.innerHeight - r.top + 8, 12);
      load();
    }
    open = !open;
  }

  function jumpTo(route: string | undefined) {
    if (!route) return;
    open = false;
    goto(route);
  }

  // Worst-status colour classes. The dot itself is always visible, the
  // popover shows the breakdown.
  const statusRing: Record<string, string> = {
    ok:   'bg-[var(--color-success-500)]',
    warn: 'bg-[var(--color-warning-500)]',
    fail: 'bg-[var(--color-danger-500)]',
    off:  'bg-[var(--fg-muted)]'
  };
  const statusLabel: Record<string, string> = {
    ok: 'All systems healthy',
    warn: 'Something degraded',
    fail: 'Action required',
    off: 'Some features disabled'
  };
  const statusText: Record<string, string> = {
    ok:   'text-[var(--color-success-400)]',
    warn: 'text-[var(--color-warning-400)]',
    fail: 'text-[var(--color-danger-400)]',
    off:  'text-[var(--fg-muted)]'
  };
</script>

<div bind:this={root} class="relative">
  <button
    bind:this={trigger}
    type="button"
    class="relative w-7 h-7 rounded-md flex items-center justify-center hover:bg-[var(--surface)] transition-colors"
    title={health ? statusLabel[health.overall] : 'System health'}
    aria-label="System health"
    onclick={toggle}
  >
    <span class="relative flex items-center justify-center">
      <span class="absolute inline-flex h-2.5 w-2.5 rounded-full opacity-50 {health ? statusRing[health.overall] : 'bg-[var(--fg-muted)]'} {health?.overall === 'fail' ? 'animate-ping' : ''}"></span>
      <span class="relative inline-flex h-2 w-2 rounded-full {health ? statusRing[health.overall] : 'bg-[var(--fg-muted)]'}"></span>
    </span>
  </button>

  {#if open}
    <div
      class="fixed z-50 w-72 bg-[var(--card)] border border-[var(--border)] rounded-lg shadow-lg py-2 text-sm"
      style="left: {popoverLeft}px; bottom: {popoverBottom}px;"
    >
      <div class="px-3 py-1.5 border-b border-[var(--border)]">
        <div class="font-medium {health ? statusText[health.overall] : ''}">
          {health ? statusLabel[health.overall] : 'Loading…'}
        </div>
        {#if error}
          <div class="text-xs text-[var(--color-danger-400)] mt-0.5">Health query failed: {error}</div>
        {/if}
      </div>
      {#if health}
        <ul class="py-1">
          {#each health.checks as c (c.name)}
            <li>
              <button
                type="button"
                class="w-full text-left px-3 py-1.5 hover:bg-[var(--surface)] flex items-center gap-2.5"
                disabled={!c.link_to}
                onclick={() => jumpTo(c.link_to)}
                title={c.message || ''}
              >
                <span class="inline-flex h-2 w-2 rounded-full shrink-0 {statusRing[c.status]}"></span>
                <span class="flex-1 min-w-0">
                  <span class="block truncate">{c.label}</span>
                  {#if c.detail}
                    <span class="block text-xs text-[var(--fg-muted)] truncate">{c.detail}</span>
                  {/if}
                </span>
              </button>
            </li>
          {/each}
        </ul>
      {/if}
    </div>
  {/if}
</div>
