<script lang="ts">
  // Containers — fleet-wide list. The sister-view to /resources: lists
  // every Docker container across every reachable host. /stacks renders
  // per-stack containers in its own detail screen — this page is the
  // operator surface for "I don't know which stack this container
  // belongs to" / "bulk-stop everything on this host".
  //
  // Slice 1: editorial rebuild on existing backend surface. Inline
  // CPU/Mem live, restart-count, image-update indicator are deferred to
  // Slice 2 (backend additions noted in the punch-list).
  //
  // RBAC: still uses v1 strings (container.control, container.exec).
  // Migration to v2.1 happens in the consolidated backend slice.
  import { api, ApiError, isFanOut } from '$lib/api';
  import { goto } from '$app/navigation';
  import { toast } from '$lib/stores/toast.svelte';
  import { copyWithToast } from '$lib/clipboard';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { allowed } from '$lib/rbac.svelte';
  import { hosts } from '$lib/stores/host.svelte';
  import { EventStream, type ConnStatus } from '$lib/events';
  import { Eyebrow } from '$lib/components/editorial';
  import { Skeleton } from '$lib/components/ui';
  import {
    Box, Play, Square, RotateCw, Trash2, RefreshCw, Server,
    AlertTriangle, Search, FileText, Terminal, Activity,
    MoreVertical, Pause, Settings, Copy,
  } from 'lucide-svelte';

  const canControl = $derived(allowed('containers.update'));
  const canExec = $derived(allowed('containers.exec'));
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
  }

  let containers = $state<Container[]>([]);
  let unreachable = $state<Array<{ host_id: string; host_name: string; reason: string }>>([]);
  let loading = $state(true);
  let showStopped = $state(true);
  let connStatus = $state<ConnStatus>('connecting');
  const live = $derived(connStatus === 'live');
  let reloadTimer: ReturnType<typeof setTimeout> | null = null;

  // Search + filter
  let search = $state('');
  type StateFilter = 'all' | 'running' | 'stopped' | 'unhealthy';
  let stateFilter = $state<StateFilter>('all');

  // Sort
  type SortKey = 'name' | 'state' | 'image' | 'stack' | 'uptime';
  let sortKey = $state<SortKey>('name');
  let sortAsc = $state(true);

  // Bulk selection
  let selected = $state<Set<string>>(new Set());
  let bulkBusy = $state(false);

  // Row more-menu
  let openMenuId = $state<string | null>(null);
  let menuRoot = $state<HTMLElement | null>(null);

  $effect(() => {
    if (!openMenuId) return;
    function onDocDown(e: MouseEvent) {
      if (!menuRoot) return;
      if (e.target instanceof Node && menuRoot.contains(e.target)) return;
      openMenuId = null;
    }
    window.addEventListener('mousedown', onDocDown);
    return () => window.removeEventListener('mousedown', onDocDown);
  });

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
    if (c.State !== 'running') return c.Status || '—';
    if (!c.Created) return '—';
    const secs = Math.floor(Date.now() / 1000) - c.Created;
    if (secs < 60) return `${secs}s`;
    if (secs < 3600) return `${Math.floor(secs / 60)}m`;
    if (secs < 86400) return `${Math.floor(secs / 3600)}h ${Math.floor((secs % 3600) / 60)}m`;
    return `${Math.floor(secs / 86400)}d ${Math.floor((secs % 86400) / 3600)}h`;
  }
  function detailHref(c: Container): string {
    const h = c.host_id ?? hosts.id;
    if (isAll && h) return `/containers/${c.Id}?host=${h}`;
    if (h && h !== 'local') return `/containers/${c.Id}?host=${h}`;
    return `/containers/${c.Id}`;
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
  function stateTone(s: string): 'ok' | 'warn' | 'fail' | 'neutral' {
    if (s === 'running') return 'ok';
    if (s === 'restarting') return 'warn';
    if (s === 'dead') return 'fail';
    return 'neutral';
  }

  // ── Counts + derived ────────────────────────────────────────────────
  const counts = $derived({
    all: containers.length,
    running: containers.filter((c) => c.State === 'running').length,
    stopped: containers.filter((c) => c.State !== 'running' && c.State !== 'restarting').length,
    unhealthy: containers.filter((c) => parseHealth(c.Status) === 'unhealthy').length,
  });

  const visible = $derived(
    containers
      .filter((c) => {
        if (stateFilter === 'running') return c.State === 'running';
        if (stateFilter === 'stopped') return c.State !== 'running' && c.State !== 'restarting';
        if (stateFilter === 'unhealthy') return parseHealth(c.Status) === 'unhealthy';
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
          case 'name':  cmp = nameOf(a).localeCompare(nameOf(b)); break;
          case 'state': cmp = a.State.localeCompare(b.State); break;
          case 'image': cmp = a.Image.localeCompare(b.Image); break;
          case 'stack': cmp = (stackOf(a) ?? '').localeCompare(stackOf(b) ?? ''); break;
          case 'uptime': cmp = (a.Created ?? 0) - (b.Created ?? 0); break;
        }
        return sortAsc ? cmp : -cmp;
      }),
  );

  const allSelected = $derived(visible.length > 0 && visible.every((c) => selected.has(c.Id)));
  function toggleAll() {
    if (allSelected) selected = new Set();
    else selected = new Set(visible.map((c) => c.Id));
  }
  function toggleOne(id: string) {
    const next = new Set(selected);
    if (next.has(id)) next.delete(id); else next.add(id);
    selected = next;
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

  async function load() {
    loading = true;
    try {
      const res = await api.containers.list(showStopped, hosts.id);
      if (isFanOut(res)) {
        containers = res.items as Container[];
        unreachable = res.unreachable_hosts;
      } else {
        containers = res as Container[];
        unreachable = [];
      }
    } catch (err) {
      toast.error('Failed to load', err instanceof ApiError ? err.message : undefined);
    } finally {
      loading = false;
    }
  }

  let prevHost = hosts.id;
  $effect(() => {
    const cur = hosts.id;
    if (cur !== prevHost) { prevHost = cur; load(); }
  });

  $effect(() => {
    load();
    stream.start();
    return () => {
      stream.stop();
      if (reloadTimer) clearTimeout(reloadTimer);
    };
  });

  // ── Actions ─────────────────────────────────────────────────────────
  async function action(c: Container, op: 'start' | 'stop' | 'restart' | 'pause' | 'unpause' | 'remove') {
    const targetHost = c.host_id ?? hosts.id;
    try {
      if (op === 'start')   await api.containers.start(c.Id, targetHost);
      else if (op === 'stop')    await api.containers.stop(c.Id, targetHost);
      else if (op === 'restart') await api.containers.restart(c.Id, targetHost);
      else if (op === 'pause' || op === 'unpause') {
        // Not exposed on the api wrapper yet — fall through gracefully.
        toast.info(op, 'Not yet wired in this build');
        return;
      } else {
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
    } finally {
      openMenuId = null;
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
    openMenuId = null;
  }
</script>

<section class="ctn">
  <!-- ─────────────────────────── Header ─────────────────────────── -->
  <header class="ctn-header">
    <div class="ctn-header-text">
      <h1 class="ed-title ctn-title">Containers</h1>
      <p class="ed-subtitle ctn-subtitle">
        {counts.all} container{counts.all === 1 ? '' : 's'} · {counts.running} running{counts.unhealthy > 0 ? ` · ${counts.unhealthy} unhealthy` : ''}{counts.stopped > 0 ? ` · ${counts.stopped} stopped` : ''} · {isAll ? 'fleet-wide' : hosts.selected?.name ?? 'local'}
      </p>
    </div>
    <div class="ctn-conn" data-state={live ? 'live' : connStatus}>
      <span class="ctn-conn-dot"></span>
      <span class="ctn-conn-text">
        {#if live}docker events · streaming
        {:else if connStatus === 'reconnecting'}reconnecting…
        {:else if connStatus === 'connecting'}connecting…
        {:else}offline{/if}
      </span>
    </div>
  </header>

  <!-- ────────────────── Summary tiles ──────────────────── -->
  <div class="ctn-summary">
    <div class="ctn-metric">
      <span class="ctn-metric-label">Containers</span>
      <span class="ctn-metric-value">{counts.all}</span>
      <span class="ctn-metric-meta">{counts.running} running</span>
    </div>
    <div class="ctn-metric" data-tone={counts.unhealthy > 0 ? 'warn' : 'ok'}>
      <span class="ctn-metric-label">Unhealthy</span>
      <span class="ctn-metric-value">{counts.unhealthy}</span>
      <span class="ctn-metric-meta">
        {counts.unhealthy > 0 ? 'running but failing checks' : 'all clear'}
      </span>
    </div>
    <div class="ctn-metric">
      <span class="ctn-metric-label">Stopped</span>
      <span class="ctn-metric-value">{counts.stopped}</span>
      <span class="ctn-metric-meta">exited · paused · created</span>
    </div>
    <div class="ctn-metric">
      <span class="ctn-metric-label">Hosts</span>
      <span class="ctn-metric-value">
        {isAll ? hosts.available.length : 1}
        {#if unreachable.length > 0}
          <span class="ctn-metric-unreach">· {unreachable.length} down</span>
        {/if}
      </span>
      <span class="ctn-metric-meta">{isAll ? 'across fleet' : 'lens scoped'}</span>
    </div>
  </div>

  <!-- ────────────────── Unreachable banner ─────────────── -->
  {#if unreachable.length > 0}
    <div class="ctn-unreach" role="alert">
      <AlertTriangle size={14} strokeWidth={1.5} class="ctn-unreach-icon" />
      <span class="ctn-unreach-text">
        <strong>{unreachable.length} host{unreachable.length === 1 ? '' : 's'} unreachable</strong>
        — {unreachable.map((u) => u.host_name).join(', ')}.
        Containers from these hosts are missing from the list.
      </span>
    </div>
  {/if}

  <!-- ────────────────── Toolbar ────────────────────────── -->
  <div class="ctn-toolbar">
    <div class="ctn-search">
      <Search size={12} strokeWidth={1.5} class="ctn-search-icon" />
      <input
        type="search"
        placeholder="search name · image · stack · id…"
        bind:value={search}
        class="ed-underline-input ctn-search-input"
      />
    </div>

    <div class="ctn-filter" role="tablist">
      {#each [
        { id: 'all'       as const, label: 'all',       n: counts.all },
        { id: 'running'   as const, label: 'running',   n: counts.running },
        { id: 'stopped'   as const, label: 'stopped',   n: counts.stopped },
        { id: 'unhealthy' as const, label: 'unhealthy', n: counts.unhealthy },
      ] as opt}
        {#if opt.id !== 'unhealthy' || counts.unhealthy > 0}
          <button
            type="button"
            class="ctn-filter-pill"
            class:ctn-filter-active={stateFilter === opt.id}
            onclick={() => (stateFilter = opt.id)}
          >
            {opt.label}<span class="ctn-filter-count">{opt.n}</span>
          </button>
        {/if}
      {/each}
    </div>

    <label class="ctn-show-stopped">
      <input type="checkbox" bind:checked={showStopped} onchange={load} />
      <span>show stopped</span>
    </label>

    <span class="ctn-spacer"></span>

    <button
      type="button"
      class="dm-btn dm-btn-ghost dm-btn-sm"
      onclick={load}
      disabled={loading}
      title="Refresh"
    >
      <RefreshCw size={12} strokeWidth={1.5} class={loading ? 'ctn-spin' : ''} />
      Refresh
    </button>
  </div>

  <!-- ────────────────── Bulk action bar ────────────────── -->
  {#if selected.size > 0 && canControl}
    <div class="ctn-bulkbar">
      <span class="ctn-bulkbar-count">{selected.size} selected</span>
      <div class="ctn-bulkbar-actions">
        <button
          type="button"
          class="dm-btn dm-btn-ghost dm-btn-xs"
          onclick={() => bulkAction('start')}
          disabled={bulkBusy}
        >
          <Play size={11} strokeWidth={1.5} /> Start
        </button>
        <button
          type="button"
          class="dm-btn dm-btn-ghost dm-btn-xs"
          onclick={() => bulkAction('stop')}
          disabled={bulkBusy}
        >
          <Square size={11} strokeWidth={1.5} /> Stop
        </button>
        <button
          type="button"
          class="dm-btn dm-btn-ghost dm-btn-xs"
          onclick={() => bulkAction('restart')}
          disabled={bulkBusy}
        >
          <RotateCw size={11} strokeWidth={1.5} /> Restart
        </button>
        <button
          type="button"
          class="dm-btn dm-btn-ghost dm-btn-xs ctn-danger-btn"
          onclick={() => bulkAction('remove')}
          disabled={bulkBusy}
        >
          <Trash2 size={11} strokeWidth={1.5} /> Remove
        </button>
        <button
          type="button"
          class="dm-btn dm-btn-ghost dm-btn-xs"
          onclick={() => (selected = new Set())}
        >
          Clear
        </button>
      </div>
    </div>
  {/if}

  <!-- ────────────────── Table ───────────────────────────── -->
  {#if loading && containers.length === 0}
    <div class="ctn-loading">
      <Skeleton width="100%" height="6rem" />
    </div>
  {:else if containers.length === 0}
    <div class="ctn-empty">
      <Box size={20} strokeWidth={1.4} />
      <p class="ctn-empty-title">No containers</p>
      <p class="ed-subtitle ctn-empty-blurb">
        Deploy a stack or pull an image to get started.
      </p>
    </div>
  {:else if visible.length === 0}
    <div class="ctn-empty">
      <Eyebrow>no match</Eyebrow>
      <p class="ctn-empty-title">No containers match this filter.</p>
    </div>
  {:else}
    <div class="ctn-table" bind:this={menuRoot}>
      <div class="ctn-row ctn-row-head" data-with-host={isAll}>
        {#if canControl}
          <span class="ctn-col-check">
            <input type="checkbox" checked={allSelected} onchange={toggleAll} />
          </span>
        {/if}
        <button type="button" class="ctn-h" onclick={() => toggleSort('name')}>
          name · stack
          <span class="ctn-h-arrow">{sortKey === 'name' ? (sortAsc ? '▲' : '▼') : '·'}</span>
        </button>
        <button type="button" class="ctn-h" onclick={() => toggleSort('state')}>
          state
          <span class="ctn-h-arrow">{sortKey === 'state' ? (sortAsc ? '▲' : '▼') : '·'}</span>
        </button>
        <span class="ctn-h">health</span>
        <button type="button" class="ctn-h" onclick={() => toggleSort('image')}>
          image
          <span class="ctn-h-arrow">{sortKey === 'image' ? (sortAsc ? '▲' : '▼') : '·'}</span>
        </button>
        <button type="button" class="ctn-h" onclick={() => toggleSort('uptime')}>
          uptime
          <span class="ctn-h-arrow">{sortKey === 'uptime' ? (sortAsc ? '▲' : '▼') : '·'}</span>
        </button>
        <span class="ctn-h">ports</span>
        {#if isAll}
          <span class="ctn-h">host</span>
        {/if}
        <span class="ctn-h ctn-h-right">·</span>
      </div>

      {#each visible as c (c.Id)}
        {@const running = c.State === 'running'}
        {@const stack = stackOf(c)}
        {@const health = parseHealth(c.Status)}
        {@const tone = stateTone(c.State)}
        {@const exit = exitCodeFromStatus(c.Status)}
        {@const img = imageNameAndTag(c.Image)}
        <div
          class="ctn-row"
          class:ctn-row-selected={selected.has(c.Id)}
          data-with-host={isAll}
        >
          {#if canControl}
            <span class="ctn-col-check">
              <input
                type="checkbox"
                checked={selected.has(c.Id)}
                onchange={() => toggleOne(c.Id)}
              />
            </span>
          {/if}

          <div class="ctn-cell-name">
            <a href={detailHref(c)} class="ctn-name" title={nameOf(c)}>
              {nameOf(c)}
            </a>
            <div class="ctn-name-sub">
              {#if stack}
                <a href="/stacks/{stack}" class="ctn-stack-link">stack: {stack}</a>
              {:else}
                <span class="ctn-stack-none">standalone</span>
              {/if}
              <span class="ctn-id">{c.Id.slice(0, 12)}</span>
            </div>
          </div>

          <div class="ctn-cell-state" data-tone={tone}>
            <span class="ctn-state-dot" data-tone={tone}></span>
            <span class="ctn-state-label">{c.State}</span>
            {#if exit !== null && c.State === 'exited'}
              <span class="ctn-state-exit">({exit})</span>
            {/if}
            {#if c.restart_count && c.restart_count > 0}
              <span class="ctn-restart-chip" class:ctn-restart-warn={c.restart_count >= 5}
                title="Container has restarted {c.restart_count}x since its last (re)create">
                ↻{c.restart_count}
              </span>
            {/if}
          </div>

          <div class="ctn-cell-health">
            {#if health === 'healthy'}
              <span class="ctn-state-dot" data-tone="ok"></span>
              <span class="ctn-health-text ctn-health-ok">healthy</span>
            {:else if health === 'unhealthy'}
              <span class="ctn-state-dot" data-tone="fail"></span>
              <span class="ctn-health-text ctn-health-fail">unhealthy</span>
            {:else if health === 'starting'}
              <span class="ctn-state-dot" data-tone="warn"></span>
              <span class="ctn-health-text ctn-health-warn">starting</span>
            {:else}
              <span class="ctn-health-text ctn-muted">—</span>
            {/if}
          </div>

          <div class="ctn-cell-image" title={c.Image}>
            <span class="ctn-image-name">{img.name}</span>
            {#if img.tag}
              <span class="ctn-image-sep">:</span>
              <span class="ctn-image-tag">{img.tag}</span>
            {/if}
            {#if c.image_update_available}
              <span class="ctn-image-update" title="A newer image is available upstream">↑</span>
            {/if}
          </div>

          <span class="ctn-cell-mono">{uptime(c)}</span>

          <div class="ctn-cell-ports">
            {#if c.Ports && c.Ports.length > 0}
              {@const published = c.Ports.filter((p) => p.PublicPort)}
              {@const internal = c.Ports.filter((p) => !p.PublicPort)}
              {#each published.slice(0, 3) as p}
                <span class="ctn-port-pill ctn-port-pub">
                  {p.PublicPort}:{p.PrivatePort}
                </span>
              {/each}
              {#if internal.length > 0 && published.length < 3}
                {#each internal.slice(0, 3 - published.length) as p}
                  <span class="ctn-port-pill ctn-port-int">
                    {p.PrivatePort}/{p.Type}
                  </span>
                {/each}
              {/if}
              {#if c.Ports.length > 3}
                <span class="ctn-port-more">+{c.Ports.length - 3}</span>
              {/if}
            {:else}
              <span class="ctn-muted">—</span>
            {/if}
          </div>

          {#if isAll}
            <span class="ctn-cell-host">
              <Server size={10} strokeWidth={1.5} />
              {c.host_name || 'local'}
            </span>
          {/if}

          <div class="ctn-cell-actions">
            <a href="{detailHref(c)}#logs" class="ctn-icon-btn" title="Logs">
              <FileText size={12} strokeWidth={1.5} />
            </a>
            {#if canExec}
              <a href="{detailHref(c)}#exec" class="ctn-icon-btn" title="Exec — open shell" class:ctn-icon-disabled={!running}>
                <Terminal size={12} strokeWidth={1.5} />
              </a>
            {/if}
            {#if canControl}
              {#if running}
                <button
                  type="button"
                  class="ctn-icon-btn"
                  title="Stop"
                  onclick={() => action(c, 'stop')}
                >
                  <Square size={12} strokeWidth={1.5} />
                </button>
              {:else}
                <button
                  type="button"
                  class="ctn-icon-btn"
                  title="Start"
                  onclick={() => action(c, 'start')}
                >
                  <Play size={12} strokeWidth={1.5} />
                </button>
              {/if}
              <button
                type="button"
                class="ctn-icon-btn"
                title="More"
                aria-label="More actions"
                onclick={(e) => { e.stopPropagation(); openMenuId = openMenuId === c.Id ? null : c.Id; }}
              >
                <MoreVertical size={12} strokeWidth={1.5} />
              </button>
              {#if openMenuId === c.Id}
                <div class="ctn-menu" role="menu">
                  <button
                    type="button"
                    class="ctn-menu-item"
                    onclick={() => action(c, 'restart')}
                    disabled={!running}
                  >
                    <RotateCw size={11} strokeWidth={1.5} /> Restart
                  </button>
                  <a href="{detailHref(c)}#stats" class="ctn-menu-item" onclick={() => (openMenuId = null)}>
                    <Activity size={11} strokeWidth={1.5} /> Stats
                  </a>
                  <a href={detailHref(c)} class="ctn-menu-item" onclick={() => (openMenuId = null)}>
                    <Settings size={11} strokeWidth={1.5} /> Inspect
                  </a>
                  <button
                    type="button"
                    class="ctn-menu-item"
                    onclick={() => action(c, running ? 'pause' : 'unpause')}
                    disabled={!running}
                  >
                    <Pause size={11} strokeWidth={1.5} /> {running ? 'Pause' : 'Unpause'}
                  </button>
                  <button
                    type="button"
                    class="ctn-menu-item"
                    onclick={() => copyId(c.Id)}
                  >
                    <Copy size={11} strokeWidth={1.5} /> Copy ID
                  </button>
                  <div class="ctn-menu-sep"></div>
                  <button
                    type="button"
                    class="ctn-menu-item ctn-menu-danger"
                    onclick={() => action(c, 'remove')}
                  >
                    <Trash2 size={11} strokeWidth={1.5} /> Remove
                  </button>
                </div>
              {/if}
            {/if}
          </div>
        </div>
      {/each}
    </div>
  {/if}
</section>

<style>
  .ctn {
    display: flex;
    flex-direction: column;
    gap: 18px;
  }

  /* ── Header ─────────────────────────────────────────────────── */
  .ctn-header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 24px;
    flex-wrap: wrap;
  }
  .ctn-header-text { min-width: 0; max-width: 70ch; }
  .ctn-title {
    font-size: 28px;
    line-height: 1.1;
    margin-top: 12px;
  }
  .ctn-subtitle {
    margin-top: 8px;
    max-width: 70ch;
  }
  .ctn-conn {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    padding: 5px 10px;
    border: 1px solid var(--border);
    border-radius: 999px;
    background: var(--bg-elevated);
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fg-subtle);
    flex-shrink: 0;
  }
  .ctn-conn-dot {
    width: 6px;
    height: 6px;
    border-radius: 999px;
    background: var(--border-strong);
  }
  .ctn-conn[data-state="live"] .ctn-conn-dot {
    background: var(--color-success-400);
    box-shadow: 0 0 0 3px color-mix(in srgb, var(--color-success-500) 18%, transparent);
    animation: ctn-blink 2.2s infinite;
  }
  .ctn-conn[data-state="reconnecting"] .ctn-conn-dot {
    background: var(--color-warning-400);
    animation: ctn-blink 1.2s infinite;
  }
  @keyframes ctn-blink {
    0%, 100% { opacity: 1; }
    50% { opacity: 0.4; }
  }

  /* ── Summary tiles ──────────────────────────────────────────── */
  .ctn-summary {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: 10px;
  }
  .ctn-metric {
    padding: 12px 14px;
    border: 1px solid var(--border);
    border-radius: 5px;
    background: var(--surface);
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .ctn-metric[data-tone="warn"] {
    border-color: color-mix(in srgb, var(--color-warning-500) 35%, var(--border));
  }
  .ctn-metric-label {
    font-family: var(--font-mono);
    font-size: 10.5px;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: var(--fg-subtle);
  }
  .ctn-metric-value {
    font-size: 22px;
    color: var(--fg);
    font-weight: 500;
    line-height: 1.2;
  }
  .ctn-metric[data-tone="warn"] .ctn-metric-value {
    color: var(--color-warning-400);
  }
  .ctn-metric-meta {
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
  }
  .ctn-metric-unreach {
    color: var(--color-warning-400);
    margin-left: 4px;
    font-size: 14px;
  }

  /* ── Unreachable banner ─────────────────────────────────────── */
  .ctn-unreach {
    padding: 10px 12px;
    border: 1px solid color-mix(in srgb, var(--color-warning-500) 35%, var(--border));
    background: color-mix(in srgb, var(--color-warning-500) 6%, var(--surface));
    border-radius: 5px;
    display: flex;
    gap: 10px;
    align-items: flex-start;
    color: var(--fg-muted);
    font-size: 12px;
    line-height: 1.5;
  }
  .ctn-unreach :global(.ctn-unreach-icon) { color: var(--color-warning-400); flex-shrink: 0; margin-top: 1px; }
  .ctn-unreach-text strong { color: var(--color-warning-400); font-weight: 500; }

  /* ── Toolbar ────────────────────────────────────────────────── */
  .ctn-toolbar {
    display: flex;
    align-items: center;
    gap: 14px;
    flex-wrap: wrap;
  }
  .ctn-search {
    position: relative;
    flex: 0 1 320px;
    min-width: 220px;
  }
  .ctn-search :global(.ctn-search-icon) {
    position: absolute;
    left: 0;
    top: 50%;
    transform: translateY(-50%);
    color: var(--fg-subtle);
    pointer-events: none;
  }
  .ctn-search-input {
    padding-left: 18px;
    font-size: 12.5px;
    font-family: var(--font-mono);
  }
  .ctn-filter {
    display: inline-flex;
    border: 1px solid var(--border);
    border-radius: 4px;
    overflow: hidden;
  }
  .ctn-filter-pill {
    padding: 4px 9px;
    font-family: var(--font-mono);
    font-size: 10px;
    line-height: 1.2;
    letter-spacing: 0.04em;
    color: var(--fg-subtle);
    background: transparent;
    border: 0;
    border-right: 1px solid var(--border);
    cursor: pointer;
  }
  .ctn-filter-pill:last-child { border-right: 0; }
  .ctn-filter-pill:hover { color: var(--fg); }
  .ctn-filter-active {
    background: var(--bg-elevated);
    color: var(--fg);
  }
  .ctn-filter-count {
    margin-left: 5px;
    color: var(--fg-subtle);
    font-size: 9.5px;
  }
  .ctn-filter-active .ctn-filter-count { color: var(--fg-muted); }
  .ctn-show-stopped {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 11.5px;
    color: var(--fg-subtle);
    font-family: var(--font-mono);
    cursor: pointer;
  }
  .ctn-show-stopped input { accent-color: var(--color-brand-500); }
  .ctn-spacer { flex: 1; }
  .ctn-spin { animation: ctn-spin 0.9s linear infinite; }
  @keyframes ctn-spin {
    from { transform: rotate(0deg); }
    to   { transform: rotate(360deg); }
  }

  /* ── Bulk action bar ────────────────────────────────────────── */
  .ctn-bulkbar {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 8px 14px;
    border: 1px solid color-mix(in srgb, var(--color-brand-500) 35%, var(--border));
    border-radius: 5px;
    background: color-mix(in srgb, var(--color-brand-500) 5%, var(--surface));
  }
  .ctn-bulkbar-count {
    font-size: 12.5px;
    font-weight: 500;
    color: var(--fg);
  }
  .ctn-bulkbar-actions {
    display: flex;
    gap: 6px;
    margin-left: auto;
    flex-wrap: wrap;
  }
  .ctn-danger-btn { color: var(--color-danger-400); }

  /* ── Table ──────────────────────────────────────────────────── */
  .ctn-table {
    border: 1px solid var(--border);
    border-radius: 6px;
    overflow: visible;
  }
  .ctn-row {
    display: grid;
    grid-template-columns:
      28px
      minmax(220px, 1.4fr)
      110px
      100px
      minmax(180px, 1.2fr)
      80px
      minmax(140px, 1fr)
      120px;
    gap: 14px;
    align-items: center;
    padding: 12px 14px;
    border-bottom: 1px solid var(--border-subtle);
    position: relative;
  }
  .ctn-row[data-with-host="true"] {
    grid-template-columns:
      28px
      minmax(220px, 1.4fr)
      110px
      100px
      minmax(180px, 1.2fr)
      80px
      minmax(140px, 1fr)
      110px
      120px;
  }
  .ctn-row:last-child { border-bottom: 0; }
  .ctn-row-head {
    background: var(--bg-elevated);
    border-bottom: 1px solid var(--border);
    padding: 10px 14px;
  }
  .ctn-row-selected {
    background: color-mix(in srgb, var(--color-brand-500) 4%, transparent);
  }

  .ctn-col-check input { accent-color: var(--color-brand-500); }
  .ctn-h {
    background: transparent;
    border: 0;
    padding: 0;
    text-align: left;
    cursor: pointer;
    font-family: var(--font-mono);
    font-size: 10.5px;
    letter-spacing: 0.06em;
    text-transform: uppercase;
    color: var(--fg-subtle);
    display: inline-flex;
    align-items: center;
    gap: 4px;
  }
  .ctn-h:hover { color: var(--fg); }
  .ctn-h-arrow {
    color: var(--fg-subtle);
    font-size: 8px;
  }
  .ctn-h-right { text-align: right; justify-content: flex-end; }

  /* Cells */
  .ctn-cell-name { min-width: 0; }
  .ctn-name {
    font-family: var(--font-mono);
    font-size: 12.5px;
    color: var(--fg);
    text-decoration: none;
    display: block;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .ctn-name:hover {
    color: var(--accent-fg);
    text-decoration: underline;
    text-underline-offset: 3px;
  }
  .ctn-name-sub {
    margin-top: 3px;
    display: flex;
    gap: 8px;
    align-items: center;
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
  }
  .ctn-stack-link {
    color: var(--accent-fg);
    text-decoration: none;
  }
  .ctn-stack-link:hover { text-decoration: underline; text-underline-offset: 3px; }
  .ctn-stack-none { color: var(--fg-subtle); }
  .ctn-id { color: var(--fg-subtle); }

  .ctn-cell-state {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .ctn-state-dot {
    width: 6px;
    height: 6px;
    border-radius: 999px;
    background: var(--border-strong);
    flex-shrink: 0;
  }
  .ctn-state-dot[data-tone="ok"]   { background: var(--color-success-500); }
  .ctn-state-dot[data-tone="warn"] { background: var(--color-warning-500); animation: ctn-blink 1.2s infinite; }
  .ctn-state-dot[data-tone="fail"] { background: var(--color-danger-500); }
  .ctn-state-label {
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: var(--fg-muted);
  }
  .ctn-cell-state[data-tone="ok"]   .ctn-state-label { color: var(--color-success-400); }
  .ctn-cell-state[data-tone="warn"] .ctn-state-label { color: var(--color-warning-400); }
  .ctn-cell-state[data-tone="fail"] .ctn-state-label { color: var(--color-danger-400); }
  .ctn-state-exit {
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--fg-subtle);
  }
  .ctn-restart-chip {
    font-family: var(--font-mono);
    font-size: 10px;
    padding: 1px 5px;
    margin-left: 4px;
    border-radius: 3px;
    background: var(--surface-hover);
    color: var(--fg-muted);
    border: 1px solid var(--border-subtle);
  }
  .ctn-restart-warn {
    color: var(--color-warning-400);
    background: color-mix(in srgb, var(--color-warning-500) 10%, transparent);
    border-color: color-mix(in srgb, var(--color-warning-500) 30%, transparent);
  }
  .ctn-image-update {
    display: inline-block;
    margin-left: 4px;
    padding: 0 4px;
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--color-success-400);
    background: color-mix(in srgb, var(--color-success-500) 10%, transparent);
    border: 1px solid color-mix(in srgb, var(--color-success-500) 30%, transparent);
    border-radius: 3px;
  }

  .ctn-cell-health {
    display: flex;
    align-items: center;
    gap: 6px;
    font-family: var(--font-mono);
    font-size: 11px;
  }
  .ctn-health-text { font-family: var(--font-mono); font-size: 11px; }
  .ctn-health-ok   { color: var(--color-success-400); }
  .ctn-health-fail { color: var(--color-danger-400); }
  .ctn-health-warn { color: var(--color-warning-400); }
  .ctn-muted { color: var(--fg-subtle); }

  .ctn-cell-image {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: var(--fg-muted);
  }
  .ctn-image-sep { color: var(--fg-subtle); }
  .ctn-image-tag { color: var(--accent-fg); }

  .ctn-cell-mono {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fg-muted);
  }

  .ctn-cell-ports {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    align-items: center;
    min-width: 0;
    overflow: hidden;
  }
  .ctn-port-pill {
    font-family: var(--font-mono);
    font-size: 10px;
    padding: 1px 6px;
    border-radius: 999px;
    border: 1px solid var(--border-subtle);
  }
  .ctn-port-pub {
    color: var(--color-success-400);
    border-color: color-mix(in srgb, var(--color-success-500) 35%, var(--border));
    background: color-mix(in srgb, var(--color-success-500) 6%, transparent);
  }
  .ctn-port-int {
    color: var(--fg-subtle);
  }
  .ctn-port-more {
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--fg-subtle);
    align-self: center;
  }

  .ctn-cell-host {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 2px 7px;
    border: 1px solid var(--border-subtle);
    border-radius: 3px;
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--fg-muted);
    width: fit-content;
  }

  .ctn-cell-actions {
    display: flex;
    align-items: center;
    gap: 2px;
    justify-content: flex-end;
    position: relative;
  }
  .ctn-icon-btn {
    width: 24px;
    height: 24px;
    background: transparent;
    border: 0;
    border-radius: 4px;
    color: var(--fg-muted);
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    text-decoration: none;
    font: inherit;
  }
  .ctn-icon-btn:hover {
    color: var(--fg);
    background: var(--surface-hover);
  }
  .ctn-icon-disabled {
    opacity: 0.35;
    pointer-events: none;
  }

  /* Row more-menu */
  .ctn-menu {
    position: absolute;
    right: 0;
    top: calc(100% + 4px);
    z-index: 30;
    background: var(--bg-elevated);
    border: 1px solid var(--border-strong);
    border-radius: 6px;
    padding: 4px;
    min-width: 160px;
    box-shadow: 0 12px 30px rgba(2, 6, 23, 0.4);
    display: flex;
    flex-direction: column;
    gap: 1px;
  }
  .ctn-menu-item {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    padding: 6px 10px;
    background: transparent;
    border: 0;
    border-radius: 3px;
    cursor: pointer;
    text-align: left;
    font-size: 12px;
    color: var(--fg);
    font: inherit;
    text-decoration: none;
    width: 100%;
  }
  .ctn-menu-item:hover { background: var(--surface-hover); }
  .ctn-menu-item:disabled {
    color: var(--fg-subtle);
    cursor: not-allowed;
    opacity: 0.5;
  }
  .ctn-menu-danger { color: var(--color-danger-400); }
  .ctn-menu-sep {
    height: 1px;
    background: var(--border-subtle);
    margin: 2px 0;
  }

  /* ── Loading / empty ────────────────────────────────────────── */
  .ctn-loading { padding: 0; }
  .ctn-empty {
    padding: 44px 24px;
    text-align: center;
    border: 1px dashed var(--border);
    border-radius: 6px;
    color: var(--fg-subtle);
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 8px;
  }
  .ctn-empty-title {
    margin: 8px 0 0;
    font-size: 16px;
    color: var(--fg);
    font-weight: 500;
  }
  .ctn-empty-blurb {
    margin: 0;
    max-width: 50ch;
  }

  /* ── Responsive ─────────────────────────────────────────────── */
  @media (max-width: 1100px) {
    .ctn-summary { grid-template-columns: repeat(2, minmax(0, 1fr)); }
    .ctn-row,
    .ctn-row[data-with-host="true"] {
      grid-template-columns: 28px minmax(0, 1fr) auto;
      grid-auto-flow: row;
    }
    .ctn-row > :nth-child(n+4) {
      grid-column: 2 / -1;
    }
    .ctn-row-head { display: none; }
  }
</style>
