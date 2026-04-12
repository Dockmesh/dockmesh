<script lang="ts">
  import { api } from '$lib/api';

  let stats = $state({
    containers: 0,
    containersRunning: 0,
    stacks: 0,
    images: 0,
    networks: 0,
    volumes: 0
  });
  let health = $state<{ status: string; version: string; docker: boolean } | null>(null);
  let loading = $state(true);
  let error = $state('');

  async function load() {
    loading = true;
    error = '';
    try {
      const [h, containers, stacks, images, networks, volumes] = await Promise.all([
        api.health(),
        api.containers.list(true).catch(() => []),
        api.stacks.list().catch(() => []),
        api.images.list().catch(() => []),
        api.networks.list().catch(() => []),
        api.volumes.list().catch(() => [])
      ]);
      health = h;
      stats.containers = containers.length;
      stats.containersRunning = containers.filter((c: any) => c.State === 'running').length;
      stats.stacks = stacks.length;
      stats.images = images.length;
      stats.networks = networks.length;
      stats.volumes = volumes.length;
    } catch (err: any) {
      error = err.message ?? 'Failed to load';
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    load();
  });
</script>

<section class="space-y-4">
  <div class="flex justify-between items-center">
    <h2 class="text-xl font-semibold">Dashboard</h2>
    <button class="px-3 py-1 text-sm border border-[var(--border)] rounded" onclick={load} disabled={loading}>
      Refresh
    </button>
  </div>

  {#if error}
    <div class="p-3 rounded border border-red-500/30 bg-red-500/10 text-red-500 text-sm">{error}</div>
  {/if}

  <div class="grid grid-cols-2 lg:grid-cols-4 gap-4">
    <Card label="Containers" value={`${stats.containersRunning} / ${stats.containers}`} hint="running / total" />
    <Card label="Stacks" value={stats.stacks} />
    <Card label="Images" value={stats.images} />
    <Card label="Networks" value={stats.networks} />
    <Card label="Volumes" value={stats.volumes} />
    <Card
      label="Docker"
      value={health?.docker ? 'connected' : 'offline'}
      hint={health?.version ? `dockmesh ${health.version}` : ''}
    />
  </div>
</section>

{#snippet Card({ label, value, hint }: { label: string; value: string | number; hint?: string })}
  <div class="p-4 rounded border border-[var(--border)] bg-[var(--panel)]">
    <div class="text-sm text-[var(--muted)]">{label}</div>
    <div class="text-2xl font-bold mt-1">{value}</div>
    {#if hint}<div class="text-xs text-[var(--muted)] mt-1">{hint}</div>{/if}
  </div>
{/snippet}
