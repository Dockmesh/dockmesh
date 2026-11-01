<script lang="ts">
  import { untrack } from 'svelte';
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import { api, ApiError, type BackupJob, type BackupRun, type BackupTarget } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import { hosts } from '$lib/stores/host.svelte';
  import { Skeleton, EmptyState, StatBar, StatusChip } from '$lib/components/ui';
  import OpProgress from '$lib/components/operations/OpProgress.svelte';
  import CancelOpButton from '$lib/components/operations/CancelOpButton.svelte';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { pageContext } from '$lib/stores/pageContext.svelte';
  import { operations, isActive } from '$lib/stores/operations.svelte';
  import { autoRefresh } from '$lib/autorefresh';
  import {
    fmtBytes, fmtAgo, fmtUntil, fmtDuration, cronHuman, targetTypeLabel,
    jobSourceName, jobSourceKind, runTone, runLabel,
    hostLabel, mayOnJob
  } from '../../_format';
  import { Play, Pencil, Trash2, Lock, Box } from 'lucide-svelte';

  const id = $derived(parseInt(page.params.id ?? '', 10));

  let job = $state<BackupJob | null>(null);
  let runs = $state<BackupRun[]>([]);
  let targets = $state<BackupTarget[]>([]);
  let loading = $state(true);
  let notFound = $state(false);

  $effect(() => {
    if (job) pageContext.set(job.name);
    return () => pageContext.clear();
  });

  async function load() {
    try {
      const [j, r, t] = await Promise.all([api.backups.getJob(id), api.backups.listRuns(200), api.backups.listTargets()]);
      job = j;
      runs = r;
      targets = t;
      notFound = false;
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) notFound = true;
      else toast.error('Failed to load job', err instanceof ApiError ? err.message : undefined);
    } finally {
      loading = false;
    }
  }
  $effect(() => { void id; untrack(load); });
  $effect(() => autoRefresh(load, 10_000));

  const jobRuns = $derived(job ? runs.filter((r) => r.job_id === job!.id) : []);
  const lastRun = $derived(jobRuns[0] ?? null);
  const okRuns = $derived(jobRuns.filter((r) => r.status === 'success' || r.status === 'success_with_warnings'));
  const avgSize = $derived(okRuns.length ? okRuns.reduce((s, r) => s + r.size_bytes, 0) / okRuns.length : 0);
  const target = $derived(job?.target_id ? targets.find((t) => t.id === job!.target_id) : undefined);
  const hostName = $derived(hostLabel(job?.host_id));
  const hookCount = $derived((job?.pre_hooks?.length ?? 0) + (job?.post_hooks?.length ?? 0));

  let startedRunId = $state<number | null>(null);
  const liveRunId = $derived(startedRunId && (!lastRun || lastRun.id < startedRunId) ? startedRunId : lastRun?.id);
  const liveOp = $derived.by(() => {
    const op = operations.backupRun(liveRunId);
    return op && (isActive(op) || liveRunId === startedRunId) ? op : undefined;
  });

  let busy = $state(false);
  async function runNow() {
    if (!job) return;
    busy = true;
    try {
      const { run } = await operations.startBackup(job.id);
      startedRunId = run.id;
      void load();
    } catch (err) {
      toast.error('Could not start the job', err instanceof ApiError ? err.message : undefined);
    } finally {
      busy = false;
    }
  }
  async function deleteJob() {
    if (!job) return;
    if (!(await confirm.ask({
      title: 'Delete backup job',
      message: `Delete backup job "${job.name}"?`,
      body: 'Existing backup runs are kept. The schedule is removed and no new runs will be triggered.',
      confirmLabel: 'Delete', danger: true
    }))) return;
    try {
      await api.backups.deleteJob(job.id);
      toast.success('Deleted', job.name);
      goto('/backups?tab=jobs');
    } catch (err) {
      toast.error('Delete failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  const RUN_COLS = '120px 150px 90px 100px minmax(0, 1fr)';
</script>

{#if loading && !job}
  <section class="dm-page"><div class="dm-tbl"><div class="dm-tr" style="grid-template-columns: 1fr"><Skeleton width="50%" height="0.9rem" /></div></div></section>
{:else if notFound || !job}
  <section class="dm-page">
    <EmptyState icon={Box} title="Job not found" description="It was deleted, or you do not have access to it.">
      {#snippet action()}<a class="dm-btn" href="/backups?tab=jobs">All jobs</a>{/snippet}
    </EmptyState>
  </section>
{:else}
  <section class="dm-page">
    <div class="dm-ph">
      <div class="title">
        <h1>{job.name}</h1>
        <div class="dm-sub">{jobSourceName(job)} on {hostName} → {target?.name ?? targetTypeLabel(job.target_type)}{job.enabled ? '' : ' · disabled'}</div>
      </div>
      <span class="dm-sp"></span>
      {#if mayOnJob('backups.delete', job)}
        <button type="button" class="dm-btn dm-btn-danger" onclick={deleteJob}><Trash2 size={13} /> Delete</button>
      {/if}
      {#if mayOnJob('backups.update', job)}
        <a href="/backups?tab=jobs&edit={job.id}" class="dm-btn"><Pencil size={13} /> Edit</a>
        <button type="button" class="dm-btn dm-btn-primary" disabled={!job.enabled || busy || !!(liveOp && isActive(liveOp))} onclick={runNow}>
          <Play size={13} /> Run now
        </button>
      {/if}
    </div>

    <StatBar
      items={[
        {
          label: 'Last run',
          value: lastRun ? (lastRun.status === 'failed' ? 'Failed' : lastRun.status === 'running' ? 'Running' : 'Succeeded') : '—',
          note: lastRun ? fmtAgo(lastRun.started_at) : 'no runs yet',
          tone: lastRun?.status === 'failed' ? 'bad' : lastRun?.status === 'success_with_warnings' ? 'warn' : ''
        },
        { label: 'Schedule', value: cronHuman(job.schedule), note: job.enabled && job.next_run_at ? `next ${fmtUntil(job.next_run_at)}` : job.enabled ? '' : 'disabled' },
        { label: 'Average size', value: fmtBytes(avgSize), note: `${jobRuns.length} runs` },
        { label: 'Keeps', value: `${job.retention_count} newest`, note: job.retention_days > 0 ? `at most ${job.retention_days} days` : 'no age limit' }
      ]}
    />

    {#if liveOp}
      <div class="live">
        <a class="dm-sub" href="/backups/runs/{liveRunId}">Run #{liveRunId}</a>
        <div class="live-op"><OpProgress op={liveOp} /></div>
        <button type="button" class="dm-btn dm-btn-sm dm-btn-ghost" onclick={() => operations.open(liveOp.id)}>Steps and log</button>
        <CancelOpButton op={liveOp} />
      </div>
    {:else if lastRun?.status === 'failed'}
      <div class="fail" role="alert">
        <StatusChip tone="bad" label="last run failed" />
        <span class="dm-mono dm-trunc" title={lastRun.error}>{lastRun.error ?? ''}</span>
        <a class="dm-btn dm-btn-sm" href="/backups/runs/{lastRun.id}">Open log</a>
      </div>
    {/if}

    <div class="grid">
      <section>
        <h2 class="dm-h2">Runs</h2>
        {#if jobRuns.length === 0}
          <div class="dm-tbl"><div class="dm-empty">No runs yet.</div></div>
        {:else}
          <div class="dm-tbl" role="table" aria-label="Runs of {job.name}">
            {#each jobRuns.slice(0, 20) as r (r.id)}
              {@const op = operations.backupRun(r.id)}
              <a class="dm-tr dm-click row" role="row" style="grid-template-columns: {RUN_COLS}" href="/backups/runs/{r.id}">
                <span class="dm-num dm-dim" title={new Date(r.started_at).toLocaleString()}>{fmtAgo(r.started_at)}</span>
                <span>
                  {#if op && isActive(op)}<OpProgress {op} compact />{:else}<StatusChip tone={runTone(r.status)} label={runLabel(r.status)} title={r.error} />{/if}
                </span>
                <span class="dm-num">{r.status === 'failed' ? '—' : fmtBytes(r.size_bytes)}</span>
                <span class="dm-num dm-dim">{fmtDuration(r.started_at, r.finished_at)}</span>
                <span class="dm-trunc dm-dim small" title={r.error}>{r.status === 'failed' ? (r.error ?? '') : ''}</span>
              </a>
            {/each}
          </div>
        {/if}
      </section>
      <section>
        <h2 class="dm-h2">Details</h2>
        <dl class="dm-kv">
          <dt>What</dt><dd>{jobSourceName(job)} <span class="dm-dim">({jobSourceKind(job)})</span></dd>
          <dt>Runs on</dt><dd>{#if job.host_id && job.host_id !== 'local'}<a href="/hosts/{job.host_id}">{hostName}</a>{:else}{hostName}{/if}</dd>
          <dt>Target</dt>
          <dd>
            {#if target}<a href="/backups/targets/{target.id}">{target.name}</a> <span class="dm-dim">({targetTypeLabel(target.type)})</span>
            {:else}{targetTypeLabel(job.target_type)}{/if}
          </dd>
          <dt>Encryption</dt><dd>{#if job.encrypt}<Lock size={11} /> age, key in Settings › Encryption key{:else}off{/if}</dd>
          <dt>Retention</dt><dd>keep the {job.retention_count} newest runs{job.retention_days > 0 ? `, none older than ${job.retention_days} days` : ''}</dd>
          <dt>Schedule</dt><dd>{cronHuman(job.schedule)} <span class="dm-mono dm-dim">{job.schedule}</span></dd>
          <dt>Hooks</dt>
          <dd>
            {#if hookCount === 0}<span class="dm-dim">none</span>{:else}
              {#each job.pre_hooks ?? [] as h, i (`pre-${i}`)}<div class="hook"><span class="dm-dim">before</span> <span class="dm-mono">{h.container}: {h.cmd.join(' ')}</span></div>{/each}
              {#each job.post_hooks ?? [] as h, i (`post-${i}`)}<div class="hook"><span class="dm-dim">after</span> <span class="dm-mono">{h.container}: {h.cmd.join(' ')}</span></div>{/each}
            {/if}
          </dd>
        </dl>
      </section>
    </div>
  </section>
{/if}

<style>
  .title { min-width: 0; }
  .grid { display: grid; grid-template-columns: minmax(0, 1.4fr) minmax(280px, 1fr); gap: 28px; align-items: start; }
  .row { color: inherit; text-decoration: none; }
  .small { font-size: 12px; }
  .hook { font-size: 12.5px; overflow-wrap: anywhere; }
  .live { display: flex; align-items: center; gap: 12px; min-height: 44px; border-top: 1px solid var(--border); border-bottom: 1px solid var(--border); padding: 0 6px; }
  .live-op { flex: 1; min-width: 0; max-width: 420px; display: flex; }
  .live-op :global(.opp) { flex: 1; }
  .fail { display: flex; align-items: center; gap: 12px; padding: 8px 12px; border-radius: var(--r); border: 1px solid color-mix(in srgb, var(--bad) 45%, transparent); background: color-mix(in srgb, var(--bad) 8%, transparent); font-size: 12.5px; min-width: 0; }
  .fail .dm-trunc { flex: 1; }
  @media (max-width: 1100px) { .grid { grid-template-columns: minmax(0, 1fr); } }
</style>
