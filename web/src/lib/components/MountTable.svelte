<script lang="ts">
  // Per-filesystem disk usage of one host, fullest first (server order).
  import type { DiskMount } from '$lib/api';

  interface Props {
    mounts?: DiskMount[];
    unsupported?: boolean;
  }

  let { mounts = [], unsupported = false }: Props = $props();

  const ROLE_LABEL: Record<string, string> = {
    docker_root: 'Docker',
    containerd_root: 'containerd',
    stacks: 'stacks',
    data: 'Dockmesh data',
    backup_target: 'backups',
  };

  function fmtBytes(n: number): string {
    if (!n) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
    const i = Math.min(units.length - 1, Math.floor(Math.log(n) / Math.log(1024)));
    return `${(n / Math.pow(1024, i)).toFixed(i >= 3 ? 1 : 0)} ${units[i]}`;
  }

  function level(pct: number): 'ok' | 'warn' | 'crit' {
    if (pct >= 90) return 'crit';
    if (pct >= 80) return 'warn';
    return 'ok';
  }
</script>

{#if unsupported}
  <p class="mt-hint">Update the agent on this host to see disk usage per mount.</p>
{:else if mounts.length === 0}
  <p class="mt-hint">No mounts reported.</p>
{:else}
  <table class="mt">
    <thead>
      <tr>
        <th>Mount</th>
        <th>Used</th>
        <th class="mt-num">Size</th>
        <th>Holds</th>
      </tr>
    </thead>
    <tbody>
      {#each mounts as m (m.mount_point)}
        <tr>
          <td class="mt-path font-mono" title={m.fs_type}>{m.mount_point}</td>
          <td>
            {#if m.error}
              <span class="mt-err">{m.error}</span>
            {:else}
              <div class="mt-used">
                <span class="mt-bar"><span class="mt-fill mt-{level(m.percent)}" style="width: {Math.min(100, m.percent)}%"></span></span>
                <span class="font-mono">{m.percent.toFixed(0)}%</span>
              </div>
            {/if}
          </td>
          <td class="mt-num font-mono">{m.error ? '—' : `${fmtBytes(m.used)} / ${fmtBytes(m.total)}`}</td>
          <td>
            {#each m.roles ?? [] as r (r)}
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
