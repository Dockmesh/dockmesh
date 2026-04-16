<script lang="ts">
  import { api, ApiError, isFanOut } from '$lib/api';
  import { Card, Badge, Skeleton, EmptyState } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { hosts } from '$lib/stores/host.svelte';
  import { HardDrive, RefreshCw, Server, Search } from 'lucide-svelte';

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

  async function load() {
    loading = true;
    try {
      const raw = await api.volumes.list(hosts.id);
      if (isFanOut(raw)) {
        volumes = raw.items as VolumeRow[];
      } else {
        volumes = (raw as any[]).map((v: any) => ({
          ...v,
          host_id: undefined,
          host_name: undefined
        }));
      }
    } catch (err) {
      toast.error('Failed to load volumes', err instanceof ApiError ? err.message : undefined);
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    hosts.id;
    load();
  });

  const visible = $derived(
    volumes.filter((v) => {
      if (!search.trim()) return true;
      const q = search.toLowerCase();
      return v.Name.toLowerCase().includes(q) || v.Driver.toLowerCase().includes(q);
    })
  );

  function fmtDate(ts?: string): string {
    if (!ts) return '—';
    return new Date(ts).toLocaleString();
  }
</script>

<section class="space-y-6">
  <div class="flex items-start justify-between flex-wrap gap-3">
    <div>
      <h2 class="text-2xl font-semibold tracking-tight">Volumes</h2>
      <p class="text-sm text-[var(--fg-muted)] mt-0.5">
        Docker volumes across {hosts.isAll ? 'all hosts' : hosts.selected?.name ?? 'local'}.
      </p>
    </div>
    <button
      onclick={load}
      class="dm-btn dm-btn-secondary dm-btn-sm"
      disabled={loading}
    >
      <RefreshCw class="w-3.5 h-3.5 {loading ? 'animate-spin' : ''}" /> Refresh
    </button>
  </div>

  {#if !loading && volumes.length > 0}
    <div class="relative max-w-sm">
      <Search class="w-3.5 h-3.5 text-[var(--fg-subtle)] absolute left-2.5 top-1/2 -translate-y-1/2 pointer-events-none" />
      <input
        type="search"
        placeholder="Search volumes…"
        bind:value={search}
        class="dm-input pl-8 pr-3 py-1.5 text-sm w-full"
      />
    </div>
  {/if}

  {#if loading}
    <Card><Skeleton class="m-5" width="80%" height="6rem" /></Card>
  {:else if volumes.length === 0}
    <Card>
      <EmptyState
        icon={HardDrive}
        title="No volumes"
        description="Docker volumes will appear here once containers with named volumes are running."
      />
    </Card>
  {:else}
    <Card>
      <div class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead>
            <tr class="border-b border-[var(--border)] text-[var(--fg-muted)] text-xs uppercase tracking-wider">
              <th class="text-left px-5 py-3">Name</th>
              <th class="text-left px-3 py-3">Driver</th>
              <th class="text-left px-3 py-3">Scope</th>
              {#if hosts.isAll}
                <th class="text-left px-3 py-3">Host</th>
              {/if}
              <th class="text-left px-3 py-3">Created</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-[var(--border)]">
            {#each visible as v}
              <tr class="hover:bg-[var(--surface-hover)]">
                <td class="px-5 py-3">
                  <div class="font-mono text-sm truncate max-w-[300px]" title={v.Name}>{v.Name}</div>
                </td>
                <td class="px-3 py-3 text-xs text-[var(--fg-muted)]">{v.Driver}</td>
                <td class="px-3 py-3">
                  <Badge variant={v.Scope === 'local' ? 'default' : 'info'}>{v.Scope}</Badge>
                </td>
                {#if hosts.isAll}
                  <td class="px-3 py-3">
                    <span class="inline-flex items-center gap-1 font-mono text-[10px] px-1.5 py-0.5 rounded border border-[var(--border)] text-[var(--fg-muted)]">
                      <Server class="w-2.5 h-2.5" />
                      {v.host_name || v.host_id || 'local'}
                    </span>
                  </td>
                {/if}
                <td class="px-3 py-3 text-xs text-[var(--fg-muted)]">{fmtDate(v.CreatedAt)}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </Card>
  {/if}
</section>
