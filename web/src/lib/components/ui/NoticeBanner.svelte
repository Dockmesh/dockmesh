<script lang="ts">
  import { goto } from '$app/navigation';
  import { auth } from '$lib/stores/auth.svelte';
  import { notices } from '$lib/stores/notices.svelte';
  import { toast } from '$lib/stores/toast.svelte';
  import { ApiError, type Notice } from '$lib/api';
  import { bannerNotice, noticeAction, noticeSummary, sortNotices } from '$lib/notices';
  import NoticeLabel from './NoticeLabel.svelte';
  import NoticeBody from './NoticeBody.svelte';
  import { ArrowRight, ChevronDown, X } from 'lucide-svelte';

  $effect(() => {
    if (!auth.isAuthenticated) {
      notices.clear();
      return;
    }
    notices.refresh();
    const iv = setInterval(notices.refresh, 60_000);
    return () => clearInterval(iv);
  });

  let panelOpen = $state(false);
  const top = $derived(bannerNotice(notices.items));
  const all = $derived(sortNotices(notices.items));
  const more = $derived(top ? notices.items.length - 1 : 0);

  $effect(() => {
    if (!top) panelOpen = false;
  });

  async function dismiss(n: Notice) {
    try {
      await notices.dismiss(n);
    } catch (err) {
      toast.error('Dismiss failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  function open(n: Notice) {
    panelOpen = false;
    goto(n.link!);
  }

  const tone = {
    critical: 'border-[var(--bad-bd)] bg-[var(--bad-bg)]',
    warning: 'border-[var(--warn-bd)] bg-[var(--warn-bg)]',
    info: 'border-[var(--border)] bg-[var(--surface)]',
  };
  const actionBtn =
    'inline-flex items-center gap-1.5 px-3 py-1.5 rounded-md border border-[var(--border)] text-[var(--fg)] hover:bg-[var(--surface-hover)] text-xs font-medium transition-colors whitespace-nowrap';
  const quietBtn =
    'inline-flex items-center gap-1 px-2 py-1.5 rounded-md text-[var(--fg-muted)] hover:bg-[var(--surface-hover)] hover:text-[var(--fg)] text-xs font-medium transition-colors whitespace-nowrap';
</script>

<svelte:window onkeydown={(e) => { if (e.key === 'Escape') panelOpen = false; }} />

{#if top}
  <div class="app-band">
    <div class="rounded-[9px] border {tone[top.severity]}" role={top.severity === 'critical' ? 'alert' : 'status'}>
      <div class="px-4 py-2.5 flex flex-wrap items-center gap-x-3 gap-y-2 text-sm">
        <NoticeLabel severity={top.severity} />
        <button
          type="button"
          class="flex-1 min-w-[12rem] text-left truncate"
          onclick={() => (panelOpen = !panelOpen)}
          aria-expanded={panelOpen}
          aria-controls="notice-panel"
          title="Show the full text"
        >
          <span class="font-medium text-[var(--fg)]">{top.title}</span>
          {#if top.body}
            <span class="text-[var(--fg-muted)] ml-1">· {noticeSummary(top.body)}</span>
          {/if}
        </button>
        <div class="flex items-center gap-1.5 ml-auto">
          {#if more > 0}
            <button type="button" class={quietBtn} onclick={() => (panelOpen = !panelOpen)} aria-expanded={panelOpen} aria-controls="notice-panel">
              +{more} more {more === 1 ? 'notice' : 'notices'}
              <ChevronDown class="w-3 h-3 transition-transform {panelOpen ? 'rotate-180' : ''}" />
            </button>
          {/if}
          {#if top.link}
            <button type="button" class={actionBtn} onclick={() => open(top)}>
              {noticeAction(top)}
              <ArrowRight class="w-3 h-3" />
            </button>
          {/if}
          {#if top.dismissible}
            <button
              type="button"
              onclick={() => dismiss(top)}
              class="p-1.5 rounded-md text-[var(--fg-muted)] hover:bg-[var(--surface-hover)] hover:text-[var(--fg)] transition-colors"
              aria-label="Hide this notice from the banner"
              title="Hide from banner (stays in the notices until resolved)"
            >
              <X class="w-4 h-4" />
            </button>
          {/if}
        </div>
      </div>
    </div>

    {#if panelOpen}
      <section id="notice-panel" aria-label="Notices" class="mt-2 rounded-md border border-[var(--border)] bg-[var(--surface)]">
        <div class="flex items-center justify-between px-4 py-2 border-b border-[var(--border)]">
          <span class="font-mono text-[10.5px] uppercase tracking-[0.06em] text-[var(--fg-muted)]">Notices · {all.length}</span>
          <button type="button" class={quietBtn} onclick={() => (panelOpen = false)} aria-label="Close notices">
            <X class="w-3.5 h-3.5" /> Close
          </button>
        </div>
        <ul class="divide-y divide-[var(--border)]">
          {#each all as n (n.id)}
            <li class="px-4 py-3 text-sm">
              <div class="flex flex-wrap items-center gap-x-3 gap-y-1.5">
                <NoticeLabel severity={n.severity} />
                <span class="font-medium text-[var(--fg)] flex-1 min-w-[12rem]">{n.title}</span>
                <div class="flex items-center gap-1.5 ml-auto">
                  {#if n.dismissible && !n.dismissed && n.severity !== 'info'}
                    <button type="button" class={quietBtn} onclick={() => dismiss(n)} title="Stays here until resolved">Hide from banner</button>
                  {/if}
                  {#if n.link}
                    <button type="button" class={actionBtn} onclick={() => open(n)}>
                      {noticeAction(n)}
                      <ArrowRight class="w-3 h-3" />
                    </button>
                  {/if}
                </div>
              </div>
              {#if n.body}
                <div class="mt-2 max-w-[80ch]"><NoticeBody body={n.body} /></div>
              {/if}
            </li>
          {/each}
        </ul>
      </section>
    {/if}
  </div>
{/if}
