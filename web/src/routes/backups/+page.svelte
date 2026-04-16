<script lang="ts">
  import { goto } from '$app/navigation';
  import { page } from '$app/stores';
  import { api, ApiError } from '$lib/api';
  import type { BackupJob, BackupJobInput, BackupRun, BackupSource, BackupHook, BackupTarget } from '$lib/api';
  import { allowed } from '$lib/rbac';
  import { Card, Button, Input, Modal, Badge, EmptyState, Skeleton } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import {
    Archive, Plus, Play, Trash2, RefreshCw, Undo2, HardDrive, Cloud, Lock,
    Search, Clock, Copy, ChevronDown
  } from 'lucide-svelte';

  type Tab = 'jobs' | 'runs' | 'targets';
  let tab = $state<Tab>((new URLSearchParams($page.url.search).get('tab') as Tab) || 'jobs');

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
  let jSources = $state<Array<{ type: string; name: string }>>([{ type: 'volume', name: '' }]);
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
    editingTarget = null; tName = ''; tType = 'local'; tConfig = {}; showTarget = true;
  }
  function openEditTarget(t: BackupTarget) {
    editingTarget = t; tName = t.name; tType = t.type as any;
    const cfg = typeof t.config === 'object' ? t.config : {};
    tConfig = {};
    for (const [k, v] of Object.entries(cfg)) tConfig[k] = String(v ?? '');
    showTarget = true;
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
    if (!confirm(`Delete target "${t.name}"?`)) return;
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

  async function loadJobs() {
    loading = true;
    try { jobs = await api.backups.listJobs(); } catch (err) { toast.error('Failed', err instanceof ApiError ? err.message : undefined); } finally { loading = false; }
  }

  async function loadRuns() {
    loading = true;
    try { runs = await api.backups.listRuns(500); } catch (err) { toast.error('Failed', err instanceof ApiError ? err.message : undefined); } finally { loading = false; }
  }

  $effect(() => {
    if (!allowed('user.manage')) { goto('/'); return; }
    if (tab === 'jobs') { loadJobs(); loadTargets(); }
    else if (tab === 'runs') { loadRuns(); loadJobs(); }
    else if (tab === 'targets') loadTargets();
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

  // Runs filtering
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
    jSources = [{ type: 'volume', name: '' }]; jSchedule = '0 3 * * *';
    jRetentionCount = 7; jRetentionDays = 0; jEncrypt = false; jEnabled = true;
    jPreHooks = []; jPostHooks = []; editing = null;
  }

  function openNew() { resetForm(); showJob = true; }

  function openEdit(j: BackupJob) {
    editing = j;
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
    jSources = j.sources.map(s => ({ type: s.type, name: s.name }));
    if (jSources.length === 0) jSources = [{ type: 'volume', name: '' }];
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

  async function deleteJob(j: BackupJob) {
    if (!confirm(`Delete backup job "${j.name}"?`)) return;
    try { await api.backups.deleteJob(j.id); toast.success('Deleted'); await loadJobs(); } catch (err) { toast.error('Failed', err instanceof ApiError ? err.message : undefined); }
  }

  async function runJob(j: BackupJob) {
    if (!confirm(`Run "${j.name}" now?`)) return;
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
    const vol = r.sources.find(s => s.type === 'volume');
    restoreVolume = vol ? `${vol.name}-restored` : '';
    showRestore = true;
  }
  async function doRestore() {
    if (!restoreRun || restoreConfirm !== restoreVolume) return;
    try { await api.backups.restore(restoreRun.id, restoreVolume.trim()); toast.success('Restored', restoreVolume); showRestore = false; } catch (err) { toast.error('Restore failed', err instanceof ApiError ? err.message : undefined); }
  }

  function addSource() { jSources = [...jSources, { type: 'volume', name: '' }]; }
  function removeSource(i: number) { jSources = jSources.filter((_, idx) => idx !== i); }
  function addPreHook() { jPreHooks = [...jPreHooks, { container: '', cmd: '' }]; }
  function addPostHook() { jPostHooks = [...jPostHooks, { container: '', cmd: '' }]; }
</script>

<section class="space-y-4">
  <!-- Header + summary -->
  <div class="flex items-center justify-between flex-wrap gap-3">
    <div>
      <h2 class="text-2xl font-semibold tracking-tight">Backups</h2>
      <p class="text-sm text-[var(--fg-muted)] mt-0.5">
        {activeJobs} active job{activeJobs === 1 ? '' : 's'}
        {#if recentRuns.length > 0}
          · 24h: <span class="text-[var(--color-success-400)]">{recentSuccess} ok</span>{#if recentFailed > 0}, <span class="text-[var(--color-danger-400)]">{recentFailed} failed</span>{/if}
        {/if}
        {#if nextRun} · next: {fmtTime(nextRun)}{/if}
      </p>
    </div>
    <div class="flex gap-2">
      <Button variant="secondary" size="sm" onclick={() => tab === 'jobs' ? loadJobs() : loadRuns()}>
        <RefreshCw class="w-3.5 h-3.5 {loading ? 'animate-spin' : ''}" /> Refresh
      </Button>
      {#if tab === 'jobs'}
        <Button variant="primary" size="sm" onclick={openNew}><Plus class="w-3.5 h-3.5" /> New job</Button>
      {/if}
    </div>
  </div>

  <!-- Tabs -->
  <div class="border-b border-[var(--border)] flex gap-1">
    <button class="px-4 py-2.5 text-sm border-b-2 transition-colors flex items-center gap-2 {tab === 'jobs' ? 'border-[var(--color-brand-500)] text-[var(--fg)]' : 'border-transparent text-[var(--fg-muted)] hover:text-[var(--fg)]'}" onclick={() => (tab = 'jobs')}>
      <Archive class="w-3.5 h-3.5" /> Jobs
    </button>
    <button class="px-4 py-2.5 text-sm border-b-2 transition-colors flex items-center gap-2 {tab === 'runs' ? 'border-[var(--color-brand-500)] text-[var(--fg)]' : 'border-transparent text-[var(--fg-muted)] hover:text-[var(--fg)]'}" onclick={() => (tab = 'runs')}>
      <Clock class="w-3.5 h-3.5" /> Runs
    </button>
    <button class="px-4 py-2.5 text-sm border-b-2 transition-colors flex items-center gap-2 {tab === 'targets' ? 'border-[var(--color-brand-500)] text-[var(--fg)]' : 'border-transparent text-[var(--fg-muted)] hover:text-[var(--fg)]'}" onclick={() => (tab = 'targets')}>
      <HardDrive class="w-3.5 h-3.5" /> Targets
    </button>
  </div>

  <!-- ===== JOBS TAB ===== -->
  {#if tab === 'jobs'}
    {#if loading && jobs.length === 0}
      <Card><Skeleton class="m-5" width="70%" height="3rem" /></Card>
    {:else if jobs.length === 0}
      <Card><EmptyState icon={Archive} title="No backup jobs" description="Create a job to snapshot volumes or stacks on a schedule." /></Card>
    {:else}
      <Card>
        <div class="overflow-x-auto">
          <table class="w-full text-sm">
            <thead>
              <tr class="border-b border-[var(--border)] text-[var(--fg-muted)] text-xs uppercase tracking-wider">
                <th class="text-center px-3 py-3 w-14">Enabled</th>
                <th class="text-left px-3 py-3">Name</th>
                <th class="text-left px-3 py-3">Target</th>
                <th class="text-left px-3 py-3">Sources</th>
                <th class="text-left px-3 py-3">Schedule</th>
                <th class="text-left px-3 py-3">Last Run</th>
                <th class="text-left px-3 py-3">Next Run</th>
                <th class="text-right px-3 py-3 w-28">Actions</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-[var(--border)]">
              {#each jobs as j (j.id)}
                <tr class="hover:bg-[var(--surface-hover)]">
                  <td class="px-3 py-2.5 text-center">
                    <label class="relative inline-flex items-center cursor-pointer">
                      <input type="checkbox" class="sr-only peer" checked={j.enabled} onchange={() => toggleJob(j)} />
                      <div class="w-8 h-4.5 bg-[var(--surface)] border border-[var(--border)] rounded-full peer-checked:bg-[var(--color-brand-500)] peer-checked:border-[var(--color-brand-500)] after:content-[''] after:absolute after:top-[1px] after:left-[1px] after:bg-white after:rounded-full after:h-3.5 after:w-3.5 after:transition-transform peer-checked:after:translate-x-3.5"></div>
                    </label>
                  </td>
                  <td class="px-3 py-2.5">
                    <button class="text-left" onclick={() => openEdit(j)}>
                      <div class="font-medium text-sm flex items-center gap-1.5">
                        {j.name}
                        {#if j.encrypt}<Lock class="w-3 h-3 text-[var(--color-brand-400)]" />{/if}
                      </div>
                    </button>
                  </td>
                  <td class="px-3 py-2.5">
                    <Badge variant={j.target_type === 's3' ? 'info' : 'default'}>
                      {j.target_type === 's3' ? 'S3' : 'Local'}
                    </Badge>
                  </td>
                  <td class="px-3 py-2.5 text-xs text-[var(--fg-muted)]" title={j.sources.map(s => `${s.type}:${s.name}`).join(', ')}>
                    {j.sources.length} source{j.sources.length === 1 ? '' : 's'}
                  </td>
                  <td class="px-3 py-2.5 text-xs">
                    <div class="font-mono text-[var(--fg-muted)]">{cronHuman(j.schedule)}</div>
                  </td>
                  <td class="px-3 py-2.5 text-xs text-[var(--fg-muted)]">{fmtTime(j.last_run_at)}</td>
                  <td class="px-3 py-2.5 text-xs text-[var(--fg-muted)]">{fmtTime(j.next_run_at)}</td>
                  <td class="px-3 py-2.5">
                    <div class="flex gap-0.5 justify-end">
                      <button class="p-1.5 rounded-md text-[var(--color-success-400)] hover:bg-[var(--surface-hover)]" title="Run now" onclick={() => runJob(j)}>
                        <Play class="w-3.5 h-3.5" />
                      </button>
                      <button class="p-1.5 rounded-md text-[var(--fg-muted)] hover:text-[var(--fg)] hover:bg-[var(--surface-hover)]" title="Duplicate" onclick={() => duplicateJob(j)}>
                        <Copy class="w-3.5 h-3.5" />
                      </button>
                      <button class="p-1.5 rounded-md text-[var(--color-danger-400)] hover:bg-[color-mix(in_srgb,var(--color-danger-500)_10%,transparent)]" title="Delete" onclick={() => deleteJob(j)}>
                        <Trash2 class="w-3.5 h-3.5" />
                      </button>
                    </div>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      </Card>
    {/if}

  <!-- ===== RUNS TAB ===== -->
  {:else if tab === 'runs'}
    <!-- Filters -->
    <div class="flex flex-wrap items-center gap-3">
      <select class="dm-input !py-1 !px-2 !w-auto text-xs" bind:value={runJobFilter}>
        <option value="">All jobs</option>
        {#each jobs as j}<option value={j.name}>{j.name}</option>{/each}
      </select>
      <div class="flex gap-1 text-xs">
        {#each [['all', 'All'], ['success', 'Success'], ['failed', 'Failed'], ['running', 'Running']] as [key, label]}
          <button
            class="px-2.5 py-1 rounded-full border transition-colors {runStatusFilter === key
              ? 'bg-[var(--surface)] border-[var(--border-strong)] text-[var(--fg)]'
              : 'border-[var(--border)] text-[var(--fg-muted)] hover:bg-[var(--surface-hover)]'}"
            onclick={() => (runStatusFilter = key as typeof runStatusFilter)}
          >{label}</button>
        {/each}
      </div>
      <span class="text-xs text-[var(--fg-subtle)] ml-auto">{filteredRuns.length} run{filteredRuns.length === 1 ? '' : 's'}</span>
    </div>

    {#if loading && runs.length === 0}
      <Card><Skeleton class="m-5" width="70%" height="3rem" /></Card>
    {:else if runs.length === 0}
      <Card><EmptyState icon={Clock} title="No runs yet" description="Trigger a backup job or wait for its schedule." /></Card>
    {:else if filteredRuns.length === 0}
      <Card class="p-8 text-center text-sm text-[var(--fg-muted)]">No runs match this filter.</Card>
    {:else}
      <Card>
        <div class="overflow-x-auto">
          <table class="w-full text-sm">
            <thead>
              <tr class="border-b border-[var(--border)] text-[var(--fg-muted)] text-xs uppercase tracking-wider">
                <th class="text-left px-5 py-3">Job</th>
                <th class="text-left px-3 py-3">Status</th>
                <th class="text-left px-3 py-3">Started</th>
                <th class="text-left px-3 py-3">Duration</th>
                <th class="text-left px-3 py-3">Size</th>
                <th class="text-right px-3 py-3 w-20">Actions</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-[var(--border)]">
              {#each filteredRuns as r (r.id)}
                <tr class="hover:bg-[var(--surface-hover)]">
                  <td class="px-5 py-2.5 font-medium">{r.job_name}</td>
                  <td class="px-3 py-2.5">
                    <Badge variant={statusVariant(r.status)} dot>{r.status}</Badge>
                    {#if r.encrypted}<Lock class="w-3 h-3 inline ml-1 text-[var(--color-brand-400)]" />{/if}
                  </td>
                  <td class="px-3 py-2.5 text-xs text-[var(--fg-muted)]">{fmtTime(r.started_at)}</td>
                  <td class="px-3 py-2.5 text-xs font-mono tabular-nums">{fmtDuration(r.started_at, r.finished_at)}</td>
                  <td class="px-3 py-2.5 text-xs font-mono tabular-nums">{fmtBytes(r.size_bytes)}</td>
                  <td class="px-3 py-2.5 text-right">
                    {#if r.status === 'success'}
                      <button class="p-1.5 rounded-md text-[var(--fg-muted)] hover:text-[var(--fg)] hover:bg-[var(--surface-hover)]" title="Restore" onclick={() => openRestore(r)}>
                        <Undo2 class="w-3.5 h-3.5" />
                      </button>
                    {/if}
                  </td>
                </tr>
                {#if r.error}
                  <tr><td colspan="6" class="px-5 pb-3 text-xs text-[var(--color-danger-400)] font-mono break-all">{r.error}</td></tr>
                {/if}
              {/each}
            </tbody>
          </table>
        </div>
      </Card>
    {/if}

  <!-- ===== TARGETS TAB ===== -->
  {:else if tab === 'targets'}
    <div class="flex justify-between items-center">
      <span class="text-sm text-[var(--fg-muted)]">{bTargets.length} target{bTargets.length === 1 ? '' : 's'}</span>
      <Button variant="primary" size="sm" onclick={openNewTarget}><Plus class="w-3.5 h-3.5" /> New target</Button>
    </div>

    {#if targetsLoading && bTargets.length === 0}
      <Card><Skeleton class="m-5" width="70%" height="3rem" /></Card>
    {:else if bTargets.length === 0}
      <Card><EmptyState icon={HardDrive} title="No backup targets" description="Configure a storage destination (local, NAS, SFTP, S3, WebDAV) to use in backup jobs." /></Card>
    {:else}
      <Card>
        <div class="overflow-x-auto">
          <table class="w-full text-sm">
            <thead>
              <tr class="border-b border-[var(--border)] text-[var(--fg-muted)] text-xs uppercase tracking-wider">
                <th class="text-left px-5 py-3">Name</th>
                <th class="text-left px-3 py-3">Type</th>
                <th class="text-left px-3 py-3">Status</th>
                <th class="text-left px-3 py-3">Storage</th>
                <th class="text-left px-3 py-3">Last Checked</th>
                <th class="text-right px-3 py-3 w-28">Actions</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-[var(--border)]">
              {#each bTargets as t (t.id)}
                <tr class="hover:bg-[var(--surface-hover)]">
                  <td class="px-5 py-2.5">
                    <button class="text-left" onclick={() => openEditTarget(t)}>
                      <div class="font-medium text-sm">{t.name}</div>
                    </button>
                  </td>
                  <td class="px-3 py-2.5">
                    <Badge variant={t.type === 's3' ? 'info' : t.type === 'sftp' ? 'success' : t.type === 'smb' ? 'warning' : t.type === 'webdav' ? 'info' : 'default'}>
                      {t.type.toUpperCase()}
                    </Badge>
                  </td>
                  <td class="px-3 py-2.5">
                    {#if t.status === 'connected'}
                      <Badge variant="success" dot>connected</Badge>
                    {:else if t.status === 'error'}
                      <Badge variant="danger" dot>error</Badge>
                    {:else}
                      <Badge variant="default">unknown</Badge>
                    {/if}
                  </td>
                  <td class="px-3 py-2.5 text-xs font-mono tabular-nums">
                    {#if t.total_bytes > 0}
                      {fmtBytes(t.total_bytes - t.used_bytes)} free / {fmtBytes(t.total_bytes)}
                    {:else}
                      <span class="text-[var(--fg-subtle)]">—</span>
                    {/if}
                  </td>
                  <td class="px-3 py-2.5 text-xs text-[var(--fg-muted)]">{fmtTime(t.last_checked_at)}</td>
                  <td class="px-3 py-2.5">
                    <div class="flex gap-0.5 justify-end">
                      <button class="p-1.5 rounded-md text-[var(--color-brand-400)] hover:bg-[var(--surface-hover)]" title="Test connection" onclick={() => testTarget(t)}>
                        <RefreshCw class="w-3.5 h-3.5" />
                      </button>
                      <button class="p-1.5 rounded-md text-[var(--color-danger-400)] hover:bg-[color-mix(in_srgb,var(--color-danger-500)_10%,transparent)]" title="Delete" onclick={() => deleteTarget(t)}>
                        <Trash2 class="w-3.5 h-3.5" />
                      </button>
                    </div>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      </Card>
    {/if}
  {/if}
</section>

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

    <!-- Sources -->
    <fieldset class="space-y-3">
      <legend class="text-xs font-medium text-[var(--fg-muted)] uppercase tracking-wider">What to back up</legend>
      {#each jSources as src, i}
        <div class="flex gap-2 items-end">
          <div class="w-28">
            <label for="src-type-{i}" class="block text-xs text-[var(--fg-muted)] mb-1">Type</label>
            <select id="src-type-{i}" class="dm-input text-sm" bind:value={jSources[i].type}>
              <option value="volume">Volume</option>
              <option value="stack">Stack</option>
              <option value="system">System</option>
            </select>
          </div>
          <div class="flex-1">
            <label for="src-name-{i}" class="block text-xs text-[var(--fg-muted)] mb-1">Name</label>
            <input id="src-name-{i}" type="text" class="dm-input text-sm" placeholder={src.type === 'system' ? 'dockmesh' : `my-${src.type}`} bind:value={jSources[i].name} />
          </div>
          {#if jSources.length > 1}
            <button type="button" class="p-1.5 text-[var(--color-danger-400)]" onclick={() => removeSource(i)}><Trash2 class="w-3.5 h-3.5" /></button>
          {/if}
        </div>
      {/each}
      <button type="button" class="text-xs text-[var(--color-brand-400)] hover:underline" onclick={addSource}>+ Add source</button>
    </fieldset>

    <!-- Target -->
    <fieldset class="space-y-3">
      <legend class="text-xs font-medium text-[var(--fg-muted)] uppercase tracking-wider">Where to store</legend>
      {#if bTargets.length > 0}
        <div>
          <label for="job-target" class="block text-xs text-[var(--fg-muted)] mb-1">Select a configured target</label>
          <select id="job-target" class="dm-input text-sm" bind:value={jTargetType}
            onchange={(e) => {
              const val = (e.target as HTMLSelectElement).value;
              if (val.startsWith('id:')) {
                const tid = parseInt(val.slice(3));
                const t = bTargets.find(bt => bt.id === tid);
                if (t) { jTargetType = t.type as any; jLocalPath = (t.config as any)?.path ?? ''; }
              }
            }}>
            <option value="local">Local directory (inline)</option>
            {#each bTargets as bt}
              <option value="id:{bt.id}">{bt.name} ({bt.type.toUpperCase()}){bt.status === 'connected' ? ' ✓' : ''}</option>
            {/each}
          </select>
        </div>
        <p class="text-[10px] text-[var(--fg-subtle)]">Select a pre-configured target or use "Local directory" for inline config. Manage targets in the Targets tab.</p>
      {:else}
        <div>
          <label for="target-type" class="block text-xs text-[var(--fg-muted)] mb-1">Target type</label>
          <select id="target-type" class="dm-input text-sm" bind:value={jTargetType}>
            <option value="local">Local directory</option>
            <option value="s3">S3 / MinIO / Wasabi</option>
          </select>
        </div>
      {/if}
      {#if jTargetType === 'local'}
        <Input label="Path" bind:value={jLocalPath} placeholder="./data/backups" hint="Absolute or relative to Dockmesh working directory" />
      {:else if jTargetType === 's3'}
        <div class="grid grid-cols-2 gap-3">
          <Input label="Endpoint" bind:value={jS3Endpoint} placeholder="s3.amazonaws.com" />
          <Input label="Bucket" bind:value={jS3Bucket} placeholder="my-backups" />
          <Input label="Access Key" bind:value={jS3AccessKey} />
          <Input label="Secret Key" type="password" bind:value={jS3SecretKey} />
        </div>
      {/if}
      <label class="flex items-center gap-2 text-sm cursor-pointer">
        <input type="checkbox" bind:checked={jEncrypt} class="accent-[var(--color-brand-500)]" />
        Encrypt with age (uses server's secrets key)
      </label>
    </fieldset>

    <!-- Schedule -->
    <fieldset class="space-y-3">
      <legend class="text-xs font-medium text-[var(--fg-muted)] uppercase tracking-wider">Schedule</legend>
      <div class="flex flex-wrap gap-1.5 mb-2">
        {#each cronPresets as p}
          <button type="button"
            class="text-[10px] px-2 py-1 rounded border transition-colors {jSchedule === p.cron ? 'border-[var(--color-brand-500)] bg-[color-mix(in_srgb,var(--color-brand-500)_10%,transparent)] text-[var(--color-brand-400)]' : 'border-[var(--border)] text-[var(--fg-muted)] hover:border-[var(--color-brand-500)]'}"
            onclick={() => (jSchedule = p.cron)}
          >{p.label}</button>
        {/each}
      </div>
      <div class="grid grid-cols-3 gap-3">
        <Input label="Cron expression" bind:value={jSchedule} placeholder="0 3 * * *" hint={cronHuman(jSchedule)} />
        <Input label="Keep last N backups" type="number" bind:value={jRetentionCount as any} />
        <Input label="Keep N days" type="number" bind:value={jRetentionDays as any} />
      </div>
    </fieldset>

    <!-- Hooks (collapsible) -->
    <details class="border border-[var(--border)] rounded-lg">
      <summary class="px-4 py-2.5 text-xs font-medium text-[var(--fg-muted)] uppercase tracking-wider cursor-pointer hover:bg-[var(--surface-hover)]">
        Pre/Post Hooks (optional)
      </summary>
      <div class="px-4 pb-4 space-y-3">
        <div class="text-xs text-[var(--fg-subtle)]">Run commands via docker exec before/after the backup. Useful for pg_dump, mysqldump, etc.</div>
        <div class="text-xs font-medium text-[var(--fg-muted)]">Pre-hooks</div>
        {#each jPreHooks as hook, i}
          <div class="flex gap-2">
            <input type="text" class="dm-input text-xs flex-1" placeholder="container name" bind:value={jPreHooks[i].container} />
            <input type="text" class="dm-input text-xs flex-[2]" placeholder="pg_dumpall -U postgres -f /tmp/dump.sql" bind:value={jPreHooks[i].cmd} />
            <button type="button" class="text-[var(--color-danger-400)]" onclick={() => (jPreHooks = jPreHooks.filter((_, idx) => idx !== i))}><Trash2 class="w-3 h-3" /></button>
          </div>
        {/each}
        <button type="button" class="text-[10px] text-[var(--color-brand-400)] hover:underline" onclick={addPreHook}>+ Add pre-hook</button>

        <div class="text-xs font-medium text-[var(--fg-muted)] pt-2">Post-hooks</div>
        {#each jPostHooks as hook, i}
          <div class="flex gap-2">
            <input type="text" class="dm-input text-xs flex-1" placeholder="container name" bind:value={jPostHooks[i].container} />
            <input type="text" class="dm-input text-xs flex-[2]" placeholder="command" bind:value={jPostHooks[i].cmd} />
            <button type="button" class="text-[var(--color-danger-400)]" onclick={() => (jPostHooks = jPostHooks.filter((_, idx) => idx !== i))}><Trash2 class="w-3 h-3" /></button>
          </div>
        {/each}
        <button type="button" class="text-[10px] text-[var(--color-brand-400)] hover:underline" onclick={addPostHook}>+ Add post-hook</button>
      </div>
    </details>
  </form>

  {#snippet footer()}
    <Button variant="secondary" onclick={() => (showJob = false)}>Cancel</Button>
    <Button variant="primary" type="submit" form="backup-form" disabled={!jName.trim() || jSources.every(s => !s.name.trim())}>
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
