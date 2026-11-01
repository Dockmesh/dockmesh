<script lang="ts">
  import { untrack } from 'svelte';
  import { goto } from '$app/navigation';
  import { api, ApiError, type AttentionItem, type NotificationItem, type Operation } from '$lib/api';
  import { fmtAgo } from '$lib/format';
  import { urlTab } from '$lib/urlTab.svelte';
  import { autoRefresh } from '$lib/autorefresh';
  import { hosts } from '$lib/stores/host.svelte';
  import { notices } from '$lib/stores/notices.svelte';
  import { operations } from '$lib/stores/operations.svelte';
  import { toast } from '$lib/stores/toast.svelte';
  import { withoutBanner } from '$lib/notices';
  import { sortAttention } from '$lib/attention';
  import { inAppLink } from '$lib/links';
  import { buildFeed, CATEGORY_LABEL, type FeedCategory, type FeedEntry } from '$lib/activity';
  import { Tabs, Segmented, StatusChip, Menu, EmptyState, Skeleton, LiveIndicator } from '$lib/components/ui';
  import { sevTone } from '../alerts/ruleKinds';
  import { CheckCheck } from 'lucide-svelte';

  const TABS = ['attention', 'notifications'] as const;
  const tabs = urlTab(TABS, 'attention');
  const tab = $derived(tabs.current);

  let attention = $state<AttentionItem[] | null>(null);
  let items = $state<NotificationItem[]>([]);
  let ops = $state<Operation[]>([]);
  let loaded = $state(false);
  let refreshing = $state(false);
  let lastLoaded = $state<number | null>(null);

  let sev = $state('');
  let hostF = $state('');
  let readF = $state('all');
  let catF = $state('');

  async function load() {
    refreshing = true;
    try {
      const [a, n, o] = await Promise.all([
        api.attention.list().catch(() => null),
        api.notifications.list({ limit: 500 }).catch(() => [] as NotificationItem[]),
        api.operations.list({ limit: 100 }).catch(() => [] as Operation[])
      ]);
      attention = a;
      items = n;
      ops = o;
      lastLoaded = Date.now();
    } finally {
      loaded = true;
      refreshing = false;
    }
  }
  $effect(() => {
    untrack(load);
  });
  $effect(() => autoRefresh(load, 30_000));

  const att = $derived(attention ? sortAttention(withoutBanner(attention, notices.items)) : []);
  const hostName = (id: string) => hosts.available.find((h) => h.id === id)?.name ?? id;
  const attHosts = $derived([...new Set(att.map((a) => a.object?.host_id).filter((h): h is string => !!h))]);
  const attShown = $derived(att.filter((a) => (!sev || a.severity === sev) && (!hostF || a.object?.host_id === hostF)));
  const sevCount = (s: string) => att.filter((a) => (!s || a.severity === s) && (!hostF || a.object?.host_id === hostF)).length;

  const feed = $derived(buildFeed(items, ops, notices.items));
  const unread = $derived(feed.filter((e) => e.unread).length);
  const cats = $derived([...new Set(feed.map((e) => e.category))] as FeedCategory[]);
  const feedShown = $derived(feed.filter((e) => (readF === 'all' || e.unread) && (!catF || e.category === catF)));

  async function markRead(e: FeedEntry) {
    const n = e.notification;
    if (!n || n.read_at) return;
    try {
      await api.notifications.markRead(n.id);
      items = items.map((x) => (x.id === n.id ? { ...x, read_at: new Date().toISOString() } : x));
    } catch {
      /* the entry stays unread */
    }
  }
  async function markAllRead() {
    try {
      await api.notifications.markAllRead();
      const now = new Date().toISOString();
      items = items.map((x) => (x.read_at ? x : { ...x, read_at: now }));
    } catch (err) {
      toast.error('Mark all read failed', err instanceof ApiError ? err.message : undefined);
    }
  }
  async function remove(e: FeedEntry) {
    const n = e.notification;
    if (!n) return;
    try {
      await api.notifications.delete(n.id);
      items = items.filter((x) => x.id !== n.id);
    } catch (err) {
      toast.error('Delete failed', err instanceof ApiError ? err.message : undefined);
    }
  }
  async function openEntry(e: FeedEntry) {
    await markRead(e);
    if (e.operation) operations.open(e.operation.id);
    else if (e.link) goto(e.link);
  }

  const ATT_COLS = '110px minmax(0, 1fr) 160px 90px 72px';
  const FEED_COLS = '150px minmax(0, 1fr) 110px 90px 36px';
</script>

