<script lang="ts">
  import { untrack } from 'svelte';
  import { page } from '$app/state';
  import { api, ApiError, runRestorable, type BackupRun, type BackupJob } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import RestoreDialog from '../../_RestoreDialog.svelte';
  import SystemRestoreDialog from '../../_SystemRestoreDialog.svelte';
  import { Skeleton, EmptyState, StatBar, StatusChip } from '$lib/components/ui';
  import OperationSteps from '$lib/components/operations/OperationSteps.svelte';
  import CancelOpButton from '$lib/components/operations/CancelOpButton.svelte';
  import { toast } from '$lib/stores/toast.svelte';
  import { copyWithToast } from '$lib/clipboard';
  import { pageContext } from '$lib/stores/pageContext.svelte';
  import { operations, isActive, opRef, failedStep } from '$lib/stores/operations.svelte';
  import { fmtBytes, fmtDuration, runTone, runLabel, isSystemRun, mayOnJob, cronHuman, targetTypeLabel } from '../../_format';
  import { Copy, Lock, Undo2, Box, Play, Download } from 'lucide-svelte';
  import { m } from '$lib/paraglide/messages.js';
  import { fmtDateTime, fmtTime } from '$lib/i18n';

  const id = $derived(parseInt(page.params.id ?? '', 10));

  $effect(() => {
    pageContext.set(m.backups_run_n({ id }));
    return () => pageContext.clear();
  });

  let run = $state<BackupRun | null>(null);
  let job = $state<BackupJob | null>(null);
  let loading = $state(true);
  let notFound = $state(false);
  let logEntries = $state<Array<{ id: number; ts: string; stream: string; line: string }>>([]);
  let logLoaded = $state(false);

  async function load() {
    try {
      const r = await api.backups.getRun(id);
      run = r;
      notFound = false;
      if (!job || job.id !== r.job_id) job = await api.backups.getJob(r.job_id).catch(() => null);
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) notFound = true;
      else toast.error(m.backups_run_load_failed(), err instanceof ApiError ? err.message : undefined);
    } finally {
      loading = false;
    }
  }

  async function loadLog() {
    try {
      logEntries = await api.backups.runLog(id);
    } catch {
      logEntries = [];
    }
    logLoaded = true;
  }

  // The operation of this run, when it is newer than the 7-day retention:
  // its steps and log come live from the operations store.
  const op = $derived(operations.backupRun(id));
  const opLog = $derived(op ? operations.logOf(op.id) : []);
  let opLoaded = $state('');
  $effect(() => {
    void id;
    loading = true;
    logLoaded = false;
    opLoaded = '';
    untrack(load).then(async () => {
      const found = await operations.find(opRef.backupRun(id), 'backup.run', 0, 1500);
      if (found) {
        await operations.load(found.id);
        opLoaded = found.id;
      } else {
        await loadLog();
      }
    });
  });

  // Reload the run row when its operation ends, so size, checksum and
  // status match what the operation reported.
  let lastStatus = '';
  $effect(() => {
    const s = op?.status ?? '';
    if (lastStatus && lastStatus !== s && op && !isActive(op)) void untrack(load);
    lastStatus = s;
  });
  // Without an operation a running run is followed by polling its log.
  $effect(() => {
    if (op || run?.status !== 'running') return;
    const t = setInterval(() => { void load(); void loadLog(); }, 2000);
    return () => clearInterval(t);
  });

  let logEl = $state<HTMLElement | null>(null);
  $effect(() => {
    void opLog.length;
    void logEntries.length;
    if (logEl && (op ? isActive(op) : run?.status === 'running')) logEl.scrollTop = logEl.scrollHeight;
  });

  let restorePreset = $state<'in-place' | 'alongside' | null>(null);
  const systemRun = $derived(run ? isSystemRun(run) : false);
  const canDownload = $derived(run ? mayOnJob('backups.download', run) : false);
  const canRestore = $derived(run ? runRestorable(run) && mayOnJob('backups.restore', run) : false);

  async function download() {
    if (!run) return;
    try {
      await api.backups.downloadArchive(run.id);
      toast.success(m.backups_download_started(), `backup-run-${run.id}.tar.gz`);
    } catch (err) {
      toast.error(m.backups_download_failed(), err instanceof ApiError ? err.message : undefined);
    }
  }

  let starting = $state(false);
  async function runAgain() {
    if (!job) return;
    starting = true;
    try {
      const next = await (await operations.startBackup(job.id)).op;
      if (next) void operations.open(next.id);
      else toast.success(m.backups_backup_started(), job.name);
    } catch (err) {
      toast.error(m.backups_job_start_failed(), err instanceof ApiError ? err.message : undefined);
    } finally {
      starting = false;
    }
  }

  function copyDetails() {
    if (!run) return;
    const lines = [
      `Run #${run.id}`,
      `Job: ${run.job_name}`,
      `Status: ${run.status}`,
      `Started: ${run.started_at}`,
      run.finished_at ? `Finished: ${run.finished_at}` : '',
      `Duration: ${fmtDuration(run.started_at, run.finished_at)}`,
      `Size: ${fmtBytes(run.size_bytes)}`,
      run.target_path ? `Target path: ${run.target_path}` : '',
      run.sha256 ? `SHA-256: ${run.sha256}` : '',
      run.encrypted ? 'Encrypted: yes (age)' : '',
      run.error ? `Error: ${run.error}` : ''
    ].filter(Boolean).join('\n');
    void copyWithToast(lines, m.common_copied());
  }

  const failedStepOf = $derived(op ? failedStep(op) : undefined);
