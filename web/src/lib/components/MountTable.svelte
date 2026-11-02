<script lang="ts">
  import { m } from '$lib/paraglide/messages.js';
  // Per-filesystem disk usage of one host, fullest first (server order).
  import { fmtBytes } from '$lib/format';
  import type { DiskMount } from '$lib/api';

  interface Props {
    mounts?: DiskMount[];
    unsupported?: boolean;
  }

  let { mounts = [], unsupported = false }: Props = $props();

  const ROLE_LABEL: Record<string, string> = {
    docker_root: 'Docker',
    containerd_root: 'containerd',
    stacks: m.metrics_mount_role_stacks(),
    data: m.metrics_mount_role_data(),
    backup_target: m.metrics_mount_role_backups(),
  };

  function level(pct: number): 'ok' | 'warn' | 'crit' {
    if (pct >= 90) return 'crit';
    if (pct >= 80) return 'warn';
    return 'ok';
  }
</script>

{#if unsupported}
  <p class="mt-hint">{m.metrics_mount_unsupported()}</p>
{:else if mounts.length === 0}
  <p class="mt-hint">{m.metrics_mount_none()}</p>
{:else}
  <table class="mt">
    <thead>
      <tr>
        <th>{m.metrics_mount_col_mount()}</th>
        <th>{m.metrics_mount_col_used()}</th>
        <th class="mt-num">{m.common_size()}</th>
        <th>{m.metrics_mount_col_holds()}</th>
      </tr>
    </thead>
    <tbody>
      {#each mounts as mt, i (`${mt.mount_point}|${mt.fs_type}|${i}`)}
        <tr>
          <td class="mt-path font-mono" title={mt.fs_type}>{mt.mount_point}</td>
          <td>
            {#if mt.error}
              <span class="mt-err">{mt.error}</span>
            {:else}
              <div class="mt-used">
                <span class="mt-bar"><span class="mt-fill mt-{level(mt.percent)}" style="width: {Math.min(100, mt.percent)}%"></span></span>
                <span class="font-mono">{mt.percent.toFixed(0)}%</span>
              </div>
            {/if}
          </td>
          <td class="mt-num font-mono">{mt.error ? '—' : `${fmtBytes(mt.used)} / ${fmtBytes(mt.total)}`}</td>
          <td>
            {#each mt.roles ?? [] as r (r)}
              <span class="mt-role">{ROLE_LABEL[r] ?? r}</span>
            {/each}
          </td>
        </tr>
      {/each}
    </tbody>
  </table>
{/if}

<style>
  .mt {
    width: 100%;
    border-collapse: collapse;
    font-size: 12px;
  }
  .mt th {
    text-align: left;
    font-weight: 500;
    color: var(--fg-muted);
    padding: 4px 8px;
    border-bottom: 1px solid var(--border);
  }
  .mt td {
    padding: 4px 8px;
    border-bottom: 1px solid var(--border);
    vertical-align: middle;
  }
  .mt-num {
    text-align: right;
    white-space: nowrap;
  }
  .mt-path {
    max-width: 260px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .mt-used {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .mt-bar {
    width: 80px;
    height: 6px;
    border-radius: 3px;
    background: var(--border);
    overflow: hidden;
  }
  .mt-fill {
    display: block;
    height: 100%;
  }
  .mt-ok { background: var(--accent); }
  .mt-warn { background: var(--color-warning-500); }
  .mt-crit { background: var(--color-danger-500); }
  .mt-role {
    display: inline-block;
    margin-right: 4px;
    padding: 0 5px;
    border: 1px solid var(--border);
    border-radius: 3px;
    font-size: 11px;
    color: var(--fg-muted);
  }
  .mt-err {
    color: var(--color-danger-500);
  }
  .mt-hint {
    font-size: 12px;
    color: var(--fg-muted);
  }
</style>
