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
  import { m } from '$lib/paraglide/messages.js';
  import { fmtDateTime } from '$lib/i18n';

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
      else toast.error(m.backups_job_load_failed(), err instanceof ApiError ? err.message : undefined);
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
      toast.error(m.backups_job_start_failed(), err instanceof ApiError ? err.message : undefined);
    } finally {
      busy = false;
    }
  }
  async function deleteJob() {
    if (!job) return;
    if (!(await confirm.ask({
      title: m.backups_delete_job_title(),
      message: m.backups_delete_job_message({ name: job.name }),
      body: m.backups_delete_job_body(),
      confirmLabel: m.common_delete(), danger: true
    }))) return;
    try {
      await api.backups.deleteJob(job.id);
      toast.success(m.backups_deleted(), job.name);
      goto('/backups?tab=jobs');
    } catch (err) {
      toast.error(m.backups_delete_failed(), err instanceof ApiError ? err.message : undefined);
    }
  }

  const RUN_COLS = '120px 150px 90px 100px minmax(0, 1fr)';
</script>

{#if loading && !job}
  <section class="dm-page"><div class="dm-tbl"><div class="dm-tr" style="grid-template-columns: 1fr"><Skeleton width="50%" height="0.9rem" /></div></div></section>
{:else if notFound || !job}
  <section class="dm-page">
    <EmptyState icon={Box} title={m.backups_job_not_found()} description={m.backups_not_found_desc()}>
      {#snippet action()}<a class="dm-btn" href="/backups?tab=jobs">{m.backups_all_jobs()}</a>{/snippet}
    </EmptyState>
  </section>
{:else}
  <section class="dm-page">
    <div class="dm-ph">
      <div class="title">
        <h1>{job.name}</h1>
        <div class="dm-sub">{m.backups_job_sub({ source: jobSourceName(job), host: hostName, target: target?.name ?? targetTypeLabel(job.target_type) })}{job.enabled ? '' : ` · ${m.backups_job_disabled()}`}</div>
      </div>
      <span class="dm-sp"></span>
      {#if mayOnJob('backups.delete', job)}
        <button type="button" class="dm-btn dm-btn-danger" onclick={deleteJob}><Trash2 size={13} /> {m.common_delete()}</button>
      {/if}
      {#if mayOnJob('backups.update', job)}
        <a href="/backups?tab=jobs&edit={job.id}" class="dm-btn"><Pencil size={13} /> {m.common_edit()}</a>
        <button type="button" class="dm-btn dm-btn-primary" disabled={!job.enabled || busy || !!(liveOp && isActive(liveOp))} onclick={runNow}>
          <Play size={13} /> {m.backups_run_now()}
        </button>
      {/if}
    </div>

    <StatBar
      items={[
        {
          label: m.backups_col_last_run(),
          value: lastRun ? (lastRun.status === 'failed' ? m.common_failed() : lastRun.status === 'running' ? m.common_running() : m.backups_filter_succeeded()) : '—',
          note: lastRun ? fmtAgo(lastRun.started_at) : m.backups_no_runs_yet_note(),
          tone: lastRun?.status === 'failed' ? 'bad' : lastRun?.status === 'success_with_warnings' ? 'warn' : ''
        },
        { label: m.backups_col_schedule(), value: cronHuman(job.schedule), note: job.enabled && job.next_run_at ? m.backups_next_until({ until: fmtUntil(job.next_run_at) }) : job.enabled ? '' : m.backups_job_disabled() },
        { label: m.backups_average_size(), value: fmtBytes(avgSize), note: m.backups_n_runs({ count: jobRuns.length }) },
        { label: m.backups_keeps(), value: m.backups_n_newest({ n: job.retention_count }), note: job.retention_days > 0 ? m.backups_at_most_days({ n: job.retention_days }) : m.backups_no_age_limit() }
      ]}
    />

    {#if liveOp}
      <div class="live">
        <a class="dm-sub" href="/backups/runs/{liveRunId}">{m.backups_run_n({ id: liveRunId ?? '' })}</a>
        <div class="live-op"><OpProgress op={liveOp} /></div>
        <button type="button" class="dm-btn dm-btn-sm dm-btn-ghost" onclick={() => operations.open(liveOp.id)}>{m.backups_steps_and_log()}</button>
        <CancelOpButton op={liveOp} />
      </div>
    {:else if lastRun?.status === 'failed'}
      <div class="fail" role="alert">
        <StatusChip tone="bad" label={m.backups_last_run_failed()} />
        <span class="dm-mono dm-trunc" title={lastRun.error}>{lastRun.error ?? ''}</span>
        <a class="dm-btn dm-btn-sm" href="/backups/runs/{lastRun.id}">{m.backups_open_log()}</a>
      </div>
    {/if}

    <div class="grid">
      <section>
        <h2 class="dm-h2">{m.backups_tab_runs()}</h2>
        {#if jobRuns.length === 0}
          <div class="dm-tbl"><div class="dm-empty">{m.backups_no_runs_yet()}</div></div>
        {:else}
          <div class="dm-tbl" role="table" aria-label={m.backups_runs_of({ name: job.name })}>
            {#each jobRuns.slice(0, 20) as r (r.id)}
              {@const op = operations.backupRun(r.id)}
              <a class="dm-tr dm-click row" role="row" style="grid-template-columns: {RUN_COLS}" href="/backups/runs/{r.id}">
                <span class="dm-num dm-dim" title={fmtDateTime(r.started_at)}>{fmtAgo(r.started_at)}</span>
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
        <h2 class="dm-h2">{m.common_details()}</h2>
        <dl class="dm-kv">
          <dt>{m.backups_col_what()}</dt><dd>{jobSourceName(job)} <span class="dm-dim">({jobSourceKind(job)})</span></dd>
          <dt>{m.backups_runs_on()}</dt><dd>{#if job.host_id && job.host_id !== 'local'}<a href="/hosts/{job.host_id}">{hostName}</a>{:else}{hostName}{/if}</dd>
          <dt>{m.common_target()}</dt>
          <dd>
            {#if target}<a href="/backups/targets/{target.id}">{target.name}</a> <span class="dm-dim">({targetTypeLabel(target.type)})</span>
            {:else}{targetTypeLabel(job.target_type)}{/if}
          </dd>
          <dt>{m.backups_encryption()}</dt><dd>{#if job.encrypt}<Lock size={11} /> {m.backups_encryption_age_key()}{:else}{m.backups_off()}{/if}</dd>
          <dt>{m.backups_retention()}</dt><dd>{m.backups_retention_keep_newest({ n: job.retention_count })}{job.retention_days > 0 ? m.backups_retention_none_older({ n: job.retention_days }) : ''}</dd>
          <dt>{m.backups_col_schedule()}</dt><dd>{cronHuman(job.schedule)} <span class="dm-mono dm-dim">{job.schedule}</span></dd>
          <dt>{m.backups_hooks()}</dt>
          <dd>
            {#if hookCount === 0}<span class="dm-dim">{m.backups_none_lc()}</span>{:else}
              {#each job.pre_hooks ?? [] as h, i (`pre-${i}`)}<div class="hook"><span class="dm-dim">{m.backups_hook_before()}</span> <span class="dm-mono">{h.container}: {h.cmd.join(' ')}</span></div>{/each}
              {#each job.post_hooks ?? [] as h, i (`post-${i}`)}<div class="hook"><span class="dm-dim">{m.backups_hook_after()}</span> <span class="dm-mono">{h.container}: {h.cmd.join(' ')}</span></div>{/each}
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
