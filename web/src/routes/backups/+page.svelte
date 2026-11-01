<script lang="ts">
  import { untrack } from 'svelte';
  import { goto } from '$app/navigation';
  import { urlTab } from '$lib/urlTab.svelte';
  import { page } from '$app/state';
  import { api, ApiError, runRestorable } from '$lib/api';
  import type { BackupJob, BackupJobInput, BackupRun, BackupTarget } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { operations, isActive } from '$lib/stores/operations.svelte';
  import { autoRefresh } from '$lib/autorefresh';
  import { Tabs, Segmented, StatusChip, StatBar, Switch, Skeleton, EmptyState } from '$lib/components/ui';
  import Menu, { type MenuItem } from '$lib/components/ui/Menu.svelte';
  import OpProgress from '$lib/components/operations/OpProgress.svelte';
  import CancelOpButton from '$lib/components/operations/CancelOpButton.svelte';
  import JobWizard from './_JobWizard.svelte';
  import TargetWizard from './_TargetWizard.svelte';
  import RestoreDialog from './_RestoreDialog.svelte';
  import SystemRestoreDialog from './_SystemRestoreDialog.svelte';
  import {
    fmtBytes, fmtAgo, fmtUntil, fmtDuration, cronHuman, targetTypeLabel,
    jobSourceName, jobSourceKind, runTone, runLabel, isSystemRun, mayOnJob,
    hostLabel
  } from './_format';
  import { Plus, Lock, Play, AlertTriangle } from 'lucide-svelte';

  type Tab = 'jobs' | 'runs' | 'targets';
  const TABS: Tab[] = ['jobs', 'runs', 'targets'];
  const tabs = urlTab(TABS, 'jobs');
  const tab = $derived(tabs.current);
  const setTab = (t: string) => tabs.set(t);

  let jobs = $state<BackupJob[]>([]);
  let runs = $state<BackupRun[]>([]);
  let bTargets = $state<BackupTarget[]>([]);
  let loaded = $state(false);

  async function loadJobs() {
    try { jobs = await api.backups.listJobs(); } catch (err) { toast.error('Could not load jobs', err instanceof ApiError ? err.message : undefined); }
  }
  async function loadRuns() {
    try { runs = await api.backups.listRuns(500); } catch (err) { toast.error('Could not load runs', err instanceof ApiError ? err.message : undefined); }
  }
  async function loadTargets() {
    try { bTargets = await api.backups.listTargets(); } catch (err) { toast.error('Could not load targets', err instanceof ApiError ? err.message : undefined); }
  }
  async function loadAll() {
    await Promise.all([loadJobs(), loadRuns(), loadTargets()]);
    loaded = true;
  }

  $effect(() => {
    if (!allowed('backups.view')) { goto('/'); return; }
    untrack(loadAll);
  });
  $effect(() => autoRefresh(loadAll, 5_000));

  // ?edit=<id> comes from the job and target detail pages.
  let editHandled = '';
  $effect(() => {
    const id = Number(page.url.searchParams.get('edit'));
    const key = `${tab}:${id}`;
    if (!id || editHandled === key || !loaded) return;
    if (tab === 'jobs') {
      const j = jobs.find((x) => x.id === id);
      if (j) { editHandled = key; openEdit(j); }
    } else if (tab === 'targets') {
      const t = bTargets.find((x) => x.id === id);
      if (t) { editHandled = key; openEditTarget(t); }
    }
  });

  const sortedJobs = $derived(jobs.slice().sort((a, b) => a.name.localeCompare(b.name)));
  const sortedTargets = $derived(bTargets.slice().sort((a, b) => a.name.localeCompare(b.name)));
  const recentRuns = $derived(runs.filter((r) => Date.now() - new Date(r.started_at).getTime() < 86_400_000));
  const recentFailed = $derived(recentRuns.filter((r) => r.status === 'failed').length);
  const nextRun = $derived(
    jobs.filter((j) => j.enabled && j.next_run_at).sort((a, b) => (a.next_run_at ?? '').localeCompare(b.next_run_at ?? ''))[0]
  );
  const reviewJobs = $derived(jobs.filter((j) => j.needs_review));

  function targetById(id?: number): BackupTarget | undefined {
    return id ? bTargets.find((t) => t.id === id) : undefined;
  }
  function lastRunOf(j: BackupJob): BackupRun | undefined {
    return runs.find((r) => r.job_id === j.id);
  }
  function jobTone(j: BackupJob): '' | 'ok' | 'warn' | 'bad' {
    if (!j.enabled) return '';
    const r = lastRunOf(j);
    if (!r) return '';
    if (r.status === 'failed') return 'bad';
    if (r.status === 'success_with_warnings') return 'warn';
    return 'ok';
  }

  // The run just started from this page, shown before the list catches up.
  let startedRun = $state<Record<number, number>>({});
  function liveOpFor(j: BackupJob) {
    const r = lastRunOf(j);
    const started = startedRun[j.id];
    const runId = started && (!r || r.id < started) ? started : r?.id;
    const op = operations.backupRun(runId);
    return op && (isActive(op) || started === runId) ? op : undefined;
  }

  async function runJob(j: BackupJob) {
    try {
      const { run } = await operations.startBackup(j.id);
      startedRun[j.id] = run.id;
      void loadRuns();
    } catch (err) {
      toast.error(`Could not start ${j.name}`, err instanceof ApiError ? err.message : undefined);
    }
  }
  async function runAllEnabled() {
    if (runs.some((r) => r.status === 'running')) {
      toast.info('A backup is already running', 'Start the others once it has finished.');
      return;
    }
    const list = jobs.filter((j) => j.enabled);
    if (!(await confirm.ask({ title: 'Run all enabled jobs', message: `Start ${list.length} backup job${list.length === 1 ? '' : 's'} now?`, confirmLabel: 'Run all' }))) return;
    for (const j of list) await runJob(j);
  }

  async function toggleJob(j: BackupJob) {
    try {
      const input: BackupJobInput = { name: j.name, host_id: j.host_id, target_id: j.target_id, target_type: j.target_type, target_config: j.target_config, sources: j.sources, schedule: j.schedule, retention_count: j.retention_count, retention_days: j.retention_days, encrypt: j.encrypt, pre_hooks: j.pre_hooks, post_hooks: j.post_hooks, enabled: !j.enabled };
      await api.backups.updateJob(j.id, input);
      await loadJobs();
    } catch (err) { toast.error('Could not change the job', err instanceof ApiError ? err.message : undefined); }
  }
  async function deleteJob(j: BackupJob) {
    if (!(await confirm.ask({ title: 'Delete backup job', message: `Delete backup job "${j.name}"?`, body: 'Existing backup runs are kept. The schedule is removed and no new runs will be triggered.', confirmLabel: 'Delete', danger: true }))) return;
    try { await api.backups.deleteJob(j.id); toast.success('Deleted', j.name); await loadJobs(); } catch (err) { toast.error('Delete failed', err instanceof ApiError ? err.message : undefined); }
  }
  async function ackReview(j: BackupJob, mode: 'keep' | 'disable') {
    try {
      await api.backups.acknowledgeReview(j.id, mode);
      toast.success(mode === 'keep' ? 'Marked as reviewed' : 'Disabled', j.name);
      await loadJobs();
    } catch (err) { toast.error('Failed', err instanceof ApiError ? err.message : (err as Error).message); }
  }

  function targetPendingHostKey(t: BackupTarget): boolean {
    return t.type === 'sftp' && !!(t.config as { host_key_pending?: string } | null)?.host_key_pending;
  }
  function pendingHostKey(j: BackupJob): boolean {
    const t = targetById(j.target_id);
    if (t) return targetPendingHostKey(t);
    return j.target_type === 'sftp' && !!(j.target_config as { host_key_pending?: string } | null)?.host_key_pending;
  }
  const HOST_KEY_BODY = 'Only do this if you know why the server key changed (server reinstalled, key rotated). Otherwise someone may be intercepting the connection.';
  async function acceptHostKey(j: BackupJob) {
    if (!(await confirm.ask({ title: 'Accept new SFTP host key', message: `Trust the new host key for "${j.name}"?`, body: HOST_KEY_BODY, confirmLabel: 'Accept key', danger: true }))) return;
    try { await api.backups.acceptHostKey(j.id); toast.success('Host key accepted'); await Promise.all([loadJobs(), loadTargets()]); } catch (err) { toast.error('Failed', err instanceof ApiError ? err.message : undefined); }
  }
  async function acceptTargetHostKey(t: BackupTarget) {
    if (!(await confirm.ask({ title: 'Accept new SFTP host key', message: `Trust the new host key for target "${t.name}"?`, body: HOST_KEY_BODY, confirmLabel: 'Accept key', danger: true }))) return;
    try { await api.backups.acceptTargetHostKey(t.id); toast.success('Host key accepted'); await loadTargets(); } catch (err) { toast.error('Failed', err instanceof ApiError ? err.message : undefined); }
  }

  async function deleteTarget(t: BackupTarget) {
    if (!(await confirm.ask({ title: 'Delete backup target', message: `Delete target "${t.name}"?`, body: 'Backup jobs that write to this target will fail on their next run until you reassign them.', confirmLabel: 'Delete', danger: true }))) return;
    try { await api.backups.deleteTarget(t.id); toast.success('Deleted', t.name); await loadTargets(); } catch (err) { toast.error('Delete failed', err instanceof ApiError ? err.message : undefined); }
  }
  let testing = $state<Record<number, boolean>>({});
  async function testTarget(t: BackupTarget) {
    testing[t.id] = true;
    try {
      const res = await api.backups.testTarget(t.id);
      if (res.status === 'connected') toast.success(`${t.name} connected`, res.total_bytes > 0 ? `${fmtBytes(res.free_bytes)} free of ${fmtBytes(res.total_bytes)}` : undefined);
      else toast.error(`${t.name}: connection failed`, res.error);
      await loadTargets();
    } catch (err) {
      toast.error('Test failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      testing[t.id] = false;
    }
  }

  async function downloadRunArchive(r: BackupRun) {
    try {
      await api.backups.downloadArchive(r.id);
      toast.success('Download started', `backup-run-${r.id}.tar.gz`);
    } catch (err) {
      toast.error('Download failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  let wizardJobOpen = $state(false);
  let wizardJobEdit = $state<BackupJob | null>(null);
  let wizardTargetOpen = $state(false);
  let wizardTargetEdit = $state<BackupTarget | null>(null);
  function openNew() { wizardJobEdit = null; wizardJobOpen = true; }
  function openEdit(j: BackupJob) { wizardJobEdit = j; wizardJobOpen = true; }
  function openNewTarget() { wizardTargetEdit = null; wizardTargetOpen = true; }
  function openEditTarget(t: BackupTarget) { wizardTargetEdit = t; wizardTargetOpen = true; }

  async function wizardSaveJob(payload: BackupJobInput, isEdit: boolean, originalId?: number) {
    try {
      if (isEdit && originalId) {
        await api.backups.updateJob(originalId, payload);
        toast.success('Updated', payload.name);
      } else {
        await api.backups.createJob(payload);
        toast.success('Created', payload.name);
      }
      wizardJobOpen = false;
      wizardJobEdit = null;
      await Promise.all([loadJobs(), loadTargets()]);
    } catch (err) {
      toast.error('Save failed', err instanceof ApiError ? err.message : (err as Error).message);
    }
  }
  async function wizardSaveTarget(payload: { name: string; type: string; config: Record<string, any> }, isEdit: boolean, originalId?: number) {
    try {
      if (isEdit && originalId) {
        await api.backups.updateTarget(originalId, payload);
        toast.success('Updated', payload.name);
      } else {
        await api.backups.createTarget(payload);
        toast.success('Created', payload.name);
      }
      wizardTargetOpen = false;
      wizardTargetEdit = null;
      await loadTargets();
    } catch (err) {
      toast.error('Save failed', err instanceof ApiError ? err.message : (err as Error).message);
    }
  }

  let restoreRun = $state<BackupRun | null>(null);
  let restorePreset = $state<'in-place' | 'alongside'>('alongside');
  let systemRestoreOpen = $state(false);
  let systemRestoreRun = $state<BackupRun | null>(null);
  function openRestore(r: BackupRun, preset: 'in-place' | 'alongside' = 'alongside') {
    if (isSystemRun(r)) {
      systemRestoreRun = r;
      systemRestoreOpen = true;
      return;
    }
    restorePreset = preset;
    restoreRun = r;
  }

  let runJobFilter = $state('');
  let runStatusFilter = $state<'all' | 'success' | 'failed' | 'running'>('all');
  const filteredRuns = $derived(
    runs.filter((r) => {
      if (runJobFilter && r.job_name !== runJobFilter) return false;
      if (runStatusFilter === 'success') return r.status === 'success' || r.status === 'success_with_warnings';
      if (runStatusFilter !== 'all' && r.status !== runStatusFilter) return false;
      return true;
    })
  );
  const runCounts = $derived({
    all: runs.length,
    success: runs.filter((r) => r.status === 'success' || r.status === 'success_with_warnings').length,
    failed: runs.filter((r) => r.status === 'failed').length,
    running: runs.filter((r) => r.status === 'running').length
  });

  const canCreate = $derived(allowed('backups.create'));
  const canUpdate = $derived(allowed('backups.update'));
  const canRun = $derived(allowed('backups.run'));

  function jobMenu(j: BackupJob): MenuItem[] {
    const last = lastRunOf(j);
    return [
      { label: 'Open', kbd: '↵', href: `/backups/jobs/${j.id}` },
      ...(mayOnJob('backups.run', j) ? [{ label: 'Run now', disabled: !j.enabled, onclick: () => runJob(j) }] : []),
      ...(mayOnJob('backups.update', j) ? [{ label: 'Edit…', onclick: () => openEdit(j) }] : []),
      ...(last && runRestorable(last) && mayOnJob('backups.restore', last) ? [{ label: 'Restore last run…', onclick: () => openRestore(last) }] : []),
      ...(pendingHostKey(j) && mayOnJob('backups.update', j) ? [{ label: 'Accept new host key…', danger: true, onclick: () => acceptHostKey(j) }] : []),
      ...(mayOnJob('backups.delete', j) ? [{ separator: true }, { label: 'Delete…', danger: true, onclick: () => deleteJob(j) }] : [])
    ];
  }
  function runMenu(r: BackupRun): MenuItem[] {
    const restorable = runRestorable(r);
    return [
      { label: 'Open', kbd: '↵', href: `/backups/runs/${r.id}` },
      ...(operations.backupRun(r.id) ? [{ label: 'Steps and log', onclick: () => operations.open(operations.backupRun(r.id)!.id) }] : []),
      ...(restorable && mayOnJob('backups.restore', r) ? [{ label: isSystemRun(r) ? 'Restore system…' : 'Restore…', onclick: () => openRestore(r) }] : []),
      ...(restorable && mayOnJob('backups.download', r) ? [{ label: 'Download archive', onclick: () => downloadRunArchive(r) }] : [])
    ];
  }
  function targetMenu(t: BackupTarget): MenuItem[] {
    return [
      { label: 'Open', kbd: '↵', href: `/backups/targets/${t.id}` },
      ...(canUpdate
        ? [
            { label: 'Test connection', onclick: () => testTarget(t) },
            { label: 'Edit…', onclick: () => openEditTarget(t) },
            ...(targetPendingHostKey(t) ? [{ label: 'Accept new host key…', danger: true, onclick: () => acceptTargetHostKey(t) }] : [])
          ]
        : []),
      ...(allowed('backups.delete') ? [{ separator: true }, { label: 'Delete…', danger: true, onclick: () => deleteTarget(t) }] : [])
    ];
  }
  function rowKey(e: KeyboardEvent, href: string) {
    if (e.key === 'Enter' && e.target === e.currentTarget) goto(href);
  }

  const JOB_COLS = '14px minmax(160px, 1.2fr) minmax(160px, 1.3fr) 110px minmax(110px, 0.9fr) minmax(130px, 1fr) minmax(170px, 1.2fr) 50px 40px';
  const RUN_COLS = '110px minmax(150px, 1fr) minmax(220px, 1.4fr) 90px 100px minmax(120px, 1fr) 40px';
  const TGT_COLS = '14px minmax(160px, 1fr) 80px minmax(200px, 2fr) 60px minmax(150px, 1fr) 40px';

  function targetLocation(t: BackupTarget): string {
    const c = (t.config ?? {}) as Record<string, any>;
    if (t.type === 's3') return `${c.endpoint ?? ''}/${c.bucket ?? ''}${c.prefix ? '/' + c.prefix : ''}`;
    if (t.type === 'smb') return `//${c.host ?? ''}/${c.share ?? ''}${c.path ? '/' + c.path : ''}`;
    if (t.type === 'sftp') return `${c.username ? c.username + '@' : ''}${c.host ?? ''}:${c.path ?? ''}`;
    if (t.type === 'webdav') return c.url ?? '';
    return c.path ?? '';
  }
  function targetJobCount(t: BackupTarget): number {
    return t.job_count ?? jobs.filter((j) => j.target_id === t.id).length;
  }
</script>

<section class="dm-page">
  <div class="dm-ph">
    <h1>Backups</h1>
    <span class="dm-sub">
      {jobs.filter((j) => j.enabled).length} jobs active · last 24 h: {recentRuns.length} runs{#if recentFailed}, <span class="bad">{recentFailed} failed</span>{/if}
    </span>
    <span class="dm-sp"></span>
    {#if tab === 'jobs'}
      {#if canRun && jobs.some((j) => j.enabled)}
        <button type="button" class="dm-btn" onclick={runAllEnabled}><Play size={13} /> Run all enabled</button>
      {/if}
      {#if canCreate}<button type="button" class="dm-btn dm-btn-primary" onclick={openNew}><Plus size={14} strokeWidth={2} /> New backup job</button>{/if}
    {:else if tab === 'runs'}
      {#if allowed('system.upgrade')}
        <button type="button" class="dm-btn" onclick={() => { systemRestoreRun = null; systemRestoreOpen = true; }}>Restore system from file…</button>
      {/if}
    {:else if canCreate}
      <button type="button" class="dm-btn dm-btn-primary" onclick={openNewTarget}><Plus size={14} strokeWidth={2} /> New target</button>
    {/if}
  </div>

  <Tabs
    tabs={[
      { key: 'jobs', label: 'Jobs', count: jobs.length },
      { key: 'runs', label: 'Runs', count: runs.length, dot: recentFailed > 0 },
      { key: 'targets', label: 'Targets', count: bTargets.length, dot: bTargets.some((t) => t.status === 'error' || targetPendingHostKey(t)) }
    ]}
    active={tab}
    onchange={setTab}
    label="Backup sections"
  />

  {#if tab === 'jobs'}
    {#if canUpdate && reviewJobs.length > 0}
      <div class="attn" role="status">
        <div class="attn-h">
          <AlertTriangle size={14} strokeWidth={1.8} />
          <b>{reviewJobs.length} auto-created job{reviewJobs.length === 1 ? '' : 's'} need{reviewJobs.length === 1 ? 's' : ''} your review</b>
          <span class="dm-dim">· older versions created a daily local backup on their own. Keep leaves it running, Disable stops the schedule and keeps the history.</span>
        </div>
        {#each reviewJobs as j (j.id)}
          <div class="attn-row">
            <span class="dm-mono">{j.name}</span>
            <span class="dm-dim dm-trunc" title={j.review_reason}>{j.review_reason}</span>
            <span class="dm-sp"></span>
            <button type="button" class="dm-btn dm-btn-sm" onclick={() => ackReview(j, 'keep')}>Keep</button>
            <button type="button" class="dm-btn dm-btn-sm dm-btn-ghost" onclick={() => ackReview(j, 'disable')}>Disable</button>
          </div>
        {/each}
      </div>
    {/if}

    {#if loaded && jobs.length > 0}
      <StatBar
        items={[
          { label: 'Jobs', value: `${jobs.filter((j) => j.enabled).length} of ${jobs.length}`, note: 'enabled' },
          { label: 'Runs · 24 h', value: recentRuns.length, note: recentFailed ? `${recentFailed} failed` : 'none failed', tone: recentFailed ? 'bad' : '' },
          { label: 'Next run', value: nextRun ? fmtUntil(nextRun.next_run_at) : '—', note: nextRun?.name ?? '' },
          { label: 'Targets', value: bTargets.length, note: bTargets.filter((t) => t.status === 'error').length ? `${bTargets.filter((t) => t.status === 'error').length} with errors` : '', tone: bTargets.some((t) => t.status === 'error') ? 'bad' : '' }
        ]}
      />
    {/if}

    {#if !loaded}
      <div class="dm-tbl">{#each Array(4) as _, i (i)}<div class="dm-tr" style="grid-template-columns: 1fr"><Skeleton width="60%" height="0.9rem" /></div>{/each}</div>
    {:else if jobs.length === 0}
      <EmptyState
        title="Nothing is backed up yet"
        description="Create a job for the Dockmesh server, a stack or a volume. Start with a daily backup of the server to a target on another machine (SFTP, SMB, S3)."
      >
        {#snippet action()}
          {#if canCreate}<button type="button" class="dm-btn dm-btn-primary" onclick={openNew}><Plus size={14} strokeWidth={2} /> New backup job</button>{/if}
        {/snippet}
      </EmptyState>
    {:else}
      <div class="dm-tbl" role="table" aria-label="Backup jobs">
        <div class="dm-tr dm-th" role="row" style="grid-template-columns: {JOB_COLS}">
          <span></span><span>Job</span><span>What</span><span>Host</span><span>Target</span><span>Schedule</span><span>Last run</span><span>On</span><span></span>
        </div>
        {#each sortedJobs as j (j.id)}
          {@const last = lastRunOf(j)}
          {@const op = liveOpFor(j)}
          {@const tgt = targetById(j.target_id)}
          {@const href = `/backups/jobs/${j.id}`}
          <div class="dm-tr dm-click" role="row" tabindex="0" style="grid-template-columns: {JOB_COLS}" onclick={() => goto(href)} onkeydown={(e) => rowKey(e, href)}>
            <span><span class="dm-dot {jobTone(j)}"></span></span>
            <span class="dm-nm">
              <a {href} onclick={(e) => e.stopPropagation()}>{j.name}</a>{#if j.encrypt}<span class="lock" title="Encrypted"><Lock size={11} /></span>{/if}
              {#if pendingHostKey(j)}<small class="bad">SFTP host key changed</small>{:else if !j.enabled}<small>disabled</small>{/if}
            </span>
            <span class="dm-nm"><span class="dm-t what">{jobSourceName(j)}</span><small>{jobSourceKind(j)}</small></span>
            <span class="dm-dim dm-trunc" title={hostLabel(j.host_id)}>{hostLabel(j.host_id)}</span>
            <span class="dm-trunc muted" title={tgt ? `${tgt.name} (${targetTypeLabel(tgt.type)})` : targetTypeLabel(j.target_type)}>{tgt?.name ?? targetTypeLabel(j.target_type)}</span>
            <span class="dm-trunc muted" title={j.schedule}>{cronHuman(j.schedule)}</span>
            <span class="dm-nm">
              {#if op}
                <span class="op-cell"><OpProgress {op} /><CancelOpButton {op} /></span>
              {:else if last}
                <span class="dm-t lr" class:bad={last.status === 'failed'} title={last.error}>
                  {last.status === 'failed' ? 'Failed' : last.status === 'running' ? 'Running' : fmtBytes(last.size_bytes)}{last.status === 'success_with_warnings' ? ' · warnings' : ''}
                </span>
                <small>{fmtAgo(last.started_at)}</small>
              {:else}
                <span class="dm-dim">never</span>
              {/if}
            </span>
            <span onclick={(e) => e.stopPropagation()} role="presentation"><Switch checked={j.enabled} label="Enabled" hideLabel disabled={!mayOnJob('backups.update', j)} onchange={() => toggleJob(j)} /></span>
            <span class="dm-tr-menu" onclick={(e) => e.stopPropagation()} role="presentation"><Menu label="Actions for {j.name}" items={jobMenu(j)} /></span>
          </div>
        {/each}
      </div>
    {/if}

  {:else if tab === 'runs'}
    <div class="dm-toolbar">
      <Segmented
        label="Filter runs"
        bind:value={runStatusFilter}
        options={[
          { key: 'all', label: 'All', count: runCounts.all },
          { key: 'success', label: 'Succeeded', count: runCounts.success },
          { key: 'failed', label: 'Failed', count: runCounts.failed },
          { key: 'running', label: 'Running', count: runCounts.running }
        ]}
      />
      <select class="dm-filter" bind:value={runJobFilter} aria-label="Filter by job">
        <option value="">All jobs</option>
        {#each sortedJobs as j (j.id)}<option value={j.name}>{j.name}</option>{/each}
      </select>
    </div>

    {#if !loaded}
      <div class="dm-tbl">{#each Array(4) as _, i (i)}<div class="dm-tr" style="grid-template-columns: 1fr"><Skeleton width="60%" height="0.9rem" /></div>{/each}</div>
    {:else if runs.length === 0}
      <EmptyState title="No runs yet" description="Run a job or wait for its schedule. Every run is recorded, the failed ones too." />
    {:else}
      <div class="dm-tbl" role="table" aria-label="Backup runs">
        <div class="dm-tr dm-th" role="row" style="grid-template-columns: {RUN_COLS}">
          <span>When</span><span>Job</span><span>Result</span><span>Size</span><span>Duration</span><span>Archive</span><span></span>
        </div>
        {#each filteredRuns as r (r.id)}
          {@const op = operations.backupRun(r.id)}
          {@const href = `/backups/runs/${r.id}`}
          <div class="dm-tr dm-click" role="row" tabindex="0" style="grid-template-columns: {RUN_COLS}" onclick={() => goto(href)} onkeydown={(e) => rowKey(e, href)}>
            <span class="dm-num dm-dim" title={new Date(r.started_at).toLocaleString()}>{fmtAgo(r.started_at)}</span>
            <span class="dm-nm"><a {href} onclick={(e) => e.stopPropagation()}>{r.job_name}</a>{#if r.encrypted}<span class="lock" title="Encrypted"><Lock size={11} /></span>{/if}</span>
            <span>
              {#if op && (isActive(op) || r.status === 'running')}
                <span class="op-cell"><OpProgress {op} /><CancelOpButton {op} /></span>
              {:else if r.status === 'failed'}
                <span class="fail-cell"><StatusChip tone="bad" label="Failed" /><span class="dm-trunc dm-dim" title={r.error}>{r.error ?? ''}</span></span>
              {:else}
                <StatusChip tone={runTone(r.status)} label={runLabel(r.status)} title={r.error} />
              {/if}
            </span>
            <span class="dm-num">{r.status === 'failed' ? '—' : fmtBytes(r.size_bytes)}</span>
            <span class="dm-num dm-dim">{fmtDuration(r.started_at, r.finished_at)}</span>
            <span class="dm-trunc muted dm-mono small" title={r.target_path}>{r.pruned_at ? 'removed by retention' : (r.target_path?.split('/').pop() ?? '—')}</span>
            <span class="dm-tr-menu" onclick={(e) => e.stopPropagation()} role="presentation"><Menu label="Actions for run {r.id}" items={runMenu(r)} /></span>
          </div>
        {:else}
          <div class="dm-empty">No runs match this filter.</div>
        {/each}
      </div>
    {/if}

  {:else}
    {#if !loaded}
      <div class="dm-tbl">{#each Array(3) as _, i (i)}<div class="dm-tr" style="grid-template-columns: 1fr"><Skeleton width="60%" height="0.9rem" /></div>{/each}</div>
    {:else if bTargets.length === 0}
      <EmptyState
        title="No targets yet"
        description="A target is where archives go: a local directory, an SMB share, SFTP, S3-compatible storage or WebDAV. Several jobs can share one."
      >
        {#snippet action()}
          {#if canCreate}<button type="button" class="dm-btn dm-btn-primary" onclick={openNewTarget}><Plus size={14} strokeWidth={2} /> New target</button>{/if}
        {/snippet}
      </EmptyState>
    {:else}
      <div class="dm-tbl" role="table" aria-label="Backup targets">
        <div class="dm-tr dm-th" role="row" style="grid-template-columns: {TGT_COLS}">
          <span></span><span>Target</span><span>Type</span><span>Location</span><span>Jobs</span><span>Last test</span><span></span>
        </div>
        {#each sortedTargets as t (t.id)}
          {@const tone = targetPendingHostKey(t) || t.status === 'error' ? 'bad' : t.status === 'connected' ? 'ok' : ''}
          {@const href = `/backups/targets/${t.id}`}
          {@const usedPct = t.total_bytes > 0 ? Math.round((t.used_bytes / t.total_bytes) * 100) : null}
          <div class="dm-tr dm-click" role="row" tabindex="0" style="grid-template-columns: {TGT_COLS}" onclick={() => goto(href)} onkeydown={(e) => rowKey(e, href)}>
            <span><span class="dm-dot {tone}"></span></span>
            <span class="dm-nm">
              <a {href} onclick={(e) => e.stopPropagation()}>{t.name}</a>
              {#if targetPendingHostKey(t)}<small class="bad">SFTP host key changed</small>{:else if usedPct != null}<small class:warn={usedPct > 80}>{fmtBytes(t.free_bytes)} free · {usedPct} % used</small>{/if}
            </span>
            <span class="muted">{targetTypeLabel(t.type)}</span>
            <span class="dm-mono dm-trunc muted small" title={targetLocation(t)}>{targetLocation(t)}</span>
            <span class="dm-num">{targetJobCount(t)}</span>
            <span class="dm-nm">
              {#if testing[t.id]}
                <span class="dm-t lr">Testing…</span>
              {:else}
                <span class="dm-t lr" class:bad={t.status === 'error'}>{t.status === 'connected' ? 'Connected' : t.status === 'error' ? 'Failed' : 'Not tested'}</span>
                <small>{fmtAgo(t.last_checked_at)}</small>
              {/if}
            </span>
            <span class="dm-tr-menu" onclick={(e) => e.stopPropagation()} role="presentation"><Menu label="Actions for {t.name}" items={targetMenu(t)} /></span>
          </div>
        {/each}
      </div>
    {/if}
  {/if}
</section>

<JobWizard
  open={wizardJobOpen}
  editing={wizardJobEdit}
  targets={bTargets}
  onclose={() => { wizardJobOpen = false; wizardJobEdit = null; }}
  onsave={wizardSaveJob}
/>
<TargetWizard
  open={wizardTargetOpen}
  editing={wizardTargetEdit}
  onclose={() => { wizardTargetOpen = false; wizardTargetEdit = null; }}
  onsave={wizardSaveTarget}
/>
<RestoreDialog
  run={restoreRun}
  preset={restorePreset}
  originHost={jobs.find((j) => j.id === restoreRun?.job_id)?.host_id ?? ''}
  onclose={() => (restoreRun = null)}
/>
<SystemRestoreDialog open={systemRestoreOpen} run={systemRestoreRun} onclose={() => (systemRestoreOpen = false)} />

<style>
  .bad { color: var(--bad); }
  .op-cell { display: inline-flex; align-items: center; gap: 6px; min-width: 0; max-width: 100%; }
  .warn { color: var(--warn); }
  .muted { color: var(--muted); }
  .small { font-size: 12px; }
  .lock { color: var(--subtle); margin-left: 6px; display: inline-flex; vertical-align: -1px; }
  .what { font-weight: 500 !important; }
  .lr { font-weight: 500 !important; }
  .lr.bad { color: var(--bad) !important; }
  .fail-cell { display: inline-flex; align-items: center; gap: 8px; min-width: 0; max-width: 100%; }
  .fail-cell > :global(:first-child) { flex: none; }
  .attn { border: 1px solid color-mix(in srgb, var(--warn) 40%, transparent); background: color-mix(in srgb, var(--warn) 6%, transparent); border-radius: var(--r); padding: 10px 12px; display: flex; flex-direction: column; gap: 6px; }
  .attn-h { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; font-size: var(--fs-sm); }
  .attn-h :global(svg) { color: var(--warn); }
  .attn-row { display: flex; align-items: center; gap: 10px; min-height: 32px; font-size: var(--fs-sm); min-width: 0; }
</style>
