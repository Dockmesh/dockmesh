<script lang="ts">
  import { api } from '$lib/api';
  import { Card, Skeleton, Badge } from '$lib/components/ui';
  import { Box, Layers, Image as ImageIcon, Network, HardDrive, Activity, RefreshCw } from 'lucide-svelte';

  let stats = $state({
    containers: 0,
    containersRunning: 0,
    stacks: 0,
    images: 0,
    networks: 0,
    volumes: 0
  });
  let health = $state<{ status: string; version: string; docker: boolean } | null>(null);
  let recentAudit = $state<any[]>([]);
  let loading = $state(true);
  let error = $state('');

  async function load() {
    loading = true;
    error = '';
    try {
      const [h, containers, stacks, images, networks, volumes, audit] = await Promise.all([
        api.health(),
        api.containers.list(true).catch(() => []),
        api.stacks.list().catch(() => []),
        api.images.list().catch(() => []),
        api.networks.list().catch(() => []),
        api.volumes.list().catch(() => []),
        api.audit.list(10).catch(() => [])
      ]);
      health = h;
      stats.containers = containers.length;
      stats.containersRunning = containers.filter((c: any) => c.State === 'running').length;
      stats.stacks = stacks.length;
      stats.images = images.length;
      stats.networks = networks.length;
      stats.volumes = volumes.length;
      recentAudit = audit;
    } catch (err: any) {
      error = err.message ?? 'Failed to load';
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    load();
  });

  function actionColor(action: string): 'success' | 'warning' | 'danger' | 'info' | 'default' {
    if (action.includes('delete') || action.includes('remove') || action.includes('failed')) return 'danger';
    if (action.includes('create') || action.includes('deploy') || action.includes('start')) return 'success';
    if (action.includes('update') || action.includes('refresh')) return 'info';
    return 'default';
  }

  function fmtTime(ts: string): string {
    const t = new Date(ts);
    const diff = (Date.now() - t.getTime()) / 1000;
    if (diff < 60) return 'just now';
    if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
    if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
    return t.toLocaleDateString();
  }
</script>

<section class="space-y-6">
  <!-- Header row -->
  <div class="flex items-center justify-between flex-wrap gap-3">
    <div>
      <h2 class="text-2xl font-semibold tracking-tight">Overview</h2>
      <p class="text-sm text-[var(--fg-muted)] mt-0.5">
        {#if health?.docker}
          Connected to Docker · {health.version}
        {:else if health}
          <span class="text-[var(--color-warning-400)]">Docker daemon unreachable</span>
        {:else}
          Loading system status…
        {/if}
      </p>
    </div>
    <button
      onclick={load}
      class="dm-btn dm-btn-secondary dm-btn-sm"
      disabled={loading}
      aria-label="Refresh"
    >
      <RefreshCw class="w-3.5 h-3.5 {loading ? 'animate-spin' : ''}" />
      Refresh
    </button>
  </div>

  {#if error}
    <div class="dm-card p-4 border-[color-mix(in_srgb,var(--color-danger-500)_30%,transparent)] text-[var(--color-danger-400)] text-sm">
      {error}
    </div>
  {/if}

  <!-- Metric cards -->
  <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4">
    {#snippet metric(label: string, value: string | number, sub: string, Icon: any, iconColor: string)}
      <Card class="p-5">
        <div class="flex items-start justify-between">
          <div class="min-w-0 flex-1">
            <div class="text-xs text-[var(--fg-muted)] uppercase tracking-wider font-medium">{label}</div>
            {#if loading}
              <Skeleton class="mt-2" width="60%" height="1.75rem" />
            {:else}
              <div class="text-2xl font-semibold mt-1 font-mono tabular-nums">{value}</div>
            {/if}
            <div class="text-xs text-[var(--fg-subtle)] mt-1">{sub}</div>
          </div>
          <div class="w-9 h-9 rounded-lg {iconColor} flex items-center justify-center shrink-0">
            <Icon class="w-4.5 h-4.5" />
          </div>
        </div>
      </Card>
    {/snippet}

    {@render metric(
      'Containers',
      `${stats.containersRunning}/${stats.containers}`,
      'running of total',
      Box,
      'bg-[color-mix(in_srgb,var(--color-brand-500)_15%,transparent)] text-[var(--color-brand-400)]'
    )}
    {@render metric('Stacks', stats.stacks, 'filesystem-backed', Layers, 'bg-[color-mix(in_srgb,var(--color-success-500)_15%,transparent)] text-[var(--color-success-400)]')}
    {@render metric('Images', stats.images, 'local', ImageIcon, 'bg-[color-mix(in_srgb,#a855f7_15%,transparent)] text-[#c084fc]')}
    {@render metric('Networks', stats.networks, 'docker networks', Network, 'bg-[color-mix(in_srgb,#f97316_15%,transparent)] text-[#fb923c]')}
  </div>

  <!-- Secondary row: volumes + recent activity -->
  <div class="grid grid-cols-1 lg:grid-cols-3 gap-4">
    <Card class="p-5">
      <div class="flex items-start justify-between mb-3">
        <div>
          <div class="text-xs text-[var(--fg-muted)] uppercase tracking-wider font-medium">Volumes</div>
          <div class="text-2xl font-semibold mt-1 font-mono tabular-nums">{stats.volumes}</div>
          <div class="text-xs text-[var(--fg-subtle)] mt-1">persistent storage</div>
        </div>
        <div class="w-9 h-9 rounded-lg bg-[color-mix(in_srgb,#eab308_15%,transparent)] text-[#facc15] flex items-center justify-center shrink-0">
          <HardDrive class="w-4.5 h-4.5" />
        </div>
      </div>
    </Card>

    <!-- Recent activity spans 2 cols -->
    <Card class="lg:col-span-2">
      <div class="p-5 border-b border-[var(--border)] flex items-center gap-2">
        <Activity class="w-4 h-4 text-[var(--fg-muted)]" />
        <h3 class="font-semibold text-sm">Recent activity</h3>
      </div>
      <div class="divide-y divide-[var(--border)]">
        {#if loading}
          {#each Array(5) as _}
            <div class="px-5 py-3 flex items-center gap-3">
              <Skeleton width="5rem" height="1.25rem" />
              <Skeleton width="8rem" height="1rem" />
              <div class="flex-1"></div>
              <Skeleton width="4rem" height="0.85rem" />
            </div>
          {/each}
        {:else if recentAudit.length === 0}
          <div class="px-5 py-8 text-center text-sm text-[var(--fg-muted)]">No activity yet</div>
        {:else}
          {#each recentAudit as e}
            <div class="px-5 py-3 flex items-center gap-3 text-sm">
              <Badge variant={actionColor(e.action)} dot>
                {e.action}
              </Badge>
              <span class="font-mono text-xs text-[var(--fg-muted)] truncate flex-1">{e.target || '—'}</span>
              <span class="text-xs text-[var(--fg-subtle)] shrink-0">{fmtTime(e.ts)}</span>
            </div>
          {/each}
        {/if}
      </div>
    </Card>
  </div>
</section>
