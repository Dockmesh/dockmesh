<script lang="ts">
  import { page } from '$app/state';
  import { api, ApiError, runRestorable, type BackupRun, type BackupJob } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import RestoreDialog from '../../_RestoreDialog.svelte';
  import SystemRestoreDialog from '../../_SystemRestoreDialog.svelte';
  import { Skeleton, EmptyState, StatBar, StatusChip } from '$lib/components/ui';
  import OperationSteps from '$lib/components/operations/OperationSteps.svelte';
  import { toast } from '$lib/stores/toast.svelte';
  import { copyWithToast } from '$lib/clipboard';
  import { pageContext } from '$lib/stores/pageContext.svelte';
  import { operations, isActive, opRef, failedStep } from '$lib/stores/operations.svelte';
  import { fmtBytes, fmtDuration, runTone, runLabel, isSystemRun, mayOnJob, cronHuman, targetTypeLabel } from '../../_format';
  import { Copy, Lock, Undo2, Box, Play, Download } from 'lucide-svelte';

  const id = $derived(parseInt(page.params.id ?? '', 10));

  $effect(() => {
    pageContext.set(`Run #${id}`);
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
      else toast.error('Failed to load run', err instanceof ApiError ? err.message : undefined);
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
    load().then(async () => {
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
    if (lastStatus && lastStatus !== s && op && !isActive(op)) void load();
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
      toast.success('Download started', `backup-run-${run.id}.tar.gz`);
    } catch (err) {
      toast.error('Download failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  let starting = $state(false);
  async function runAgain() {
    if (!job) return;
    starting = true;
    try {
      const next = await (await operations.startBackup(job.id)).op;
      if (next) void operations.open(next.id);
      else toast.success('Backup started', job.name);
    } catch (err) {
      toast.error('Could not start the job', err instanceof ApiError ? err.message : undefined);
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
    void copyWithToast(lines, 'Copied');
  }

  const failedStepOf = $derived(op ? failedStep(op) : undefined);
</script>

{#if loading && !run}
  <section class="dm-page"><div class="dm-tbl"><div class="dm-tr" style="grid-template-columns: 1fr"><Skeleton width="50%" height="0.9rem" /></div></div></section>
{:else if notFound || !run}
  <section class="dm-page">
    <EmptyState icon={Box} title="Run not found" description="It was removed, or you do not have access to it.">
      {#snippet action()}<a class="dm-btn" href="/backups?tab=runs">All runs</a>{/snippet}
    </EmptyState>
  </section>
{:else}
  <section class="dm-page">
    <div class="dm-ph">
      <div class="title">
        <h1>Run #{run.id}</h1>
        <div class="dm-sub">
          {#if job}<a href="/backups/jobs/{job.id}">{run.job_name}</a>{:else}{run.job_name}{/if}
          · {new Date(run.started_at).toLocaleString()}
        </div>
      </div>
      <StatusChip tone={runTone(op && isActive(op) ? 'running' : run.status)} label={op && isActive(op) ? 'running' : runLabel(run.status)} />
      {#if run.encrypted}<span class="dm-sub enc"><Lock size={12} /> encrypted</span>{/if}
      <span class="dm-sp"></span>
      <button type="button" class="dm-btn" onclick={copyDetails}><Copy size={13} /> Copy details</button>
      {#if run.status === 'failed' && job && allowed('backups.update')}
        <button type="button" class="dm-btn dm-btn-primary" disabled={starting} onclick={runAgain}><Play size={13} /> Run again</button>
      {/if}
      {#if runRestorable(run) && canDownload}
        <button type="button" class="dm-btn" onclick={download}><Download size={13} /> Download</button>
      {/if}
      {#if canRestore}
        <button type="button" class="dm-btn dm-btn-primary" onclick={() => (restorePreset = 'alongside')}>
          <Undo2 size={13} /> {systemRun ? 'Restore system…' : 'Restore…'}
        </button>
      {/if}
    </div>

    <StatBar
      items={[
        { label: 'Size', value: run.status === 'failed' || !run.size_bytes ? '—' : fmtBytes(run.size_bytes) },
        { label: 'Duration', value: fmtDuration(run.started_at, run.finished_at) },
        { label: 'Target', value: job ? targetTypeLabel(job.target_type) : '—', note: run.target_path?.split('/').pop() ?? '' },
        {
          label: 'Checksum',
          value: run.sha256 ? 'verified' : '—',
          note: run.sha256 ? 'read back after upload' : run.status === 'failed' ? 'nothing kept' : ''
        }
      ]}
    />

    {#if run.error && !op}
      <div class="fail" role="alert"><b>{run.status === 'failed' ? 'Failed' : 'Warning'}</b><p class="dm-mono">{run.error}</p></div>
    {:else if op?.status === 'failed'}
      <div class="fail" role="alert">
        <b>{failedStepOf ? `Failed at “${failedStepOf.label}”` : 'Failed'}</b>
        {#if op.error}<p class="dm-mono">{op.error}</p>{/if}
      </div>
    {/if}

    <div class="grid">
      <div class="col">
        {#if op}
          <section>
            <h2 class="dm-h2">Steps</h2>
            <OperationSteps {op} />
          </section>
        {/if}
        <section>
          <h2 class="dm-h2">Log {#if op && isActive(op)}<span class="dm-sub">· live</span>{:else if !op && run.status === 'running'}<span class="dm-sub">· refreshing every 2 s</span>{/if}</h2>
          <div class="log dm-mono" bind:this={logEl}>
            {#if op}
              {#each opLog as l, i (i)}
                <div class="ln {l.level}"><span class="t">{new Date(l.ts).toLocaleTimeString()}</span><span class="x">{l.text}</span></div>
              {:else}
                <div class="dm-dim">{opLoaded ? (isActive(op) ? 'Waiting for output…' : 'No log lines.') : 'Loading…'}</div>
              {/each}
            {:else if !logLoaded}
              <div class="dm-dim">Loading…</div>
            {:else}
              {#each logEntries as e (e.id)}
                <div class="ln {e.stream === 'error' || e.stream === 'hook_stderr' ? 'error' : e.stream === 'warn' ? 'warn' : ''}">
                  <span class="t">{new Date(e.ts).toLocaleTimeString()}</span><span class="x">{e.line}</span>
                </div>
              {:else}
                <div class="dm-dim">No log lines recorded for this run.</div>
              {/each}
            {/if}
          </div>
        </section>
      </div>
      <div class="col">
        <section>
          <h2 class="dm-h2">Details</h2>
          <dl class="dm-kv">
            <dt>Started</dt><dd class="dm-num">{new Date(run.started_at).toLocaleString()}</dd>
            {#if run.finished_at}<dt>Finished</dt><dd class="dm-num">{new Date(run.finished_at).toLocaleString()}</dd>{/if}
            {#if run.sources?.length}
              <dt>What</dt>
              <dd>{#each run.sources as s, i (i)}{i ? ', ' : ''}{s.type === 'system' ? 'Dockmesh server' : s.name} <span class="dm-dim">({s.type})</span>{/each}</dd>
            {/if}
            {#if run.target_path}<dt>Archive</dt><dd class="dm-mono small">{run.target_path}</dd>{/if}
            {#if run.pruned_at}<dt>Removed</dt><dd>by retention, {new Date(run.pruned_at).toLocaleString()}</dd>{/if}
            {#if run.sha256}<dt>SHA-256</dt><dd class="dm-mono small">{run.sha256}</dd>{/if}
            <dt>Encryption</dt><dd>{run.encrypted ? 'age, key in Settings › Encryption key' : 'off'}</dd>
            {#if job}
              <dt>Job</dt><dd><a href="/backups/jobs/{job.id}">{job.name}</a> <span class="dm-dim">· {cronHuman(job.schedule)}</span></dd>
            {/if}
          </dl>
        </section>
        {#if run.status === 'failed'}
          <p class="dm-sub">No archive was kept, so this run cannot be restored.</p>
        {:else if systemRun && runRestorable(run)}
          <p class="dm-sub">Restoring a system backup replaces this server's database, users and stack files, then restarts Dockmesh.</p>
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
