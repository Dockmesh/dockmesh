<script lang="ts">
  import { untrack } from 'svelte';
  import { goto } from '$app/navigation';
  import { urlTab } from '$lib/urlTab.svelte';
  import { page } from '$app/state';
  import { api, ApiError, runRestorable } from '$lib/api';
  import type { BackupJob, BackupJobInput, BackupRun, BackupTarget, RestoreLeftover } from '$lib/api';
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
  import { m } from '$lib/paraglide/messages.js';
  import { fmtDateTime } from '$lib/i18n';

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
    try { jobs = await api.backups.listJobs(); } catch (err) { toast.error(m.backups_load_jobs_failed(), err instanceof ApiError ? err.message : undefined); }
  }
  async function loadRuns() {
    try { runs = await api.backups.listRuns(500); } catch (err) { toast.error(m.backups_load_runs_failed(), err instanceof ApiError ? err.message : undefined); }
  }
  async function loadTargets() {
    try { bTargets = await api.backups.listTargets(); } catch (err) { toast.error(m.backups_load_targets_failed(), err instanceof ApiError ? err.message : undefined); }
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

  // Copies of the files a system restore replaced; admin-only on the server.
  let leftovers = $state<RestoreLeftover[]>([]);
  let deletingLeftovers = $state(false);
  async function loadLeftovers() {
    if (!allowed('system.upgrade')) return;
    try {
      leftovers = (await api.backups.stagedSystemRestore()).leftovers ?? [];
    } catch {
      leftovers = [];
    }
  }
  $effect(() => { untrack(loadLeftovers); });

  async function deleteLeftovers() {
    const ok = await confirm.ask({
      title: m.backups_leftovers_confirm_title(),
      message: m.backups_leftovers_confirm_message(),
      body: leftovers.map((l) => l.path).join(', '),
      confirmLabel: m.common_delete(),
      danger: true
    });
    if (!ok) return;
    deletingLeftovers = true;
    try {
      await api.backups.deleteRestoreLeftovers();
      toast.success(m.backups_deleted(), m.backups_leftovers_deleted());
    } catch (err) {
      toast.error(m.backups_delete_failed(), err instanceof ApiError ? err.message : undefined);
    } finally {
      deletingLeftovers = false;
      await loadLeftovers();
    }
  }

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
      toast.error(m.backups_start_failed({ name: j.name }), err instanceof ApiError ? err.message : undefined);
    }
  }
  async function runAllEnabled() {
    if (runs.some((r) => r.status === 'running')) {
      toast.info(m.backups_already_running(), m.backups_already_running_hint());
      return;
    }
    const list = jobs.filter((j) => j.enabled);
    if (!(await confirm.ask({ title: m.backups_run_all_title(), message: m.backups_run_all_message({ count: list.length }), confirmLabel: m.backups_run_all_confirm() }))) return;
    for (const j of list) await runJob(j);
  }

  async function toggleJob(j: BackupJob) {
    try {
      const input: BackupJobInput = { name: j.name, host_id: j.host_id, target_id: j.target_id, target_type: j.target_type, target_config: j.target_config, sources: j.sources, schedule: j.schedule, retention_count: j.retention_count, retention_days: j.retention_days, encrypt: j.encrypt, pre_hooks: j.pre_hooks, post_hooks: j.post_hooks, enabled: !j.enabled };
      await api.backups.updateJob(j.id, input);
      await loadJobs();
    } catch (err) { toast.error(m.backups_job_change_failed(), err instanceof ApiError ? err.message : undefined); }
  }
  async function deleteJob(j: BackupJob) {
    if (!(await confirm.ask({ title: m.backups_delete_job_title(), message: m.backups_delete_job_message({ name: j.name }), body: m.backups_delete_job_body(), confirmLabel: m.common_delete(), danger: true }))) return;
    try { await api.backups.deleteJob(j.id); toast.success(m.backups_deleted(), j.name); await loadJobs(); } catch (err) { toast.error(m.backups_delete_failed(), err instanceof ApiError ? err.message : undefined); }
  }
  async function ackReview(j: BackupJob, mode: 'keep' | 'disable') {
    try {
      await api.backups.acknowledgeReview(j.id, mode);
      toast.success(mode === 'keep' ? m.backups_marked_reviewed() : m.common_disabled(), j.name);
      await loadJobs();
    } catch (err) { toast.error(m.common_failed(), err instanceof ApiError ? err.message : (err as Error).message); }
  }

  function targetPendingHostKey(t: BackupTarget): boolean {
    return t.type === 'sftp' && !!(t.config as { host_key_pending?: string } | null)?.host_key_pending;
  }
  function pendingHostKey(j: BackupJob): boolean {
    const t = targetById(j.target_id);
    if (t) return targetPendingHostKey(t);
    return j.target_type === 'sftp' && !!(j.target_config as { host_key_pending?: string } | null)?.host_key_pending;
  }
  async function acceptHostKey(j: BackupJob) {
    if (!(await confirm.ask({ title: m.backups_accept_host_key_title(), message: m.backups_accept_host_key_message({ name: j.name }), body: m.backups_host_key_body(), confirmLabel: m.backups_accept_key(), danger: true }))) return;
    try { await api.backups.acceptHostKey(j.id); toast.success(m.backups_host_key_accepted()); await Promise.all([loadJobs(), loadTargets()]); } catch (err) { toast.error(m.common_failed(), err instanceof ApiError ? err.message : undefined); }
  }
  async function acceptTargetHostKey(t: BackupTarget) {
    if (!(await confirm.ask({ title: m.backups_accept_host_key_title(), message: m.backups_accept_target_host_key_message({ name: t.name }), body: m.backups_host_key_body(), confirmLabel: m.backups_accept_key(), danger: true }))) return;
    try { await api.backups.acceptTargetHostKey(t.id); toast.success(m.backups_host_key_accepted()); await loadTargets(); } catch (err) { toast.error(m.common_failed(), err instanceof ApiError ? err.message : undefined); }
  }

  async function deleteTarget(t: BackupTarget) {
    if (!(await confirm.ask({ title: m.backups_delete_target_title(), message: m.backups_delete_target_message({ name: t.name }), body: m.backups_delete_target_body(), confirmLabel: m.common_delete(), danger: true }))) return;
    try { await api.backups.deleteTarget(t.id); toast.success(m.backups_deleted(), t.name); await loadTargets(); } catch (err) { toast.error(m.backups_delete_failed(), err instanceof ApiError ? err.message : undefined); }
  }
  let testing = $state<Record<number, boolean>>({});
  async function testTarget(t: BackupTarget) {
    testing[t.id] = true;
    try {
      const res = await api.backups.testTarget(t.id);
      if (res.status === 'connected') toast.success(m.backups_target_connected({ name: t.name }), res.total_bytes > 0 ? m.backups_free_of({ free: fmtBytes(res.free_bytes), total: fmtBytes(res.total_bytes) }) : undefined);
      else toast.error(m.backups_target_connection_failed({ name: t.name }), res.error);
      await loadTargets();
    } catch (err) {
      toast.error(m.backups_test_failed(), err instanceof ApiError ? err.message : undefined);
    } finally {
      testing[t.id] = false;
    }
  }

  async function downloadRunArchive(r: BackupRun) {
    try {
      await api.backups.downloadArchive(r.id);
      toast.success(m.backups_download_started(), `backup-run-${r.id}.tar.gz`);
    } catch (err) {
      toast.error(m.backups_download_failed(), err instanceof ApiError ? err.message : undefined);
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
        toast.success(m.common_updated(), payload.name);
      } else {
        await api.backups.createJob(payload);
        toast.success(m.common_created(), payload.name);
      }
      wizardJobOpen = false;
      wizardJobEdit = null;
      await Promise.all([loadJobs(), loadTargets()]);
    } catch (err) {
      toast.error(m.backups_save_failed(), err instanceof ApiError ? err.message : (err as Error).message);
    }
  }
  async function wizardSaveTarget(payload: { name: string; type: string; config: Record<string, any> }, isEdit: boolean, originalId?: number) {
    try {
      if (isEdit && originalId) {
        await api.backups.updateTarget(originalId, payload);
        toast.success(m.common_updated(), payload.name);
      } else {
        await api.backups.createTarget(payload);
        toast.success(m.common_created(), payload.name);
      }
      wizardTargetOpen = false;
      wizardTargetEdit = null;
      await loadTargets();
    } catch (err) {
      toast.error(m.backups_save_failed(), err instanceof ApiError ? err.message : (err as Error).message);
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
      { label: m.common_open(), kbd: '↵', href: `/backups/jobs/${j.id}` },
      ...(mayOnJob('backups.run', j) ? [{ label: m.backups_run_now(), disabled: !j.enabled, onclick: () => runJob(j) }] : []),
      ...(mayOnJob('backups.update', j) ? [{ label: m.backups_edit_ellipsis(), onclick: () => openEdit(j) }] : []),
      ...(last && runRestorable(last) && mayOnJob('backups.restore', last) ? [{ label: m.backups_restore_last_run(), onclick: () => openRestore(last) }] : []),
      ...(pendingHostKey(j) && mayOnJob('backups.update', j) ? [{ label: m.backups_accept_new_host_key(), danger: true, onclick: () => acceptHostKey(j) }] : []),
      ...(mayOnJob('backups.delete', j) ? [{ separator: true }, { label: m.common_delete_ellipsis(), danger: true, onclick: () => deleteJob(j) }] : [])
    ];
  }
  function runMenu(r: BackupRun): MenuItem[] {
    const restorable = runRestorable(r);
    return [
      { label: m.common_open(), kbd: '↵', href: `/backups/runs/${r.id}` },
      ...(operations.backupRun(r.id) ? [{ label: m.backups_steps_and_log(), onclick: () => operations.open(operations.backupRun(r.id)!.id) }] : []),
      ...(restorable && mayOnJob('backups.restore', r) ? [{ label: isSystemRun(r) ? m.backups_restore_system() : m.backups_restore_ellipsis(), onclick: () => openRestore(r) }] : []),
      ...(restorable && mayOnJob('backups.download', r) ? [{ label: m.backups_download_archive(), onclick: () => downloadRunArchive(r) }] : [])
    ];
  }
  function targetMenu(t: BackupTarget): MenuItem[] {
    return [
      { label: m.common_open(), kbd: '↵', href: `/backups/targets/${t.id}` },
      ...(canUpdate
        ? [
            { label: m.backups_test_connection(), onclick: () => testTarget(t) },
            { label: m.backups_edit_ellipsis(), onclick: () => openEditTarget(t) },
            ...(targetPendingHostKey(t) ? [{ label: m.backups_accept_new_host_key(), danger: true, onclick: () => acceptTargetHostKey(t) }] : [])
          ]
        : []),
      ...(allowed('backups.delete') ? [{ separator: true }, { label: m.common_delete_ellipsis(), danger: true, onclick: () => deleteTarget(t) }] : [])
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
    <h1>{m.backups_title()}</h1>
    <span class="dm-sub">
      {m.backups_sub_summary({ active: jobs.filter((j) => j.enabled).length, runs: recentRuns.length })}{#if recentFailed}, <span class="bad">{m.backups_n_failed({ n: recentFailed })}</span>{/if}
    </span>
    <span class="dm-sp"></span>
    {#if tab === 'jobs'}
      {#if canRun && jobs.some((j) => j.enabled)}
        <button type="button" class="dm-btn" onclick={runAllEnabled}><Play size={13} /> {m.backups_run_all_enabled()}</button>
      {/if}
      {#if canCreate}<button type="button" class="dm-btn dm-btn-primary" onclick={openNew}><Plus size={14} strokeWidth={2} /> {m.backups_new_job()}</button>{/if}
    {:else if tab === 'runs'}
      {#if allowed('system.upgrade')}
        <button type="button" class="dm-btn" onclick={() => { systemRestoreRun = null; systemRestoreOpen = true; }}>{m.backups_restore_system_from_file()}</button>
      {/if}
    {:else if canCreate}
      <button type="button" class="dm-btn dm-btn-primary" onclick={openNewTarget}><Plus size={14} strokeWidth={2} /> {m.backups_new_target()}</button>
    {/if}
  </div>

  {#if leftovers.length > 0}
    <div class="attn" role="status" data-testid="restore-leftovers">
      <div class="attn-h">
        <AlertTriangle size={14} strokeWidth={1.8} />
        <b>{m.backups_leftovers_title()}</b>
        <span class="dm-dim">· {m.backups_leftovers_hint()}</span>
        <span class="dm-sp"></span>
        <button type="button" class="dm-btn dm-btn-sm dm-btn-danger" onclick={deleteLeftovers} disabled={deletingLeftovers}>
          {deletingLeftovers ? m.backups_deleting() : m.common_delete()}
        </button>
      </div>
      {#each leftovers as l (l.path)}
        <div class="attn-row">
          <span class="dm-mono dm-trunc" title={l.path}>{l.path}</span>
          <span class="dm-sp"></span>
          <span class="dm-dim">{fmtBytes(l.bytes)} · {fmtDateTime(l.restored_at)}</span>
        </div>
      {/each}
    </div>
  {/if}

  <Tabs
    tabs={[
      { key: 'jobs', label: m.backups_tab_jobs(), count: jobs.length },
      { key: 'runs', label: m.backups_tab_runs(), count: runs.length, dot: recentFailed > 0 },
      { key: 'targets', label: m.backups_tab_targets(), count: bTargets.length, dot: bTargets.some((t) => t.status === 'error' || targetPendingHostKey(t)) }
    ]}
    active={tab}
    onchange={setTab}
    label={m.backups_sections_label()}
  />

  {#if tab === 'jobs'}
    {#if canUpdate && reviewJobs.length > 0}
      <div class="attn" role="status">
        <div class="attn-h">
          <AlertTriangle size={14} strokeWidth={1.8} />
          <b>{m.backups_review_title({ count: reviewJobs.length })}</b>
          <span class="dm-dim">· {m.backups_review_hint()}</span>
        </div>
        {#each reviewJobs as j (j.id)}
          <div class="attn-row">
            <span class="dm-mono">{j.name}</span>
            <span class="dm-dim dm-trunc" title={j.review_reason}>{j.review_reason}</span>
            <span class="dm-sp"></span>
            <button type="button" class="dm-btn dm-btn-sm" onclick={() => ackReview(j, 'keep')}>{m.backups_keep()}</button>
            <button type="button" class="dm-btn dm-btn-sm dm-btn-ghost" onclick={() => ackReview(j, 'disable')}>{m.common_disable()}</button>
          </div>
        {/each}
      </div>
    {/if}

    {#if loaded && jobs.length > 0}
      <StatBar
        items={[
          { label: m.backups_tab_jobs(), value: m.backups_n_of_total({ n: jobs.filter((j) => j.enabled).length, total: jobs.length }), note: m.backups_stat_enabled() },
          { label: m.backups_stat_runs_24h(), value: recentRuns.length, note: recentFailed ? m.backups_n_failed({ n: recentFailed }) : m.backups_none_failed(), tone: recentFailed ? 'bad' : '' },
          { label: m.backups_next_run(), value: nextRun ? fmtUntil(nextRun.next_run_at) : '—', note: nextRun?.name ?? '' },
          { label: m.backups_tab_targets(), value: bTargets.length, note: bTargets.filter((t) => t.status === 'error').length ? m.backups_n_with_errors({ n: bTargets.filter((t) => t.status === 'error').length }) : '', tone: bTargets.some((t) => t.status === 'error') ? 'bad' : '' }
        ]}
      />
    {/if}

    {#if !loaded}
      <div class="dm-tbl">{#each Array(4) as _, i (i)}<div class="dm-tr" style="grid-template-columns: 1fr"><Skeleton width="60%" height="0.9rem" /></div>{/each}</div>
    {:else if jobs.length === 0}
      <EmptyState
        title={m.backups_jobs_empty_title()}
        description={m.backups_jobs_empty_desc()}
      >
        {#snippet action()}
          {#if canCreate}<button type="button" class="dm-btn dm-btn-primary" onclick={openNew}><Plus size={14} strokeWidth={2} /> {m.backups_new_job()}</button>{/if}
        {/snippet}
      </EmptyState>
    {:else}
      <div class="dm-tbl" role="table" aria-label={m.backups_jobs_table_label()}>
        <div class="dm-tr dm-th" role="row" style="grid-template-columns: {JOB_COLS}">
          <span></span><span>{m.backups_col_job()}</span><span>{m.backups_col_what()}</span><span>{m.common_host()}</span><span>{m.common_target()}</span><span>{m.backups_col_schedule()}</span><span>{m.backups_col_last_run()}</span><span>{m.backups_col_on()}</span><span></span>
        </div>
        {#each sortedJobs as j (j.id)}
          {@const last = lastRunOf(j)}
          {@const op = liveOpFor(j)}
          {@const tgt = targetById(j.target_id)}
          {@const href = `/backups/jobs/${j.id}`}
          <div class="dm-tr dm-click" role="row" tabindex="0" style="grid-template-columns: {JOB_COLS}" onclick={() => goto(href)} onkeydown={(e) => rowKey(e, href)}>
            <span><span class="dm-dot {jobTone(j)}"></span></span>
            <span class="dm-nm">
              <a {href} onclick={(e) => e.stopPropagation()}>{j.name}</a>{#if j.encrypt}<span class="lock" title={m.backups_encrypted()}><Lock size={11} /></span>{/if}
              {#if pendingHostKey(j)}<small class="bad">{m.backups_host_key_changed()}</small>{:else if !j.enabled}<small>{m.backups_job_disabled()}</small>{/if}
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
                  {last.status === 'failed' ? m.common_failed() : last.status === 'running' ? m.common_running() : fmtBytes(last.size_bytes)}{last.status === 'success_with_warnings' ? ` · ${m.backups_run_warnings()}` : ''}
                </span>
                <small>{fmtAgo(last.started_at)}</small>
              {:else}
                <span class="dm-dim">{m.common_never()}</span>
              {/if}
            </span>
            <span onclick={(e) => e.stopPropagation()} role="presentation"><Switch checked={j.enabled} label={m.common_enabled()} hideLabel disabled={!mayOnJob('backups.update', j)} onchange={() => toggleJob(j)} /></span>
            <span class="dm-tr-menu" onclick={(e) => e.stopPropagation()} role="presentation"><Menu label={m.backups_actions_for({ name: j.name })} items={jobMenu(j)} /></span>
          </div>
        {/each}
      </div>
    {/if}

  {:else if tab === 'runs'}
    <div class="dm-toolbar">
      <Segmented
        label={m.backups_filter_runs()}
        bind:value={runStatusFilter}
        options={[
          { key: 'all', label: m.common_all(), count: runCounts.all },
          { key: 'success', label: m.backups_filter_succeeded(), count: runCounts.success },
          { key: 'failed', label: m.common_failed(), count: runCounts.failed },
          { key: 'running', label: m.common_running(), count: runCounts.running }
        ]}
      />
      <select class="dm-filter" bind:value={runJobFilter} aria-label={m.backups_filter_by_job()}>
        <option value="">{m.backups_all_jobs()}</option>
        {#each sortedJobs as j (j.id)}<option value={j.name}>{j.name}</option>{/each}
      </select>
    </div>

    {#if !loaded}
      <div class="dm-tbl">{#each Array(4) as _, i (i)}<div class="dm-tr" style="grid-template-columns: 1fr"><Skeleton width="60%" height="0.9rem" /></div>{/each}</div>
    {:else if runs.length === 0}
      <EmptyState title={m.backups_runs_empty_title()} description={m.backups_runs_empty_desc()} />
    {:else}
      <div class="dm-tbl" role="table" aria-label={m.backups_runs_table_label()}>
        <div class="dm-tr dm-th" role="row" style="grid-template-columns: {RUN_COLS}">
          <span>{m.backups_col_when()}</span><span>{m.backups_col_job()}</span><span>{m.backups_col_result()}</span><span>{m.common_size()}</span><span>{m.backups_col_duration()}</span><span>{m.backups_col_archive()}</span><span></span>
        </div>
        {#each filteredRuns as r (r.id)}
          {@const op = operations.backupRun(r.id)}
          {@const href = `/backups/runs/${r.id}`}
          <div class="dm-tr dm-click" role="row" tabindex="0" style="grid-template-columns: {RUN_COLS}" onclick={() => goto(href)} onkeydown={(e) => rowKey(e, href)}>
            <span class="dm-num dm-dim" title={fmtDateTime(r.started_at)}>{fmtAgo(r.started_at)}</span>
            <span class="dm-nm"><a {href} onclick={(e) => e.stopPropagation()}>{r.job_name}</a>{#if r.encrypted}<span class="lock" title={m.backups_encrypted()}><Lock size={11} /></span>{/if}</span>
            <span>
              {#if op && (isActive(op) || r.status === 'running')}
                <span class="op-cell"><OpProgress {op} /><CancelOpButton {op} /></span>
              {:else if r.status === 'failed'}
                <span class="fail-cell"><StatusChip tone="bad" label={m.common_failed()} /><span class="dm-trunc dm-dim" title={r.error}>{r.error ?? ''}</span></span>
              {:else}
                <StatusChip tone={runTone(r.status)} label={runLabel(r.status)} title={r.error} />
              {/if}
            </span>
            <span class="dm-num">{r.status === 'failed' ? '—' : fmtBytes(r.size_bytes)}</span>
            <span class="dm-num dm-dim">{fmtDuration(r.started_at, r.finished_at)}</span>
            <span class="dm-trunc muted dm-mono small" title={r.target_path}>{r.pruned_at ? m.backups_removed_by_retention() : (r.target_path?.split('/').pop() ?? '—')}</span>
            <span class="dm-tr-menu" onclick={(e) => e.stopPropagation()} role="presentation"><Menu label={m.backups_actions_for_run({ id: r.id })} items={runMenu(r)} /></span>
          </div>
        {:else}
          <div class="dm-empty">{m.backups_runs_no_match()}</div>
        {/each}
      </div>
    {/if}

  {:else}
    {#if !loaded}
      <div class="dm-tbl">{#each Array(3) as _, i (i)}<div class="dm-tr" style="grid-template-columns: 1fr"><Skeleton width="60%" height="0.9rem" /></div>{/each}</div>
    {:else if bTargets.length === 0}
      <EmptyState
        title={m.backups_targets_empty_title()}
        description={m.backups_targets_empty_desc()}
      >
        {#snippet action()}
          {#if canCreate}<button type="button" class="dm-btn dm-btn-primary" onclick={openNewTarget}><Plus size={14} strokeWidth={2} /> {m.backups_new_target()}</button>{/if}
        {/snippet}
      </EmptyState>
    {:else}
      <div class="dm-tbl" role="table" aria-label={m.backups_targets_table_label()}>
        <div class="dm-tr dm-th" role="row" style="grid-template-columns: {TGT_COLS}">
          <span></span><span>{m.common_target()}</span><span>{m.common_type()}</span><span>{m.backups_col_location()}</span><span>{m.backups_tab_jobs()}</span><span>{m.backups_col_last_test()}</span><span></span>
        </div>
        {#each sortedTargets as t (t.id)}
          {@const tone = targetPendingHostKey(t) || t.credentials_error || t.status === 'error' ? 'bad' : t.status === 'connected' ? 'ok' : ''}
          {@const href = `/backups/targets/${t.id}`}
          {@const usedPct = t.total_bytes > 0 ? Math.round((t.used_bytes / t.total_bytes) * 100) : null}
          <div class="dm-tr dm-click" role="row" tabindex="0" style="grid-template-columns: {TGT_COLS}" onclick={() => goto(href)} onkeydown={(e) => rowKey(e, href)}>
            <span><span class="dm-dot {tone}"></span></span>
            <span class="dm-nm">
              <a {href} onclick={(e) => e.stopPropagation()}>{t.name}</a>
              {#if t.credentials_error}<small class="bad" title={t.credentials_error}>{m.backups_credentials_undecryptable()}</small>{:else if targetPendingHostKey(t)}<small class="bad">{m.backups_host_key_changed()}</small>{:else if usedPct != null}<small class:warn={usedPct > 80}>{m.backups_free_used({ free: fmtBytes(t.free_bytes), pct: usedPct })}</small>{/if}
            </span>
            <span class="muted">{targetTypeLabel(t.type)}</span>
            <span class="dm-mono dm-trunc muted small" title={targetLocation(t)}>{targetLocation(t)}</span>
            <span class="dm-num">{targetJobCount(t)}</span>
            <span class="dm-nm">
              {#if testing[t.id]}
                <span class="dm-t lr">{m.backups_testing()}</span>
              {:else}
                <span class="dm-t lr" class:bad={t.status === 'error'}>{t.status === 'connected' ? m.backups_connected() : t.status === 'error' ? m.common_failed() : m.backups_not_tested()}</span>
                <small>{fmtAgo(t.last_checked_at)}</small>
              {/if}
            </span>
            <span class="dm-tr-menu" onclick={(e) => e.stopPropagation()} role="presentation"><Menu label={m.backups_actions_for({ name: t.name })} items={targetMenu(t)} /></span>
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
