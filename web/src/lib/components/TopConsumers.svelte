<script lang="ts">
  import { fmtMem } from '$lib/format';
  import { api, type ContainerStatsBatch } from '$lib/api';
  import Segmented from './ui/Segmented.svelte';

  // Newest stored sample per container, ranked; refetched whenever the
  // host page refreshes (refreshAt).
  let {
    host,
    hostName,
    memTotal = 0,
    refreshAt = null
  }: {
    host: string;
    hostName: string;
    memTotal?: number;
    refreshAt?: number | null;
  } = $props();

  const TOP = 8;
  let by = $state<'cpu' | 'mem'>('cpu');
  let batch = $state<ContainerStatsBatch | null>(null);
  let failed = $state(false);

  $effect(() => {
    void refreshAt;
    const h = host;
    api.hosts
      .containerStats(h)
      .then((b) => {
        if (h !== host) return;
        batch = b;
        failed = false;
      })
      .catch(() => {
        if (h === host) failed = true;
      });
  });

  const rows = $derived(
    [...(batch?.containers ?? [])]
      .sort((a, b) => (by === 'cpu' ? b.cpu_percent - a.cpu_percent : b.mem_used - a.mem_used))
      .slice(0, TOP)
  );
  const COLS = 'minmax(0, 1.6fr) minmax(0, 1fr) 80px 150px';

  function memPct(c: { mem_used: number; mem_limit: number; mem_unlimited: boolean }): number | null {
    const base = c.mem_unlimited ? memTotal : c.mem_limit;
    return base > 0 ? Math.round((c.mem_used / base) * 100) : null;
  }
</script>

<div>
  <div class="tc-h">
    <h2 class="dm-h2">Top consumers</h2>
    <span class="dm-sp"></span>
    <Segmented
      label="Rank by"
      size="sm"
      options={[{ key: 'cpu', label: 'CPU' }, { key: 'mem', label: 'Memory' }]}
      value={by}
      onchange={(k) => (by = k as 'cpu' | 'mem')}
    />
  </div>
  {#if failed}
    <div class="dm-tbl"><div class="dm-empty">Could not load container samples.</div></div>
  {:else if batch && !batch.history}
    <div class="dm-tbl">
      <div class="dm-empty">
        {batch.reason === 'agent_no_history'
          ? `Container samples are not recorded for agent hosts; open a container on ${hostName} for live values.`
          : 'No container samples stored.'}
      </div>
    </div>
  {:else if batch && rows.length === 0}
    <div class="dm-tbl"><div class="dm-empty">No running container reported in the last minute.</div></div>
  {:else if batch}
    <div class="dm-tbl" role="table" aria-label="Top consumers on {hostName}">
      <div class="dm-tr dm-th" role="row" style="grid-template-columns: {COLS}">
        <span role="columnheader">Container</span>
        <span role="columnheader">Stack</span>
        <span role="columnheader" class="r">CPU</span>
        <span role="columnheader" class="r">Memory</span>
      </div>
      {#each rows as c (c.id)}
        {@const mp = memPct(c)}
        <a
          class="dm-tr dm-click lnk"
          role="row"
          style="grid-template-columns: {COLS}"
          href="/containers/{c.id}{host === 'local' ? '' : `?host=${encodeURIComponent(host)}`}"
        >
          <span role="cell" class="dm-trunc dm-mono">{c.name}</span>
          <span role="cell" class="dm-trunc dm-dim">{c.stack || 'standalone'}</span>
          <span role="cell" class="dm-num r" class:warn={c.cpu_percent >= 70} class:bad={c.cpu_percent >= 90}
            title="{(c.cpu_percent_core / 100).toFixed(2)} cores">{c.cpu_percent.toFixed(1)} %</span>
          <span role="cell" class="dm-num r" title={mp != null ? `${mp} % of ${c.mem_unlimited ? 'host RAM' : 'its limit'}` : ''}>
            {fmtMem(c.mem_used)}{#if mp != null}<span class="dm-dim"> · {mp} %</span>{/if}
          </span>
        </a>
      {/each}
    </div>
    <p class="dm-sub tc-hint">CPU % of the whole host; memory relative to the container's limit, or host RAM without one.</p>
  {/if}
</div>

<style>
  .tc-h { display: flex; align-items: center; gap: 10px; margin-bottom: 8px; }
  .tc-h .dm-h2 { margin: 0; }
  .lnk { color: var(--fg); text-decoration: none; }
  .r { text-align: right; }
  .warn { color: var(--warn); }
  .bad { color: var(--bad); }
  .tc-hint { margin: 6px 0 0; font-size: var(--fs-xs); }
</style>
