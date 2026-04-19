<script lang="ts">
  import { api, ApiError, isFanOut } from '$lib/api';
  import { goto } from '$app/navigation';
  import { Card, Badge, EmptyState, Button, Skeleton } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { allowed } from '$lib/rbac';
  import { hosts } from '$lib/stores/host.svelte';
  import { EventStream, type ConnStatus } from '$lib/events';
  import {
    Box, Play, Square, RotateCw, Trash2, RefreshCw, Server, Layers,
    AlertTriangle, Search, FileText, Terminal, Activity, ChevronUp, ChevronDown
  } from 'lucide-svelte';

  const canControl = $derived(allowed('container.control'));
  const canExec = $derived(allowed('container.exec'));
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
  type StateFilter = 'all' | 'running' | 'stopped' | 'exited' | 'unhealthy';
  let stateFilter = $state<StateFilter>('all');

  // Sort
  type SortKey = 'name' | 'state' | 'image' | 'stack' | 'uptime';
  let sortKey = $state<SortKey>('name');
  let sortAsc = $state(true);

  // Bulk selection
  let selected = $state<Set<string>>(new Set());
  let bulkBusy = $state(false);

  function toggleOne(id: string) {
    const next = new Set(selected);
    if (next.has(id)) next.delete(id); else next.add(id);
    selected = next;
  }

  // Counts for filter pills
  const counts = $derived({
    all: containers.length,
    running: containers.filter(c => c.State === 'running').length,
    stopped: containers.filter(c => c.State !== 'running').length,
    exited: containers.filter(c => c.State === 'exited').length,
    unhealthy: containers.filter(c => (c.Status ?? '').toLowerCase().includes('unhealthy')).length
  });

  // Filtering + sorting
  const visible = $derived(
    containers
      .filter(c => {
        if (stateFilter === 'running') return c.State === 'running';
        if (stateFilter === 'stopped') return c.State !== 'running';
        if (stateFilter === 'exited') return c.State === 'exited';
        if (stateFilter === 'unhealthy') return (c.Status ?? '').toLowerCase().includes('unhealthy');
        return true;
      })
      .filter(c => {
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
          case 'name': cmp = nameOf(a).localeCompare(nameOf(b)); break;
          case 'state': cmp = a.State.localeCompare(b.State); break;
          case 'image': cmp = a.Image.localeCompare(b.Image); break;
          case 'stack': cmp = (stackOf(a) ?? '').localeCompare(stackOf(b) ?? ''); break;
          case 'uptime': cmp = (a.Created ?? 0) - (b.Created ?? 0); break;
        }
        return sortAsc ? cmp : -cmp;
      })
  );

  const allSelected = $derived(visible.length > 0 && visible.every(c => selected.has(c.Id)));
  function toggleAll() {
    if (allSelected) { selected = new Set(); }
    else { selected = new Set(visible.map(c => c.Id)); }
  }

  function toggleSort(key: SortKey) {
    if (sortKey === key) { sortAsc = !sortAsc; }
    else { sortKey = key; sortAsc = true; }
  }

  // Helpers
  function nameOf(c: Container): string {
    return (c.Names?.[0] ?? c.Id.slice(0, 12)).replace(/^\//, '');
  }
  function stackOf(c: Container): string | null {
    return c.Labels?.['com.docker.compose.project'] ?? null;
  }
  function portSummary(c: Container): string {
    if (!c.Ports) return '';
    const seen = new Set<string>();
    for (const p of c.Ports) if (p.PublicPort) seen.add(`${p.PublicPort}:${p.PrivatePort}`);
    return [...seen].join(', ');
  }
  function uptime(c: Container): string {
    if (!c.Created) return '—';
    const secs = Math.floor(Date.now() / 1000) - c.Created;
    if (secs < 60) return `${secs}s`;
    if (secs < 3600) return `${Math.floor(secs / 60)}m`;
    if (secs < 86400) return `${Math.floor(secs / 3600)}h ${Math.floor((secs % 3600) / 60)}m`;
    return `${Math.floor(secs / 86400)}d ${Math.floor((secs % 86400) / 3600)}h`;
  }
  function detailHref(c: Container): string {
    // In all-mode, always pass the specific host_id so the detail page
    // doesn't fall back to hosts.id which would be "all" (503 on inspect).
    const h = c.host_id ?? hosts.id;
    if (isAll && h) return `/containers/${c.Id}?host=${h}`;
    if (h && h !== 'local') return `/containers/${c.Id}?host=${h}`;
    return `/containers/${c.Id}`;
  }

  // Data loading
  function scheduleReload() {
    if (reloadTimer) clearTimeout(reloadTimer);
    reloadTimer = setTimeout(load, 300);
  }
  const stream = new EventStream({
    onMessage: (msg) => { if (msg.source === 'docker' && msg.type === 'container') scheduleReload(); },
    onStatus: (s) => { connStatus = s; }
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

  $effect(() => { load(); stream.start(); return () => { stream.stop(); if (reloadTimer) clearTimeout(reloadTimer); }; });

  // Actions
  async function action(c: Container, op: 'start' | 'stop' | 'restart' | 'remove') {
    const targetHost = c.host_id ?? hosts.id;
    try {
      if (op === 'start') await api.containers.start(c.Id, targetHost);
      else if (op === 'stop') await api.containers.stop(c.Id, targetHost);
      else if (op === 'restart') await api.containers.restart(c.Id, targetHost);
      else {
        if (!(await confirm.ask({ title: 'Remove container', message: `Remove container "${nameOf(c)}"?`, body: 'Container volumes are kept. Image stays available for redeploy.', confirmLabel: 'Remove', danger: true }))) return;
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
    if (op === 'remove' && !(await confirm.ask({ title: 'Remove containers', message: `Remove ${selected.size} container(s)?`, body: 'Volumes are kept. Running containers are force-stopped and removed.', confirmLabel: 'Remove', danger: true }))) return;
    bulkBusy = true;
    let ok = 0;
    let fail = 0;
    for (const c of containers.filter(c => selected.has(c.Id))) {
      try {
        const h = c.host_id ?? hosts.id;
        if (op === 'start') await api.containers.start(c.Id, h);
        else if (op === 'stop') await api.containers.stop(c.Id, h);
        else if (op === 'restart') await api.containers.restart(c.Id, h);
        else await api.containers.remove(c.Id, true, h);
        ok++;
      } catch { fail++; }
    }
    toast.success(`${op}: ${ok} succeeded${fail ? `, ${fail} failed` : ''}`);
    selected = new Set();
    bulkBusy = false;
    await load();
  }
</script>

<section class="space-y-4">
  <!-- Header -->
  <div class="flex items-center justify-between flex-wrap gap-3">
    <div>
      <div class="flex items-center gap-2">
        <h2 class="text-2xl font-semibold tracking-tight">Containers</h2>
        {#if live}
          <Badge variant="success" dot>live</Badge>
        {:else if connStatus === 'reconnecting'}
          <Badge variant="warning" dot>reconnecting</Badge>
        {/if}
      </div>
      <p class="text-sm text-[var(--fg-muted)] mt-0.5">
        {containers.length} container{containers.length === 1 ? '' : 's'}
        {#if isAll}across all hosts{:else if hosts.selected?.name && hosts.id !== 'local'}on {hosts.selected.name}{/if}
      </p>
    </div>
    <div class="flex items-center gap-2">
      <label class="flex items-center gap-2 text-sm text-[var(--fg-muted)] cursor-pointer">
        <input type="checkbox" bind:checked={showStopped} onchange={load} class="accent-[var(--color-brand-500)]" />
        stopped
      </label>
      <Button variant="secondary" size="sm" onclick={load}>
        <RefreshCw class="w-3.5 h-3.5 {loading ? 'animate-spin' : ''}" /> Refresh
      </Button>
    </div>
  </div>

  <!-- Search + filter pills -->
  {#if containers.length > 0}
    <div class="flex flex-wrap items-center gap-3">
      <div class="relative flex-1 min-w-[200px] max-w-sm">
        <Search class="w-3.5 h-3.5 text-[var(--fg-subtle)] absolute left-2.5 top-1/2 -translate-y-1/2 pointer-events-none" />
        <input
          type="search"
          placeholder="Search by name, image, stack or ID…"
          bind:value={search}
          class="dm-input pl-8 pr-3 py-1.5 text-sm w-full"
        />
      </div>
      <div class="flex gap-1 text-xs">
        {#snippet pill(key: StateFilter, label: string, n: number)}
          <button
            class="px-2.5 py-1 rounded-full border transition-colors {stateFilter === key
              ? 'bg-[var(--surface)] border-[var(--border-strong)] text-[var(--fg)]'
              : 'border-[var(--border)] text-[var(--fg-muted)] hover:bg-[var(--surface-hover)] hover:text-[var(--fg)]'}"
            onclick={() => (stateFilter = key)}
          >
            {label} <span class="tabular-nums">{n}</span>
          </button>
        {/snippet}
        {@render pill('all', 'All', counts.all)}
        {@render pill('running', 'Running', counts.running)}
        {@render pill('stopped', 'Stopped', counts.stopped)}
        {#if counts.unhealthy > 0}
          {@render pill('unhealthy', 'Unhealthy', counts.unhealthy)}
        {/if}
      </div>
    </div>
  {/if}

  <!-- Bulk action bar -->
  {#if selected.size > 0}
    <div class="flex items-center gap-3 px-4 py-2.5 rounded-lg bg-[var(--surface)] border border-[var(--border)]">
      <span class="text-sm font-medium">{selected.size} selected</span>
      <div class="flex gap-1.5 ml-auto">
        <Button size="xs" variant="secondary" onclick={() => bulkAction('start')} disabled={bulkBusy}>
          <Play class="w-3.5 h-3.5" /> Start
        </Button>
        <Button size="xs" variant="secondary" onclick={() => bulkAction('stop')} disabled={bulkBusy}>
          <Square class="w-3.5 h-3.5" /> Stop
        </Button>
        <Button size="xs" variant="secondary" onclick={() => bulkAction('restart')} disabled={bulkBusy}>
          <RotateCw class="w-3.5 h-3.5" /> Restart
        </Button>
        <Button size="xs" variant="danger" onclick={() => bulkAction('remove')} disabled={bulkBusy}>
          <Trash2 class="w-3.5 h-3.5" /> Remove
        </Button>
        <button class="text-xs text-[var(--fg-muted)] hover:text-[var(--fg)] ml-2" onclick={() => (selected = new Set())}>
          Clear
        </button>
      </div>
    </div>
  {/if}

  <!-- Unreachable hosts banner -->
  {#if unreachable.length > 0}
    <div class="dm-card p-3 flex items-start gap-2.5 border border-[color-mix(in_srgb,var(--color-warning-500)_30%,transparent)]">
      <AlertTriangle class="w-4 h-4 text-[var(--color-warning-400)] shrink-0 mt-0.5" />
      <div class="text-xs">
        <span class="font-medium text-[var(--color-warning-400)]">{unreachable.length} host{unreachable.length === 1 ? '' : 's'} unreachable</span>
        <span class="text-[var(--fg-muted)]"> — {unreachable.map(u => u.host_name).join(', ')}</span>
      </div>
    </div>
  {/if}

  <!-- Table -->
  {#if loading && containers.length === 0}
    <Card>
      <div class="divide-y divide-[var(--border)]">
        {#each Array(6) as _}
          <div class="px-5 py-3.5 flex items-center gap-4">
            <Skeleton width="1rem" height="1rem" />
            <Skeleton width="30%" height="0.85rem" />
            <Skeleton width="20%" height="0.75rem" />
          </div>
        {/each}
      </div>
    </Card>
  {:else if containers.length === 0}
    <Card>
      <EmptyState icon={Box} title="No containers" description="Deploy a stack or pull an image to get started." />
    </Card>
  {:else if visible.length === 0}
    <Card class="p-8 text-center text-sm text-[var(--fg-muted)]">No containers match this filter.</Card>
  {:else}
    <Card>
      <div class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead>
            <tr class="border-b border-[var(--border)] text-[var(--fg-muted)] text-xs uppercase tracking-wider">
              {#if canControl}
                <th class="w-10 px-3 py-3">
                  <input type="checkbox" checked={allSelected} onchange={toggleAll}
                         class="accent-[var(--color-brand-500)]" />
                </th>
              {/if}
              {#snippet sortHeader(key: SortKey, label: string)}
                <th class="text-left px-3 py-3 cursor-pointer select-none hover:text-[var(--fg)]" onclick={() => toggleSort(key)}>
                  <span class="inline-flex items-center gap-1">
                    {label}
                    {#if sortKey === key}
                      {#if sortAsc}<ChevronUp class="w-3 h-3" />{:else}<ChevronDown class="w-3 h-3" />{/if}
                    {/if}
                  </span>
                </th>
              {/snippet}
              {@render sortHeader('name', 'Name')}
              {@render sortHeader('state', 'State')}
              {@render sortHeader('image', 'Image')}
              {@render sortHeader('stack', 'Stack')}
              <th class="text-left px-3 py-3">Ports</th>
              {@render sortHeader('uptime', 'Uptime')}
              {#if isAll}
                <th class="text-left px-3 py-3">Host</th>
              {/if}
              <th class="text-right px-3 py-3 w-36">Actions</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-[var(--border)]">
            {#each visible as c (c.Id)}
              {@const running = c.State === 'running'}
              {@const stack = stackOf(c)}
              <tr class="hover:bg-[var(--surface-hover)] transition-colors {selected.has(c.Id) ? 'bg-[color-mix(in_srgb,var(--color-brand-500)_5%,transparent)]' : ''}">
                {#if canControl}
                  <td class="w-10 px-3 py-2.5">
                    <input type="checkbox" checked={selected.has(c.Id)} onchange={() => toggleOne(c.Id)}
                           class="accent-[var(--color-brand-500)]" />
                  </td>
                {/if}
                <td class="px-3 py-2.5">
                  <a href={detailHref(c)} class="font-mono text-sm hover:text-[var(--color-brand-400)] truncate block max-w-[200px]" title={nameOf(c)}>
                    {nameOf(c)}
                  </a>
                </td>
                <td class="px-3 py-2.5">
                  <Badge variant={running ? 'success' : c.State === 'exited' ? 'default' : 'warning'} dot>
                    {c.State}
                  </Badge>
                </td>
                <td class="px-3 py-2.5">
                  <span class="text-xs text-[var(--fg-muted)] font-mono truncate block max-w-[180px]" title={c.Image}>{c.Image}</span>
                </td>
                <td class="px-3 py-2.5">
                  {#if stack}
                    <a href="/stacks/{stack}" class="text-xs text-[var(--color-brand-400)] hover:underline font-mono">{stack}</a>
                  {:else}
                    <span class="text-xs text-[var(--fg-subtle)]">—</span>
                  {/if}
                </td>
                <td class="px-3 py-2.5 text-xs text-[var(--fg-muted)] font-mono">{portSummary(c) || '—'}</td>
                <td class="px-3 py-2.5 text-xs text-[var(--fg-muted)] font-mono tabular-nums">{uptime(c)}</td>
                {#if isAll}
                  <td class="px-3 py-2.5">
                    <span class="inline-flex items-center gap-1 font-mono text-[10px] px-1.5 py-0.5 rounded border border-[var(--border)] text-[var(--fg-muted)]">
                      <Server class="w-2.5 h-2.5" />
                      {c.host_name || 'local'}
                    </span>
                  </td>
                {/if}
                <td class="px-3 py-2.5">
                  <div class="flex gap-0.5 justify-end">
                    <a href="{detailHref(c)}#logs" class="p-1.5 rounded-md text-[var(--fg-muted)] hover:text-[var(--fg)] hover:bg-[var(--surface-hover)]" title="Logs">
                      <FileText class="w-3.5 h-3.5" />
                    </a>
                    {#if canExec}
                      <a href="{detailHref(c)}#exec" class="p-1.5 rounded-md text-[var(--fg-muted)] hover:text-[var(--fg)] hover:bg-[var(--surface-hover)]" title="Terminal">
                        <Terminal class="w-3.5 h-3.5" />
                      </a>
                    {/if}
                    <a href="{detailHref(c)}#stats" class="p-1.5 rounded-md text-[var(--fg-muted)] hover:text-[var(--fg)] hover:bg-[var(--surface-hover)]" title="Stats">
                      <Activity class="w-3.5 h-3.5" />
                    </a>
                    {#if canControl}
                      {#if running}
                        <button class="p-1.5 rounded-md text-[var(--fg-muted)] hover:text-[var(--fg)] hover:bg-[var(--surface-hover)]" title="Restart" onclick={() => action(c, 'restart')}>
                          <RotateCw class="w-3.5 h-3.5" />
                        </button>
                        <button class="p-1.5 rounded-md text-[var(--fg-muted)] hover:text-[var(--fg)] hover:bg-[var(--surface-hover)]" title="Stop" onclick={() => action(c, 'stop')}>
                          <Square class="w-3.5 h-3.5" />
                        </button>
                      {:else}
                        <button class="p-1.5 rounded-md text-[var(--fg-muted)] hover:text-[var(--fg)] hover:bg-[var(--surface-hover)]" title="Start" onclick={() => action(c, 'start')}>
                          <Play class="w-3.5 h-3.5" />
                        </button>
                      {/if}
                      <button class="p-1.5 rounded-md text-[var(--color-danger-400)] hover:bg-[color-mix(in_srgb,var(--color-danger-500)_10%,transparent)]" title="Remove" onclick={() => action(c, 'remove')}>
                        <Trash2 class="w-3.5 h-3.5" />
                      </button>
                    {/if}
                  </div>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </Card>
  {/if}
</section>
