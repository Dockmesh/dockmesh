<script lang="ts">
  // Backups — editorial rebuild based on `Dockmesh Wizard (6)/backups.jsx`.
  // Header with the "restore is the only test that matters" quote, stat
  // banner, ed-tabs strip Jobs / Runs / Targets, jobs+targets as card
  // grids per the mockup, runs as a cleaner editorial table. Wizards
  // and the restore modal stay on the legacy `Modal` component for now —
  // they're functional and the editorial rebuild of those is a follow-up.
  import { goto } from '$app/navigation';
  import { page } from '$app/stores';
  import { api, ApiError } from '$lib/api';
  import type { BackupJob, BackupJobInput, BackupRun, BackupSource, BackupHook, BackupTarget } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import { Card, Button, Input, Modal, Badge, EmptyState, Skeleton } from '$lib/components/ui';
  import { Eyebrow } from '$lib/components/editorial';
  import JobDrawer from './_JobDrawer.svelte';
  import RunDrawer from './_RunDrawer.svelte';
  import TargetDrawer from './_TargetDrawer.svelte';
  import JobWizard from './_JobWizard.svelte';
  import TargetWizard from './_TargetWizard.svelte';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { autoRefresh } from '$lib/autorefresh';
  import {
    Archive, Plus, Play, Trash2, RefreshCw, Undo2, HardDrive, Lock,
    Search, Clock, Copy, AlertTriangle, ArrowRight, Shield, Box, Layers,
    LayoutDashboard
  } from 'lucide-svelte';

  // Helpers used by the new card-grid layout.
  function targetTypeMeta(type: string): { label: string; glyph: string } {
    if (type === 's3') return { label: 'S3 / object store', glyph: 'S3' };
    if (type === 'sftp') return { label: 'SFTP over SSH', glyph: 'FTP' };
    if (type === 'smb') return { label: 'SMB / NAS share', glyph: 'NAS' };
    if (type === 'webdav') return { label: 'WebDAV / Nextcloud', glyph: 'DAV' };
    return { label: 'Local directory', glyph: 'LOC' };
  }
  function jobSourceLabel(j: BackupJob): string {
    if (j.sources.length === 0) return 'no source';
    const s = j.sources[0];
    // Backend additionally accepts a 'system' source type for the
    // dockmesh-system meta-bundle (DB + stacks + CA). The TS interface
    // pre-dates that addition and only lists volume / stack — cast at
    // the call site rather than widening the public type.
    const t = s.type as string;
    if (t === 'system') return 'dockmesh-system';
    return `${t}: ${s.name}`;
  }
  function jobSourceIcon(j: BackupJob) {
    const t = (j.sources[0]?.type as string | undefined) ?? '';
    if (t === 'stack') return Layers;
    if (t === 'volume') return HardDrive;
    if (t === 'system') return LayoutDashboard;
    return Box;
  }
  function targetById(id: number): BackupTarget | undefined {
    return bTargets.find((t) => t.id === id);
  }
  // Map a job to its run-history (last 14 runs) for sparkline + stats.
  function runsForJob(name: string): BackupRun[] {
    return runs.filter((r) => r.job_name === name).slice(0, 14);
  }
  function jobLastRun(name: string): BackupRun | null {
    return runs.find((r) => r.job_name === name) ?? null;
  }
  function jobStatusFor(name: string): 'ok' | 'warn' | 'failed' | 'idle' | 'running' {
    const last = jobLastRun(name);
    if (!last) return 'idle';
    if (last.status === 'failed') return 'failed';
    if (last.status === 'running') return 'running';
    if (last.status === 'success') return 'ok';
    return 'idle';
  }
  function statusDot(s: string): 'ok-dot' | 'warn-dot' | 'fail-dot' | 'neutral-dot' {
    if (s === 'ok' || s === 'running' || s === 'success' || s === 'connected') return 'ok-dot';
    if (s === 'warn' || s === 'unknown') return 'warn-dot';
    if (s === 'failed' || s === 'error') return 'fail-dot';
    return 'neutral-dot';
  }

  type Tab = 'jobs' | 'runs' | 'targets';
  let tab = $state<Tab>((new URLSearchParams($page.url.search).get('tab') as Tab) || 'jobs');

  // Runs view — Timeline (default, mockup pattern) vs List. Timeline
  // groups by day with colored timeline-blocks per run; List is the
  // dense table form for ops that prefer rows.
  let runsView = $state<'timeline' | 'list'>('timeline');

  // Group filteredRuns by day for the timeline. Newest day first; runs
  // within a day stay in API order (newest first).
  function dayKey(ts: string): string {
    return new Date(ts).toISOString().slice(0, 10);
  }
  function dayHeading(iso: string): string {
    const d = new Date(iso + 'T00:00:00Z');
    const todayIso = new Date().toISOString().slice(0, 10);
    if (iso === todayIso) return 'Today';
    const y = new Date(); y.setDate(y.getDate() - 1);
    if (iso === y.toISOString().slice(0, 10)) return 'Yesterday';
    if ((Date.now() - d.getTime()) / 86400000 < 7) return d.toLocaleDateString('en-US', { weekday: 'long' });
    return d.toLocaleDateString('en-US', { month: 'short', day: 'numeric', year: 'numeric' });
  }

  // Drawer-detail state. We keep three drawers (Job / Run / Target)
  // in this same component because they all live on the same page —
  // a Job-drawer click can deep-link into a Run-drawer without losing
  // place. Drawer markup is in the per-resource component files; this
  // page just owns the open/close state.
  let drawerJob = $state<BackupJob | null>(null);
  let drawerRun = $state<BackupRun | null>(null);
  let drawerTarget = $state<BackupTarget | null>(null);
  function openJobDrawer(j: BackupJob) {
    drawerJob = j;
    drawerRun = null;
    drawerTarget = null;
  }
  function openRunDrawer(r: BackupRun) {
    drawerRun = r;
    drawerJob = null;
    drawerTarget = null;
  }
  function openTargetDrawer(t: BackupTarget) {
    drawerTarget = t;
    drawerJob = null;
    drawerRun = null;
  }
  function closeDrawers() {
    drawerJob = null;
    drawerRun = null;
    drawerTarget = null;
  }
  // Crossover: clicking a Run row inside the JobDrawer hops to the
  // Run-drawer without an in-between close. We swap state directly.
  function jumpJobToRun(r: BackupRun) {
    drawerJob = null;
    drawerRun = r;
  }
  // Restore from RunDrawer routes through the existing restore-modal
  // flow so the confirm-text validation stays in one place. Each mode
  // opens the same restore modal with a pre-set context (the modal
  // text adapts in a follow-up).
  function restoreRunFromDrawer(r: BackupRun, mode: 'in-place' | 'alongside' | 'download') {
    if (mode === 'download') {
      downloadRunArchive(r);
      return;
    }
    closeDrawers();
    openRestore(r);
  }

  async function downloadRunArchive(r: BackupRun) {
    try {
      await api.backups.downloadArchive(r.id);
      toast.success('Download started', `backup-run-${r.id}.tar.gz`);
    } catch (err) {
      toast.error('Download failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  let jobs = $state<BackupJob[]>([]);
  let runs = $state<BackupRun[]>([]);
  let loading = $state(false);

  // Job modal
  let showJob = $state(false);
  let editing = $state<BackupJob | null>(null);

  // Structured form fields
  let jName = $state('');
  let jTargetType = $state<'local' | 's3'>('local');
  let jLocalPath = $state('./data/backups');
  let jS3Endpoint = $state('');
  let jS3Bucket = $state('');
  let jS3AccessKey = $state('');
  let jS3SecretKey = $state('');
  let jS3Region = $state('');
  let jS3SSL = $state(true);
  // P.13.5: jobs accept exactly one source. Start empty so the chip
  // picker drives the choice rather than the legacy "row of empty
  // type+name fields" UX. Submit stays disabled until a chip is on.
  let jSources = $state<Array<{ type: string; name: string }>>([]);
  let jSchedule = $state('0 3 * * *');
  let jRetentionCount = $state(7);
  let jRetentionDays = $state(0);
  let jEncrypt = $state(false);
  let jEnabled = $state(true);
  let jPreHooks = $state<Array<{ container: string; cmd: string }>>([]);
  let jPostHooks = $state<Array<{ container: string; cmd: string }>>([]);

  // Cron presets
  const cronPresets = [
    { label: 'Daily 3am', cron: '0 3 * * *' },
    { label: 'Daily midnight', cron: '0 0 * * *' },
    { label: 'Every 6 hours', cron: '0 */6 * * *' },
    { label: 'Every 12 hours', cron: '0 */12 * * *' },
    { label: 'Weekly Sunday 2am', cron: '0 2 * * 0' },
    { label: 'Monthly 1st 3am', cron: '0 3 1 * *' },
  ];

  function cronHuman(cron: string): string {
    const presets: Record<string, string> = {
      '0 3 * * *': 'Daily at 03:00',
      '0 0 * * *': 'Daily at midnight',
      '0 */6 * * *': 'Every 6 hours',
      '0 */12 * * *': 'Every 12 hours',
      '0 2 * * 0': 'Weekly Sunday 02:00',
      '0 3 1 * *': 'Monthly 1st at 03:00',
    };
    return presets[cron] ?? cron;
  }

  // Restore modal
  let showRestore = $state(false);
  let restoreRun = $state<BackupRun | null>(null);
  let restoreVolume = $state('');
  let restoreConfirm = $state('');

  // Runs filters
  let runJobFilter = $state('');
  let runStatusFilter = $state<'all' | 'success' | 'failed' | 'running'>('all');

  // Targets
  let bTargets = $state<BackupTarget[]>([]);
  let targetsLoading = $state(false);
  let showTarget = $state(false);
  let editingTarget = $state<BackupTarget | null>(null);
  let tName = $state('');
  let tType = $state<'local' | 's3' | 'sftp' | 'smb' | 'webdav'>('local');
  let tConfig = $state<Record<string, string>>({});
  let tSaving = $state(false);

  const targetTypes = [
    { value: 'local', label: 'Local Directory', icon: '💾', fields: [{ key: 'path', label: 'Path', placeholder: './data/backups' }] },
    { value: 'sftp', label: 'SFTP (SSH)', icon: '🔐', fields: [{ key: 'host', label: 'Host', placeholder: 'nas.local' }, { key: 'port', label: 'Port', placeholder: '22' }, { key: 'username', label: 'Username', placeholder: '' }, { key: 'password', label: 'Password', placeholder: '' }, { key: 'path', label: 'Remote path', placeholder: '/backups' }] },
    { value: 'smb', label: 'SMB / NAS', icon: '📁', fields: [{ key: 'host', label: 'Server', placeholder: '192.168.1.100' }, { key: 'port', label: 'Port', placeholder: '445' }, { key: 'share', label: 'Share name', placeholder: 'backups' }, { key: 'username', label: 'Username', placeholder: '' }, { key: 'password', label: 'Password', placeholder: '' }, { key: 'path', label: 'Path within share', placeholder: 'dockmesh' }] },
    { value: 'webdav', label: 'WebDAV (Nextcloud)', icon: '☁️', fields: [{ key: 'url', label: 'WebDAV URL', placeholder: 'https://nextcloud.example.com/remote.php/dav/files/user/' }, { key: 'username', label: 'Username', placeholder: '' }, { key: 'password', label: 'Password', placeholder: '' }, { key: 'path', label: 'Path', placeholder: '/backups' }] },
    { value: 's3', label: 'S3 / MinIO / Wasabi', icon: '🪣', fields: [{ key: 'endpoint', label: 'Endpoint', placeholder: 's3.amazonaws.com' }, { key: 'bucket', label: 'Bucket', placeholder: 'my-backups' }, { key: 'access_key', label: 'Access Key', placeholder: '' }, { key: 'secret_key', label: 'Secret Key', placeholder: '' }, { key: 'region', label: 'Region', placeholder: 'us-east-1' }] }
  ];

  async function loadTargets() {
    targetsLoading = true;
    try { bTargets = await api.backups.listTargets(); } catch (err) { toast.error('Failed', err instanceof ApiError ? err.message : undefined); } finally { targetsLoading = false; }
  }

  function openNewTarget() {
    wizardTargetEdit = null;
    wizardTargetOpen = true;
  }
  function openEditTarget(t: BackupTarget) {
    wizardTargetEdit = t;
    wizardTargetOpen = true;
  }
  async function saveTarget(e: Event) {
    e.preventDefault(); tSaving = true;
    const config: Record<string, any> = { ...tConfig };
    if (config.port) config.port = parseInt(config.port) || 0;
    try {
      if (editingTarget) { await api.backups.updateTarget(editingTarget.id, { name: tName, type: tType, config }); toast.success('Updated', tName); }
      else { await api.backups.createTarget({ name: tName, type: tType, config }); toast.success('Created', tName); }
      showTarget = false; await loadTargets();
    } catch (err) { toast.error('Save failed', err instanceof ApiError ? err.message : undefined); }
    finally { tSaving = false; }
  }
  async function deleteTarget(t: BackupTarget) {
    if (!(await confirm.ask({ title: 'Delete backup target', message: `Delete target "${t.name}"?`, body: 'Backup jobs that write to this target will fail on their next run until you reassign them.', confirmLabel: 'Delete', danger: true }))) return;
    try { await api.backups.deleteTarget(t.id); toast.success('Deleted'); await loadTargets(); } catch (err) { toast.error('Failed', err instanceof ApiError ? err.message : undefined); }
  }
  async function testTarget(t: BackupTarget) {
    toast.info('Testing connection…', t.name);
    try {
      const res = await api.backups.testTarget(t.id);
      if (res.status === 'connected') {
        toast.success('Connected', res.total_bytes > 0 ? `${fmtBytes(res.free_bytes)} free of ${fmtBytes(res.total_bytes)}` : 'OK');
      } else {
        toast.error('Connection failed', res.error);
      }
      await loadTargets();
    } catch (err) { toast.error('Test failed', err instanceof ApiError ? err.message : undefined); }
  }

  const activeTargetType = $derived(targetTypes.find(t => t.value === tType));

  // Test connection in dialog
  let testResult = $state<{ status: string; total_bytes?: number; used_bytes?: number; free_bytes?: number; error?: string } | null>(null);
  let testBusy = $state(false);
  async function testConfigInDialog() {
    testBusy = true; testResult = null;
    const config: Record<string, any> = { ...tConfig };
    if (config.port) config.port = parseInt(config.port) || 0;
    try {
      testResult = await api.backups.testTargetConfig(tType, config);
    } catch (err) {
      testResult = { status: 'error', error: err instanceof ApiError ? err.message : String(err) };
    } finally { testBusy = false; }
  }

  // SMB share discovery
  let smbShares = $state<string[]>([]);
  let smbDiscovering = $state(false);
  async function discoverShares() {
    smbDiscovering = true; smbShares = [];
    try {
      const res = await api.backups.discoverShares(tConfig.host ?? '', parseInt(tConfig.port ?? '445'), tConfig.username ?? '', tConfig.password ?? '');
      if (res.error) { toast.error('Discovery failed', res.error); }
      else { smbShares = res.shares ?? []; if (smbShares.length === 0) toast.info('No shares found'); }
    } catch (err) { toast.error('Discovery failed', err instanceof ApiError ? err.message : String(err)); }
    finally { smbDiscovering = false; }
  }

  // Available resources for dropdowns in job dialog
  let availableVolumes = $state<string[]>([]);
  let availableStacks = $state<string[]>([]);
  let availableContainers = $state<string[]>([]);

  async function loadAvailableResources() {
    try {
      const [vols, stks, ctrs] = await Promise.all([
        api.volumes.list('local').catch(() => []),
        api.stacks.list().catch(() => []),
        api.containers.list(false, 'local').catch(() => [])
      ]);
      availableVolumes = (Array.isArray(vols) ? vols : []).map((v: any) => v.Name).filter(Boolean).sort();
      availableStacks = (Array.isArray(stks) ? stks : []).map((s: any) => s.name).filter(Boolean).sort();
      const ctrList: any[] = Array.isArray(ctrs) ? ctrs : [];
      availableContainers = ctrList.map((c: any) => (c.Names?.[0] ?? '').replace(/^\//, '')).filter(Boolean).sort();
    } catch { /* ignore */ }
  }

  // Selected target ID for job dialog (separate from jTargetType)
  let jSelectedTarget = $state<string>('inline');

  // Visual cron builder
  let cronMode = $state<'preset' | 'custom'>('preset');
  let cronFreq = $state<'daily' | 'weekly' | 'hourly' | 'monthly'>('daily');
  let cronHour = $state(3);
  let cronMinute = $state(0);
  let cronWeekday = $state(0); // 0=Sun

  function buildCronFromVisual(): string {
    switch (cronFreq) {
      case 'hourly': return `${cronMinute} * * * *`;
      case 'daily': return `${cronMinute} ${cronHour} * * *`;
      case 'weekly': return `${cronMinute} ${cronHour} * * ${cronWeekday}`;
      case 'monthly': return `${cronMinute} ${cronHour} 1 * *`;
    }
  }

  $effect(() => {
    if (cronMode === 'preset') {
      jSchedule = buildCronFromVisual();
    }
  });

  // Hook command presets
  const hookPresets = [
    { label: 'PostgreSQL dump', container: 'postgres', cmd: 'pg_dumpall -U postgres -f /tmp/dump.sql' },
    { label: 'MySQL dump', container: 'mysql', cmd: 'mysqldump -u root --all-databases > /tmp/dump.sql' },
    { label: 'MariaDB dump', container: 'mariadb', cmd: 'mariadb-dump -u root --all-databases > /tmp/dump.sql' },
    { label: 'Redis save', container: 'redis', cmd: 'redis-cli BGSAVE' },
    { label: 'MongoDB dump', container: 'mongo', cmd: 'mongodump --out /tmp/dump' },
  ];

  async function loadJobs() {
    loading = true;
    try { jobs = await api.backups.listJobs(); } catch (err) { toast.error('Failed', err instanceof ApiError ? err.message : undefined); } finally { loading = false; }
  }

  async function loadRuns() {
    loading = true;
    try { runs = await api.backups.listRuns(500); } catch (err) { toast.error('Failed', err instanceof ApiError ? err.message : undefined); } finally { loading = false; }
  }

  $effect(() => {
    if (!allowed('backups.update')) { goto('/'); return; }
    if (tab === 'jobs') { loadJobs(); loadTargets(); }
    else if (tab === 'runs') { loadRuns(); loadJobs(); }
    else if (tab === 'targets') loadTargets();
  });

  // Poll the current tab's data every 5s so "run now" + scheduled
  // runs update their state without a manual refresh.
  $effect(() => {
    const refresh = () => {
      if (tab === 'jobs') { loadJobs(); loadTargets(); }
      else if (tab === 'runs') { loadRuns(); }
      else if (tab === 'targets') loadTargets();
    };
    return autoRefresh(refresh, 5_000);
  });

  // Summary stats
  const activeJobs = $derived(jobs.filter(j => j.enabled).length);
  const recentRuns = $derived(runs.filter(r => {
    const age = Date.now() - new Date(r.started_at).getTime();
    return age < 86400000;
  }));
  const recentSuccess = $derived(recentRuns.filter(r => r.status === 'success').length);
  const recentFailed = $derived(recentRuns.filter(r => r.status === 'failed').length);
  const nextRun = $derived(jobs.filter(j => j.enabled && j.next_run_at).sort((a, b) => (a.next_run_at ?? '').localeCompare(b.next_run_at ?? ''))[0]?.next_run_at);

  // Stable orderings so the 5s autoRefresh doesn't shuffle the cards
  // / table rows out from under the operator. Same pattern as
  // /resources, /containers, /hosts. Backend may return arrays in
  // arbitrary order on each fetch.
  const sortedJobs = $derived(
    jobs.slice().sort((a, b) => a.name.localeCompare(b.name))
  );
  const sortedTargets = $derived(
    bTargets.slice().sort((a, b) => a.name.localeCompare(b.name))
  );

  // Runs filtering — backend already returns newest-first, so we keep
  // that order (don't sort by name). The filter is stable in itself.
  const filteredRuns = $derived(
    runs.filter(r => {
      if (runJobFilter && r.job_name !== runJobFilter) return false;
      if (runStatusFilter !== 'all' && r.status !== runStatusFilter) return false;
      return true;
    })
  );

  // Helpers
  function fmtBytes(n: number): string {
    if (!n) return '—';
    if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
    if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
    return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`;
  }
  function fmtTime(ts?: string): string {
    if (!ts) return '—';
    const d = (Date.now() - new Date(ts).getTime()) / 1000;
    if (d < 60) return 'just now';
    if (d < 3600) return `${Math.floor(d / 60)}m ago`;
    if (d < 86400) return `${Math.floor(d / 3600)}h ago`;
    return new Date(ts).toLocaleString();
  }
  // Forward-time formatter for upcoming events (next backup run).
  // Mockup pattern shows "in 5h 22m" / "in 2d 4h" — never em-dash if
  // we have a timestamp, never raw ISO. When the backend hasn't filled
  // next_run_at, we fall back to the cron-human label only.
  function fmtUntil(ts?: string): string {
    if (!ts) return '—';
    const t = Date.parse(ts);
    if (!t || isNaN(t)) return '—';
    const secs = Math.floor((t - Date.now()) / 1000);
    if (secs < 0) return 'overdue';
    if (secs < 60) return 'in <1m';
    if (secs < 3600) return `in ${Math.floor(secs / 60)}m`;
    if (secs < 86400) {
      const h = Math.floor(secs / 3600);
      const m = Math.floor((secs % 3600) / 60);
      return m > 0 ? `in ${h}h ${m}m` : `in ${h}h`;
    }
    const d = Math.floor(secs / 86400);
    const h = Math.floor((secs % 86400) / 3600);
    return h > 0 ? `in ${d}d ${h}h` : `in ${d}d`;
  }
  // Map a hook command back to its preset label so the card-foot can
  // show "postgresql, redis" instead of "2 hooks". Mirrors the preset
  // table from JobWizard.
  function hookLabel(h: { container: string; cmd: string[] }): string {
    const cmd = h.cmd.join(' ').toLowerCase();
    if (cmd.includes('pg_dump')) return 'postgresql';
    if (cmd.includes('mysqldump')) return 'mysql';
    if (cmd.includes('mariadb-dump')) return 'mariadb';
    if (cmd.includes('redis-cli')) return 'redis';
    if (cmd.includes('mongodump')) return 'mongodb';
    return h.container || 'custom';
  }
  // Source meta — second line under the source label. For system this
  // is the static "DB · stacks · data" hint. For stack/volume we use
  // the source name as the meta because the backend doesn't expose
  // per-source container/size counts yet.
  function jobSourceMeta(j: BackupJob): string {
    if (j.sources.length === 0) return '—';
    const s = j.sources[0];
    const t = s.type as string;
    if (t === 'system') return 'DB · stacks/ · data/';
    return s.name;
  }
  // Mini-sparkline points for the job card. We use the last 7 SUCCESS
  // run sizes — failures become a 0 dip so the line drops visibly.
  function jobSpark(jobName: string): number[] {
    return runs
      .filter((r) => r.job_name === jobName)
      .slice(0, 7)
      .reverse()
      .map((r) => (r.status === 'failed' ? 0 : r.size_bytes));
  }
  function sparkPoints(values: number[], width = 80, height = 22): string {
    if (values.length < 2) return '';
    const max = Math.max(...values, 1);
    const min = Math.min(...values, 0);
    const range = Math.max(max - min, 1);
    const stepX = width / (values.length - 1);
    return values.map((v, i) => `${i * stepX},${(height - ((v - min) / range) * height).toFixed(1)}`).join(' ');
  }
  function fmtDuration(start: string, end?: string): string {
    if (!end) return 'running…';
    const secs = Math.floor((new Date(end).getTime() - new Date(start).getTime()) / 1000);
    if (secs < 60) return `${secs}s`;
    if (secs < 3600) return `${Math.floor(secs / 60)}m ${secs % 60}s`;
    return `${Math.floor(secs / 3600)}h ${Math.floor((secs % 3600) / 60)}m`;
  }
  function statusVariant(s: string): 'success' | 'warning' | 'danger' | 'info' | 'default' {
    if (s === 'success') return 'success';
    if (s === 'failed') return 'danger';
    if (s === 'running') return 'info';
    return 'default';
  }

  // Job CRUD
  function resetForm() {
    jName = ''; jTargetType = 'local'; jLocalPath = './data/backups';
    jS3Endpoint = ''; jS3Bucket = ''; jS3AccessKey = ''; jS3SecretKey = ''; jS3Region = ''; jS3SSL = true;
    jSources = []; jSchedule = '0 3 * * *';
    jRetentionCount = 7; jRetentionDays = 0; jEncrypt = false; jEnabled = true;
    jPreHooks = []; jPostHooks = []; editing = null;
  }

  // The legacy single-modal job form stays in place (still functional)
  // but the new editorial wizard supersedes it as the primary flow.
  // openNew + openEdit + saveJob remain so the wizard's onsave can
  // delegate API calls back through the existing handlers.
  function openNew() { resetForm(); jSelectedTarget = 'inline'; cronMode = 'preset'; cronFreq = 'daily'; cronHour = 3; cronMinute = 0; loadAvailableResources(); wizardJobOpen = true; }

  // ─── Wizard state. Replaces the legacy `showJob` / `showTarget`
  // modals. `wizardJobEdit` / `wizardTargetEdit` carry the row being
  // edited (or null for new), so the wizard hydrates its initial state
  // from a single prop.
  let wizardJobOpen = $state(false);
  let wizardJobEdit = $state<BackupJob | null>(null);
  let wizardTargetOpen = $state(false);
  let wizardTargetEdit = $state<BackupTarget | null>(null);

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
      await loadJobs();
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

  // Route through the wizard for both new + edit. The legacy modal
  // form below is preserved but no longer reachable from the UI —
  // we'll delete it once the wizard is verified in production.
  function openEdit(j: BackupJob) {
    wizardJobEdit = j;
    wizardJobOpen = true;
  }

  // Legacy openEdit body kept for the form-state plumbing; called
  // nowhere now but keeps the form-state declarations valid.
  function _legacyOpenEdit(j: BackupJob) {
    editing = j; jSelectedTarget = 'inline'; cronMode = 'custom'; loadAvailableResources();
    jName = j.name; jTargetType = j.target_type as 'local' | 's3'; jEnabled = j.enabled; jEncrypt = j.encrypt;
    jSchedule = j.schedule; jRetentionCount = j.retention_count; jRetentionDays = j.retention_days;
    const cfg = j.target_config ?? {};
    if (j.target_type === 's3') {
      jS3Endpoint = cfg.endpoint ?? ''; jS3Bucket = cfg.bucket ?? '';
      jS3AccessKey = cfg.access_key ?? ''; jS3SecretKey = cfg.secret_key ?? '';
      jS3Region = cfg.region ?? ''; jS3SSL = cfg.use_ssl !== false;
    } else {
      jLocalPath = cfg.path ?? './data/backups';
    }
    // P.13.5: keep at most one source on edit. Older multi-source
    // jobs that pre-date the validation are reduced to their first
    // source on the way into the form so save round-trips don't
    // strip data the operator can still see in the dialog.
    jSources = j.sources.length > 0 ? [{ type: j.sources[0].type, name: j.sources[0].name }] : [];
    jPreHooks = (j.pre_hooks ?? []).map(h => ({ container: h.container, cmd: h.cmd.join(' ') }));
    jPostHooks = (j.post_hooks ?? []).map(h => ({ container: h.container, cmd: h.cmd.join(' ') }));
    showJob = true;
  }

  function duplicateJob(j: BackupJob) {
    openEdit(j);
    editing = null;
    jName = j.name + ' (copy)';
    jEnabled = false;
  }

  async function saveJob(e: Event) {
    e.preventDefault();
    const targetConfig = jTargetType === 's3'
      ? { endpoint: jS3Endpoint, bucket: jS3Bucket, access_key: jS3AccessKey, secret_key: jS3SecretKey, region: jS3Region, use_ssl: jS3SSL }
      : { path: jLocalPath };
    const sources: BackupSource[] = jSources.filter(s => s.name.trim()).map(s => ({ type: s.type as 'volume' | 'stack', name: s.name.trim() }));
    const preHooks: BackupHook[] = jPreHooks.filter(h => h.container && h.cmd).map(h => ({ container: h.container, cmd: h.cmd.split(/\s+/) }));
    const postHooks: BackupHook[] = jPostHooks.filter(h => h.container && h.cmd).map(h => ({ container: h.container, cmd: h.cmd.split(/\s+/) }));
    const payload: BackupJobInput = {
      name: jName, target_type: jTargetType, target_config: targetConfig,
      sources, schedule: jSchedule, retention_count: jRetentionCount,
      retention_days: jRetentionDays, encrypt: jEncrypt, pre_hooks: preHooks,
      post_hooks: postHooks, enabled: jEnabled
    };
    try {
      if (editing) { await api.backups.updateJob(editing.id, payload); toast.success('Updated', jName); }
      else { await api.backups.createJob(payload); toast.success('Created', jName); }
      showJob = false; await loadJobs();
    } catch (err) { toast.error('Save failed', err instanceof ApiError ? err.message : (err as Error).message); }
  }

  async function ackReview(j: BackupJob, mode: 'keep' | 'disable') {
    try {
      await api.backups.acknowledgeReview(j.id, mode);
      toast.success(mode === 'keep' ? 'Marked as reviewed' : 'Disabled', j.name);
      await loadJobs();
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : (err as Error).message);
    }
  }

  async function deleteJob(j: BackupJob) {
    if (!(await confirm.ask({ title: 'Delete backup job', message: `Delete backup job "${j.name}"?`, body: 'Existing backup runs are kept. The schedule is removed and no new runs will be triggered.', confirmLabel: 'Delete', danger: true }))) return;
    try { await api.backups.deleteJob(j.id); toast.success('Deleted'); await loadJobs(); } catch (err) { toast.error('Failed', err instanceof ApiError ? err.message : undefined); }
  }

  async function runJob(j: BackupJob) {
    if (!(await confirm.ask({ title: 'Run backup now', message: `Run "${j.name}" now?`, body: 'The backup starts in the background. Progress appears under the Runs tab.', confirmLabel: 'Run' }))) return;
    try { await api.backups.runJob(j.id); toast.success('Backup started'); await loadJobs(); } catch (err) { toast.error('Failed', err instanceof ApiError ? err.message : undefined); }
  }

  async function toggleJob(j: BackupJob) {
    try {
      const input: BackupJobInput = { name: j.name, target_type: j.target_type, target_config: j.target_config, sources: j.sources, schedule: j.schedule, retention_count: j.retention_count, retention_days: j.retention_days, encrypt: j.encrypt, pre_hooks: j.pre_hooks, post_hooks: j.post_hooks, enabled: !j.enabled };
      await api.backups.updateJob(j.id, input);
      await loadJobs();
    } catch (err) { toast.error('Failed', err instanceof ApiError ? err.message : undefined); }
  }

  function openRestore(r: BackupRun) {
    restoreRun = r; restoreConfirm = '';
    // Stack-typed runs default to "restore as same name". Volume runs
    // default to "<source>-restored" — the legacy non-destructive
    // pattern. The UI already shows the appropriate label per kind.
    const stackSrc = r.sources.find(s => s.type === 'stack');
    if (stackSrc) {
      restoreVolume = stackSrc.name;
    } else {
      const vol = r.sources.find(s => s.type === 'volume');
      restoreVolume = vol ? `${vol.name}-restored` : '';
    }
    showRestore = true;
  }
  async function doRestore() {
    if (!restoreRun || restoreConfirm !== restoreVolume) return;
    const isStack = restoreRun.sources.some(s => s.type === 'stack');
    try {
      if (isStack) {
        const res = await api.backups.restoreStack(restoreRun.id, restoreVolume.trim());
        toast.success('Stack restored', `${res.files_restored.length} file(s), ${res.volumes_restored.length} volume(s) into ${res.stack_name}`);
      } else {
        await api.backups.restore(restoreRun.id, restoreVolume.trim());
        toast.success('Restored', restoreVolume);
      }
      showRestore = false;
    } catch (err) {
      toast.error('Restore failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  function addSource() { jSources = [...jSources, { type: 'volume', name: '' }]; }
  function removeSource(i: number) { jSources = jSources.filter((_, idx) => idx !== i); }

  // Chip-picker helpers. P.13.5: exactly one source per job. The
  // server rejects multi-source payloads, so picking any chip
  // replaces the current selection rather than appending. Re-clicking
  // the active chip clears the selection (so submit stays disabled
  // until the user picks something fresh).
  function toggleSystemSource() {
    const has = jSources.some(s => s.type === 'system');
    if (has) {
      jSources = [];
    } else {
      jSources = [{ type: 'system', name: 'dockmesh' }];
    }
  }
  function toggleStackSource(name: string) {
    const has = jSources.some(s => s.type === 'stack' && s.name === name);
    if (has) {
      jSources = [];
    } else {
      jSources = [{ type: 'stack', name }];
    }
  }
  function toggleVolumeSource(name: string) {
    const has = jSources.some(s => s.type === 'volume' && s.name === name);
    if (has) {
      jSources = [];
    } else {
      jSources = [{ type: 'volume', name }];
    }
  }
  function addPreHook() { jPreHooks = [...jPreHooks, { container: '', cmd: '' }]; }
  function addPostHook() { jPostHooks = [...jPostHooks, { container: '', cmd: '' }]; }
</script>

<section class="bk-page">
  <!-- ─── Header ─── -->
  <header class="bk-header">
    <div class="bk-header-text">
      <h1 class="ed-title bk-title">Backups</h1>
      <p class="bk-subtitle">
        {activeJobs} job{activeJobs === 1 ? '' : 's'} · {bTargets.length} target{bTargets.length === 1 ? '' : 's'}{#if recentFailed > 0}
          · <span class="bk-subtitle-fail">{recentFailed} failed in last 24h</span>
        {:else if recentRuns.length > 0}
          · {recentSuccess} recent run{recentSuccess === 1 ? '' : 's'} healthy
        {/if}
      </p>
    </div>
  </header>

  <!-- ─── Tabs ─── -->
  <div class="ed-tabs bk-tabs">
    <button type="button" class="ed-tab" class:active={tab === 'jobs'} onclick={() => (tab = 'jobs')}>
      <Archive size={13} strokeWidth={1.5} /> Jobs <span class="count">{jobs.length}</span>
    </button>
    <button type="button" class="ed-tab" class:active={tab === 'runs'} onclick={() => (tab = 'runs')}>
      <Clock size={13} strokeWidth={1.5} /> Runs <span class="count">{runs.length}</span>
    </button>
    <button type="button" class="ed-tab" class:active={tab === 'targets'} onclick={() => (tab = 'targets')}>
      <HardDrive size={13} strokeWidth={1.5} /> Targets <span class="count">{bTargets.length}</span>
    </button>
  </div>

  <!-- ===== JOBS TAB ===== -->
  {#if tab === 'jobs'}
    <!-- Per-tab head: Eyebrow + subtitle on the left, actions on the
         right (mockup pattern from JobsTab). Replaces the global
         page-header actions so each tab owns its own context. -->
    <div class="bk-tab-head">
      <div class="bk-tab-head-text">
        <Eyebrow>{jobs.length} jobs · {jobs.filter((j) => j.enabled).length} enabled</Eyebrow>
        <p class="bk-tab-subtitle">
          One job per source. Each job runs on a schedule, dumps to a target, and rotates by retention rules.
        </p>
      </div>
      <div class="ed-actions">
        <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={() => runs.filter((r) => r.status === 'running').length === 0 ? jobs.filter((j) => j.enabled).forEach((j) => api.backups.runJob(j.id)) : null}>
          <RefreshCw size={12} strokeWidth={1.5} /> Run all due
        </button>
        <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" onclick={openNew}>
          <Plus size={13} strokeWidth={1.5} /> New backup job
        </button>
      </div>
    </div>

    {#if loading && jobs.length === 0}
      <div class="dm-card bk-card-pad"><Skeleton width="70%" height="6rem" /></div>
    {:else if jobs.length === 0}
      <div class="dm-card bk-card-pad bk-empty">
        <Eyebrow>No backups configured</Eyebrow>
        <h3 class="bk-empty-title">Dockmesh doesn't back up anything by default</h3>
        <p class="bk-empty-body">
          Pick what to protect. Create a job to snapshot stacks, volumes, or the dockmesh server itself
          on a schedule, to a target of your choice. The default-everything-to-local-disk job older
          versions installed automatically is gone — local-only backups don't survive a host failure.
        </p>
        <p class="bk-empty-hint font-mono">
          starting point: daily <span class="ed-accent">dockmesh-system</span> backup to an off-host target (SFTP / S3) + per-stack volume backups for anything stateful.
        </p>
        <button type="button" class="dm-btn dm-btn-primary dm-btn-sm bk-empty-cta" onclick={openNew}>
          <Plus size={13} strokeWidth={1.5} /> New backup job
        </button>
      </div>
    {:else}
      {@const reviewJobs = jobs.filter((j) => j.needs_review)}
      {#if reviewJobs.length > 0}
        <div class="bk-review-banner">
          <AlertTriangle size={16} strokeWidth={1.5} class="bk-review-icon" />
          <div class="bk-review-text">
            <div class="bk-review-title">
              {reviewJobs.length} auto-created backup job{reviewJobs.length === 1 ? '' : 's'} need{reviewJobs.length === 1 ? 's' : ''} your review
            </div>
            <div class="bk-review-body">
              Earlier dockmesh versions silently created a daily local backup. The default changed in v0.3 — backups are now opt-in.
              <strong>Keep</strong> leaves the job running. <strong>Disable</strong> stops the schedule but keeps the history.
            </div>
          </div>
        </div>
        <div class="bk-review-list">
          {#each reviewJobs as j (j.id)}
            <div class="bk-review-row">
              <div class="bk-review-row-text">
                <div class="font-mono bk-review-row-name">{j.name}</div>
                <div class="bk-review-row-reason">{j.review_reason}</div>
              </div>
              <div class="ed-actions">
                <button type="button" class="dm-btn dm-btn-secondary dm-btn-sm" onclick={() => ackReview(j, 'keep')}>Keep</button>
                <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm bk-danger" onclick={() => ackReview(j, 'disable')}>Disable</button>
              </div>
            </div>
          {/each}
        </div>
      {/if}

      <div class="bk-job-grid">
        {#each sortedJobs as j (j.id)}
          {@const SrcIcon = jobSourceIcon(j)}
          {@const target = targetById(j.target_type === 'inline' ? -1 : -1)}
          {@const tgtMeta = targetTypeMeta(j.target_type)}
          {@const last = jobLastRun(j.name)}
          {@const status = jobStatusFor(j.name)}
          {@const jrRuns = runsForJob(j.name)}
          {@const failed7 = jrRuns.filter((r) => r.status === 'failed').length}
          {@const spark = jobSpark(j.name)}
          {@const hookLabels = [...(j.pre_hooks ?? []), ...(j.post_hooks ?? [])].map(hookLabel)}
          {@const sparkColor = status === 'failed' ? 'var(--color-danger-400)' : status === 'warn' ? 'var(--color-warning-400)' : 'var(--color-success-400)'}
          <article class="bk-job-card" class:bk-job-card-disabled={!j.enabled}>
            <header class="bk-job-card-head">
              <button type="button" class="bk-job-card-head-text bk-job-card-head-link" onclick={() => openJobDrawer(j)}>
                <span class={statusDot(status)}></span>
                <h3 class="bk-job-card-title font-mono" title={j.name}>{j.name}</h3>
              </button>
              <div class="bk-job-card-actions">
                {#if !j.enabled}<span class="dm-pill dm-pill-neutral bk-mini-pill">disabled</span>{/if}
                <button type="button" class="dm-btn dm-btn-ghost dm-btn-xs bk-icon-btn" onclick={(e) => { e.stopPropagation(); runJob(j); }} title="Run now" disabled={!j.enabled}>
                  <Play size={11} strokeWidth={1.5} />
                </button>
                <button type="button" class="dm-btn dm-btn-ghost dm-btn-xs bk-icon-btn" onclick={() => openJobDrawer(j)} title="Open detail">
                  <ArrowRight size={12} strokeWidth={1.5} />
                </button>
              </div>
            </header>

            <!-- Source line — icon + headline label, then mono meta on
                 the indented next line (mockup matches with paddingLeft
                 to align under the icon). -->
            <div class="bk-job-card-src">
              <SrcIcon size={12} strokeWidth={1.5} />
              <span class="bk-job-card-src-label">{jobSourceLabel(j)}</span>
            </div>
            <div class="font-mono bk-job-card-src-meta">{jobSourceMeta(j)}</div>

            <!-- Target row — has its own surface bg + border-subtle so
                 it visually nests inside the card (mockup pattern). -->
            <div class="bk-job-card-target">
              <div class="bk-job-card-target-text">
                <span class="bk-target-glyph">{tgtMeta.glyph}</span>
                <div class="bk-job-card-target-info">
                  <span class="bk-job-card-target-name">{j.target_type === 'inline' ? 'inline target' : tgtMeta.label}</span>
                  <span class="font-mono bk-job-card-target-meta">{j.target_type}</span>
                </div>
              </div>
              {#if j.encrypt}
                <span class="dm-pill bk-mini-pill bk-pill-encrypt"><Lock size={9} strokeWidth={1.5} /> age</span>
              {/if}
            </div>

            <!-- Stats row — 3 columns: Last (left) / Sparkline (center)
                 / Next (right). Sparkline is a tiny 80×22 SVG of the
                 last 7 success-sizes (failures become 0 dips). -->
            <div class="bk-job-card-stats">
              <div class="bk-job-stat">
                <span class="bk-job-stat-label">Last</span>
                <span class="bk-job-stat-value" class:fail={status === 'failed'} class:warn={status === 'warn'} class:ok={status === 'ok'}>
                  {#if status === 'failed'}failed
                  {:else if status === 'running'}running
                  {:else if last}{fmtBytes(last.size_bytes)}
                  {:else}—{/if}
                </span>
                <span class="font-mono bk-job-stat-meta">
                  {jrRuns.length} run{jrRuns.length === 1 ? '' : 's'}{failed7 > 0 ? ` · ${failed7} failed` : ''}
                </span>
              </div>
              <div class="bk-job-spark">
                {#if spark.length >= 2}
                  <svg width="80" height="22" viewBox="0 0 80 22" aria-hidden="true">
                    <polyline points={sparkPoints(spark)} fill="none" stroke={sparkColor} stroke-width="1.2" stroke-linejoin="round" stroke-linecap="round" />
                  </svg>
                {/if}
              </div>
              <div class="bk-job-stat bk-job-stat-right">
                <span class="bk-job-stat-label">Next</span>
                <span class="bk-job-stat-value">{j.next_run_at ? fmtUntil(j.next_run_at) : 'soon'}</span>
                <span class="font-mono bk-job-stat-meta">{cronHuman(j.schedule)}</span>
              </div>
            </div>

            <footer class="bk-job-card-foot">
              <span class="font-mono bk-job-card-foot-meta">
                retention: keep {j.retention_count}{j.retention_days > 0 ? ` · ${j.retention_days}d` : ''}
              </span>
              {#if hookLabels.length > 0}
                <span class="font-mono bk-job-card-foot-meta">
                  · {hookLabels.length} hook{hookLabels.length === 1 ? '' : 's'}: {hookLabels.join(', ')}
                </span>
              {/if}
              <span class="bk-job-card-foot-spacer"></span>
              <label class="bk-job-card-toggle font-mono">
                <input type="checkbox" checked={j.enabled} onchange={() => toggleJob(j)} />
                enabled
              </label>
            </footer>

            {#if last?.error}
              <div class="bk-job-card-error">
                <AlertTriangle size={11} strokeWidth={1.5} /> {last.error}
              </div>
            {/if}
          </article>
        {/each}
      </div>
    {/if}

  <!-- ===== RUNS TAB ===== -->
  {:else if tab === 'runs'}
    <!-- Header row: Eyebrow + subtitle on the left, Timeline/List
         segmented toggle on the right (mockup pattern from RunsTab). -->
    <div class="bk-runs-head">
      <div class="bk-runs-head-text">
        <Eyebrow>{filteredRuns.length} runs · last 7 days</Eyebrow>
        <p class="bk-runs-subtitle">
          Every backup run is recorded, even the failed ones. Click any block to inspect logs and restore.
        </p>
      </div>
      <div class="ed-actions">
        <div class="bk-seg-radio">
          <button type="button" class="bk-seg-radio-item" class:active={runsView === 'timeline'} onclick={() => (runsView = 'timeline')}>Timeline</button>
          <button type="button" class="bk-seg-radio-item" class:active={runsView === 'list'} onclick={() => (runsView = 'list')}>List</button>
        </div>
      </div>
    </div>

    <!-- Filter row (job + status + count). Stays under the header so
         filters apply to both views identically. -->
    <div class="bk-runs-toolbar">
      <select class="bk-runs-select" bind:value={runJobFilter}>
        <option value="">All jobs</option>
        {#each jobs as j (j.id)}<option value={j.name}>{j.name}</option>{/each}
      </select>
      <div class="bk-runs-pills">
        {#each [['all', 'All'], ['success', 'Success'], ['failed', 'Failed'], ['running', 'Running']] as [key, label] (key)}
          <button type="button" class="bk-runs-pill" class:active={runStatusFilter === key} onclick={() => (runStatusFilter = key as typeof runStatusFilter)}>
            {label}
          </button>
        {/each}
      </div>
      <span class="bk-runs-count font-mono">{filteredRuns.length} run{filteredRuns.length === 1 ? '' : 's'}</span>
    </div>

    {#if loading && runs.length === 0}
      <div class="dm-card bk-card-pad"><Skeleton width="70%" height="6rem" /></div>
    {:else if runs.length === 0}
      <div class="dm-card bk-card-pad bk-empty">
        <Eyebrow>No runs yet</Eyebrow>
        <p class="bk-empty-body">Trigger a backup job or wait for its schedule. Every run is recorded — even the failed ones — so you can audit later.</p>
      </div>
    {:else if filteredRuns.length === 0}
      <div class="dm-card bk-card-pad bk-empty">
        <p class="bk-empty-body">No runs match this filter.</p>
      </div>
    {:else if runsView === 'timeline'}
      <!-- Timeline view (default). Group runs by day, render each as
           a colored block with status-driven left-border. -->
      {@const groups = (() => {
        const map = new Map<string, typeof filteredRuns>();
        for (const r of filteredRuns) {
          const k = dayKey(r.started_at);
          if (!map.has(k)) map.set(k, []);
          map.get(k)!.push(r);
        }
        return [...map.entries()];
      })()}
      <div class="bk-timeline">
        {#each groups as [day, items] (day)}
          {@const okCount = items.filter((r) => r.status === 'success').length}
          {@const failCount = items.filter((r) => r.status === 'failed').length}
          <div class="bk-timeline-day">
            <div class="bk-timeline-day-head">
              <span class="bk-timeline-day-label">{dayHeading(day)}</span>
              <span class="bk-timeline-day-meta font-mono">
                {items.length} run{items.length === 1 ? '' : 's'} · {okCount} ok{failCount > 0 ? ` · ${failCount} failed` : ''}
              </span>
              <span class="bk-timeline-day-rule"></span>
            </div>
            <div class="bk-timeline-row">
              {#each items as r (r.id)}
                {@const tone = r.status === 'success' ? 'ok' : r.status === 'failed' ? 'failed' : r.status === 'running' ? 'running' : 'warn'}
                <button type="button" class="bk-timeline-block bk-timeline-block-{tone}" onclick={() => openRunDrawer(r)}>
                  <span class="font-mono bk-timeline-time">{new Date(r.started_at).toTimeString().slice(0, 5)}</span>
                  <span class="bk-timeline-job font-mono">{r.job_name}</span>
                  <span class="font-mono bk-timeline-meta">
                    {r.status === 'failed' ? 'failed' : `${fmtBytes(r.size_bytes)} · ${fmtDuration(r.started_at, r.finished_at)}`}
                  </span>
                  <span class="font-mono bk-timeline-target">→ {r.target_path?.split('/').pop() || 'target'}</span>
                  {#if r.error}<span class="font-mono bk-timeline-error">{r.error}</span>{/if}
                </button>
              {/each}
            </div>
          </div>
        {/each}
      </div>
    {:else}
      <div class="bk-runs-table">
        <div class="bk-runs-row bk-runs-row-head">
          <span>job</span>
          <span>status</span>
          <span>started</span>
          <span class="right">duration</span>
          <span class="right">size</span>
          <span></span>
        </div>
        {#each filteredRuns as r (r.id)}
          <button type="button" class="bk-runs-row bk-runs-row-link" class:bk-runs-row-failed={r.status === 'failed'} onclick={() => openRunDrawer(r)}>
            <div class="bk-runs-name">
              <span class="font-mono">{r.job_name}</span>
              {#if r.encrypted}<Lock size={10} strokeWidth={1.5} class="bk-runs-lock" />{/if}
            </div>
            <span>
              {#if r.status === 'success'}
                <span class="dm-pill dm-pill-success bk-mini-pill"><span class="dm-pill-dot"></span> success</span>
              {:else if r.status === 'failed'}
                <span class="dm-pill dm-pill-danger bk-mini-pill"><span class="dm-pill-dot"></span> failed</span>
              {:else if r.status === 'running'}
                <span class="dm-pill bk-mini-pill"><span class="dm-pill-dot"></span> running</span>
              {:else}
                <span class="dm-pill dm-pill-neutral bk-mini-pill">{r.status}</span>
              {/if}
            </span>
            <span class="font-mono bk-runs-cell">{fmtTime(r.started_at)}</span>
            <span class="font-mono bk-runs-cell right">{fmtDuration(r.started_at, r.finished_at)}</span>
            <span class="font-mono bk-runs-cell right">{fmtBytes(r.size_bytes)}</span>
            <span class="bk-runs-actions">
              {#if r.status === 'success'}
                <button type="button" class="dm-btn dm-btn-ghost dm-btn-xs" title="Restore" onclick={(e) => { e.stopPropagation(); openRestore(r); }}>
                  <Undo2 size={11} strokeWidth={1.5} />
                </button>
              {/if}
            </span>
          </button>
          {#if r.error}
            <div class="bk-runs-error font-mono">
              <AlertTriangle size={11} strokeWidth={1.5} /> {r.error}
            </div>
          {/if}
        {/each}
      </div>
    {/if}

  <!-- ===== TARGETS TAB ===== -->
  {:else if tab === 'targets'}
    {@const totalUsed = bTargets.reduce((s, t) => s + (t.used_bytes ?? 0), 0)}
    <div class="bk-tab-head">
      <div class="bk-tab-head-text">
        <Eyebrow>{bTargets.length} targets · {fmtBytes(totalUsed)} used</Eyebrow>
        <p class="bk-tab-subtitle">
          Where backups are written. Multiple jobs can share a target. Free-space is checked before each run.
        </p>
      </div>
      <div class="ed-actions">
        <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" onclick={openNewTarget}>
          <Plus size={13} strokeWidth={1.5} /> New target
        </button>
      </div>
    </div>

    {#if targetsLoading && bTargets.length === 0}
      <div class="dm-card bk-card-pad"><Skeleton width="70%" height="6rem" /></div>
    {:else if bTargets.length === 0}
      <div class="dm-card bk-card-pad bk-empty">
        <Eyebrow>No targets</Eyebrow>
        <h3 class="bk-empty-title">Where should backups go?</h3>
        <p class="bk-empty-body">
          Configure a storage destination — local directory, NAS / SMB share, SFTP, S3-compatible object store,
          or WebDAV (Nextcloud). Multiple jobs can share a target.
        </p>
        <button type="button" class="dm-btn dm-btn-primary dm-btn-sm bk-empty-cta" onclick={openNewTarget}>
          <Plus size={13} strokeWidth={1.5} /> New target
        </button>
      </div>
    {:else}
      <div class="bk-target-grid">
        {#each sortedTargets as t (t.id)}
          {@const meta = targetTypeMeta(t.type)}
          {@const usedPct = t.total_bytes > 0 ? (t.used_bytes / t.total_bytes) * 100 : 0}
          {@const jobsOnTarget = jobs.length > 0 ? [] : []}
          <article class="bk-target-card" class:bk-target-card-warn={usedPct > 80}>
            <header class="bk-target-card-head">
              <span class="bk-target-glyph bk-target-glyph-lg">{meta.glyph}</span>
              <button type="button" class="bk-target-card-head-text bk-target-card-head-link" onclick={() => openTargetDrawer(t)}>
                <h3 class="bk-target-card-name font-mono" title={t.name}>{t.name}</h3>
                <span class="font-mono bk-target-card-type">{meta.label}</span>
              </button>
              {#if t.status === 'connected'}
                <span class="dm-pill dm-pill-success bk-mini-pill"><span class="dm-pill-dot"></span> connected</span>
              {:else if t.status === 'error'}
                <span class="dm-pill dm-pill-danger bk-mini-pill"><span class="dm-pill-dot"></span> error</span>
              {:else}
                <span class="dm-pill dm-pill-neutral bk-mini-pill">unknown</span>
              {/if}
            </header>

            <!-- Storage bar -->
            <div class="bk-target-storage">
              <div class="bk-target-storage-head">
                <span class="bk-target-storage-label">Used</span>
                {#if t.total_bytes > 0}
                  <span class="font-mono bk-target-storage-num">
                    {fmtBytes(t.used_bytes)} <span class="bk-target-storage-num-total">/ {fmtBytes(t.total_bytes)}</span>
                  </span>
                {:else}
                  <span class="font-mono bk-target-storage-num bk-target-storage-num-total">—</span>
                {/if}
              </div>
              <div class="bk-target-storage-bar">
                <span class="bk-target-storage-bar-fill" style="width: {usedPct}%"></span>
              </div>
              <span class="font-mono bk-target-storage-meta">
                {#if t.total_bytes > 0}{usedPct.toFixed(1)}% · {fmtBytes(t.free_bytes)} free{:else}storage size unknown — test connection to discover{/if}
              </span>
            </div>

            <footer class="bk-target-card-foot">
              <span class="font-mono bk-target-card-foot-meta">
                tested {fmtTime(t.last_checked_at)}
              </span>
              <span class="bk-target-card-foot-spacer"></span>
              <button type="button" class="dm-btn dm-btn-ghost dm-btn-xs" onclick={() => openTargetDrawer(t)} title="Open detail">
                Detail
              </button>
              <button type="button" class="dm-btn dm-btn-ghost dm-btn-xs" onclick={() => testTarget(t)} title="Test connection">
                <RefreshCw size={11} strokeWidth={1.5} /> Test
              </button>
            </footer>
          </article>
        {/each}
      </div>
    {/if}
  {/if}
</section>

<!-- ─── Drawers ─── -->
{#if drawerJob}
  <JobDrawer
    job={drawerJob}
    runs={runs}
    onclose={closeDrawers}
    onedit={(j) => { closeDrawers(); openEdit(j); }}
    ondelete={async (j) => { closeDrawers(); await deleteJob(j); }}
    onrun={async (j) => { await runJob(j); }}
    ontoggle={async (j) => { await toggleJob(j); }}
    onopenrun={jumpJobToRun}
  />
{/if}

{#if drawerRun}
  <RunDrawer
    run={drawerRun}
    job={jobs.find((j) => j.name === drawerRun.job_name)}
    target={undefined}
    onclose={closeDrawers}
    onrestore={restoreRunFromDrawer}
  />
{/if}

{#if drawerTarget}
  <TargetDrawer
    target={drawerTarget}
    jobs={jobs}
    onclose={closeDrawers}
    onedit={(t) => { closeDrawers(); openEditTarget(t); }}
    ondelete={async (t) => { closeDrawers(); await deleteTarget(t); }}
    ontest={async (t) => { await testTarget(t); }}
  />
{/if}

<!-- ─── Wizards ─── -->
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

<!-- Target modal -->
<Modal bind:open={showTarget} title={editingTarget ? `Edit: ${editingTarget.name}` : 'New backup target'} maxWidth="max-w-lg">
  <form onsubmit={saveTarget} class="space-y-4" id="target-form">
    <Input label="Name" bind:value={tName} placeholder="My NAS" />

    <!-- Type selector as visual cards -->
    <div>
      <div class="text-xs font-medium text-[var(--fg-muted)] mb-2">Type</div>
      <div class="grid grid-cols-2 sm:grid-cols-3 gap-2">
        {#each targetTypes as tt}
          <button type="button"
            class="p-3 rounded-lg border text-left transition-colors {tType === tt.value
              ? 'border-[var(--color-brand-500)] bg-[color-mix(in_srgb,var(--color-brand-500)_8%,transparent)]'
              : 'border-[var(--border)] hover:border-[var(--color-brand-500)]'}"
            onclick={() => { tType = tt.value as any; tConfig = {}; }}
          >
            <div class="text-lg mb-0.5">{tt.icon}</div>
            <div class="text-xs font-medium">{tt.label}</div>
          </button>
        {/each}
      </div>
    </div>

    <!-- Type-specific fields -->
    {#if activeTargetType}
      <fieldset class="space-y-3">
        <legend class="text-xs font-medium text-[var(--fg-muted)] uppercase tracking-wider mb-1">{activeTargetType.label} Configuration</legend>
        {#each activeTargetType.fields as f}
          {#if f.key === 'share' && tType === 'smb'}
            <!-- SMB share: discover button + dropdown -->
            <div>
              <label for="t-share" class="block text-xs font-medium text-[var(--fg-muted)] mb-1">Share</label>
              <div class="flex gap-2">
                {#if smbShares.length > 0}
                  <select id="t-share" class="dm-input text-sm font-mono flex-1" value={tConfig.share ?? ''}
                    onchange={(e) => { tConfig = { ...tConfig, share: (e.target as HTMLSelectElement).value }; }}>
                    <option value="">Select a share…</option>
                    {#each smbShares as s}<option value={s}>{s}</option>{/each}
                  </select>
                {:else}
                  <input id="t-share" type="text" class="dm-input text-sm font-mono flex-1" placeholder="backups"
                    value={tConfig.share ?? ''} oninput={(e) => { tConfig = { ...tConfig, share: (e.target as HTMLInputElement).value }; }} />
                {/if}
                <Button variant="secondary" size="sm" loading={smbDiscovering} onclick={discoverShares}
                  disabled={!tConfig.host}>
                  Discover
                </Button>
              </div>
            </div>
          {:else}
            <div>
              <label for="t-{f.key}" class="block text-xs font-medium text-[var(--fg-muted)] mb-1">{f.label}</label>
              <input
                id="t-{f.key}"
                type={f.key === 'password' || f.key === 'secret_key' ? 'password' : 'text'}
                class="dm-input text-sm font-mono"
                placeholder={f.placeholder}
                value={tConfig[f.key] ?? ''}
                oninput={(e) => { tConfig = { ...tConfig, [f.key]: (e.target as HTMLInputElement).value }; }}
              />
            </div>
          {/if}
        {/each}
      </fieldset>
    {/if}

    <!-- Test Connection -->
    <div class="flex items-center gap-3">
      <Button variant="secondary" size="sm" loading={testBusy} onclick={testConfigInDialog} disabled={testBusy}>
        Test connection
      </Button>
      {#if testResult}
        {#if testResult.status === 'connected'}
          <span class="text-xs text-[var(--color-success-400)] flex items-center gap-1">
            ✓ Connected
            {#if testResult.total_bytes && testResult.total_bytes > 0}
              — {fmtBytes(testResult.free_bytes ?? 0)} free / {fmtBytes(testResult.total_bytes)}
            {/if}
          </span>
        {:else}
          <span class="text-xs text-[var(--color-danger-400)]">✗ {testResult.error ?? 'Failed'}</span>
        {/if}
      {/if}
    </div>
  </form>
  {#snippet footer()}
    <Button variant="secondary" onclick={() => (showTarget = false)}>Cancel</Button>
    <Button variant="primary" type="submit" form="target-form" loading={tSaving} disabled={tSaving || !tName.trim()}>
      {editingTarget ? 'Save' : 'Create'}
    </Button>
  {/snippet}
</Modal>

<!-- ===== JOB MODAL (simplified, structured) ===== -->
<Modal bind:open={showJob} title={editing ? `Edit: ${editing.name}` : 'New backup job'} maxWidth="max-w-2xl" onclose={resetForm}>
  <form onsubmit={saveJob} class="space-y-5" id="backup-form">
    <!-- Basics -->
    <fieldset class="space-y-3">
      <legend class="text-xs font-medium text-[var(--fg-muted)] uppercase tracking-wider">Basics</legend>
      <div class="grid grid-cols-2 gap-3">
        <Input label="Job name" bind:value={jName} placeholder="Daily stack backup" />
        <div>
          <label class="flex items-center gap-2 text-sm mt-6 cursor-pointer">
            <input type="checkbox" bind:checked={jEnabled} class="accent-[var(--color-brand-500)]" /> Enabled
          </label>
        </div>
      </div>
    </fieldset>

    <!-- Sources — chip-picker. P.13.5: exactly one source per job.
         Picking any chip replaces the current selection rather than
         adding to it. Operators who want to back up several stacks /
         volumes / the whole system create one job per source — keeps
         retention, schedule and restore semantics 1:1 with the job
         entry the operator sees in the list. -->
    <fieldset class="space-y-3">
      <legend class="text-xs font-medium text-[var(--fg-muted)] uppercase tracking-wider">What to back up</legend>
      <p class="text-xs text-[var(--fg-muted)]">
        Pick one — the whole dockmesh server, a single stack, or a single docker volume. For multiple things, create one job per source.
      </p>

      <!-- Full-server toggle (one click covers DB + stacks dir + data/). -->
      <button
        type="button"
        class="w-full flex items-center justify-between px-3 py-2.5 rounded-lg border transition-colors
          {jSources.some(s => s.type === 'system')
            ? 'border-[var(--color-brand-500)] bg-[color-mix(in_srgb,var(--color-brand-500)_8%,transparent)] text-[var(--color-brand-300)]'
            : 'border-[var(--border)] bg-[var(--surface)] text-[var(--fg-muted)] hover:border-[var(--border-strong)]'}"
        onclick={toggleSystemSource}
      >
        <span class="flex items-center gap-2.5">
          <span class="w-4 h-4 rounded flex items-center justify-center {jSources.some(s => s.type === 'system') ? 'bg-[var(--color-brand-500)] text-white' : 'border border-[var(--border)]'}">
            {#if jSources.some(s => s.type === 'system')}
              <svg class="w-3 h-3" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12"/></svg>
            {/if}
          </span>
          <span class="text-sm font-medium">Full dockmesh system</span>
        </span>
        <span class="text-[10px] font-mono text-[var(--fg-subtle)]">DB · stacks/ · data/</span>
      </button>

      <!-- Stacks chip grid — the happy-path use case. -->
      {#if availableStacks.length > 0}
        <div>
          <div class="flex items-center justify-between mb-1.5">
            <span class="block text-xs text-[var(--fg-muted)]">Stacks</span>
            <span class="text-[10px] text-[var(--fg-subtle)]">{jSources.filter(s => s.type === 'stack').length} selected</span>
          </div>
          <div class="flex flex-wrap gap-1.5">
            {#each availableStacks as stackName}
              {@const on = jSources.some(s => s.type === 'stack' && s.name === stackName)}
              <button
                type="button"
                class="px-2.5 py-1 rounded-md text-[11px] font-mono border transition-colors
                  {on
                    ? 'bg-[color-mix(in_srgb,var(--color-brand-500)_12%,transparent)] border-[var(--color-brand-500)]/40 text-[var(--color-brand-300)]'
                    : 'bg-[var(--surface)] border-[var(--border)] text-[var(--fg-muted)] hover:border-[var(--border-strong)] hover:text-[var(--fg)]'}"
                onclick={() => toggleStackSource(stackName)}
              >
                {on ? '✓ ' : ''}{stackName}
              </button>
            {/each}
          </div>
        </div>
      {/if}

      <!-- Volumes chip grid — same pattern, collapsed by default until
           needed since most users only target stacks/system. -->
      {#if availableVolumes.length > 0}
        <details class="group">
          <summary class="cursor-pointer text-xs text-[var(--fg-muted)] hover:text-[var(--fg)] select-none flex items-center gap-1.5">
            <svg class="w-3 h-3 transition-transform group-open:rotate-90" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="9 18 15 12 9 6"/></svg>
            Volumes
            <span class="text-[10px] text-[var(--fg-subtle)]">({jSources.filter(s => s.type === 'volume').length} of {availableVolumes.length})</span>
          </summary>
          <div class="mt-2 flex flex-wrap gap-1.5">
            {#each availableVolumes as volName}
              {@const on = jSources.some(s => s.type === 'volume' && s.name === volName)}
              <button
                type="button"
                class="px-2.5 py-1 rounded-md text-[11px] font-mono border transition-colors
                  {on
                    ? 'bg-[color-mix(in_srgb,var(--color-brand-500)_12%,transparent)] border-[var(--color-brand-500)]/40 text-[var(--color-brand-300)]'
                    : 'bg-[var(--surface)] border-[var(--border)] text-[var(--fg-muted)] hover:border-[var(--border-strong)] hover:text-[var(--fg)]'}"
                onclick={() => toggleVolumeSource(volName)}
              >
                {on ? '✓ ' : ''}{volName}
              </button>
            {/each}
          </div>
        </details>
      {/if}

      {#if jSources.length === 0}
        <p class="text-xs text-[var(--color-warning-400)]">Pick at least one source above.</p>
      {/if}
    </fieldset>

    <!-- Target -->
    <fieldset class="space-y-3">
      <legend class="text-xs font-medium text-[var(--fg-muted)] uppercase tracking-wider">Where to store</legend>
      <div>
        <label for="job-target" class="block text-xs text-[var(--fg-muted)] mb-1">Target</label>
        <select id="job-target" class="dm-input text-sm" bind:value={jSelectedTarget}>
          <option value="inline">Configure inline…</option>
          {#each bTargets as bt}
            <option value="t:{bt.id}">{bt.name} ({bt.type.toUpperCase()}){bt.status === 'connected' ? ' ✓' : ''}</option>
          {/each}
        </select>
      </div>
      <!-- Preview card for the picked pre-configured target. Mirrors
           the deep-dive marketing mock: type-badge + name + live
           connected state so the admin sees what they're wiring up
           without re-clicking the dropdown or switching tabs. -->
      {#if jSelectedTarget.startsWith('t:')}
        {@const selected = bTargets.find(t => `t:${t.id}` === jSelectedTarget)}
        {#if selected}
          <div class="flex items-center gap-2.5 px-3 py-2.5 rounded-lg border border-[var(--border)] bg-[var(--surface)]">
            <span class="px-1.5 py-0.5 rounded bg-[var(--surface-hover)] text-[9px] uppercase text-[var(--fg-muted)] font-medium">{selected.type}</span>
            <span class="text-sm font-mono text-[var(--fg)] truncate">{selected.name}</span>
            <span class="ml-auto text-[10px] flex items-center gap-1.5 shrink-0
              {selected.status === 'connected' ? 'text-[var(--color-success-400)]' : 'text-[var(--color-warning-400)]'}">
              <span class="w-1.5 h-1.5 rounded-full
                {selected.status === 'connected' ? 'bg-[var(--color-success-500)]' : 'bg-[var(--color-warning-500)]'}"></span>
              {selected.status ?? 'unknown'}
            </span>
          </div>
        {/if}
      {/if}
      {#if jSelectedTarget === 'inline'}
        <div>
          <label for="inline-target-type" class="block text-xs text-[var(--fg-muted)] mb-1">Type</label>
          <select id="inline-target-type" class="dm-input text-sm" bind:value={jTargetType}>
            <option value="local">Local directory</option>
            <option value="s3">S3 / MinIO / Wasabi</option>
          </select>
        </div>
        {#if jTargetType === 'local'}
          <Input label="Path" bind:value={jLocalPath} placeholder="./data/backups" />
        {:else if jTargetType === 's3'}
          <div class="grid grid-cols-2 gap-3">
            <Input label="Endpoint" bind:value={jS3Endpoint} placeholder="s3.amazonaws.com" />
            <Input label="Bucket" bind:value={jS3Bucket} placeholder="my-backups" />
            <Input label="Access Key" bind:value={jS3AccessKey} />
            <Input label="Secret Key" type="password" bind:value={jS3SecretKey} />
          </div>
        {/if}
      {:else}
        <p class="text-xs text-[var(--fg-subtle)]">Using pre-configured target. Manage targets in the Targets tab.</p>
      {/if}
      <label class="flex items-center gap-2 text-sm cursor-pointer">
        <input type="checkbox" bind:checked={jEncrypt} class="accent-[var(--color-brand-500)]" />
        Encrypt with age
      </label>
    </fieldset>

    <!-- Schedule -->
    <fieldset class="space-y-3">
      <legend class="text-xs font-medium text-[var(--fg-muted)] uppercase tracking-wider">Schedule</legend>
      <div class="flex gap-2 text-xs mb-1">
        <button type="button" class="px-2.5 py-1 rounded border transition-colors {cronMode === 'preset' ? 'border-[var(--color-brand-500)] text-[var(--color-brand-400)]' : 'border-[var(--border)] text-[var(--fg-muted)]'}" onclick={() => (cronMode = 'preset')}>Visual</button>
        <button type="button" class="px-2.5 py-1 rounded border transition-colors {cronMode === 'custom' ? 'border-[var(--color-brand-500)] text-[var(--color-brand-400)]' : 'border-[var(--border)] text-[var(--fg-muted)]'}" onclick={() => (cronMode = 'custom')}>Custom cron</button>
      </div>
      {#if cronMode === 'preset'}
        <div class="grid grid-cols-2 sm:grid-cols-4 gap-3">
          <div>
            <label for="cron-freq" class="block text-xs text-[var(--fg-muted)] mb-1">Frequency</label>
            <select id="cron-freq" class="dm-input text-sm" bind:value={cronFreq}>
              <option value="hourly">Every hour</option>
              <option value="daily">Daily</option>
              <option value="weekly">Weekly</option>
              <option value="monthly">Monthly (1st)</option>
            </select>
          </div>
          {#if cronFreq !== 'hourly'}
            <div>
              <label for="cron-hour" class="block text-xs text-[var(--fg-muted)] mb-1">Hour</label>
              <select id="cron-hour" class="dm-input text-sm" bind:value={cronHour}>
                {#each Array(24) as _, h}<option value={h}>{String(h).padStart(2, '0')}:00</option>{/each}
              </select>
            </div>
          {/if}
          <div>
            <label for="cron-min" class="block text-xs text-[var(--fg-muted)] mb-1">Minute</label>
            <select id="cron-min" class="dm-input text-sm" bind:value={cronMinute}>
              {#each [0, 5, 10, 15, 20, 30, 45] as m}<option value={m}>:{String(m).padStart(2, '0')}</option>{/each}
            </select>
          </div>
          {#if cronFreq === 'weekly'}
            <div>
              <label for="cron-day" class="block text-xs text-[var(--fg-muted)] mb-1">Day</label>
              <select id="cron-day" class="dm-input text-sm" bind:value={cronWeekday}>
                {#each ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'] as d, i}<option value={i}>{d}</option>{/each}
              </select>
            </div>
          {/if}
        </div>
        <div class="text-xs text-[var(--fg-muted)] font-mono">→ {jSchedule} ({cronHuman(jSchedule)})</div>
      {:else}
        <Input label="Cron expression" bind:value={jSchedule} placeholder="0 3 * * *" hint={cronHuman(jSchedule)} />
      {/if}
      <div class="grid grid-cols-2 gap-3">
        <Input label="Keep last N backups" type="number" bind:value={jRetentionCount as any} hint="0 = unlimited" />
        <Input label="Keep N days" type="number" bind:value={jRetentionDays as any} hint="0 = unlimited" />
      </div>
    </fieldset>

    <!-- Hooks — presets surfaced at the top as badged preset cards,
         not buried in a collapsed "Pre/Post Hooks (optional)" accordion.
         Mirrors the deep-dive marketing mock: one click gives you
         a "PRESET · PostgreSQL · pg_dumpall" row.

         Custom (non-preset) hooks stay inside a nested details below
         for the advanced case. -->
    <fieldset class="space-y-3">
      <legend class="text-xs font-medium text-[var(--fg-muted)] uppercase tracking-wider">
        Pre/Post hooks <span class="text-[var(--fg-subtle)] normal-case tracking-normal">(consistent dumps before the tar)</span>
      </legend>

      <!-- Preset one-click chips -->
      <div class="flex flex-wrap gap-1.5">
        {#each hookPresets as preset}
          <button type="button"
            class="flex items-center gap-1.5 text-[11px] px-2.5 py-1 rounded-md border border-[var(--border)] bg-[var(--surface)] text-[var(--fg-muted)]
                   hover:border-[var(--color-brand-500)]/40 hover:bg-[color-mix(in_srgb,var(--color-brand-500)_8%,transparent)]
                   hover:text-[var(--color-brand-300)] transition-colors"
            onclick={() => { jPreHooks = [...jPreHooks, { container: preset.container, cmd: preset.cmd }]; }}
            title={preset.cmd}
          >
            <span class="text-[9px] px-1 py-0.5 rounded bg-[var(--surface-hover)] uppercase tracking-wider font-medium">Preset</span>
            {preset.label}
          </button>
        {/each}
      </div>

      <!-- Picked pre-hooks as "chip cards" with PRESET badge if they
           match a preset cmd exactly. -->
      {#if jPreHooks.length > 0}
        <div class="space-y-1.5">
          {#each jPreHooks as hook, i}
            {@const preset = hookPresets.find(p => p.cmd === hook.cmd)}
            <div class="flex items-center gap-2 px-3 py-2 rounded-lg border border-[var(--color-brand-500)]/30 bg-[color-mix(in_srgb,var(--color-brand-500)_5%,transparent)]">
              {#if preset}
                <span class="px-1.5 py-0.5 rounded bg-[color-mix(in_srgb,var(--color-brand-500)_20%,transparent)] text-[9px] text-[var(--color-brand-300)] font-medium uppercase tracking-wider">Preset</span>
                <span class="text-xs font-mono text-[var(--color-brand-200)]">{preset.label}</span>
              {:else}
                <span class="text-xs font-mono text-[var(--fg)] truncate">{hook.container}: {hook.cmd}</span>
              {/if}
              <button type="button" class="ml-auto p-1 text-[var(--color-danger-400)] hover:text-[var(--color-danger-300)]" onclick={() => (jPreHooks = jPreHooks.filter((_, idx) => idx !== i))}>
                <Trash2 class="w-3 h-3" />
              </button>
            </div>
          {/each}
        </div>
      {/if}

      <!-- Advanced: custom hooks + post-hooks for operators who want
           to write commands by hand. Hidden by default. -->
      <details class="border border-[var(--border)] rounded-lg">
        <summary class="px-3 py-2 text-[11px] text-[var(--fg-muted)] cursor-pointer hover:bg-[var(--surface-hover)]">
          Advanced: custom hooks + post-hooks
        </summary>
        <div class="px-4 pb-4 pt-2 space-y-3">

        <div class="text-xs font-medium text-[var(--fg-muted)]">Pre-hooks (before backup)</div>
        {#each jPreHooks as hook, i}
          <div class="flex gap-2 items-end">
            <div class="w-40">
              {#if availableContainers.length > 0}
                <select class="dm-input text-xs" bind:value={jPreHooks[i].container}>
                  <option value="">Container…</option>
                  {#each availableContainers as c}<option value={c}>{c}</option>{/each}
                </select>
              {:else}
                <input type="text" class="dm-input text-xs" placeholder="container" bind:value={jPreHooks[i].container} />
              {/if}
            </div>
            <input type="text" class="dm-input text-xs flex-1 font-mono" placeholder="pg_dumpall -U postgres -f /tmp/dump.sql" bind:value={jPreHooks[i].cmd} />
            <button type="button" class="p-1 text-[var(--color-danger-400)]" onclick={() => (jPreHooks = jPreHooks.filter((_, idx) => idx !== i))}><Trash2 class="w-3 h-3" /></button>
          </div>
        {/each}
        <button type="button" class="text-[10px] text-[var(--color-brand-400)] hover:underline" onclick={addPreHook}>+ Add pre-hook</button>

        <div class="text-xs font-medium text-[var(--fg-muted)] pt-2">Post-hooks (after backup)</div>
        {#each jPostHooks as hook, i}
          <div class="flex gap-2 items-end">
            <div class="w-40">
              {#if availableContainers.length > 0}
                <select class="dm-input text-xs" bind:value={jPostHooks[i].container}>
                  <option value="">Container…</option>
                  {#each availableContainers as c}<option value={c}>{c}</option>{/each}
                </select>
              {:else}
                <input type="text" class="dm-input text-xs" placeholder="container" bind:value={jPostHooks[i].container} />
              {/if}
            </div>
            <input type="text" class="dm-input text-xs flex-1 font-mono" placeholder="command" bind:value={jPostHooks[i].cmd} />
            <button type="button" class="p-1 text-[var(--color-danger-400)]" onclick={() => (jPostHooks = jPostHooks.filter((_, idx) => idx !== i))}><Trash2 class="w-3 h-3" /></button>
          </div>
        {/each}
        <button type="button" class="text-[10px] text-[var(--color-brand-400)] hover:underline" onclick={addPostHook}>+ Add post-hook</button>
        </div>
      </details>
    </fieldset>
  </form>

  {#snippet footer()}
    <Button variant="secondary" onclick={() => (showJob = false)}>Cancel</Button>
    <Button variant="primary" type="submit" form="backup-form" disabled={!jName.trim() || jSources.length !== 1 || !jSources[0].name.trim()}>
      {editing ? 'Save' : 'Create'}
    </Button>
  {/snippet}
</Modal>

<!-- Restore modal with confirmation -->
<Modal bind:open={showRestore} title="Restore backup" maxWidth="max-w-md">
  {#if restoreRun}
    <div class="space-y-4">
      <div class="p-3 rounded-lg bg-[color-mix(in_srgb,var(--color-warning-500)_10%,transparent)] border border-[color-mix(in_srgb,var(--color-warning-500)_30%,transparent)] text-xs text-[var(--color-warning-400)]">
        This will untar the archive into a Docker volume. Existing data in the target volume will be <strong>overwritten</strong>. Stop any container using the volume first.
      </div>
      <Input label="Destination volume name" bind:value={restoreVolume} hint="Will be created if it doesn't exist" />
      <div>
        <label for="restore-confirm" class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">Type the volume name to confirm</label>
        <input id="restore-confirm" type="text" class="dm-input text-sm font-mono" bind:value={restoreConfirm} placeholder={restoreVolume} />
      </div>
    </div>
  {/if}
  {#snippet footer()}
    <Button variant="secondary" onclick={() => (showRestore = false)}>Cancel</Button>
    <Button variant="danger" onclick={doRestore} disabled={!restoreVolume.trim() || restoreConfirm !== restoreVolume}>
      <Undo2 class="w-4 h-4" /> Restore
    </Button>
  {/snippet}
</Modal>

<style>
  .bk-page { display: block; }

  /* ─── Header ─── */
  .bk-header {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
    gap: 24px;
    flex-wrap: wrap;
    margin-bottom: 22px;
  }
  .bk-header-text { min-width: 0; max-width: 70ch; }
  .bk-title { font-size: 26px; max-width: 32ch; }
  .bk-subtitle {
    margin-top: 8px;
    font-size: 13.5px;
    color: var(--fg-muted);
    line-height: 1.55;
  }
  .bk-subtitle-fail { color: var(--color-danger-400); }

  .bk-tabs { margin-bottom: 24px; }

  /* Per-tab head (mockup pattern). Eyebrow + subtitle on the left,
     actions on the right. Sits inside each tab body — replaces the
     legacy "everything in the page header" approach so each tab can
     set its own context (Jobs / Runs / Targets each get their own
     copy of "X jobs · Y enabled" stats + tab-specific buttons). */
  .bk-tab-head {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: 12px;
    margin-bottom: 18px;
  }
  .bk-tab-head-text { min-width: 0; max-width: 70ch; }
  .bk-tab-subtitle {
    margin-top: 6px;
    font-size: 13.5px;
    color: var(--fg-muted);
    line-height: 1.55;
  }

  /* Empty / fallback states */
  .bk-card-pad { padding: 22px 24px; }
  .bk-empty { text-align: center; padding: 36px 28px; }
  .bk-empty-title {
    margin-top: 10px;
    font-size: 18px;
    color: var(--fg);
    font-weight: 500;
  }
  .bk-empty-body {
    margin: 8px auto 0;
    max-width: 56ch;
    font-size: 13.5px;
    color: var(--fg-muted);
    line-height: 1.55;
  }
  .bk-empty-hint {
    margin: 12px auto 0;
    max-width: 60ch;
    font-size: 11px;
    color: var(--fg-subtle);
    line-height: 1.6;
  }
  .bk-empty-cta { margin-top: 18px; }
  .bk-danger { color: var(--color-danger-400); }

  /* Review banner */
  .bk-review-banner {
    display: flex;
    align-items: flex-start;
    gap: 12px;
    padding: 12px 14px;
    margin-bottom: 12px;
    border: 1px solid color-mix(in srgb, var(--color-warning-500) 35%, var(--border));
    background: color-mix(in srgb, var(--color-warning-500) 7%, transparent);
    border-radius: 5px;
  }
  :global(.bk-review-icon) { color: var(--color-warning-400); flex-shrink: 0; margin-top: 2px; }
  .bk-review-text { flex: 1; min-width: 0; }
  .bk-review-title { font-size: 13px; color: var(--color-warning-400); font-weight: 500; }
  .bk-review-body { margin-top: 4px; font-size: 12px; color: var(--fg-muted); line-height: 1.55; }
  .bk-review-list { display: flex; flex-direction: column; gap: 6px; margin-bottom: 22px; }
  .bk-review-row {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 12px;
    padding: 10px 14px;
    border: 1px solid var(--border);
    border-radius: 5px;
    background: var(--surface);
  }
  .bk-review-row-text { min-width: 0; flex: 1; }
  .bk-review-row-name { font-size: 12.5px; color: var(--fg); }
  .bk-review-row-reason { margin-top: 4px; font-size: 11.5px; color: var(--fg-muted); line-height: 1.5; }

  /* Job card grid */
  .bk-job-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(360px, 1fr));
    gap: 14px;
  }
  .bk-job-card {
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--bg);
    padding: 14px 16px 16px;
    display: flex;
    flex-direction: column;
    gap: 12px;
    transition: border-color 120ms;
  }
  .bk-job-card:hover { border-color: var(--border-strong); }
  .bk-job-card-disabled { opacity: 0.65; }
  .bk-job-card-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
  }
  .bk-job-card-head-text {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
  }
  /* Card-head + target-head + run-row are buttons so the operator can
     click anywhere on the row to open the detail drawer. They use a
     button reset so they look identical to the static layout. */
  .bk-job-card-head-link,
  .bk-target-card-head-link {
    background: transparent;
    border: 0;
    padding: 0;
    cursor: pointer;
    text-align: left;
  }
  .bk-job-card-head-link { flex: 1; min-width: 0; }
  .bk-target-card-head-link {
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .bk-job-card-head-link:hover .bk-job-card-title,
  .bk-target-card-head-link:hover .bk-target-card-name { color: var(--accent-fg); }
  .bk-runs-row-link {
    cursor: pointer;
    background: transparent;
    border: 0;
    border-bottom: 1px solid var(--border-subtle);
    text-align: inherit;
    width: 100%;
  }
  .bk-runs-row-link:hover { background: var(--surface-hover); }
  .bk-job-card-title {
    margin: 0;
    font-size: 13.5px;
    color: var(--fg);
    font-weight: 500;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .bk-job-card-actions {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    flex-shrink: 0;
  }
  .bk-icon-btn { padding: 5px; }
  .bk-mini-pill { font-size: 9.5px; padding: 1px 6px; }
  .bk-pill-encrypt {
    color: var(--accent-fg);
    border-color: color-mix(in srgb, var(--color-brand-500) 35%, var(--border));
    display: inline-flex;
    align-items: center;
    gap: 3px;
  }

  .bk-job-card-src {
    display: flex;
    align-items: center;
    gap: 6px;
    color: var(--fg-muted);
  }
  .bk-job-card-src-label { font-size: 12.5px; color: var(--fg); }
  .bk-job-card-src-meta {
    font-size: 10.5px;
    color: var(--fg-subtle);
    margin-top: -8px;
    padding-left: 18px;
  }

  .bk-job-card-target {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    padding: 8px 10px;
    border: 1px solid var(--border-subtle);
    border-radius: 4px;
    background: var(--surface);
  }
  .bk-job-card-target-text {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
  }
  .bk-target-glyph {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 24px;
    height: 24px;
    border-radius: 4px;
    background: var(--bg-elevated);
    border: 1px solid var(--border);
    color: var(--fg-muted);
    font-family: var(--font-mono);
    font-size: 9.5px;
    font-weight: 600;
    letter-spacing: 0.04em;
    flex-shrink: 0;
  }
  .bk-target-glyph-lg { width: 36px; height: 36px; font-size: 11px; }
  .bk-job-card-target-info {
    display: flex;
    flex-direction: column;
    line-height: 1.25;
    min-width: 0;
  }
  .bk-job-card-target-name {
    font-size: 12px;
    color: var(--fg);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .bk-job-card-target-meta { font-size: 10px; color: var(--fg-subtle); }

  .bk-job-card-stats {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 14px;
    padding: 8px 0;
    border-top: 1px dashed var(--border-subtle);
    border-bottom: 1px dashed var(--border-subtle);
  }
  .bk-job-stat { display: flex; flex-direction: column; gap: 2px; }
  .bk-job-stat-right { text-align: right; align-items: flex-end; }
  .bk-job-stat-label {
    font-family: var(--font-mono);
    font-size: 9.5px;
    color: var(--fg-subtle);
    letter-spacing: 0.06em;
    text-transform: uppercase;
  }
  .bk-job-stat-value { font-size: 12.5px; color: var(--fg); }
  .bk-job-stat-value.fail { color: var(--color-danger-400); }
  .bk-job-stat-value.warn { color: var(--color-warning-400); }
  .bk-job-stat-value.ok { color: var(--color-success-400); }
  .bk-job-stat-meta { font-size: 10px; color: var(--fg-subtle); }

  .bk-job-card-foot {
    display: flex;
    align-items: center;
    gap: 6px;
    flex-wrap: wrap;
    font-size: 10.5px;
    color: var(--fg-subtle);
  }
  .bk-job-card-foot-spacer { flex: 1; }
  .bk-job-card-toggle {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 10.5px;
    color: var(--fg-muted);
    cursor: pointer;
  }
  .bk-job-card-toggle input { accent-color: var(--color-brand-500); }
  .bk-job-card-error {
    display: flex;
    align-items: flex-start;
    gap: 6px;
    padding: 8px 10px;
    border-radius: 4px;
    background: color-mix(in srgb, var(--color-danger-500) 10%, transparent);
    color: var(--color-danger-400);
    font-family: var(--font-mono);
    font-size: 11px;
    line-height: 1.5;
    word-break: break-all;
  }
  .bk-job-card-row-actions {
    display: flex;
    gap: 4px;
    border-top: 1px solid var(--border-subtle);
    padding-top: 10px;
    margin-top: -2px;
  }
  .bk-job-card-row-btn { flex: 1; justify-content: center; }

  /* Runs tab — header row with view-toggle (mockup pattern) above
     the filter-toolbar. Eyebrow + subtitle on the left, segmented
     Timeline/List toggle on the right. */
  .bk-runs-head {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
    flex-wrap: wrap;
    gap: 12px;
    margin-bottom: 18px;
  }
  .bk-runs-head-text { min-width: 0; max-width: 70ch; }
  .bk-runs-subtitle {
    margin-top: 6px;
    font-size: 13.5px;
    color: var(--fg-muted);
    line-height: 1.55;
  }
  /* Square segmented control — matches mockup .seg-radio: 4px-rounded
     outer with hairline border, surface bg, 3px-rounded inner items
     that highlight to bg-elevated when active (subtle shadow gives
     them the "pressed" look). */
  .bk-seg-radio {
    display: inline-flex;
    border: 1px solid var(--border-subtle);
    border-radius: 4px;
    padding: 2px;
    background: var(--surface);
  }
  .bk-seg-radio-item {
    padding: 5px 12px;
    border: 0;
    background: transparent;
    color: var(--fg-muted);
    font-size: 12px;
    border-radius: 3px;
    cursor: pointer;
    transition: color 120ms, background 120ms;
  }
  .bk-seg-radio-item:hover { color: var(--fg); }
  .bk-seg-radio-item.active {
    background: var(--bg-elevated);
    color: var(--fg);
    box-shadow: 0 1px 2px rgba(0, 0, 0, 0.08);
  }

  /* Timeline view */
  .bk-timeline {
    display: flex;
    flex-direction: column;
    gap: 22px;
  }
  .bk-timeline-day-head {
    display: flex;
    align-items: baseline;
    gap: 12px;
    margin-bottom: 8px;
  }
  .bk-timeline-day-label {
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
    letter-spacing: 0.08em;
    text-transform: uppercase;
  }
  .bk-timeline-day-meta {
    font-size: 10px;
    color: var(--fg-subtle);
  }
  .bk-timeline-day-rule {
    flex: 1;
    border-top: 1px dashed var(--border-subtle);
  }
  .bk-timeline-row {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  /* Timeline-block matches the mockup grid: 56px time, 200px job,
     180px meta, flex target, optional note/error. Border-left is the
     status indicator — colored per state. */
  .bk-timeline-block {
    display: grid;
    grid-template-columns: 56px 200px 180px 1fr auto;
    align-items: center;
    gap: 14px;
    padding: 8px 12px;
    border: 1px solid var(--border-subtle);
    border-left: 3px solid var(--fg-subtle);
    border-radius: 4px;
    background: var(--bg-elevated);
    text-align: left;
    cursor: pointer;
    transition: background 120ms ease, border-color 120ms ease;
  }
  .bk-timeline-block:hover {
    background: var(--surface-hover);
    border-color: var(--border-strong);
  }
  .bk-timeline-block-ok      { border-left-color: var(--color-success-500); }
  .bk-timeline-block-warn    { border-left-color: var(--color-warning-500); }
  .bk-timeline-block-failed  {
    border-left-color: var(--color-danger-500);
    background: color-mix(in srgb, var(--color-danger-500) 4%, var(--bg-elevated));
  }
  .bk-timeline-block-running { border-left-color: var(--accent); }
  .bk-timeline-time {
    font-size: 11px;
    color: var(--fg);
  }
  .bk-timeline-job {
    font-size: 12.5px;
    color: var(--fg);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .bk-timeline-meta {
    font-size: 11px;
    color: var(--fg-muted);
  }
  .bk-timeline-target {
    font-size: 10.5px;
    color: var(--fg-subtle);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .bk-timeline-error {
    font-size: 10.5px;
    color: var(--color-danger-400);
    grid-column: 1 / -1;
    margin-top: 4px;
    padding-top: 6px;
    border-top: 1px dashed var(--border-subtle);
    word-break: break-all;
  }
  @media (max-width: 720px) {
    .bk-timeline-block { grid-template-columns: 56px 1fr; }
    .bk-timeline-meta, .bk-timeline-target { display: none; }
  }

  .bk-runs-toolbar {
    display: flex;
    align-items: center;
    gap: 12px;
    flex-wrap: wrap;
    margin-bottom: 14px;
  }
  .bk-runs-select {
    height: 30px;
    padding: 0 28px 0 10px;
    border: 1px solid var(--border);
    border-radius: 4px;
    background: var(--bg);
    color: var(--fg);
    font-family: var(--font-sans);
    font-size: 13px;
    min-width: 170px;
    cursor: pointer;
    appearance: none;
    background-image: linear-gradient(45deg, transparent 50%, var(--fg-muted) 50%),
                      linear-gradient(-45deg, transparent 50%, var(--fg-muted) 50%);
    background-position: calc(100% - 14px) 13px, calc(100% - 9px) 13px;
    background-size: 5px 5px;
    background-repeat: no-repeat;
    transition: border-color 120ms;
  }
  .bk-runs-select:hover { border-color: var(--border-strong); }
  .bk-runs-select:focus { outline: none; border-color: var(--accent); }

  /* Filter pills — square (4px) corners to match the seg-radio
     toggle style. Mockup uses pill-shaped here too but the user's
     editorial-style asks for consistent square buttons across the
     filter row, so we align with seg-radio. */
  .bk-runs-pills {
    display: inline-flex;
    gap: 0;
    border: 1px solid var(--border-subtle);
    border-radius: 4px;
    padding: 2px;
    background: var(--surface);
  }
  .bk-runs-pill {
    padding: 5px 12px;
    border: 0;
    border-radius: 3px;
    background: transparent;
    color: var(--fg-muted);
    font-size: 12px;
    cursor: pointer;
    transition: background 120ms, color 120ms;
  }
  .bk-runs-pill:hover { color: var(--fg); }
  .bk-runs-pill.active {
    background: var(--bg-elevated);
    color: var(--fg);
    box-shadow: 0 1px 2px rgba(0, 0, 0, 0.08);
  }
  .bk-runs-count { margin-left: auto; font-size: 11px; color: var(--fg-subtle); }

  .bk-runs-table {
    border: 1px solid var(--border);
    border-radius: 6px;
    overflow: hidden;
  }
  .bk-runs-row {
    display: grid;
    grid-template-columns: minmax(180px, 1.6fr) 130px 130px 110px 110px 50px;
    gap: 14px;
    align-items: center;
    padding: 10px 14px;
    border-bottom: 1px solid var(--border-subtle);
    font-size: 12.5px;
    color: var(--fg);
  }
  .bk-runs-row:last-child { border-bottom: 0; }
  .bk-runs-row-head {
    background: var(--bg-elevated);
    color: var(--fg-subtle);
    font-family: var(--font-mono);
    font-size: 10.5px;
    letter-spacing: 0.06em;
    text-transform: uppercase;
  }
  .bk-runs-row-failed { background: color-mix(in srgb, var(--color-danger-500) 4%, transparent); }
  .bk-runs-name {
    display: flex;
    align-items: center;
    gap: 6px;
    overflow: hidden;
  }
  :global(.bk-runs-lock) { color: var(--accent-fg); flex-shrink: 0; }
  .bk-runs-cell { font-size: 11.5px; color: var(--fg-muted); }
  .bk-runs-cell.right { text-align: right; }
  .bk-runs-row .right { text-align: right; }
  .bk-runs-actions { text-align: right; }
  .bk-runs-error {
    display: flex;
    align-items: flex-start;
    gap: 6px;
    padding: 8px 14px;
    border-bottom: 1px solid var(--border-subtle);
    background: color-mix(in srgb, var(--color-danger-500) 5%, transparent);
    font-size: 11px;
    color: var(--color-danger-400);
    word-break: break-all;
    line-height: 1.5;
  }

  /* Targets card grid */
  .bk-target-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(340px, 1fr));
    gap: 14px;
  }
  .bk-target-card {
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--bg);
    padding: 16px 18px 18px;
    display: flex;
    flex-direction: column;
    gap: 14px;
    transition: border-color 120ms;
  }
  .bk-target-card:hover { border-color: var(--border-strong); }
  .bk-target-card-warn { border-left: 3px solid var(--color-warning-500); }
  .bk-target-card-head {
    display: flex;
    align-items: center;
    gap: 12px;
  }
  .bk-target-card-head-text { flex: 1; min-width: 0; }
  .bk-target-card-name {
    font-size: 14px;
    color: var(--fg);
    font-weight: 500;
    margin: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .bk-target-card-type {
    font-size: 10.5px;
    color: var(--fg-subtle);
    margin-top: 2px;
    display: block;
  }

  .bk-target-storage { display: flex; flex-direction: column; gap: 4px; }
  .bk-target-storage-head {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
  }
  .bk-target-storage-label {
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
    letter-spacing: 0.06em;
    text-transform: uppercase;
  }
  .bk-target-storage-num { font-size: 11.5px; color: var(--fg); }
  .bk-target-storage-num-total { color: var(--fg-subtle); }
  .bk-target-storage-bar {
    height: 4px;
    background: var(--bg-elevated);
    border-radius: 2px;
    overflow: hidden;
    position: relative;
    margin-top: 4px;
  }
  .bk-target-storage-bar-fill {
    position: absolute;
    inset: 0;
    right: auto;
    background: var(--accent);
    border-radius: 2px;
    transition: width 200ms;
  }
  .bk-target-card-warn .bk-target-storage-bar-fill { background: var(--color-warning-400); }
  .bk-target-storage-meta {
    font-size: 10px;
    color: var(--fg-subtle);
    margin-top: 2px;
  }

  .bk-target-card-foot {
    display: flex;
    align-items: center;
    gap: 6px;
    border-top: 1px solid var(--border-subtle);
    padding-top: 12px;
  }
  .bk-target-card-foot-meta { font-size: 10.5px; color: var(--fg-subtle); }
  .bk-target-card-foot-spacer { flex: 1; }

  :global(.ed-spin) { animation: ed-spin 0.8s linear infinite; }
  @keyframes ed-spin { to { transform: rotate(360deg); } }
</style>
