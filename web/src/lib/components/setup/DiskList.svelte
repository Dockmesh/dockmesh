<script lang="ts">
  // Mounted filesystems of a host with a "Use <path>" button per row.
  // Used for the server's data dir in setup and for an agent's stacks dir.
  import { m } from '$lib/paraglide/messages.js';
  import { fmtMem } from '$lib/format';

  export interface Disk {
    mountpoint: string;
    fs_type: string;
    total_bytes: number;
    free_bytes: number;
    suggested: string;
    usable: boolean;
    reason?: string;
  }

  interface Props {
    disks: Disk[];
    selected: string;
    onuse: (path: string) => void;
    disabled?: boolean;
  }

  let { disks, selected, onuse, disabled = false }: Props = $props();

  function reasonText(d: Disk): string {
    switch (d.reason) {
      case 'network share':
        return m.setup_disk_network_share();
      case 'no Unix permissions':
        return m.setup_disk_no_unix();
      default:
        return m.setup_disk_not_allowed();
    }
  }

  function usedPct(d: Disk): number {
    if (d.total_bytes <= 0) return 0;
    return Math.round(((d.total_bytes - d.free_bytes) / d.total_bytes) * 100);
  }
</script>

<div class="disks" role="list" aria-label={m.setup_disks_label()}>
  {#each disks as d (d.mountpoint)}
    <div class="disk" class:off={!d.usable} role="listitem">
      <span class="dm-mono mp" title={d.mountpoint}>{d.mountpoint}</span>
      <span class="fs">{d.fs_type}</span>
      {#if d.usable}
        <span class="use">
          <span class="dbar" aria-hidden="true"><i style:width="{usedPct(d)}%"></i></span>
          <span class="hint">{m.setup_disk_free({ free: fmtMem(d.free_bytes), total: fmtMem(d.total_bytes) })}</span>
        </span>
        {#if selected === d.suggested}
          <span class="sel">{m.setup_disk_selected()}</span>
        {:else}
          <button type="button" class="btn" {disabled} onclick={() => onuse(d.suggested)}>
            {m.setup_disk_use({ path: d.suggested })}
          </button>
        {/if}
      {:else}
        <span class="hint">{reasonText(d)}</span>
        <span class="hint end">{m.setup_disk_not_usable()}</span>
      {/if}
    </div>
  {:else}
    <div class="disk empty"><span class="hint">{m.setup_disks_none()}</span></div>
  {/each}
</div>

<style>
  .disks {
    border: 1px solid var(--border);
    border-radius: var(--r);
    overflow: hidden;
  }
  .disk {
    display: grid;
    grid-template-columns: 110px 54px minmax(0, 1fr) 210px;
    gap: 10px;
    align-items: center;
    padding: 7px 10px;
    border-top: 1px solid var(--border);
    font-size: var(--fs-sm);
  }
  .disk:first-child {
    border-top: 0;
  }
  .disk.empty {
    display: block;
  }
  .disk.off {
    color: var(--subtle);
  }
  .mp {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .use {
    display: flex;
    flex-direction: column;
    gap: 3px;
  }
  .dbar {
    height: 5px;
    border-radius: 3px;
    background: var(--field);
    overflow: hidden;
  }
  .dbar i {
    display: block;
    height: 100%;
    background: var(--accent);
  }
  .hint {
    color: var(--subtle);
    font-size: var(--fs-xs);
  }
  .end,
  .sel,
  .disk > .btn {
    justify-self: end;
  }
  .sel {
    color: var(--accent);
    font-weight: 600;
    font-size: var(--fs-sm);
    padding: 0 9px;
  }
  .btn {
    display: inline-flex;
    align-items: center;
    font: inherit;
    font-weight: 500;
    font-size: var(--fs-sm);
    border-radius: var(--r);
    padding: 0 9px;
    min-height: var(--ctl-h-sm);
    border: 1px solid var(--border);
    background: var(--surface);
    color: var(--fg);
    cursor: pointer;
    max-width: 100%;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .btn:hover:not(:disabled) {
    border-color: var(--accent);
  }
  .btn:disabled {
    opacity: 0.45;
    cursor: not-allowed;
  }
  @media (max-width: 620px) {
    .disk {
      grid-template-columns: 1fr auto;
    }
    .fs,
    .use,
    .disk.off > .hint:not(.end) {
      display: none;
    }
  }
</style>
