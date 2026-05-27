<script lang="ts">
  // Notification Center — bell icon in the top-bar + a right-aligned
  // side drawer. The drawer is portalled to document.body via a
  // Svelte action so it escapes any ancestor with `transform`,
  // `filter`, `perspective`, `will-change`, or `contain` set — any
  // of those re-roots `position: fixed` onto that ancestor (CSS
  // containing-block rule), which silently broke our previous
  // in-tree drawer rendering. Inline-styled across the board so the
  // editorial token cascade can't blank anything out either.
  import {
    api, ApiError,
    type NotificationItem,
  } from '$lib/api';
  import { toast } from '$lib/stores/toast.svelte';
  import { goto } from '$app/navigation';
  import {
    Bell, X, CheckCheck, CheckCircle2, XCircle, AlertTriangle, Info, Trash2,
  } from 'lucide-svelte';

  type Filter = 'all' | 'unread';

  let open = $state(false);
  let unread = $state(0);
  let items = $state<NotificationItem[]>([]);
  let loading = $state(false);
  let filter = $state<Filter>('all');

  $effect(() => {
    let cancelled = false;
    async function refresh() {
      try {
        if (open) {
          await loadList();
        } else {
          const r = await api.notifications.unreadCount();
          if (!cancelled) unread = r.unread;
        }
      } catch { /* tolerate poll failures */ }
    }
    refresh();
    const id = setInterval(refresh, 30_000);
    return () => { cancelled = true; clearInterval(id); };
  });

  async function loadList() {
    loading = true;
    try {
      items = await api.notifications.list({
        unread: filter === 'unread',
        limit: 100,
      });
      unread = items.filter((n) => !n.read_at).length;
    } catch (err) {
      toast.error('Load notifications failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      loading = false;
    }
  }

  async function markRead(n: NotificationItem) {
    if (n.read_at) return;
    try {
      await api.notifications.markRead(n.id);
      n.read_at = new Date().toISOString();
      unread = Math.max(0, unread - 1);
    } catch { /* non-fatal */ }
  }

  async function markAllRead() {
    try {
      await api.notifications.markAllRead();
      const now = new Date().toISOString();
      items = items.map((n) => n.read_at ? n : { ...n, read_at: now });
      unread = 0;
    } catch (err) {
      toast.error('Mark all read failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function remove(n: NotificationItem) {
    try {
      await api.notifications.delete(n.id);
      items = items.filter((x) => x.id !== n.id);
      if (!n.read_at) unread = Math.max(0, unread - 1);
    } catch (err) {
      toast.error('Delete failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function openItem(n: NotificationItem) {
    await markRead(n);
    if (n.link) {
      open = false;
      await goto(n.link);
    }
  }

  function openDrawer() {
    open = true;
    filter = 'all';
    loadList();
  }

  function severityColor(s: string): string {
    if (s === 'success') return '#16a34a';
    if (s === 'warning') return '#d97706';
    if (s === 'error')   return '#dc2626';
    return '#64748b';
  }

  function relTime(iso: string): string {
    const d = new Date(iso).getTime();
    const diff = Date.now() - d;
    const m = Math.floor(diff / 60_000);
    if (m < 1) return 'just now';
    if (m < 60) return `${m}m ago`;
    const h = Math.floor(m / 60);
    if (h < 24) return `${h}h ago`;
    const days = Math.floor(h / 24);
    if (days < 30) return `${days}d ago`;
    return new Date(iso).toLocaleDateString();
  }

  // Portal action — moves the node to document.body on mount so the
  // drawer's position:fixed is anchored to the viewport, not to some
  // transformed ancestor in the app shell.
  function portal(node: HTMLElement) {
    document.body.appendChild(node);
    return {
      destroy() {
        if (node.parentNode === document.body) {
          document.body.removeChild(node);
        }
      },
    };
  }

  // Use the editorial CSS variables — they're defined on :root and
  // get inherited even when the element is portalled to document.body
  // (CSS variables cascade through the entire document tree, not the
  // component tree). Fallbacks are dark-mode tones for the moment
  // before the theme cascade is applied.
  const dark    = 'var(--surface, #111c32)';
  const darker  = 'var(--bg, #0b1424)';
  const fg      = 'var(--fg, #f1f5f9)';
  const fgMuted = 'var(--fg-muted, #94a3b8)';
  const border  = 'var(--border, #1e293b)';
  const brand   = 'var(--color-brand-500, #2563eb)';
  // Light/dark-agnostic neutral fill — a 12% grey that reads on both
  // themes without needing two separate tokens.
  const subtleFill = 'rgba(127, 127, 127, 0.12)';
  const subtleFillStrong = 'rgba(127, 127, 127, 0.18)';
  const unreadTint = 'color-mix(in srgb, var(--color-brand-500, #2563eb) 8%, transparent)';
</script>

<button
  type="button"
  onclick={openDrawer}
  aria-label="Notifications · {unread} unread"
  title="Notifications"
  style="position: relative; display: inline-flex; align-items: center; justify-content: center; width: 26px; height: 26px; border: 1px solid transparent; border-radius: 6px; background: transparent; color: var(--fg-muted, #94a3b8); cursor: pointer;"
>
  <Bell size={13} strokeWidth={1.5} />
  {#if unread > 0}
    <span style="position: absolute; top: -3px; right: -3px; min-width: 14px; height: 14px; padding: 0 3px; border-radius: 7px; background: #dc2626; color: #ffffff; font-size: 9px; font-weight: 600; line-height: 14px; text-align: center;">
      {unread > 99 ? '99+' : unread}
    </span>
  {/if}
</button>

{#if open}
  <!-- Portalled to <body> so position:fixed actually anchors to the
       viewport. Backdrop + drawer share the same node so the action
       moves both together; the drawer sits on top via flex order. -->
  <div use:portal>
    <!-- Backdrop -->
    <div
      onclick={() => (open = false)}
      onkeydown={(e) => { if (e.key === 'Escape') open = false; }}
      role="button"
      tabindex="-1"
      aria-label="Close notifications"
      style="position: fixed; inset: 0; background: rgba(0, 0, 0, 0.45); z-index: 9998;"
    ></div>

    <!-- Side drawer -->
    <aside
      role="dialog"
      aria-label="Notifications"
      style="position: fixed; top: 0; right: 0; bottom: 0; width: min(440px, 92vw); background-color: {dark}; color: {fg}; border-left: 1px solid {border}; box-shadow: -12px 0 40px rgba(0, 0, 0, 0.5); display: flex; flex-direction: column; z-index: 9999; overflow: hidden;"
    >
      <!-- Header -->
      <div style="display: flex; align-items: center; justify-content: space-between; padding: 14px 18px; border-bottom: 1px solid {border}; background-color: {darker}; flex-shrink: 0;">
        <div style="display: inline-flex; align-items: center; gap: 10px; font-size: 14px; font-weight: 500; color: {fg};">
          <Bell size={15} strokeWidth={1.5} />
          <span>Notifications</span>
          {#if unread > 0}
            <span style="font-size: 11px; font-weight: 600; padding: 1px 8px; border-radius: 999px; background: #dc2626; color: #ffffff;">{unread}</span>
          {/if}
        </div>
        <button
          onclick={() => (open = false)}
          aria-label="Close"
          style="background: transparent; border: 0; padding: 6px; border-radius: 4px; color: {fgMuted}; cursor: pointer; display: inline-flex; align-items: center; justify-content: center;"
        >
          <X size={16} strokeWidth={1.5} />
        </button>
      </div>

      <!-- Toolbar -->
      <div style="display: flex; align-items: center; justify-content: space-between; padding: 10px 18px; border-bottom: 1px solid {border}; gap: 8px; background-color: {dark}; flex-shrink: 0;">
        <div style="display: flex; gap: 6px;">
          <button
            type="button"
            onclick={() => { filter = 'all'; loadList(); }}
            style="background-color: {filter === 'all' ? brand : subtleFill}; border: 1px solid {filter === 'all' ? brand : border}; border-radius: 999px; padding: 5px 14px; font-size: 12px; font-weight: 500; color: {filter === 'all' ? '#ffffff' : fg}; cursor: pointer;"
          >All</button>
          <button
            type="button"
            onclick={() => { filter = 'unread'; loadList(); }}
            style="background-color: {filter === 'unread' ? brand : subtleFill}; border: 1px solid {filter === 'unread' ? brand : border}; border-radius: 999px; padding: 5px 14px; font-size: 12px; font-weight: 500; color: {filter === 'unread' ? '#ffffff' : fg}; cursor: pointer;"
          >
            Unread{#if unread > 0} · {unread}{/if}
          </button>
        </div>
        {#if unread > 0}
          <button
            type="button"
            onclick={markAllRead}
            title="Mark all as read"
            style="background-color: transparent; border: 1px solid {border}; border-radius: 6px; color: {fgMuted}; font-size: 11.5px; cursor: pointer; display: inline-flex; align-items: center; gap: 5px; padding: 5px 10px;"
          >
            <CheckCheck size={12} strokeWidth={1.5} />
            <span>Mark all read</span>
          </button>
        {/if}
      </div>

      <!-- Body -->
      <div style="flex: 1 1 auto; overflow-y: auto; min-height: 0; background-color: {dark};">
        {#if loading && items.length === 0}
          <div style="padding: 64px 24px; text-align: center; color: {fgMuted}; font-size: 13px;">Loading…</div>
        {:else if items.length === 0}
          <div style="padding: 64px 24px 32px; text-align: center; display: flex; flex-direction: column; align-items: center; gap: 10px;">
            <div style="width: 56px; height: 56px; border-radius: 50%; background-color: rgba(127, 127, 127, 0.12); display: flex; align-items: center; justify-content: center; color: {fgMuted}; margin-bottom: 6px;">
              <Bell size={28} strokeWidth={1.2} />
            </div>
            <div style="font-size: 14px; font-weight: 500; color: {fg};">
              {filter === 'unread' ? "You're all caught up" : 'No notifications yet'}
            </div>
            <div style="font-size: 12px; color: {fgMuted}; max-width: 280px; line-height: 1.5;">
              {filter === 'unread'
                ? 'No unread notifications.'
                : 'Deploy events, alerts, and backup runs will appear here.'}
            </div>
          </div>
        {:else}
          <div>
            {#each items as n (n.id)}
              {@const sev = severityColor(n.severity)}
              <div
                style="position: relative; display: flex; align-items: stretch; border-bottom: 1px solid {border}; background-color: {!n.read_at ? unreadTint : 'transparent'};"
              >
                <div style="width: 3px; flex-shrink: 0; background-color: {sev};"></div>
                <button
                  type="button"
                  onclick={() => openItem(n)}
                  style="flex: 1; display: flex; align-items: flex-start; gap: 10px; padding: 12px 8px 12px 14px; background-color: transparent; border: 0; text-align: left; color: {fg}; cursor: pointer; min-width: 0;"
                >
                  <div style="flex-shrink: 0; margin-top: 1px; color: {sev};">
                    {#if n.severity === 'success'}
                      <CheckCircle2 size={16} strokeWidth={1.6} />
                    {:else if n.severity === 'warning'}
                      <AlertTriangle size={16} strokeWidth={1.6} />
                    {:else if n.severity === 'error'}
                      <XCircle size={16} strokeWidth={1.6} />
                    {:else}
                      <Info size={16} strokeWidth={1.6} />
                    {/if}
                  </div>
                  <div style="flex: 1; min-width: 0;">
                    <div style="font-size: 13px; line-height: 1.4; color: {fg}; font-weight: {!n.read_at ? 600 : 500};">
                      {n.title}
                    </div>
                    {#if n.body}
                      <div style="margin-top: 3px; font-size: 11.5px; color: {fgMuted}; line-height: 1.45; word-break: break-word;">
                        {n.body}
                      </div>
                    {/if}
                    <div style="margin-top: 5px; display: flex; align-items: center; gap: 10px; font-size: 10.5px; color: {fgMuted};">
                      <span>{relTime(n.created_at)}</span>
                      <span style="padding: 1px 6px; border-radius: 3px; background-color: rgba(127, 127, 127, 0.15); font-family: var(--font-mono, monospace);">{n.kind}</span>
                    </div>
                  </div>
                </button>
                <button
                  type="button"
                  onclick={() => remove(n)}
                  title="Remove"
                  aria-label="Remove notification"
                  style="background-color: transparent; border: 0; padding: 14px; color: {fgMuted}; cursor: pointer; display: flex; align-items: flex-start;"
                >
                  <Trash2 size={12} strokeWidth={1.5} />
                </button>
              </div>
            {/each}
          </div>
        {/if}
      </div>
    </aside>
  </div>
{/if}
