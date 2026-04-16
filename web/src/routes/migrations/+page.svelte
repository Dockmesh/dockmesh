<script lang="ts">
  import { api, ApiError, type Migration } from '$lib/api';
  import { Card, Badge, Skeleton, EmptyState } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { ArrowRightLeft, RefreshCw } from 'lucide-svelte';

  let migrations = $state<Migration[]>([]);
  let loading = $state(true);

  async function load() {
    loading = true;
    try {
      migrations = await api.migrations.list(200);
    } catch (err) {
      toast.error('Failed to load migrations', err instanceof ApiError ? err.message : undefined);
    } finally {
      loading = false;
    }
  }

  function statusVariant(s: string): 'success' | 'warning' | 'danger' | 'info' | 'default' {
    if (s === 'completed') return 'success';
    if (s === 'failed' || s === 'rolled_back') return 'danger';
    if (['pending', 'preflight', 'preparing'].includes(s)) return 'info';
    if (['stopping', 'syncing', 'starting', 'health_check'].includes(s)) return 'warning';
    return 'default';
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

  $effect(() => { load(); });
</script>

<section class="space-y-6">
  <div class="flex items-center justify-between flex-wrap gap-3">
    <div>
      <h2 class="text-2xl font-semibold tracking-tight">Migrations</h2>
      <p class="text-sm text-[var(--fg-muted)] mt-0.5">
        Stack migration history across all hosts.
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

  {#if loading && migrations.length === 0}
    <Card><Skeleton class="m-5" width="80%" height="6rem" /></Card>
  {:else if migrations.length === 0}
    <Card>
      <EmptyState
        icon={ArrowRightLeft}
        title="No migrations yet"
        description="Migrate a stack from one host to another using the Migrate button on any stack's detail page."
      />
    </Card>
  {:else}
    <Card>
      <div class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead>
            <tr class="border-b border-[var(--border)] text-[var(--fg-muted)] text-xs uppercase tracking-wider">
              <th class="text-left px-5 py-3">Stack</th>
              <th class="text-left px-3 py-3">Source → Target</th>
              <th class="text-left px-3 py-3">Status</th>
              <th class="text-left px-3 py-3">Phase</th>
              <th class="text-left px-3 py-3">Duration</th>
              <th class="text-left px-3 py-3">Error</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-[var(--border)]">
            {#each migrations as m}
              <tr class="hover:bg-[var(--surface-hover)]">
                <td class="px-5 py-3">
                  <a href="/stacks/{m.stack_name}" class="font-mono text-sm hover:text-[var(--color-brand-400)]">{m.stack_name}</a>
                </td>
                <td class="px-3 py-3 text-xs font-mono text-[var(--fg-muted)]">
                  {m.source_host_id} → {m.target_host_id}
                </td>
                <td class="px-3 py-3">
                  <Badge variant={statusVariant(m.status)} dot>{m.status}</Badge>
                </td>
                <td class="px-3 py-3 text-xs text-[var(--fg-muted)]">{m.phase ?? '—'}</td>
                <td class="px-3 py-3 text-xs tabular-nums">{fmtDuration(m.started_at, m.completed_at)}</td>
                <td class="px-3 py-3 text-xs text-[var(--color-danger-400)] max-w-[200px] truncate" title={m.error_message}>
                  {m.error_message || '—'}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </Card>
  {/if}
</section>
