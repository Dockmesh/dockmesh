<script lang="ts">
  import { untrack } from 'svelte';
  // Running and recent stack migrations, opened from the top-bar pill.
  // Polls while open; closing it leaves the migrations running.
  import { fmtBytes, fmtAgo } from '$lib/format';
  import { Modal, StatusChip } from '$lib/components/ui';
  import type { Tone } from '$lib/components/ui/StatusChip.svelte';
  import { api, type Migration } from '$lib/api';
  import { goto } from '$app/navigation';
  import { ArrowRightLeft } from 'lucide-svelte';

  interface Props {
    open: boolean;
  }
  let { open = $bindable(false) }: Props = $props();

  let active = $state<Migration[]>([]);
  let recent = $state<Migration[]>([]);
  let hostNames = $state<Map<string, string>>(new Map());
  let loading = $state(true);

  async function load() {
    try {
      const [a, all, hosts] = await Promise.all([
        api.migrations.active().catch(() => [] as Migration[]),
        api.migrations.list(50).catch(() => [] as Migration[]),
        api.hosts.list().catch(() => []),
      ]);
      active = a;
      const activeIds = new Set(a.map((m) => m.id));
      recent = all.filter((m) => !activeIds.has(m.id)).slice(0, 30);
      const m = new Map<string, string>();
      for (const h of hosts) m.set(h.id, h.name);
      hostNames = m;
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    if (!open) return;
    untrack(load);
    const t = setInterval(load, 5_000);
    return () => clearInterval(t);
  });

  function close() { open = false; }

  function openStack(name: string) {
    close();
    goto(`/stacks/${encodeURIComponent(name)}`);
  }

  function hostLabel(id: string): string {
    return hostNames.get(id) ?? id;
  }

  function fmtDuration(start?: string, end?: string): string {
    if (!start) return '—';
    const s = new Date(start).getTime();
    const e = end ? new Date(end).getTime() : Date.now();
    const secs = Math.floor((e - s) / 1000);
    if (secs < 60) return `${secs}s`;
    if (secs < 3600) return `${Math.floor(secs / 60)}m ${secs % 60}s`;
    return `${Math.floor(secs / 3600)}h ${Math.floor((secs % 3600) / 60)}m`;
  }

  function progressPercent(m: Migration): number {
    if (!m.progress) return 0;
    if (m.progress.bytes_total > 0) {
      return Math.min(100, Math.round((m.progress.bytes_done / m.progress.bytes_total) * 100));
    }
    if (m.progress.volumes_total > 0) {
      return Math.min(100, Math.round((m.progress.volume_index / m.progress.volumes_total) * 100));
    }
    return 0;
  }

  function statusTone(s: string): Tone {
    if (s === 'completed') return 'ok';
    if (s === 'failed') return 'bad';
    if (s === 'rolled_back') return 'warn';
    return 'acc';
  }
</script>

<Modal bind:open title="Stack migrations" maxWidth="max-w-2xl" onclose={close}>
  {#if loading && active.length === 0 && recent.length === 0}
    <div class="dm-empty">Loading…</div>
  {:else if active.length === 0 && recent.length === 0}
    <div class="dm-empty">No migrations yet. Move a stack from its detail page.</div>
  {/if}

  {#if active.length > 0}
    <h3 class="dm-h2">Running <span class="dm-dim dm-num">{active.length}</span></h3>
    <div class="dm-tbl run">
      {#each active as m (m.id)}
        {@const pct = progressPercent(m)}
        <button type="button" class="mrow" onclick={() => openStack(m.stack_name)}>
          <span class="top">
            <b class="dm-trunc">{m.stack_name}</b>
            <span class="dm-dim route">{hostLabel(m.source_host_id)} <ArrowRightLeft size={11} strokeWidth={1.6} /> {hostLabel(m.target_host_id)}</span>
            <span class="dm-sp"></span>
            <StatusChip tone="acc" label={m.phase || m.status} />
          </span>
          {#if m.progress && (m.progress.bytes_total > 0 || m.progress.volumes_total > 0)}
            <span class="bar"><i style="width: {pct}%"></i></span>
            <span class="meta dm-num">
              {#if m.progress.bytes_total > 0}{fmtBytes(m.progress.bytes_done)} of {fmtBytes(m.progress.bytes_total)} · {/if}volume {m.progress.volume_index} of {m.progress.volumes_total}{#if m.progress.images_total > 0} · image {m.progress.images_pulled} of {m.progress.images_total}{/if} · {pct} %
            </span>
          {/if}
          <span class="meta">Started {fmtAgo(m.started_at)} by {m.initiated_by}</span>
        </button>
      {/each}
    </div>
  {/if}

  {#if recent.length > 0}
    <h3 class="dm-h2 sp">Recent</h3>
    <div class="dm-tbl">
      {#each recent as m (m.id)}
        <button type="button" class="dm-tr dm-click rrow" style="grid-template-columns: minmax(0, 1fr) minmax(0, 1fr) 110px 70px" onclick={() => openStack(m.stack_name)} title={m.error_message ?? ''}>
          <b class="dm-trunc">{m.stack_name}</b>
          <span class="dm-dim dm-trunc">{hostLabel(m.source_host_id)} → {hostLabel(m.target_host_id)}</span>
          <span><StatusChip tone={statusTone(m.status)} label={m.status} /></span>
          <span class="dm-num dm-dim">{fmtDuration(m.started_at, m.completed_at)}</span>
        </button>
      {/each}
    </div>
  {/if}
</Modal>

<style>
  .sp { margin-top: 18px; }
  .run { border-bottom: 0; }
  .mrow { all: unset; box-sizing: border-box; display: flex; flex-direction: column; gap: 6px; width: 100%; padding: 10px 6px; border-bottom: 1px solid var(--border); cursor: pointer; }
  .mrow:hover { background: var(--band); }
  .mrow:focus-visible, .rrow:focus-visible { outline: 2px solid var(--accent); outline-offset: -2px; }
  .top { display: flex; align-items: center; gap: 10px; min-width: 0; }
  .route { display: inline-flex; align-items: center; gap: 6px; }
  .bar { height: 5px; border-radius: 3px; background: var(--field); overflow: hidden; }
  .bar i { display: block; height: 100%; background: var(--accent); }
  .meta { font-size: var(--fs-xs); color: var(--subtle); }
  .rrow { all: unset; box-sizing: border-box; display: grid; gap: 12px; align-items: center; width: 100%; padding: 0 6px; height: var(--row-h); border-bottom: 1px solid var(--border); cursor: pointer; }
  .rrow:hover { background: var(--band); }
</style>
