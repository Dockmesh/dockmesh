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

  // Worst-status colour + motion. The dot is always visible; motion
  // (glow + ring + ping) scales with severity so a user scanning the
  // sidebar at a glance gets the right urgency signal:
  //   ok   — steady green glow
  //   warn — pulsing yellow
  //   fail — red with expanding ping
  //   off  — dim grey, no motion
  const statusRing: Record<string, string> = {
    ok:   'bg-emerald-500',
    warn: 'bg-amber-500',
    fail: 'bg-rose-500',
    off:  'bg-slate-500'
  };
  const statusGlow: Record<string, string> = {
    ok:   'shadow-[0_0_10px_2px_rgba(16,185,129,0.65)]',
    warn: 'shadow-[0_0_10px_2px_rgba(245,158,11,0.75)]',
    fail: 'shadow-[0_0_12px_3px_rgba(244,63,94,0.85)]',
    off:  ''
  };
  const statusMotion: Record<string, string> = {
    ok:   '',
    warn: 'animate-pulse',
    fail: 'animate-ping',
    off:  ''
  };
  const statusLabel: Record<string, string> = {
    ok: 'All systems healthy',
    warn: 'Something degraded',
    fail: 'Action required',
    off: 'Some features disabled'
  };
  const statusText: Record<string, string> = {
    ok:   'text-emerald-400',
    warn: 'text-amber-400',
    fail: 'text-rose-400',
    off:  'text-slate-400'
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
      {#if health && health.overall !== 'off'}
        <!-- Animated halo — ping for fail, pulse for warn, slow pulse
             for ok. `off` renders no halo so the dot stays calm when
             features are merely disabled rather than broken. -->
        <span
          class="absolute inline-flex h-3 w-3 rounded-full opacity-60 {statusRing[health.overall]} {statusMotion[health.overall] || 'animate-pulse'}"
        ></span>
      {/if}
      <span
        class="relative inline-flex h-2 w-2 rounded-full {health ? statusRing[health.overall] : 'bg-slate-500'} {health ? statusGlow[health.overall] : ''}"
      ></span>
    </span>
  </button>

  {#if open}
    <div
      class="fixed z-50 w-72 bg-[var(--surface)] border border-[var(--border)] rounded-lg shadow-xl py-2 text-sm backdrop-blur"
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
