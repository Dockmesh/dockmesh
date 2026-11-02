<script lang="ts">
  import { m } from '$lib/paraglide/messages.js';
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
      const activeIds = new Set(a.map((mg) => mg.id));
      recent = all.filter((mg) => !activeIds.has(mg.id)).slice(0, 30);
      const names = new Map<string, string>();
      for (const h of hosts) names.set(h.id, h.name);
      hostNames = names;
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

  const MIG_STATUS: Record<string, () => string> = {
    pending: m.ops_mig_st_pending,
    preflight: m.ops_mig_st_preflight,
    preparing: m.ops_mig_st_preparing,
    pre_dump: m.ops_mig_st_pre_dump,
    stopping: m.ops_mig_st_stopping,
    syncing: m.ops_mig_st_syncing,
    starting: m.ops_mig_st_starting,
    post_restore: m.ops_mig_st_post_restore,
    health_check: m.ops_mig_st_health_check,
    committing: m.ops_mig_st_committing,
    completed: m.ops_mig_st_completed,
    failed: m.ops_mig_st_failed,
    rolled_back: m.ops_mig_st_rolled_back
  };
  function statusLabel(s: string): string {
    return MIG_STATUS[s]?.() ?? s;
  }

  function statusTone(s: string): Tone {
    if (s === 'completed') return 'ok';
    if (s === 'failed') return 'bad';
    if (s === 'rolled_back') return 'warn';
    return 'acc';
  }
</script>

<Modal bind:open title={m.ops_mig_title()} maxWidth="max-w-2xl" onclose={close}>
  {#if loading && active.length === 0 && recent.length === 0}
    <div class="dm-empty">{m.common_loading()}</div>
  {:else if active.length === 0 && recent.length === 0}
    <div class="dm-empty">{m.ops_mig_empty()}</div>
  {/if}

  {#if active.length > 0}
    <h3 class="dm-h2">{m.common_running()} <span class="dm-dim dm-num">{active.length}</span></h3>
    <div class="dm-tbl run">
      {#each active as mg (mg.id)}
        {@const pct = progressPercent(mg)}
        <button type="button" class="mrow" onclick={() => openStack(mg.stack_name)}>
          <span class="top">
            <b class="dm-trunc">{mg.stack_name}</b>
            <span class="dm-dim route">{hostLabel(mg.source_host_id)} <ArrowRightLeft size={11} strokeWidth={1.6} /> {hostLabel(mg.target_host_id)}</span>
            <span class="dm-sp"></span>
            <StatusChip tone="acc" label={statusLabel(mg.phase || mg.status)} />
          </span>
          {#if mg.progress && (mg.progress.bytes_total > 0 || mg.progress.volumes_total > 0)}
            <span class="bar"><i style="width: {pct}%"></i></span>
            <span class="meta dm-num">
              {#if mg.progress.bytes_total > 0}{m.ops_mig_bytes({ done: fmtBytes(mg.progress.bytes_done), total: fmtBytes(mg.progress.bytes_total) })} · {/if}{m.ops_mig_volume({ i: mg.progress.volume_index, n: mg.progress.volumes_total })}{#if mg.progress.images_total > 0} · {m.ops_mig_image({ i: mg.progress.images_pulled, n: mg.progress.images_total })}{/if} · {pct} %
            </span>
          {/if}
          <span class="meta">{m.ops_mig_started({ ago: fmtAgo(mg.started_at), who: mg.initiated_by })}</span>
        </button>
      {/each}
    </div>
  {/if}

  {#if recent.length > 0}
    <h3 class="dm-h2 sp">{m.ops_mig_recent()}</h3>
    <div class="dm-tbl">
      {#each recent as mg (mg.id)}
        <button type="button" class="dm-tr dm-click rrow" style="grid-template-columns: minmax(0, 1fr) minmax(0, 1fr) 110px 70px" onclick={() => openStack(mg.stack_name)} title={mg.error_message ?? ''}>
          <b class="dm-trunc">{mg.stack_name}</b>
          <span class="dm-dim dm-trunc">{hostLabel(mg.source_host_id)} → {hostLabel(mg.target_host_id)}</span>
          <span><StatusChip tone={statusTone(mg.status)} label={statusLabel(mg.status)} /></span>
          <span class="dm-num dm-dim">{fmtDuration(mg.started_at, mg.completed_at)}</span>
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
