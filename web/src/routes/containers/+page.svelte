<script lang="ts">
  // Every container on the hosts in the lens, including ones no stack manages.
  import { untrack } from 'svelte';
  import { api, ApiError, isFanOut } from '$lib/api';
  import { latest } from '$lib/latest';
  import { goto } from '$app/navigation';
  import { toast } from '$lib/stores/toast.svelte';
  import { copyWithToast } from '$lib/clipboard';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { allowed } from '$lib/rbac.svelte';
  import { hosts } from '$lib/stores/host.svelte';
  import { EventStream, type ConnStatus } from '$lib/events';
  import { Skeleton, StatusChip, StatBar, Segmented, LiveIndicator, Menu, EmptyState } from '$lib/components/ui';
  import type { MenuItem } from '$lib/components/ui/Menu.svelte';
  import type { Tone } from '$lib/components/ui/StatusChip.svelte';
  import { Box, AlertTriangle } from 'lucide-svelte';

  const canControlAll = $derived(allowed('containers.update'));
  const canExecAll = $derived(allowed('containers.exec'));
  const isAll = $derived(hosts.isAll);

  interface Container {
    Id: string;
    Names: string[];
    Image: string;
    State: string;
    Status: string;
    Created: number;
    Ports: Array<{ PrivatePort: number; PublicPort?: number; Type: string }>;
    Labels?: Record<string, string>;
    host_id?: string;
    host_name?: string;
    restart_count?: number;
    image_update_available?: boolean;
  }

  let containers = $state<Container[]>([]);
  let unreachable = $state<Array<{ host_id: string; host_name: string; reason: string }>>([]);
  let loading = $state(true);
  let lastLoaded = $state<number | null>(null);
  let connStatus = $state<ConnStatus>('connecting');
  let reloadTimer: ReturnType<typeof setTimeout> | null = null;

  // Search + filter
  let search = $state('');
  type StateFilter = 'all' | 'running' | 'attention' | 'stopped' | 'standalone';
  let stateFilter = $state<StateFilter>('all');

  // Sort
  type SortKey = 'urgency' | 'name' | 'state' | 'image' | 'uptime';
  let sortKey = $state<SortKey>('urgency');

  type View = 'list' | 'host' | 'stack';
  let view = $state<View>(
    ((typeof localStorage !== 'undefined' && localStorage.getItem('dm_containers_view')) as View) || 'list'
  );
  $effect(() => {
    localStorage.setItem('dm_containers_view', view);
  });
  let sortAsc = $state(true);

  // Bulk selection
  let selected = $state<Set<string>>(new Set());
  let bulkBusy = $state(false);


  // ── Helpers ──────────────────────────────────────────────────────────
  function nameOf(c: Container): string {
    return (c.Names?.[0] ?? c.Id.slice(0, 12)).replace(/^\//, '');
  }
  function stackOf(c: Container): string | null {
    return c.Labels?.['com.docker.compose.project'] ?? null;
  }
  function parseHealth(status: string | undefined): 'healthy' | 'unhealthy' | 'starting' | null {
    if (!status) return null;
    if (/\(healthy\)/i.test(status)) return 'healthy';
    if (/\(unhealthy\)/i.test(status)) return 'unhealthy';
    if (/\(health:\s*starting\)/i.test(status)) return 'starting';
    return null;
  }
  function uptime(c: Container): string {
    if (!c.Created) return '—';
    const secs = Math.floor(Date.now() / 1000) - c.Created;
    if (secs < 60) return `${secs}s`;
    if (secs < 3600) return `${Math.floor(secs / 60)}m`;
    if (secs < 86400) return `${Math.floor(secs / 3600)}h ${Math.floor((secs % 3600) / 60)}m`;
    return `${Math.floor(secs / 86400)}d ${Math.floor((secs % 86400) / 3600)}h`;
  }
  function detailHref(c: Container, tab?: string): string {
    const h = c.host_id ?? hosts.id;
    const q = new URLSearchParams();
    if (h && (h !== 'local' || hosts.isAll)) q.set('host', h);
    if (tab) q.set('tab', tab);
    const qs = q.toString();
    return `/containers/${c.Id}${qs ? '?' + qs : ''}`;
  }
  function exitCodeFromStatus(s: string | undefined): number | null {
    if (!s) return null;
    const m = s.match(/Exited\s*\((\d+)\)/);
    return m ? parseInt(m[1], 10) : null;
  }
  function imageNameAndTag(image: string): { name: string; tag: string | null } {
    const idx = image.lastIndexOf(':');
    if (idx < 0 || idx < image.lastIndexOf('/')) return { name: image, tag: null };
    return { name: image.slice(0, idx), tag: image.slice(idx + 1) };
  }
  function statusOf(c: Container): { tone: Tone; label: string } {
    const health = parseHealth(c.Status);
    if (c.State === 'running') {
      if (health === 'unhealthy') return { tone: 'warn', label: 'unhealthy' };
      if (health === 'starting') return { tone: 'warn', label: 'starting' };
      return { tone: 'ok', label: 'running' };
    }
    if (c.State === 'restarting' || c.State === 'paused') return { tone: 'warn', label: c.State };
    if (c.State === 'dead') return { tone: 'bad', label: 'dead' };
    const exit = exitCodeFromStatus(c.Status);
    if (c.State === 'exited' && exit !== null && exit !== 0) return { tone: 'bad', label: `exited ${exit}` };
    return { tone: 'off', label: c.State || 'unknown' };
  }
  function needsAttention(c: Container): boolean {
    const t = statusOf(c).tone;
    return t === 'warn' || t === 'bad';
  }
  const TONE_ORDER: Record<Tone, number> = { bad: 0, warn: 1, ok: 2, acc: 2, off: 3 };

  function shortImage(name: string): string {
    return name.split('/').pop() ?? name;
  }
  function portsLabel(c: Container): string {
    const pub = (c.Ports ?? []).filter((p) => p.PublicPort);
    if (pub.length === 0) return (c.Ports ?? []).length ? 'internal' : '—';
    const first = `:${pub[0].PublicPort}`;
    return pub.length > 1 ? `${first} +${pub.length - 1}` : first;
  }
  function portsTitle(c: Container): string {
    return (c.Ports ?? []).map((p) => (p.PublicPort ? `${p.PublicPort}:${p.PrivatePort}/${p.Type}` : `${p.PrivatePort}/${p.Type}`)).join(', ');
  }
  function sortArrow(k: SortKey): string {
    return sortKey === k ? (sortAsc ? ' ↑' : ' ↓') : '';
  }

  // ── Counts + derived ────────────────────────────────────────────────
  const counts = $derived({
    all: containers.length,
    running: containers.filter((c) => c.State === 'running').length,
    attention: containers.filter(needsAttention).length,
    stopped: containers.filter((c) => c.State !== 'running' && c.State !== 'restarting').length,
    standalone: containers.filter((c) => !stackOf(c)).length,
  });

  const visible = $derived(
    containers
      .filter((c) => {
        if (stateFilter === 'running') return c.State === 'running';
        if (stateFilter === 'stopped') return c.State !== 'running' && c.State !== 'restarting';
        if (stateFilter === 'attention') return needsAttention(c);
        if (stateFilter === 'standalone') return !stackOf(c);
        return true;
      })
      .filter((c) => {
        if (!search.trim()) return true;
        const q = search.toLowerCase();
        const name = (c.Names?.[0] ?? '').toLowerCase();
        const img = c.Image.toLowerCase();
        const stack = (c.Labels?.['com.docker.compose.project'] ?? '').toLowerCase();
        return name.includes(q) || img.includes(q) || stack.includes(q) || c.Id.toLowerCase().startsWith(q);
      })
      .sort((a, b) => {
        let cmp = 0;
        switch (sortKey) {
          case 'urgency':
            cmp = TONE_ORDER[statusOf(a).tone] - TONE_ORDER[statusOf(b).tone] || nameOf(a).localeCompare(nameOf(b));
            break;
          case 'name':  cmp = nameOf(a).localeCompare(nameOf(b)); break;
          case 'state': cmp = a.State.localeCompare(b.State); break;
          case 'image': cmp = a.Image.localeCompare(b.Image); break;
          case 'uptime': cmp = (a.Created ?? 0) - (b.Created ?? 0); break;
        }
        return sortAsc ? cmp : -cmp;
      }),
  );

  const selectableRows = $derived(visible.filter((c) => !isSystem(c)));
  const allSelected = $derived(selectableRows.length > 0 && selectableRows.every((c) => selected.has(c.Id)));
  function toggleAll() {
    if (allSelected) selected = new Set();
    else selected = new Set(selectableRows.map((c) => c.Id));
  }
  function toggleOne(id: string) {
    const next = new Set(selected);
    if (next.has(id)) next.delete(id); else next.add(id);
    selected = next;
  }
  const cols = $derived(
    view === 'host'
      ? '22px 14px minmax(220px, 2.2fr) 120px minmax(140px, 1.4fr) 110px 90px 40px'
      : '22px 14px minmax(220px, 2.2fr) 120px 110px minmax(140px, 1.4fr) 110px 90px 40px'
  );

  interface Group {
    key: string;
    label: string;
    sub: string;
    href?: string;
    tone: string;
    items: Container[];
  }
  const groups = $derived.by<Group[]>(() => {
    const map = new Map<string, Group>();
    for (const c of visible) {
      let key: string;
      let g: Omit<Group, 'items' | 'tone'>;
      if (view === 'host') {
        key = c.host_id ?? hosts.id;
        g = { key, label: c.host_name || (hosts.selected?.name ?? 'local'), sub: '' };
      } else {
        const st = stackOf(c);
        key = st ?? '__standalone';
        g = st
          ? { key, label: st, sub: '', href: `/stacks/${encodeURIComponent(st)}` }
          : { key, label: 'Standalone', sub: 'not managed by Dockmesh' };
      }
      if (!map.has(key)) map.set(key, { ...g, tone: '', items: [] });
      map.get(key)!.items.push(c);
    }
    const out = [...map.values()];
    for (const g of out) {
      const worst = Math.min(...g.items.map((c) => TONE_ORDER[statusOf(c).tone]));
      g.tone = worst === 0 ? 'bad' : worst === 1 ? 'warn' : worst === 2 ? 'ok' : '';
      g.sub = [g.sub, `${g.items.length} container${g.items.length === 1 ? '' : 's'}`].filter(Boolean).join(' · ');
    }
    return out.sort((a, b) => (a.key === '__standalone' ? 1 : b.key === '__standalone' ? -1 : a.label.localeCompare(b.label)));
  });

  // Containers Dockmesh runs for itself are managed from their own page.
  const isSystem = (c: Container) => !!c.Labels?.['dockmesh.component'];

  function rowMenu(c: Container): MenuItem[] {
    const running = c.State === 'running';
    const stack = stackOf(c);
    const canControl = canControlAll && !isSystem(c);
    const canExec = canExecAll && !isSystem(c);
    const items: MenuItem[] = [
      { label: 'Open', kbd: '↵', href: detailHref(c) },
      { label: 'Logs', href: detailHref(c, 'logs') },
    ];
    if (canExec) items.push({ label: 'Terminal', href: detailHref(c, 'exec'), disabled: !running });
    items.push({ label: 'Inspect', href: detailHref(c, 'inspect') });
    if (canControl) {
      items.push({ separator: true });
      if (running) {
        items.push({ label: 'Restart', onclick: () => action(c, 'restart') }, { label: 'Stop', onclick: () => action(c, 'stop') });
      } else {
        items.push({ label: 'Start', onclick: () => action(c, 'start') });
      }
    }
    items.push({ separator: true }, { label: 'Copy ID', onclick: () => copyId(c.Id) });
    if (stack) items.push({ label: `Open stack ${stack}`, href: `/stacks/${encodeURIComponent(stack)}` });
    if (c.Labels?.['dockmesh.component'] === 'proxy') items.push({ label: 'Open reverse proxy', href: '/proxy' });
    if (canControl) items.push({ separator: true }, { label: 'Remove…', danger: true, onclick: () => action(c, 'remove') });
    return items;
  }

  function toggleSort(key: SortKey) {
    if (sortKey === key) sortAsc = !sortAsc;
    else { sortKey = key; sortAsc = true; }
  }

  // ── Data ────────────────────────────────────────────────────────────
  function scheduleReload() {
    if (reloadTimer) clearTimeout(reloadTimer);
    reloadTimer = setTimeout(load, 300);
  }
  const stream = new EventStream({
    onMessage: (msg) => {
      if (msg.source === 'docker' && msg.type === 'container') scheduleReload();
    },
    onStatus: (s) => { connStatus = s; },
  });

  const nextLoad = latest();
  async function load() {
    const current = nextLoad();
    loading = true;
    try {
      const res = await api.containers.list(true, hosts.id);
      if (!current()) return;
      if (isFanOut(res)) {
        containers = res.items as Container[];
        unreachable = res.unreachable_hosts;
      } else {
        containers = res as Container[];
        unreachable = [];
      }
      lastLoaded = Date.now();
    } catch (err) {
      if (current()) toast.error('Failed to load', err instanceof ApiError ? err.message : undefined);
    } finally {
      if (current()) loading = false;
    }
  }

  $effect(() => {
    hosts.id;
    untrack(load);
  });

  $effect(() => {
    untrack(() => stream.start());
    return () => {
      stream.stop();
      if (reloadTimer) clearTimeout(reloadTimer);
    };
  });

  // ── Actions ─────────────────────────────────────────────────────────
  async function action(c: Container, op: 'start' | 'stop' | 'restart' | 'remove') {
    const targetHost = c.host_id ?? hosts.id;
    try {
      if (op === 'start')   await api.containers.start(c.Id, targetHost);
      else if (op === 'stop')    await api.containers.stop(c.Id, targetHost);
      else if (op === 'restart') await api.containers.restart(c.Id, targetHost);
      else {
        if (!(await confirm.ask({
          title: 'Remove container',
          message: `Remove container "${nameOf(c)}"?`,
          body: 'Container volumes are kept. Image stays available for redeploy.',
          confirmLabel: 'Remove', danger: true,
        }))) return;
        await api.containers.remove(c.Id, true, targetHost);
      }
      toast.success(op, nameOf(c));
      await load();
    } catch (err) {
      toast.error(`${op} failed`, err instanceof ApiError ? err.message : undefined);
    }
  }

  async function bulkAction(op: 'start' | 'stop' | 'restart' | 'remove') {
    if (selected.size === 0) return;
    if (op === 'remove' && !(await confirm.ask({
      title: 'Remove containers',
      message: `Remove ${selected.size} container${selected.size === 1 ? '' : 's'}?`,
      body: 'Volumes are kept. Running containers are force-stopped and removed.',
      confirmLabel: 'Remove', danger: true,
    }))) return;
    bulkBusy = true;
    let ok = 0, fail = 0;
    for (const c of containers.filter((c) => selected.has(c.Id))) {
      try {
        const h = c.host_id ?? hosts.id;
        if (op === 'start')        await api.containers.start(c.Id, h);
        else if (op === 'stop')    await api.containers.stop(c.Id, h);
        else if (op === 'restart') await api.containers.restart(c.Id, h);
        else                       await api.containers.remove(c.Id, true, h);
        ok++;
      } catch { fail++; }
    }
    toast.success(`${op}: ${ok} succeeded${fail ? `, ${fail} failed` : ''}`);
    selected = new Set();
    bulkBusy = false;
    await load();
  }

  function copyId(id: string) {
    void copyWithToast(id, 'ID copied');
  }
</script>

<section class="dm-page">
  <div class="dm-ph">
    <h1>Containers</h1>
    <span class="dm-sub">{counts.all}</span>
    <span class="dm-sp"></span>
    <LiveIndicator updatedAt={lastLoaded} onrefresh={load} busy={loading} paused={connStatus === 'closed'} />
  </div>

  <div class="dm-toolbar">
    <Segmented
      label="Filter containers"
      bind:value={stateFilter}
      options={[
        { key: 'all', label: 'All', count: counts.all },
        { key: 'running', label: 'Running', count: counts.running },
        { key: 'attention', label: 'Attention', count: counts.attention },
        { key: 'stopped', label: 'Stopped', count: counts.stopped },
        { key: 'standalone', label: 'Standalone', count: counts.standalone }
      ]}
    />
    <span class="dm-sp"></span>
    <input type="search" class="dm-filter" placeholder="Filter name, stack, image, id…" aria-label="Filter containers" bind:value={search} style="width: 240px" />
    <Segmented
      label="Layout"
      bind:value={view}
      options={[
        { key: 'list', label: 'List' },
        { key: 'host', label: 'By host' },
        { key: 'stack', label: 'By stack' }
      ]}
    />
  </div>

  <StatBar
    items={[
      { label: 'Running', value: `${counts.running} of ${counts.all}` },
      {
        label: 'Needs attention',
        value: counts.attention,
        note: containers.filter(needsAttention).slice(0, 3).map((c) => `${nameOf(c)} ${statusOf(c).label}`).join(' · '),
        tone: counts.attention ? 'warn' : ''
      },
      { label: 'Stopped', value: counts.stopped },
      {
        label: 'Hosts',
        value: isAll ? hosts.available.length : 1,
        note: unreachable.length ? `${unreachable.length} unreachable` : isAll ? 'all hosts' : (hosts.selected?.name ?? ''),
        tone: unreachable.length ? 'bad' : ''
      }
    ]}
  />

  {#if unreachable.length > 0}
    <div class="ct-warn" role="alert">
      <AlertTriangle size={14} strokeWidth={1.8} />
      <span><b>{unreachable.length} host{unreachable.length === 1 ? '' : 's'} unreachable</b> · {unreachable.map((u) => u.host_name).join(', ')}. Their containers are missing from the list.</span>
    </div>
  {/if}

  {#if selected.size > 0 && canControlAll}
    <div class="ct-bulk" role="region" aria-label="Bulk actions">
      <b>{selected.size} selected</b>
      <span class="dm-sp"></span>
      <button type="button" class="dm-btn dm-btn-sm" onclick={() => bulkAction('start')} disabled={bulkBusy}>Start</button>
      <button type="button" class="dm-btn dm-btn-sm" onclick={() => bulkAction('stop')} disabled={bulkBusy}>Stop</button>
      <button type="button" class="dm-btn dm-btn-sm" onclick={() => bulkAction('restart')} disabled={bulkBusy}>Restart</button>
      <button type="button" class="dm-btn dm-btn-sm dm-btn-danger" onclick={() => bulkAction('remove')} disabled={bulkBusy}>Remove…</button>
      <button type="button" class="dm-btn dm-btn-sm dm-btn-ghost" onclick={() => (selected = new Set())}>Clear</button>
    </div>
  {/if}

  {#if loading && containers.length === 0}
    <div class="dm-tbl">
      {#each Array(6) as _, i (i)}
        <div class="dm-tr" style="grid-template-columns: 1fr"><Skeleton width="50%" height="0.9rem" /></div>
      {/each}
    </div>
  {:else if containers.length === 0}
    <EmptyState icon={Box} title="No containers" description="Deploy a stack or start a template to get started." />
  {:else}
    <div class="dm-tbl" role="table" aria-label="Containers">
      <div class="dm-tr dm-th" role="row" style="grid-template-columns: {cols}">
        <span role="columnheader">
          {#if canControlAll}<input type="checkbox" checked={allSelected} onchange={toggleAll} aria-label="Select all visible containers" />{/if}
        </span>
        <span role="columnheader"></span>
        <span role="columnheader"><button type="button" onclick={() => toggleSort('name')}>Name{sortArrow('name')}</button></span>
        <span role="columnheader"><button type="button" onclick={() => toggleSort('state')}>Status{sortArrow('state')}</button></span>
        {#if view !== 'host'}<span role="columnheader">Host</span>{/if}
        <span role="columnheader"><button type="button" onclick={() => toggleSort('image')}>Image{sortArrow('image')}</button></span>
        <span role="columnheader">Ports</span>
        <span role="columnheader"><button type="button" onclick={() => toggleSort('uptime')}>Uptime{sortArrow('uptime')}</button></span>
        <span role="columnheader" class="dm-tr-menu">
          {#if sortKey !== 'urgency'}<button type="button" onclick={() => { sortKey = 'urgency'; sortAsc = true; }} title="Back to urgency order">reset</button>{/if}
        </span>
      </div>

      {#if view === 'list'}
        {#each visible as c (c.Id + (c.host_id ?? ''))}
          {@render row(c)}
        {:else}
          <div class="dm-empty">No containers match.</div>
        {/each}
      {:else}
        {#each groups as g (g.key)}
          <div class="dm-tr dm-grp" role="row" style="grid-template-columns: 14px minmax(0, 1fr) auto auto">
            <span><span class="dm-dot {g.tone}"></span></span>
            <span class="dm-trunc">
              {#if g.href}<a class="ct-glink" href={g.href}>{g.label}</a>{:else}<b>{g.label}</b>{/if}
              <span class="dm-dim"> {g.sub}</span>
            </span>
            <span class="dm-num dm-dim">{g.items.filter((c) => c.State === 'running').length} running</span>
            {#if g.key === '__standalone' && allowed('stacks.adopt')}
              <a class="dm-btn dm-btn-sm" href="/stacks">Adopt…</a>
            {:else}
              <span></span>
            {/if}
          </div>
          {#each g.items as c (c.Id + (c.host_id ?? ''))}
            {@render row(c)}
          {/each}
        {:else}
          <div class="dm-empty">No containers match.</div>
        {/each}
      {/if}
    </div>
  {/if}
</section>

{#snippet row(c: Container)}
  {@const running = c.State === 'running'}
  {@const stack = stackOf(c)}
  {@const st = statusOf(c)}
  {@const img = imageNameAndTag(c.Image)}
  <div
    class="dm-tr dm-click"
    class:dm-sel={selected.has(c.Id)}
    role="row"
    style="grid-template-columns: {cols}"
    tabindex="0"
    onclick={() => goto(detailHref(c))}
    onkeydown={(e) => { if (e.key === 'Enter' && e.target === e.currentTarget) goto(detailHref(c)); }}
  >
    <span role="cell">
      {#if canControlAll && !isSystem(c)}
        <input
          type="checkbox"
          checked={selected.has(c.Id)}
          onclick={(e) => e.stopPropagation()}
          onchange={() => toggleOne(c.Id)}
          aria-label="Select {nameOf(c)}"
        />
      {/if}
    </span>
    <span role="cell"><span class="dm-dot {st.tone === 'off' ? '' : st.tone}"></span></span>
    <span role="cell" class="dm-nm">
      <a href={detailHref(c)} onclick={(e) => e.stopPropagation()} title={nameOf(c)}>{nameOf(c)}</a>{#if isSystem(c)} <StatusChip tone="acc" label="System" title="Managed by Dockmesh" />{/if}
      <small>
        {#if stack}{#if view !== 'stack'}{`stack ${stack} · `}{/if}{:else}<span class="ct-standalone">standalone</span>{' · '}{/if}<span class="dm-mono">{c.Id.slice(0, 12)}</span>{#if c.restart_count}<span class:ct-restarts={c.restart_count >= 5} title="Restarted {c.restart_count} times since it was created"> · ↻{c.restart_count}</span>{/if}
      </small>
    </span>
    <span role="cell"><StatusChip tone={st.tone} label={st.label} title={c.Status} /></span>
    {#if view !== 'host'}<span role="cell" class="dm-num dm-dim">{c.host_name || (isAll ? 'local' : (hosts.selected?.name ?? 'local'))}</span>{/if}
    <span role="cell" class="dm-num" title={c.Image}>
      <span class="dm-dim">{shortImage(img.name)}</span>{#if img.tag}:{img.tag}{/if}
      {#if c.image_update_available}<span class="ct-upd" title="A newer image is available"> · update</span>{/if}
    </span>
    <span role="cell" class="dm-num dm-dim" title={portsTitle(c)}>{portsLabel(c)}</span>
    <span role="cell" class="dm-num dm-dim">{running ? uptime(c) : '—'}</span>
    <span role="cell" class="dm-tr-menu">
      <Menu label="Actions for {nameOf(c)}" items={rowMenu(c)} />
    </span>
  </div>
{/snippet}

<style>
  .ct-warn {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 9px 14px;
    border: 1px solid var(--bad-bd);
    background: var(--bad-bg);
    border-radius: 9px;
    font-size: 13px;
    color: var(--bad);
  }
  .ct-warn span { color: var(--fg); }
  .ct-bulk {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 12px;
    border: 1px solid var(--accent);
    border-radius: var(--r-lg);
    background: var(--accent-soft);
  }
  .ct-glink { color: var(--fg); font-weight: 600; text-decoration: none; }
  .ct-glink:hover { text-decoration: underline; }
  .ct-standalone { color: var(--warn); }
  .ct-restarts { color: var(--warn); }
  .ct-upd { color: var(--accent); }
</style>
