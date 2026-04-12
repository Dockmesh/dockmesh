<script lang="ts">
  import { api, ApiError } from '$lib/api';

  interface Container {
    Id: string;
    Names: string[];
    Image: string;
    State: string;
    Status: string;
    Ports: Array<{ PrivatePort: number; PublicPort?: number; Type: string }>;
  }

  let containers = $state<Container[]>([]);
  let loading = $state(true);
  let error = $state('');
  let showAll = $state(true);

  async function load() {
    loading = true;
    error = '';
    try {
      containers = await api.containers.list(showAll);
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Load failed';
    } finally {
      loading = false;
    }
  }

  async function action(id: string, op: 'start' | 'stop' | 'restart' | 'remove') {
    try {
      if (op === 'start') await api.containers.start(id);
      else if (op === 'stop') await api.containers.stop(id);
      else if (op === 'restart') await api.containers.restart(id);
      else if (op === 'remove') {
        if (!confirm('Remove this container?')) return;
        await api.containers.remove(id, true);
      }
      await load();
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Action failed';
    }
  }

  function portSummary(c: Container): string {
    if (!c.Ports) return '';
    const seen = new Set<string>();
    for (const p of c.Ports) {
      if (p.PublicPort) seen.add(`${p.PublicPort}→${p.PrivatePort}/${p.Type}`);
    }
    return [...seen].join(', ');
  }

  $effect(() => { load(); });
</script>

<section class="space-y-4">
  <div class="flex justify-between items-center">
    <h2 class="text-xl font-semibold">Containers</h2>
    <div class="flex gap-2 items-center">
      <label class="text-sm flex items-center gap-1">
        <input type="checkbox" bind:checked={showAll} onchange={load} /> show stopped
      </label>
      <button class="px-3 py-1 text-sm border border-[var(--border)] rounded" onclick={load}>Refresh</button>
    </div>
  </div>

  {#if error}
    <div class="p-3 rounded border border-red-500/30 bg-red-500/10 text-red-500 text-sm">{error}</div>
  {/if}

  {#if loading}
    <p class="text-[var(--muted)]">Loading…</p>
  {:else if containers.length === 0}
    <p class="text-[var(--muted)]">No containers.</p>
  {:else}
    <div class="space-y-2">
      {#each containers as c}
        <div class="p-3 rounded border border-[var(--border)] bg-[var(--panel)] flex items-center gap-3">
          <span class="w-2 h-2 rounded-full {c.State === 'running' ? 'bg-green-500' : 'bg-[var(--muted)]'}"></span>
          <div class="flex-1 min-w-0">
            <div class="font-mono text-sm truncate">{c.Names?.[0]?.replace(/^\//, '') ?? c.Id.slice(0, 12)}</div>
            <div class="text-xs text-[var(--muted)] truncate">{c.Image} · {c.Status}{portSummary(c) ? ' · ' + portSummary(c) : ''}</div>
          </div>
          <div class="flex gap-1">
            {#if c.State === 'running'}
              <button class="px-2 py-1 text-xs border border-[var(--border)] rounded" onclick={() => action(c.Id, 'restart')}>Restart</button>
              <button class="px-2 py-1 text-xs border border-[var(--border)] rounded" onclick={() => action(c.Id, 'stop')}>Stop</button>
            {:else}
              <button class="px-2 py-1 text-xs border border-[var(--border)] rounded" onclick={() => action(c.Id, 'start')}>Start</button>
            {/if}
            <button class="px-2 py-1 text-xs border border-red-500/50 text-red-500 rounded" onclick={() => action(c.Id, 'remove')}>×</button>
          </div>
        </div>
      {/each}
    </div>
  {/if}
</section>
