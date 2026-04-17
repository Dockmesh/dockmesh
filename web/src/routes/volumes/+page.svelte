<script lang="ts">
  import { api, ApiError, isFanOut } from '$lib/api';
  import { Card, Badge, Skeleton, EmptyState, Button, Modal, Input } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { allowed } from '$lib/rbac';
  import { hosts } from '$lib/stores/host.svelte';
  import {
    HardDrive, RefreshCw, Server, Search, Plus, Trash2, ChevronUp, ChevronDown
  } from 'lucide-svelte';

  const canWrite = $derived(allowed('volume.write'));

  interface VolumeRow {
    Name: string;
    Driver: string;
    Scope: string;
    Mountpoint: string;
    CreatedAt?: string;
    Labels?: Record<string, string>;
    host_id?: string;
    host_name?: string;
  }

  let volumes = $state<VolumeRow[]>([]);
  let loading = $state(true);
  let search = $state('');

  // Sort
  type SortKey = 'name' | 'driver' | 'scope' | 'created';
  let sortKey = $state<SortKey>('name');
  let sortAsc = $state(true);

  // Bulk
  let selected = $state<Set<string>>(new Set());
  let bulkBusy = $state(false);

  // Create modal
  let showCreate = $state(false);
  let newName = $state('');
  let newDriver = $state('local');
  let creating = $state(false);

  const isAll = $derived(hosts.isAll);

  async function load() {
    loading = true;
    try {
      const raw = await api.volumes.list(hosts.id);
      if (isFanOut(raw)) {
        volumes = raw.items as VolumeRow[];
      } else {
        volumes = (raw as any[]).map((v: any) => ({ ...v }));
      }
    } catch (err) {
      toast.error('Failed to load volumes', err instanceof ApiError ? err.message : undefined);
    } finally {
      loading = false;
    }
  }

  $effect(() => { hosts.id; load(); });

  // Filtering + sorting
  const visible = $derived(
    volumes
      .filter((v) => {
        if (!search.trim()) return true;
        const q = search.toLowerCase();
        return v.Name.toLowerCase().includes(q) || v.Driver.toLowerCase().includes(q);
      })
      .sort((a, b) => {
        let cmp = 0;
        switch (sortKey) {
          case 'name': cmp = a.Name.localeCompare(b.Name); break;
          case 'driver': cmp = a.Driver.localeCompare(b.Driver); break;
          case 'scope': cmp = a.Scope.localeCompare(b.Scope); break;
          case 'created': cmp = (a.CreatedAt ?? '').localeCompare(b.CreatedAt ?? ''); break;
        }
        return sortAsc ? cmp : -cmp;
      })
  );

  const allSelected = $derived(visible.length > 0 && visible.every(v => selected.has(vKey(v))));
  function vKey(v: VolumeRow): string { return `${v.host_id ?? 'local'}/${v.Name}`; }
  function toggleAll() {
    if (allSelected) { selected = new Set(); }
    else { selected = new Set(visible.map(v => vKey(v))); }
  }
  function toggleOne(v: VolumeRow) {
    const k = vKey(v);
    const next = new Set(selected);
    if (next.has(k)) next.delete(k); else next.add(k);
    selected = next;
  }

  function toggleSort(key: SortKey) {
    if (sortKey === key) { sortAsc = !sortAsc; }
    else { sortKey = key; sortAsc = true; }
  }

  function fmtDate(ts?: string): string {
    if (!ts) return '—';
    return new Date(ts).toLocaleString();
  }

  function labelCount(v: VolumeRow): number {
    return v.Labels ? Object.keys(v.Labels).length : 0;
  }

  // Actions
  async function createVolume(e: Event) {
    e.preventDefault();
    creating = true;
    try {
      await api.volumes.create(newName, newDriver);
      toast.success('Volume created', newName);
      showCreate = false;
      newName = '';
      await load();
    } catch (err) {
      toast.error('Create failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      creating = false;
    }
  }

  async function deleteVolume(v: VolumeRow) {
    if (!confirm(`Delete volume "${v.Name}"?`)) return;
    try {
      await api.volumes.remove(v.Name, true);
      toast.success('Deleted', v.Name);
      await load();
    } catch (err) {
      toast.error('Delete failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function bulkDelete() {
    if (!confirm(`Delete ${selected.size} volume(s)?`)) return;
    bulkBusy = true;
    let ok = 0, fail = 0;
    for (const v of volumes.filter(v => selected.has(vKey(v)))) {
      try { await api.volumes.remove(v.Name, true); ok++; } catch { fail++; }
    }
    toast.success(`Deleted: ${ok}${fail ? `, ${fail} failed` : ''}`);
    selected = new Set();
    bulkBusy = false;
    await load();
  }

  async function pruneVolumes() {
    if (!confirm('Remove all unused volumes? This cannot be undone.')) return;
    try {
      const res = await api.volumes.prune();
      const count = res?.VolumesDeleted?.length ?? 0;
      toast.success('Pruned', `${count} volume(s) removed`);
      await load();
    } catch (err) {
      toast.error('Prune failed', err instanceof ApiError ? err.message : undefined);
    }
  }
</script>

<section class="space-y-4">
  <!-- Header -->
  <div class="flex items-center justify-between flex-wrap gap-3">
    <div>
      <h2 class="text-2xl font-semibold tracking-tight">Volumes</h2>
      <p class="text-sm text-[var(--fg-muted)] mt-0.5">
        {volumes.length} volume{volumes.length === 1 ? '' : 's'}
        {#if isAll}across all hosts{:else if hosts.selected?.name && hosts.id !== 'local'}on {hosts.selected.name}{/if}
      </p>
    </div>
    <div class="flex items-center gap-2">
      {#if canWrite}
        <Button variant="secondary" size="sm" onclick={pruneVolumes}>
          <Trash2 class="w-3.5 h-3.5" /> Prune unused
        </Button>
        <Button variant="primary" size="sm" onclick={() => (showCreate = true)}>
          <Plus class="w-3.5 h-3.5" /> Create
        </Button>
      {/if}
      <Button variant="secondary" size="sm" onclick={load}>
        <RefreshCw class="w-3.5 h-3.5 {loading ? 'animate-spin' : ''}" /> Refresh
      </Button>
    </div>
  </div>

  <!-- Search -->
  {#if volumes.length > 0}
    <div class="relative max-w-sm">
      <Search class="w-3.5 h-3.5 text-[var(--fg-subtle)] absolute left-2.5 top-1/2 -translate-y-1/2 pointer-events-none" />
      <input type="search" placeholder="Search by name or driver…" bind:value={search} class="dm-input pl-8 pr-3 py-1.5 text-sm w-full" />
    </div>
  {/if}

  <!-- Bulk action bar -->
  {#if selected.size > 0 && canWrite}
    <div class="flex items-center gap-3 px-4 py-2.5 rounded-lg bg-[var(--surface)] border border-[var(--border)]">
      <span class="text-sm font-medium">{selected.size} selected</span>
      <div class="flex gap-1.5 ml-auto">
        <Button size="xs" variant="danger" onclick={bulkDelete} disabled={bulkBusy}>
          <Trash2 class="w-3.5 h-3.5" /> Delete
        </Button>
        <button class="text-xs text-[var(--fg-muted)] hover:text-[var(--fg)] ml-2" onclick={() => (selected = new Set())}>Clear</button>
      </div>
    </div>
  {/if}

  <!-- Table -->
  {#if loading && volumes.length === 0}
    <Card>
      <div class="divide-y divide-[var(--border)]">
        {#each Array(5) as _}
          <div class="px-5 py-3.5 flex items-center gap-4">
            <Skeleton width="1rem" height="1rem" />
            <Skeleton width="30%" height="0.85rem" />
            <Skeleton width="15%" height="0.75rem" />
          </div>
        {/each}
      </div>
    </Card>
  {:else if volumes.length === 0}
    <Card>
      <EmptyState icon={HardDrive} title="No volumes" description="Docker volumes will appear here once containers with named volumes are running." />
    </Card>
  {:else if visible.length === 0}
    <Card class="p-8 text-center text-sm text-[var(--fg-muted)]">No volumes match this search.</Card>
  {:else}
    <Card>
      <div class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead>
            <tr class="border-b border-[var(--border)] text-[var(--fg-muted)] text-xs uppercase tracking-wider">
              {#if canWrite}
                <th class="w-10 px-3 py-3">
                  <input type="checkbox" checked={allSelected} onchange={toggleAll} class="accent-[var(--color-brand-500)]" />
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
              {@render sortHeader('driver', 'Driver')}
              {@render sortHeader('scope', 'Scope')}
              <th class="text-left px-3 py-3">Mountpoint</th>
              <th class="text-right px-3 py-3">Labels</th>
              {@render sortHeader('created', 'Created')}
              {#if isAll}
                <th class="text-left px-3 py-3">Host</th>
              {/if}
              {#if canWrite}
                <th class="text-right px-3 py-3 w-20">Actions</th>
              {/if}
            </tr>
          </thead>
          <tbody class="divide-y divide-[var(--border)]">
            {#each visible as v (vKey(v))}
              <tr class="hover:bg-[var(--surface-hover)] transition-colors {selected.has(vKey(v)) ? 'bg-[color-mix(in_srgb,var(--color-brand-500)_5%,transparent)]' : ''}">
                {#if canWrite}
                  <td class="w-10 px-3 py-2.5">
                    <input type="checkbox" checked={selected.has(vKey(v))} onchange={() => toggleOne(v)} class="accent-[var(--color-brand-500)]" />
                  </td>
                {/if}
                <td class="px-3 py-2.5">
                  <a
                    href={`/volumes/${encodeURIComponent(v.Name)}${v.host_id && v.host_id !== 'local' ? `?host=${encodeURIComponent(v.host_id)}` : ''}`}
                    class="font-mono text-sm truncate block max-w-[250px] text-[var(--fg)] hover:text-[var(--color-brand-500)] hover:underline"
                    title={v.Name}
                  >{v.Name}</a>
                </td>
                <td class="px-3 py-2.5 text-xs text-[var(--fg-muted)]">{v.Driver}</td>
                <td class="px-3 py-2.5"><Badge variant={v.Scope === 'local' ? 'default' : 'info'}>{v.Scope}</Badge></td>
                <td class="px-3 py-2.5">
                  <span class="font-mono text-[10px] text-[var(--fg-muted)] truncate block max-w-[200px]" title={v.Mountpoint}>{v.Mountpoint || '—'}</span>
                </td>
                <td class="px-3 py-2.5 text-right text-xs text-[var(--fg-muted)] tabular-nums">{labelCount(v)}</td>
                <td class="px-3 py-2.5 text-xs text-[var(--fg-muted)]">{fmtDate(v.CreatedAt)}</td>
                {#if isAll}
                  <td class="px-3 py-2.5">
                    <span class="inline-flex items-center gap-1 font-mono text-[10px] px-1.5 py-0.5 rounded border border-[var(--border)] text-[var(--fg-muted)]">
                      <Server class="w-2.5 h-2.5" />
                      {v.host_name || v.host_id || 'local'}
                    </span>
                  </td>
                {/if}
                {#if canWrite}
                  <td class="px-3 py-2.5 text-right">
                    <button
                      class="p-1.5 rounded-md text-[var(--color-danger-400)] hover:bg-[color-mix(in_srgb,var(--color-danger-500)_10%,transparent)]"
                      title="Delete volume"
                      onclick={() => deleteVolume(v)}
                    >
                      <Trash2 class="w-3.5 h-3.5" />
                    </button>
                  </td>
                {/if}
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </Card>
  {/if}
</section>

<!-- Create volume modal -->
<Modal bind:open={showCreate} title="Create volume" maxWidth="max-w-sm">
  <form onsubmit={createVolume} id="create-vol-form" class="space-y-3">
    <Input label="Name" placeholder="my-volume" bind:value={newName} disabled={creating} />
    <Input label="Driver" bind:value={newDriver} disabled={creating} hint="Usually 'local'" />
  </form>
  {#snippet footer()}
    <Button variant="secondary" onclick={() => (showCreate = false)}>Cancel</Button>
    <Button variant="primary" type="submit" form="create-vol-form" loading={creating} disabled={creating || !newName.trim()}>Create</Button>
  {/snippet}
</Modal>