<section class="dm-page">
  <div class="dm-ph">
    <h1>Activity</h1>
    <span class="dm-sub">{att.length} need attention · {unread} unread</span>
    <span class="dm-sp"></span>
    <LiveIndicator updatedAt={lastLoaded} onrefresh={load} busy={refreshing} />
    {#if tab === 'notifications' && unread > 0}
      <button type="button" class="dm-btn" onclick={markAllRead}><CheckCheck size={14} /> Mark all read</button>
    {/if}
  </div>

  <Tabs
    tabs={[
      { key: 'attention', label: 'Needs attention', count: att.length || null, dot: att.some((a) => a.severity === 'critical') },
      { key: 'notifications', label: 'Notifications', count: unread || null }
    ]}
    active={tab}
    onchange={(k) => tabs.set(k)}
    label="Activity"
  />

  {#if !loaded}
    <div class="dm-tbl">{#each Array(4) as _, i (i)}<div class="dm-tr" style="grid-template-columns: 1fr"><Skeleton width="60%" height="0.9rem" /></div>{/each}</div>
  {:else if tab === 'attention'}
    <div class="dm-toolbar">
      <Segmented
        label="Severity"
        size="sm"
        bind:value={sev}
        options={[
          { key: '', label: 'All', count: sevCount('') },
          { key: 'critical', label: 'Critical', count: sevCount('critical') },
          { key: 'warning', label: 'Warning', count: sevCount('warning') },
          { key: 'info', label: 'Info', count: sevCount('info') }
        ]}
      />
      {#if attHosts.length > 1}
        <select class="dm-filter" bind:value={hostF} aria-label="Host">
          <option value="">All hosts</option>
          {#each attHosts as h (h)}<option value={h}>{hostName(h)}</option>{/each}
        </select>
      {/if}
    </div>
    {#if attention === null}
      <EmptyState title="Could not load" description="The list of open problems is not available right now. It refreshes on its own." />
    {:else if att.length === 0}
      <EmptyState title="Nothing needs attention" description="Hosts are online, containers healthy and the last backups succeeded." />
    {:else}
      <div class="dm-tbl" role="table" aria-label="Needs attention">
        <div class="dm-tr dm-th" role="row" style="grid-template-columns: {ATT_COLS}">
          <span role="columnheader">Severity</span><span role="columnheader">Problem</span><span role="columnheader">Host</span><span role="columnheader">Since</span><span role="columnheader"></span>
        </div>
        {#each attShown as a (a.id)}
          <div class="dm-tr" role="row" style="grid-template-columns: {ATT_COLS}">
            <span role="cell"><StatusChip tone={sevTone(a.severity)} label={a.severity} /></span>
            <span role="cell" class="dm-trunc" title={a.detail ? `${a.title} — ${a.detail}` : a.title}><b>{a.title}</b>{#if a.detail}<span class="dm-dim"> — {a.detail}</span>{/if}</span>
            <span role="cell" class="dm-dim dm-trunc">{a.object?.host_id ? hostName(a.object.host_id) : '—'}</span>
            <span role="cell" class="dm-num dm-dim" title={a.since ? new Date(a.since).toLocaleString() : ''}>{a.since ? fmtAgo(a.since) : '—'}</span>
            <span role="cell" class="dm-tr-menu">{#if inAppLink(a.link)}<a class="dm-btn dm-btn-sm" href={inAppLink(a.link)}>Open</a>{/if}</span>
          </div>
        {:else}
          <div class="dm-empty">Nothing matches this filter.</div>
        {/each}
      </div>
    {/if}
  {:else}
    <div class="dm-toolbar">
      <Segmented label="Read state" size="sm" bind:value={readF} options={[{ key: 'all', label: 'All', count: feed.length }, { key: 'unread', label: 'Unread', count: unread }]} />
      <select class="dm-filter" bind:value={catF} aria-label="Type">
        <option value="">All types</option>
        {#each cats as c (c)}<option value={c}>{CATEGORY_LABEL[c]}</option>{/each}
      </select>
    </div>
    {#if feed.length === 0}
      <EmptyState title="No notifications yet" description="Fired and resolved alerts, finished operations, backups and notices show up here." />
    {:else}
      <div class="dm-tbl" role="table" aria-label="Notifications">
        <div class="dm-tr dm-th" role="row" style="grid-template-columns: {FEED_COLS}">
          <span role="columnheader">Result</span><span role="columnheader">What happened</span><span role="columnheader">Type</span><span role="columnheader">When</span><span role="columnheader"></span>
        </div>
        {#each feedShown as e (e.key)}
          <div class="dm-tr" class:unread={e.unread} role="row" style="grid-template-columns: {FEED_COLS}">
            <span role="cell"><StatusChip tone={e.tone} label={e.word} /></span>
            <span role="cell" class="dm-trunc">
              {#if e.link || e.operation}
                <button type="button" class="lnk" onclick={() => openEntry(e)} title={e.body ? `${e.title} — ${e.body}` : e.title}>{e.title}</button>
              {:else}
                <span title={e.body ? `${e.title} — ${e.body}` : e.title}>{e.title}</span>
              {/if}
              {#if e.body}<span class="dm-dim"> — {e.body}</span>{/if}
            </span>
            <span role="cell" class="dm-dim">{CATEGORY_LABEL[e.category]}</span>
            <span role="cell" class="dm-num dm-dim" title={new Date(e.ts).toLocaleString()}>{fmtAgo(e.ts)}</span>
            <span role="cell" class="dm-tr-menu">
              {#if e.notification}
                <Menu
                  label="Actions for {e.title}"
                  items={[
                    ...(e.unread ? [{ label: 'Mark read', onclick: () => markRead(e) }] : []),
                    { label: 'Delete', danger: true, onclick: () => remove(e) }
                  ]}
                />
              {/if}
            </span>
          </div>
        {:else}
          <div class="dm-empty">Nothing matches this filter.</div>
        {/each}
      </div>
    {/if}
  {/if}
</section>

<style>
  .lnk { background: none; border: 0; padding: 0; font: inherit; color: var(--fg); cursor: pointer; text-align: left; }
  .lnk:hover { text-decoration: underline; }
  .unread .lnk, .unread > span:nth-child(2) > span:first-child { font-weight: 600; }
  .dm-tr:not(.unread) .lnk { font-weight: 500; }
</style>
