<script lang="ts">
  import { goto } from '$app/navigation';
  import { api, type NotificationItem } from '$lib/api';
  import { fmtAgo } from '$lib/format';
  import { badgeText } from '$lib/activity';
  import { Bell } from 'lucide-svelte';

  // Bell in the top bar: unread count and a short list of the latest
  // notifications. The full history lives on the Activity page.
  const LIMIT = 8;

  let open = $state(false);
  let unread = $state(0);
  let items = $state<NotificationItem[]>([]);
  let loading = $state(false);
  let root = $state<HTMLElement | null>(null);
  let trigger = $state<HTMLButtonElement | null>(null);

  const badge = $derived(badgeText(unread));

  $effect(() => {
    let cancelled = false;
    async function refresh() {
      try {
        const r = await api.notifications.unreadCount();
        if (!cancelled) unread = r.unread;
      } catch {
        /* keep the last count on poll failures */
      }
    }
    refresh();
    const id = setInterval(refresh, 30_000);
    return () => {
      cancelled = true;
      clearInterval(id);
    };
  });

  async function loadList() {
    loading = true;
    try {
      items = await api.notifications.list({ limit: LIMIT });
    } catch {
      items = [];
    } finally {
      loading = false;
    }
  }

  function toggle() {
    open = !open;
    if (open) loadList();
  }
  function close(refocus = false) {
    open = false;
    if (refocus) trigger?.focus();
  }

  $effect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (root && !root.contains(e.target as Node)) close();
    };
    document.addEventListener('mousedown', onDown);
    return () => document.removeEventListener('mousedown', onDown);
  });

  async function openItem(n: NotificationItem) {
    if (!n.read_at) {
      try {
        await api.notifications.markRead(n.id);
        unread = Math.max(0, unread - 1);
      } catch {
        /* opening still works */
      }
    }
    close();
    if (n.link) goto(n.link);
  }

  async function markAllRead() {
    try {
      await api.notifications.markAllRead();
      const now = new Date().toISOString();
      items = items.map((n) => (n.read_at ? n : { ...n, read_at: now }));
      unread = 0;
    } catch {
      /* the count refreshes on the next poll */
    }
  }

  const TONE: Record<string, string> = { success: 'ok', warning: 'warn', error: 'bad', critical: 'bad' };
</script>

<div class="nc" bind:this={root}>
  <button
    type="button"
    class="tb-btn tb-icon bell"
    bind:this={trigger}
    onclick={toggle}
    aria-haspopup="dialog"
    aria-expanded={open}
    aria-label={unread ? `Notifications, ${unread} unread` : 'Notifications'}
    title="Notifications"
  >
    <Bell size={15} />
    {#if badge}<span class="count" aria-hidden="true">{badge}</span>{/if}
  </button>

  {#if open}
    <div
      class="pop"
      role="dialog"
      aria-label="Latest notifications"
      tabindex="-1"
      onkeydown={(e) => {
        if (e.key === 'Escape') {
          e.stopPropagation();
          close(true);
        }
      }}
    >
      <div class="h">
        <b>Notifications</b>
        <span class="dm-sp"></span>
        {#if unread > 0}<button type="button" class="dm-btn dm-btn-ghost dm-btn-xs" onclick={markAllRead}>Mark all read</button>{/if}
      </div>
      {#if loading && items.length === 0}
        <div class="empty">Loading…</div>
      {:else if items.length === 0}
        <div class="empty">No notifications yet.</div>
      {:else}
        <ul>
          {#each items as n (n.id)}
            <li>
              <button type="button" class="it" class:unread={!n.read_at} onclick={() => openItem(n)} title={n.body ?? n.title}>
                <span class="dm-dot {TONE[n.severity] ?? ''}"></span>
                <span class="t">{n.title}</span>
                <span class="k dm-num" title={new Date(n.created_at).toLocaleString()}>{fmtAgo(n.created_at)}</span>
              </button>
            </li>
          {/each}
        </ul>
      {/if}
      <a class="all" href="/activity?tab=notifications" onclick={() => close()}>View all in Activity</a>
    </div>
  {/if}
</div>

<style>
  .nc { position: relative; }
  .bell { position: relative; }
  .count {
    position: absolute;
    top: -5px;
    right: -5px;
    min-width: 16px;
    height: 16px;
    padding: 0 4px;
    border-radius: 8px;
    background: var(--bad);
    color: #fff;
    font-size: 10px;
    font-weight: 700;
    line-height: 16px;
    text-align: center;
    font-variant-numeric: tabular-nums;
  }
  :global([data-theme='dark']) .count { color: #1a0505; }
  .pop {
    position: absolute;
    right: 0;
    top: calc(100% + 6px);
    width: 360px;
    background: var(--menu);
    border: 1px solid var(--border);
    border-radius: var(--r-lg);
    box-shadow: var(--pop);
    z-index: 60;
    padding: 4px;
    outline: none;
  }
  .h { display: flex; align-items: center; gap: 8px; padding: 8px 10px 6px; font-size: var(--fs-sm); }
  ul { list-style: none; margin: 0; padding: 0; }
  .it {
    display: flex;
    align-items: center;
    gap: 10px;
    width: 100%;
    min-height: 34px;
    padding: 0 10px;
    border: 0;
    border-radius: var(--r-sm);
    background: none;
    color: var(--muted);
    font: inherit;
    font-size: var(--fs-sm);
    cursor: pointer;
    text-align: left;
  }
  .it:hover, .it:focus-visible { background: var(--field); color: var(--fg); outline: none; }
  .it.unread { color: var(--fg); font-weight: 600; }
  .t { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .k { color: var(--subtle); font-size: var(--fs-xs); font-weight: 400; }
  .empty { padding: 18px 10px; color: var(--subtle); font-size: var(--fs-sm); text-align: center; }
  .all {
    display: block;
    margin-top: 4px;
    padding: 8px 10px;
    border-top: 1px solid var(--border);
    color: var(--accent);
    font-size: var(--fs-sm);
    text-decoration: none;
  }
  .all:hover { text-decoration: underline; }
</style>
