<script lang="ts">
  import { goto } from '$app/navigation';
  import { api, ApiError } from '$lib/api';
  import type { BackupJob, BackupJobInput, BackupRun, BackupSource } from '$lib/api';
  import { allowed } from '$lib/rbac';
  import { Card, Button, Input, Modal, Badge, EmptyState, Skeleton } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { Archive, Plus, Play, Trash2, RefreshCw, Undo2, HardDrive, Cloud, Lock } from 'lucide-svelte';

  type Tab = 'jobs' | 'runs';
  let tab = $state<Tab>('jobs');

  let jobs = $state<BackupJob[]>([]);
  let runs = $state<BackupRun[]>([]);
  let loading = $state(false);

  // Job modal
  let showJob = $state(false);
  let editing = $state<BackupJob | null>(null);
  let form = $state<BackupJobInput>(emptyForm());

  // Restore modal
  let showRestore = $state(false);
  let restoreRun = $state<BackupRun | null>(null);
  let restoreVolume = $state('');

  // Free-form text fields the user edits, then we parse on save.
  let sourcesText = $state('volume:my-data');
  let targetConfigText = $state('{"path":"/srv/dockmesh-backups"}');
  let preHooksText = $state('');
  let postHooksText = $state('');

  function emptyForm(): BackupJobInput {
    return {
      name: '',
      target_type: 'local',
      target_config: { path: '/srv/dockmesh-backups' },
      sources: [],
      schedule: '',
      retention_count: 7,
      retention_days: 0,
      encrypt: false,
      pre_hooks: [],
      post_hooks: [],
      enabled: true
    };
  }

  async function loadJobs() {
    loading = true;
    try {
      jobs = await api.backups.listJobs();
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      loading = false;
    }
  }

  async function loadRuns() {
    loading = true;
    try {
      runs = await api.backups.listRuns(100);
    } catch (err) {
      toast.error('Failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      loading = false;
    }
  }

  function openNew() {
    editing = null;
    form = emptyForm();
    sourcesText = 'volume:my-data';
    targetConfigText = '{"path":"/srv/dockmesh-backups"}';
    preHooksText = '';
    postHooksText = '';
    showJob = true;
  }

  function openEdit(j: BackupJob) {
    editing = j;
    form = {
      name: j.name,
      target_type: j.target_type,
      target_config: j.target_config,
      sources: j.sources,
      schedule: j.schedule,
      retention_count: j.retention_count,
      retention_days: j.retention_days,
      encrypt: j.encrypt,
      pre_hooks: j.pre_hooks,
      post_hooks: j.post_hooks,
      enabled: j.enabled
    };
    sourcesText = j.sources.map((s) => `${s.type}:${s.name}`).join('\n');
    targetConfigText = JSON.stringify(j.target_config, null, 2);
    preHooksText = j.pre_hooks.length ? JSON.stringify(j.pre_hooks, null, 2) : '';
    postHooksText = j.post_hooks.length ? JSON.stringify(j.post_hooks, null, 2) : '';
    showJob = true;
  }

  function parseSources(s: string): BackupSource[] {
    return s
      .split('\n')
      .map((l) => l.trim())
      .filter((l) => l.length > 0 && !l.startsWith('#'))
      .map((l) => {
        const [type, ...rest] = l.split(':');
        return { type: (type || 'volume').trim() as 'volume' | 'stack', name: rest.join(':').trim() };
      });
  }

  async function saveJob(e: Event) {
    e.preventDefault();
    try {
      const payload: BackupJobInput = {
        ...form,
        sources: parseSources(sourcesText),
        target_config: targetConfigText.trim() ? JSON.parse(targetConfigText) : {},
        pre_hooks: preHooksText.trim() ? JSON.parse(preHooksText) : [],
        post_hooks: postHooksText.trim() ? JSON.parse(postHooksText) : []
      };
      if (editing) {
        await api.backups.updateJob(editing.id, payload);
        toast.success('Job updated', payload.name);
      } else {
        await api.backups.createJob(payload);
        toast.success('Job created', payload.name);
      }
      showJob = false;
      await loadJobs();
    } catch (err) {
      toast.error('Save failed', err instanceof ApiError ? err.message : (err as Error).message);
    }
  }

  async function deleteJob(j: BackupJob) {
    if (!confirm(`Delete backup job "${j.name}"?`)) return;
    try {
      await api.backups.deleteJob(j.id);
      toast.success('Deleted', j.name);
      await loadJobs();
    } catch (err) {
      toast.error('Delete failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function runJob(j: BackupJob) {
    if (!confirm(`Run "${j.name}" now?`)) return;
    try {
      const r = await api.backups.runJob(j.id);
      toast.success('Backup started', `run #${r.id}`);
      await loadJobs();
      if (tab === 'runs') await loadRuns();
    } catch (err) {
      toast.error('Run failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  function openRestore(r: BackupRun) {
    restoreRun = r;
    const firstVol = r.sources.find((s) => s.type === 'volume');
    restoreVolume = firstVol ? `${firstVol.name}-restored` : '';
    showRestore = true;
  }

  async function doRestore() {
    if (!restoreRun || !restoreVolume.trim()) return;
    try {
      await api.backups.restore(restoreRun.id, restoreVolume.trim());
      toast.success('Restored', `volume ${restoreVolume}`);
      showRestore = false;
    } catch (err) {
      toast.error('Restore failed', err instanceof ApiError ? err.message : undefined);
    }
  }

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

  function statusVariant(s: string): 'success' | 'warning' | 'danger' | 'info' | 'default' {
    if (s === 'success') return 'success';
    if (s === 'failed') return 'danger';
    if (s === 'running') return 'info';
    return 'default';
  }

  $effect(() => {
    if (!allowed('user.manage')) {
      goto('/');
      return;
    }
    if (tab === 'jobs') loadJobs();
    else loadRuns();
  });
</script>

<section class="space-y-6">
  <div class="flex items-center justify-between flex-wrap gap-3">
    <div>
      <h2 class="text-2xl font-semibold tracking-tight">Backups</h2>
      <p class="text-sm text-[var(--fg-muted)] mt-0.5">
        Volume + stack snapshots with optional age encryption, scheduled or on-demand.
      </p>
    </div>
    <div class="flex gap-2">
      <Button variant="secondary" size="sm" onclick={() => (tab === 'jobs' ? loadJobs() : loadRuns())}>
        <RefreshCw class="w-3.5 h-3.5 {loading ? 'animate-spin' : ''}" /> Refresh
      </Button>
      {#if tab === 'jobs'}
        <Button variant="primary" onclick={openNew}>
          <Plus class="w-4 h-4" /> New job
        </Button>
      {/if}
    </div>
  </div>

  <div class="border-b border-[var(--border)] flex gap-1">
    <button
      class="px-4 py-2.5 text-sm border-b-2 transition-colors flex items-center gap-2
             {tab === 'jobs' ? 'border-[var(--color-brand-500)] text-[var(--fg)]' : 'border-transparent text-[var(--fg-muted)] hover:text-[var(--fg)]'}"
      onclick={() => (tab = 'jobs')}
    >
      <Archive class="w-3.5 h-3.5" /> Jobs
    </button>
    <button
      class="px-4 py-2.5 text-sm border-b-2 transition-colors flex items-center gap-2
             {tab === 'runs' ? 'border-[var(--color-brand-500)] text-[var(--fg)]' : 'border-transparent text-[var(--fg-muted)] hover:text-[var(--fg)]'}"
      onclick={() => (tab = 'runs')}
    >
      <Play class="w-3.5 h-3.5" /> Runs
    </button>
  </div>

  {#if tab === 'jobs'}
    {#if loading && jobs.length === 0}
      <Card><Skeleton class="m-5" width="70%" height="1rem" /></Card>
    {:else if jobs.length === 0}
      <Card>
        <EmptyState
          icon={Archive}
          title="No backup jobs"
          description="Create a job to snapshot volumes or stacks on a schedule. Local target writes tar.gz files to a directory; S3 uploads to AWS, MinIO, Wasabi, Backblaze, etc."
        >
          {#snippet action()}
            <Button variant="primary" onclick={openNew}>
              <Plus class="w-4 h-4" /> Create job
            </Button>
          {/snippet}
        </EmptyState>
      </Card>
    {:else}
      <Card>
        <div class="divide-y divide-[var(--border)]">
          {#each jobs as j}
            <div class="flex items-center gap-3 px-5 py-3 hover:bg-[var(--surface-hover)] transition-colors">
              <div class="w-10 h-10 rounded-lg bg-[color-mix(in_srgb,var(--color-brand-500)_15%,transparent)] text-[var(--color-brand-400)] flex items-center justify-center shrink-0">
                {#if j.target_type === 's3'}
                  <Cloud class="w-5 h-5" />
                {:else}
                  <HardDrive class="w-5 h-5" />
                {/if}
              </div>
              <button class="flex-1 min-w-0 text-left" onclick={() => openEdit(j)}>
                <div class="font-medium text-sm flex items-center gap-2">
                  {j.name}
                  {#if j.encrypt}<Lock class="w-3 h-3 text-[var(--color-brand-400)]" />{/if}
                  {#if !j.enabled}<Badge variant="default">disabled</Badge>{/if}
                </div>
                <div class="text-xs text-[var(--fg-muted)] font-mono truncate">
                  {j.target_type} · {j.sources.length} source(s)
                  {#if j.schedule}· cron: {j.schedule}{/if}
                  · last run: {fmtTime(j.last_run_at)}
                </div>
              </button>
              <Button size="xs" variant="ghost" onclick={() => runJob(j)} aria-label="Run now">
                <Play class="w-3.5 h-3.5 text-[var(--color-success-400)]" />
              </Button>
              <Button size="xs" variant="ghost" onclick={() => deleteJob(j)} aria-label="Delete">
                <Trash2 class="w-3.5 h-3.5 text-[var(--color-danger-400)]" />
              </Button>
            </div>
          {/each}
        </div>
      </Card>
    {/if}
  {:else if loading && runs.length === 0}
    <Card><Skeleton class="m-5" width="70%" height="1rem" /></Card>
  {:else if runs.length === 0}
    <Card>
      <EmptyState icon={Play} title="No runs yet" description="Trigger a backup job manually or wait for its schedule to fire." />
    </Card>
  {:else}
    <Card>
      <div class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead class="text-left text-xs text-[var(--fg-muted)] uppercase tracking-wider bg-[var(--bg-elevated)]">
            <tr>
              <th class="px-5 py-3 font-medium">Job</th>
              <th class="px-5 py-3 font-medium">Status</th>
              <th class="px-5 py-3 font-medium">Started</th>
              <th class="px-5 py-3 font-medium">Size</th>
              <th class="px-5 py-3 font-medium">SHA-256</th>
              <th class="px-5 py-3 font-medium"></th>
            </tr>
          </thead>
          <tbody class="divide-y divide-[var(--border)]">
            {#each runs as r}
              <tr class="hover:bg-[var(--surface-hover)]">
                <td class="px-5 py-3">{r.job_name}</td>
                <td class="px-5 py-3">
                  <Badge variant={statusVariant(r.status)} dot>{r.status}</Badge>
                  {#if r.encrypted}<Lock class="w-3 h-3 inline ml-1 text-[var(--color-brand-400)]" />{/if}
                </td>
                <td class="px-5 py-3 text-xs text-[var(--fg-muted)] font-mono whitespace-nowrap">{fmtTime(r.started_at)}</td>
                <td class="px-5 py-3 font-mono text-xs">{fmtBytes(r.size_bytes)}</td>
                <td class="px-5 py-3 font-mono text-xs text-[var(--fg-subtle)] truncate max-w-[140px]">{r.sha256 ? r.sha256.slice(0, 12) : '—'}</td>
                <td class="px-5 py-3 text-right">
                  {#if r.status === 'success'}
                    <Button size="xs" variant="ghost" onclick={() => openRestore(r)}>
                      <Undo2 class="w-3.5 h-3.5" /> Restore
                    </Button>
                  {/if}
                </td>
              </tr>
              {#if r.error}
                <tr><td colspan="6" class="px-5 pb-3 text-xs text-[var(--color-danger-400)] font-mono">{r.error}</td></tr>
              {/if}
            {/each}
          </tbody>
        </table>
      </div>
    </Card>
  {/if}
</section>

<Modal bind:open={showJob} title={editing ? 'Edit backup job' : 'New backup job'} maxWidth="max-w-2xl">
  <form onsubmit={saveJob} class="space-y-3" id="backup-form">
    <div class="grid grid-cols-2 gap-3">
      <Input label="Name" bind:value={form.name} />
      <div>
        <span class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">Target type</span>
        <select class="dm-input" bind:value={form.target_type}>
          <option value="local">Local directory</option>
          <option value="s3">S3 / MinIO / Wasabi / Backblaze</option>
        </select>
      </div>
    </div>

    <div>
      <label for="targetcfg" class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">Target config (JSON)</label>
      <textarea
        id="targetcfg"
        class="dm-input font-mono text-xs h-24 resize-y"
        bind:value={targetConfigText}
        placeholder={form.target_type === 's3' ? '{"endpoint":"s3.amazonaws.com","bucket":"my-bucket","access_key":"...","secret_key":"...","use_ssl":true}' : '{"path":"/srv/dockmesh-backups"}'}
      ></textarea>
    </div>

    <div>
      <label for="sources" class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">Sources</label>
      <textarea
        id="sources"
        class="dm-input font-mono text-xs h-20 resize-y"
        bind:value={sourcesText}
        placeholder={'volume:my-data\nstack:nextcloud'}
      ></textarea>
      <div class="text-xs text-[var(--fg-subtle)] mt-1">One per line: <code>volume:NAME</code> or <code>stack:NAME</code></div>
    </div>

    <div class="grid grid-cols-3 gap-3">
      <Input label="Schedule (cron)" placeholder="0 3 * * *" bind:value={form.schedule} />
      <Input label="Keep last N" type="number" value={String(form.retention_count)} oninput={(e) => (form.retention_count = parseInt((e.target as HTMLInputElement).value || '0', 10))} />
      <Input label="Keep N days" type="number" value={String(form.retention_days)} oninput={(e) => (form.retention_days = parseInt((e.target as HTMLInputElement).value || '0', 10))} />
    </div>

    <div class="flex gap-4">
      <label class="flex items-center gap-2 text-sm">
        <input type="checkbox" bind:checked={form.encrypt} class="accent-[var(--color-brand-500)]" />
        Encrypt with age (uses Dockmesh secrets key)
      </label>
      <label class="flex items-center gap-2 text-sm">
        <input type="checkbox" bind:checked={form.enabled} class="accent-[var(--color-brand-500)]" />
        Enabled
      </label>
    </div>

    <div>
      <label for="prehooks" class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">Pre-hooks (JSON, optional)</label>
      <textarea
        id="prehooks"
        class="dm-input font-mono text-xs h-20 resize-y"
        bind:value={preHooksText}
        placeholder={'[{"container":"postgres","cmd":["pg_dumpall","-U","postgres","-f","/var/lib/postgresql/data/dump.sql"]}]'}
      ></textarea>
    </div>
    <div>
      <label for="posthooks" class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">Post-hooks (JSON, optional)</label>
      <textarea
        id="posthooks"
        class="dm-input font-mono text-xs h-16 resize-y"
        bind:value={postHooksText}
      ></textarea>
    </div>
  </form>

  {#snippet footer()}
    <Button variant="secondary" onclick={() => (showJob = false)}>Cancel</Button>
    <Button variant="primary" type="submit" form="backup-form">{editing ? 'Save' : 'Create'}</Button>
  {/snippet}
</Modal>

<Modal bind:open={showRestore} title="Restore backup" maxWidth="max-w-md">
  {#if restoreRun}
    <p class="text-sm text-[var(--fg-muted)] mb-4">
      Untar this archive into a fresh docker volume. The volume will be created if it
      doesn't exist; existing data is overwritten. Stop any container using the
      target volume first.
    </p>
    <Input label="Destination volume name" bind:value={restoreVolume} />
  {/if}
  {#snippet footer()}
    <Button variant="secondary" onclick={() => (showRestore = false)}>Cancel</Button>
    <Button variant="primary" onclick={doRestore} disabled={!restoreVolume.trim()}>Restore</Button>
  {/snippet}
</Modal>