</script>

{#if loading && !run}
  <section class="dm-page"><div class="dm-tbl"><div class="dm-tr" style="grid-template-columns: 1fr"><Skeleton width="50%" height="0.9rem" /></div></div></section>
{:else if notFound || !run}
  <section class="dm-page">
    <EmptyState icon={Box} title={m.backups_run_not_found()} description={m.backups_run_not_found_desc()}>
      {#snippet action()}<a class="dm-btn" href="/backups?tab=runs">{m.backups_all_runs()}</a>{/snippet}
    </EmptyState>
  </section>
{:else}
  <section class="dm-page">
    <div class="dm-ph">
      <div class="title">
        <h1>{m.backups_run_n({ id: run.id })}</h1>
        <div class="dm-sub">
          {#if job}<a href="/backups/jobs/{job.id}">{run.job_name}</a>{:else}{run.job_name}{/if}
          · {fmtDateTime(run.started_at)}
        </div>
      </div>
      <StatusChip tone={runTone(op && isActive(op) ? 'running' : run.status)} label={op && isActive(op) ? m.backups_run_running() : runLabel(run.status)} />
      {#if run.encrypted}<span class="dm-sub enc"><Lock size={12} /> {m.backups_encrypted_lc()}</span>{/if}
      <span class="dm-sp"></span>
      {#if op}<CancelOpButton {op} size="md" />{/if}
      <button type="button" class="dm-btn" onclick={copyDetails}><Copy size={13} /> {m.backups_copy_details()}</button>
      {#if run.status === 'failed' && job && allowed('backups.run')}
        <button type="button" class="dm-btn dm-btn-primary" disabled={starting} onclick={runAgain}><Play size={13} /> {m.backups_run_again()}</button>
      {/if}
      {#if runRestorable(run) && canDownload}
        <button type="button" class="dm-btn" onclick={download}><Download size={13} /> {m.common_download()}</button>
      {/if}
      {#if canRestore}
        <button type="button" class="dm-btn dm-btn-primary" onclick={() => (restorePreset = 'alongside')}>
          <Undo2 size={13} /> {systemRun ? m.backups_restore_system() : m.backups_restore_ellipsis()}
        </button>
      {/if}
    </div>

    <StatBar
      items={[
        { label: m.common_size(), value: run.status === 'failed' || !run.size_bytes ? '—' : fmtBytes(run.size_bytes) },
        { label: m.backups_col_duration(), value: fmtDuration(run.started_at, run.finished_at) },
        { label: m.common_target(), value: job ? targetTypeLabel(job.target_type) : '—', note: run.target_path?.split('/').pop() ?? '' },
        {
          label: m.backups_checksum(),
          value: run.sha256 ? m.backups_checksum_verified() : '—',
          note: run.sha256 ? m.backups_checksum_read_back() : run.status === 'failed' ? m.backups_nothing_kept() : ''
        }
      ]}
    />

    {#if run.error && !op}
      <div class="fail" role="alert"><b>{run.status === 'failed' ? m.common_failed() : m.common_warning()}</b><p class="dm-mono">{run.error}</p></div>
    {:else if op?.status === 'failed'}
      <div class="fail" role="alert">
        <b>{failedStepOf ? m.backups_failed_at({ step: failedStepOf.label }) : m.common_failed()}</b>
        {#if op.error}<p class="dm-mono">{op.error}</p>{/if}
      </div>
    {/if}

    <div class="grid">
      <div class="col">
        {#if op}
          <section>
            <h2 class="dm-h2">{m.backups_wizard_steps()}</h2>
            <OperationSteps {op} />
          </section>
        {/if}
        <section>
          <h2 class="dm-h2">{m.backups_log()} {#if op && isActive(op)}<span class="dm-sub">· {m.backups_log_live()}</span>{:else if !op && run.status === 'running'}<span class="dm-sub">· {m.backups_log_refreshing()}</span>{/if}</h2>
          <div class="log dm-mono" bind:this={logEl}>
            {#if op}
              {#each opLog as l, i (i)}
                <div class="ln {l.level}"><span class="t">{fmtTime(l.ts)}</span><span class="x">{l.text}</span></div>
              {:else}
                <div class="dm-dim">{opLoaded ? (isActive(op) ? m.backups_waiting_for_output() : m.backups_no_log_lines()) : m.common_loading()}</div>
              {/each}
            {:else if !logLoaded}
              <div class="dm-dim">{m.common_loading()}</div>
            {:else}
              {#each logEntries as e (e.id)}
                <div class="ln {e.stream === 'error' || e.stream === 'hook_stderr' ? 'error' : e.stream === 'warn' ? 'warn' : ''}">
                  <span class="t">{fmtTime(e.ts)}</span><span class="x">{e.line}</span>
                </div>
              {:else}
                <div class="dm-dim">{m.backups_no_log_lines_run()}</div>
              {/each}
            {/if}
          </div>
        </section>
      </div>
      <div class="col">
        <section>
          <h2 class="dm-h2">{m.common_details()}</h2>
          <dl class="dm-kv">
            <dt>{m.backups_started()}</dt><dd class="dm-num">{fmtDateTime(run.started_at)}</dd>
            {#if run.finished_at}<dt>{m.backups_finished()}</dt><dd class="dm-num">{fmtDateTime(run.finished_at)}</dd>{/if}
            {#if run.sources?.length}
              <dt>{m.backups_col_what()}</dt>
              <dd>{#each run.sources as s, i (i)}{i ? ', ' : ''}{s.type === 'system' ? m.backups_dockmesh_server() : s.name} <span class="dm-dim">({s.type})</span>{/each}</dd>
            {/if}
            {#if run.target_path}<dt>{m.backups_col_archive()}</dt><dd class="dm-mono small">{run.target_path}</dd>{/if}
            {#if run.pruned_at}<dt>{m.backups_removed()}</dt><dd>{m.backups_removed_by_retention_at({ at: fmtDateTime(run.pruned_at) })}</dd>{/if}
            {#if run.sha256}<dt>SHA-256</dt><dd class="dm-mono small">{run.sha256}</dd>{/if}
            <dt>{m.backups_encryption()}</dt><dd>{run.encrypted ? m.backups_encryption_age_key() : m.backups_off()}</dd>
            {#if job}
              <dt>{m.backups_col_job()}</dt><dd><a href="/backups/jobs/{job.id}">{job.name}</a> <span class="dm-dim">· {cronHuman(job.schedule)}</span></dd>
            {/if}
          </dl>
        </section>
        {#if run.status === 'failed'}
          <p class="dm-sub">{m.backups_run_not_restorable()}</p>
        {:else if systemRun && runRestorable(run)}
          <p class="dm-sub">{m.backups_system_restore_note()}</p>
        {/if}
      </div>
    </div>
  </section>

  <SystemRestoreDialog open={!!restorePreset && systemRun} {run} onclose={() => (restorePreset = null)} />
  <RestoreDialog
    run={restorePreset && !systemRun ? run : null}
    preset={restorePreset ?? 'alongside'}
    originHost={job?.host_id ?? ''}
    onclose={() => (restorePreset = null)}
  />
{/if}

<style>
  .title { min-width: 0; }
  .title .dm-sub a { color: var(--accent); text-decoration: none; }
  .enc { display: inline-flex; align-items: center; gap: 4px; }
  .grid { display: grid; grid-template-columns: minmax(0, 1.6fr) minmax(280px, 1fr); gap: 28px; align-items: start; }
  .col { display: flex; flex-direction: column; gap: 22px; min-width: 0; }
  .fail { border: 1px solid color-mix(in srgb, var(--bad) 45%, transparent); background: color-mix(in srgb, var(--bad) 8%, transparent); border-radius: var(--r); padding: 10px 12px; }
  .fail b { color: var(--bad); }
  .fail p { margin: 4px 0 0; font-size: 12.5px; overflow-wrap: anywhere; }
  .small { font-size: 12px; overflow-wrap: anywhere; }
  .log { max-height: 460px; min-height: 120px; overflow: auto; background: var(--code, var(--field)); border-radius: var(--r-sm); padding: 8px 10px; font-size: 11.5px; line-height: 1.55; }
  .ln { display: grid; grid-template-columns: 70px minmax(0, 1fr); gap: 8px; }
  .ln .t { color: var(--subtle); }
  .ln .x { white-space: pre-wrap; overflow-wrap: anywhere; }
  .ln.warn .x { color: var(--warn); }
  .ln.error .x { color: var(--bad); }
  @media (max-width: 1100px) { .grid { grid-template-columns: minmax(0, 1fr); } }
</style>
