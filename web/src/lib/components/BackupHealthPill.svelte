<script lang="ts">
  // Topbar pill that surfaces backup health globally — operators don't
  // open /backups daily, but they do want to know when nightly jobs
  // start failing. Pattern from `Dockmesh Wizard (6)/backups.jsx::
  // BackupHealthPill`. Three render states:
  //   - hidden — no backup jobs configured (don't nag)
  //   - ok — green, "X/Y jobs ok" (only shown if there are recent runs)
  //   - warn / fail — red/amber, click → /backups?tab=runs
  //
  // Polling is intentionally slow (30s) — backup state moves on the
  // order of minutes, not seconds, and we don't want to add hot-traffic
  // for a feature most operators never look at.
  import { goto } from '$app/navigation';
  import { api, ApiError, type BackupJob, type BackupRun } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import { Archive, AlertTriangle } from 'lucide-svelte';

  let jobs = $state<BackupJob[]>([]);
  let recentRuns = $state<BackupRun[]>([]);
  let booted = $state(false);

  async function load() {
    try {
      const [j, r] = await Promise.all([
        api.backups.listJobs().catch(() => [] as BackupJob[]),
        api.backups.listRuns(50).catch(() => [] as BackupRun[])
      ]);
      jobs = j;
      recentRuns = r;
    } catch {
      /* ignore — pill silently hides on error */
    } finally {
      booted = true;
    }
  }

  // 30s poll — backup state is slow-moving. The mockup envisions this
  // pill being driven by a server-pushed event, but we don't have an
  // events stream for backup state yet — polling is the pragmatic fit.
  let timer: ReturnType<typeof setInterval> | null = null;
  $effect(() => {
    if (!allowed('backups.view')) return;
    load();
    timer = setInterval(load, 30_000);
    return () => { if (timer) clearInterval(timer); };
  });

  // Derive "in last 24h" failed-run count. We use last 24h instead of
  // "since last successful run" because the latter mis-categorises
  // jobs that have never succeeded (e.g. brand-new mis-configured ones
  // that never ran).
  const recent24h = $derived.by(() => {
    const cutoff = Date.now() - 24 * 60 * 60 * 1000;
    return recentRuns.filter((r) => Date.parse(r.started_at) >= cutoff);
  });
  const failed24h = $derived(recent24h.filter((r) => r.status === 'failed').length);
  const enabledJobs = $derived(jobs.filter((j) => j.enabled).length);
  const totalJobs = $derived(jobs.length);

  function go() { goto('/backups?tab=runs'); }
</script>

{#if booted && totalJobs > 0 && allowed('backups.view')}
  {#if failed24h > 0}
    <button type="button" class="bk-pill bk-pill-fail" onclick={go} title="Click to open recent runs">
      <AlertTriangle size={11} strokeWidth={1.8} />
      <span class="font-mono">{failed24h} backup{failed24h === 1 ? '' : 's'} failing</span>
    </button>
  {:else if recent24h.length > 0}
    <button type="button" class="bk-pill bk-pill-ok" onclick={go} title="Backups running healthy — click to open">
      <span class="bk-pill-dot"></span>
      <span class="font-mono">{enabledJobs}/{totalJobs} backups ok</span>
    </button>
  {/if}
{/if}

<style>
  .bk-pill {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 4px 10px;
    border: 1px solid var(--border);
    border-radius: 999px;
    background: var(--bg);
    color: var(--fg-subtle);
    font-size: 10.5px;
    cursor: pointer;
    transition: border-color 120ms, background 120ms, color 120ms;
  }
  .bk-pill:hover { border-color: var(--border-strong); }
  .bk-pill-ok {
    color: var(--color-success-400);
    border-color: color-mix(in srgb, var(--color-success-500) 30%, var(--border));
    background: color-mix(in srgb, var(--color-success-500) 6%, transparent);
  }
  .bk-pill-ok:hover {
    background: color-mix(in srgb, var(--color-success-500) 12%, transparent);
  }
  .bk-pill-fail {
    color: var(--color-danger-400);
    border-color: color-mix(in srgb, var(--color-danger-500) 40%, var(--border));
    background: color-mix(in srgb, var(--color-danger-500) 8%, transparent);
  }
  .bk-pill-fail:hover {
    background: color-mix(in srgb, var(--color-danger-500) 14%, transparent);
  }
  .bk-pill-dot {
    width: 5px;
    height: 5px;
    border-radius: 999px;
    background: var(--color-success-400);
    box-shadow: 0 0 0 3px color-mix(in srgb, var(--color-success-500) 22%, transparent);
  }
</style>
