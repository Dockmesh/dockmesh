<script lang="ts">
  import { page } from '$app/stores';
  import { goto } from '$app/navigation';
  import { api, ApiError, type ScaleCheck, type PreflightResult, type Migration, type DeployHistoryEntry, type StackDependencies, type StackEnvironments } from '$lib/api';
  import { Button, Card, Badge, Skeleton, Modal } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { allowed } from '$lib/rbac';
  import { hosts } from '$lib/stores/host.svelte';
  import { EventStream } from '$lib/events';
  import { ChevronLeft, Play, Square, Save, Trash2, AlertTriangle, RefreshCw, Server, Maximize2, ArrowRightLeft, CheckCircle2, XCircle, Loader2, GitBranch, Link as LinkIcon, Unlink, History, RotateCcw, FileText, User, Network, Plus, X, Layers } from 'lucide-svelte';
  import type { StackGitSource, StackGitSourceInput } from '$lib/api';

  import { isAllHosts } from '$lib/stores/host.svelte';

  const canWrite = $derived(allowed('stack.write'));
  const canDeploy = $derived(allowed('stack.deploy'));

  // The stack detail page always operates on a specific host, never "all".
  // If the global picker is on "all", we resolve to the deployment's host
  // (from the list response) or fall back to "local".
  let deploymentHostId = $state<string>('local');
  const stackHost = $derived(isAllHosts(hosts.id) ? deploymentHostId : hosts.id);
  const isRemote = $derived(stackHost !== 'local');

  const name = $derived($page.params.name);

  let compose = $state('');
  let env = $state('');
  let services = $state<Array<{ service: string; container_id: string; state: string; status: string; image: string }>>([]);
  let loading = $state(true);
  let busy = $state(false);

  let externalChange = $state<{ file: string; type: string } | null>(null);
  let dirty = $state(false);

  // Tab state (P.12.6 — tabs introduced to host the new History tab and
  // leave room for future Logs / Events / Migrations tabs without
  // further restructuring).
  type TabKey = 'overview' | 'history';
  let activeTab = $state<TabKey>('overview');

  // Deploy history (P.12.6)
  let historyEntries = $state<DeployHistoryEntry[]>([]);
  let historyLoading = $state(false);
  let historyLoadedOnce = $state(false);

  // Modal state: view-yaml and rollback-confirm both operate on a
  // selected entry we fetch-with-YAML on demand (list rows omit YAML).
  let yamlEntry = $state<DeployHistoryEntry | null>(null);
  let showYaml = $state(false);
  let rollbackEntry = $state<DeployHistoryEntry | null>(null);
  let showRollbackConfirm = $state(false);
  let rollbackBusy = $state(false);

  async function loadHistory() {
    historyLoading = true;
    try {
      const raw = await api.stacks.listDeployments(name);
      // Defense-in-depth: backend returns [] for empty, but treat null
      // (older servers, proxy quirks) as empty too so the UI never
      // crashes on a .length access.
      historyEntries = raw ?? [];
      historyLoadedOnce = true;
    } catch (err) {
      toast.error('Load history failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      historyLoading = false;
    }
  }

  async function openYaml(id: number) {
    try {
      yamlEntry = await api.stacks.getDeployment(name, id);
      showYaml = true;
    } catch (err) {
      toast.error('Load snapshot failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function openRollbackConfirm(id: number) {
    try {
      rollbackEntry = await api.stacks.getDeployment(name, id);
      showRollbackConfirm = true;
    } catch (err) {
      toast.error('Load snapshot failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function doRollback() {
    if (!rollbackEntry) return;
    rollbackBusy = true;
    try {
      const res = await api.stacks.rollback(name, rollbackEntry.id, stackHost);
      toast.success(
        `Rolled back to #${res.rolled_back_to}`,
        `${res.result.services.length} service(s) redeployed`
      );
      showRollbackConfirm = false;
      rollbackEntry = null;
      // Reload everything that moved.
      await load();
      await loadHistory();
    } catch (err) {
      toast.error('Rollback failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      rollbackBusy = false;
    }
  }

  function relTime(iso: string): string {
    const d = new Date(iso).getTime();
    const diff = Date.now() - d;
    const m = Math.floor(diff / 60000);
    if (m < 1) return 'just now';
    if (m < 60) return `${m}m ago`;
    const h = Math.floor(m / 60);
    if (h < 24) return `${h}h ago`;
    const days = Math.floor(h / 24);
    if (days < 30) return `${days}d ago`;
    return new Date(iso).toLocaleDateString();
  }

  // Lazy-load history the first time the tab is opened, and refresh after
  // every deploy so a fresh row appears without a manual reload.
  $effect(() => {
    if (activeTab === 'history' && !historyLoadedOnce) {
      loadHistory();
    }
  });

  // Dependencies (P.12.7)
  let deps = $state<StackDependencies | null>(null);
  let showDepsEditor = $state(false);
  let depsEditList = $state<string[]>([]);
  let depsNewEntry = $state('');
  let depsBusy = $state(false);
  // All stack names, for the picker dropdown in the editor.
  let allStackNames = $state<string[]>([]);

  async function loadDeps() {
    try {
      deps = await api.stacks.getDependencies(name);
    } catch {
      deps = null;
    }
  }

  async function openDepsEditor() {
    depsEditList = deps?.depends_on ? [...deps.depends_on] : [];
    depsNewEntry = '';
    try {
      const list = await api.stacks.list();
      allStackNames = list.map((s) => s.name).filter((n) => n !== name);
    } catch {
      allStackNames = [];
    }
    showDepsEditor = true;
  }

  function depAdd(entry: string) {
    const v = entry.trim();
    if (!v || v === name || depsEditList.includes(v)) return;
    depsEditList = [...depsEditList, v];
    depsNewEntry = '';
  }

  function depRemove(entry: string) {
    depsEditList = depsEditList.filter((d) => d !== entry);
  }

  // Environments (P.12.8)
  let envs = $state<StackEnvironments | null>(null);
  let envBusy = $state(false);

  async function loadEnvs() {
    try {
      envs = await api.stacks.getEnvironments(name);
    } catch {
      envs = null;
    }
  }

  async function setActiveEnv(active: string) {
    envBusy = true;
    try {
      await api.stacks.setActiveEnvironment(name, active);
      await loadEnvs();
      toast.success(active ? `Active environment: ${active}` : 'Environment cleared');
    } catch (err) {
      toast.error('Save failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      envBusy = false;
    }
  }

  async function saveDeps() {
    depsBusy = true;
    try {
      await api.stacks.setDependencies(name, depsEditList);
      await loadDeps();
      showDepsEditor = false;
      toast.success('Dependencies updated');
    } catch (err) {
      if (err instanceof ApiError && err.status === 422) {
        toast.error('Cycle detected', 'That would create a dependency loop — pick a different edge.');
      } else {
        toast.error('Save failed', err instanceof ApiError ? err.message : undefined);
      }
    } finally {
      depsBusy = false;
    }
  }

  // Scaling state (P.8)
  let replicaCounts = $state<Map<string, number>>(new Map());
  let showScale = $state(false);
  let scaleTarget = $state('');
  let scaleValue = $state(1);
  let scaleCheck = $state<ScaleCheck | null>(null);
  let scaleBusy = $state(false);
  let scaleForce = $state(false);

  async function loadReplicaCounts() {
    try {
      const list = await api.stacks.listScale(name, stackHost);
      const m = new Map<string, number>();
      for (const e of list) m.set(e.service, e.replicas);
      replicaCounts = m;
    } catch { /* ignore — not critical */ }
  }

  async function openScale(service: string) {
    scaleTarget = service;
    scaleValue = replicaCounts.get(service) ?? 1;
    scaleCheck = null;
    scaleForce = false;
    showScale = true;
    try {
      scaleCheck = await api.stacks.getScale(name, service, stackHost);
      if (scaleCheck) scaleValue = scaleCheck.current_replicas || 1;
    } catch { /* fail open */ }
  }

  async function doScale() {
    scaleBusy = true;
    try {
      const res = await api.stacks.scale(name, scaleTarget, scaleValue, scaleForce, stackHost);
      toast.success('Scaled', `${scaleTarget}: ${res.previous} → ${res.current}`);
      showScale = false;
      await loadReplicaCounts();
      await refreshStatus();
    } catch (err: any) {
      // Check for stateful warning (409 with force_needed).
      if (err?.status === 409) {
        try {
          const body = await err.json?.() ?? err;
          if (body?.force_needed) {
            toast.warning(body.message ?? 'Stateful service warning — enable force to proceed');
            return;
          }
        } catch {}
      }
      toast.error('Scale failed', err instanceof ApiError ? err.message : String(err));
    } finally {
      scaleBusy = false;
    }
  }

  // Migration state (P.9)
  let showMigrate = $state(false);
  let migrateTarget = $state('');
  let migratePreflight = $state<PreflightResult | null>(null);
  let migratePreflightLoading = $state(false);
  let migrateBusy = $state(false);
  let activeMigration = $state<Migration | null>(null);

  async function openMigrate() {
    migrateTarget = '';
    migratePreflight = null;
    showMigrate = true;
  }

  async function runPreflight() {
    if (!migrateTarget) return;
    migratePreflightLoading = true;
    migratePreflight = null;
    try {
      migratePreflight = await api.migrations.preflight(name, migrateTarget);
    } catch (err) {
      toast.error('Preflight failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      migratePreflightLoading = false;
    }
  }

  async function startMigration() {
    migrateBusy = true;
    try {
      const m = await api.migrations.initiate(name, migrateTarget);
      activeMigration = m;
      showMigrate = false;
      toast.success('Migration started', `${name} → ${migrateTarget}`);
      pollMigration(m.id);
    } catch (err) {
      toast.error('Migration failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      migrateBusy = false;
    }
  }

  function pollMigration(id: string) {
    const iv = setInterval(async () => {
      try {
        const m = await api.migrations.get(name, id);
        activeMigration = m;
        if (['completed', 'failed', 'rolled_back'].includes(m.status)) {
          clearInterval(iv);
          if (m.status === 'completed') {
            toast.success('Migration completed');
            await load();
          } else {
            toast.error('Migration ' + m.status, m.error_message);
          }
        }
      } catch {
        clearInterval(iv);
      }
    }, 3000);
  }

  const stream = new EventStream({
    onMessage: (msg) => {
      if (msg.source === 'stacks' && msg.name === name) {
        if (msg.type === 'modified') {
          externalChange = { file: msg.file ?? 'compose.yaml', type: msg.type };
        } else if (msg.type === 'removed' && !msg.file) {
          externalChange = { file: '', type: 'removed' };
        }
      }
      if (msg.source === 'docker' && msg.type === 'container') {
        // Container lifecycle events in our stack → reload status.
        refreshStatus();
      }
    }
  });

  // P.11.11 — git source state
  let gitSource = $state<StackGitSource | null>(null);
  let gitLoading = $state(false);
  let gitBusy = $state(false);
  let showGitDialog = $state(false);
  let gitForm = $state<StackGitSourceInput>({
    repo_url: '', branch: 'main', path_in_repo: '.', auth_kind: 'none',
    auto_deploy: false, poll_interval_sec: 300
  });

  async function loadGitSource() {
    gitLoading = true;
    try {
      gitSource = await api.stacks.getGitSource(name);
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) {
        gitSource = null;
      } else {
        gitSource = null;
      }
    } finally {
      gitLoading = false;
    }
  }

  function openGitDialog() {
    if (gitSource) {
      gitForm = {
        repo_url: gitSource.repo_url,
        branch: gitSource.branch,
        path_in_repo: gitSource.path_in_repo,
        auth_kind: gitSource.auth_kind,
        username: gitSource.username ?? '',
        auto_deploy: gitSource.auto_deploy,
        poll_interval_sec: gitSource.poll_interval_sec
      };
    } else {
      gitForm = { repo_url: '', branch: 'main', path_in_repo: '.', auth_kind: 'none', auto_deploy: false, poll_interval_sec: 300 };
    }
    showGitDialog = true;
  }

  async function saveGitSource(e: Event) {
    e.preventDefault();
    if (!gitForm.repo_url.trim()) return;
    gitBusy = true;
    try {
      const res = await api.stacks.configureGitSource(name, gitForm);
      if (res.sync_error) {
        toast.error('Saved, but first sync failed', res.sync_error);
      } else {
        toast.success('Git source saved', res.sync?.changed ? `synced ${res.sync.new_sha.slice(0, 7)}` : 'up to date');
      }
      showGitDialog = false;
      await loadGitSource();
      await load();
    } catch (err) {
      toast.error('Failed to save', err instanceof ApiError ? err.message : undefined);
    } finally {
      gitBusy = false;
    }
  }

  async function syncNow() {
    if (!gitSource) return;
    gitBusy = true;
    try {
      const res = await api.stacks.syncGitSource(name);
      toast.success(res.changed ? `Synced ${res.new_sha.slice(0, 7)}` : 'Already up to date');
      if (res.deployed) toast.success('Auto-deploy triggered');
      await loadGitSource();
      await load();
    } catch (err) {
      toast.error('Sync failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      gitBusy = false;
    }
  }

  async function disconnectGit() {
    if (!(await confirm.ask({ title: 'Disconnect git source', message: 'Disconnect git source?', body: 'The compose.yaml stays in place on disk. Future pushes to the repo will no longer sync automatically.', confirmLabel: 'Disconnect' }))) return;
    gitBusy = true;
    try {
      await api.stacks.deleteGitSource(name);
      toast.success('Git source disconnected');
      gitSource = null;
    } catch (err) {
      toast.error('Failed to disconnect', err instanceof ApiError ? err.message : undefined);
    } finally {
      gitBusy = false;
    }
  }

  async function load() {
    loading = true;
    externalChange = null;
    dirty = false;
    loadGitSource();
    loadDeps();
    loadEnvs();
    try {
      const detail = await api.stacks.get(name);
      compose = detail.compose;
      env = detail.env ?? '';
      // Resolve deployment host for all-mode routing.
      try {
        const stackList = await api.stacks.list();
        const entry = stackList.find(s => s.name === name);
        if (entry?.deployment?.host_id) {
          deploymentHostId = entry.deployment.host_id;
        }
      } catch { /* ignore — fallback to local */ }
      try {
        services = await api.stacks.status(name, stackHost);
      } catch {
        services = [];
      }
      await loadReplicaCounts();
    } catch (err) {
      toast.error('Load failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      loading = false;
    }
  }

  async function refreshStatus() {
    try {
      services = await api.stacks.status(name, stackHost);
    } catch { /* ignore */ }
  }

  // Re-load whenever the resolved host changes.
  {
    let prev: string | null = null;
    $effect(() => {
      const cur = stackHost;
      if (prev === null) { prev = cur; return; }
      if (cur !== prev) { prev = cur; refreshStatus(); }
    });
  }

  async function save() {
    busy = true;
    try {
      await api.stacks.update(name, compose, env || undefined);
      dirty = false;
      toast.success('Saved');
    } catch (err) {
      toast.error('Save failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      busy = false;
    }
  }

  async function deploy() {
    busy = true;
    try {
      const res = await api.stacks.deploy(name, stackHost);
      toast.success('Deployed', `${res.services.length} service(s) on ${hosts.selected?.name ?? 'local'}`);
      await refreshStatus();
      // If the user has opened History at least once, freshen it so
      // this deploy's new row appears without a manual reload.
      if (historyLoadedOnce) loadHistory();
    } catch (err) {
      toast.error('Deploy failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      busy = false;
    }
  }

  async function stop() {
    busy = true;
    try {
      await api.stacks.stop(name, stackHost);
      services = [];
      toast.info('Stopped');
    } catch (err) {
      toast.error('Stop failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      busy = false;
    }
  }

  async function del() {
    if (!(await confirm.ask({
      title: `Delete stack ${name}`,
      message: 'This removes compose.yaml from the stacks directory on disk.',
      body: 'Running containers continue to run. Docker volumes, networks, and images are NOT removed — stop the stack first if you want the cleanup to run.',
      confirmLabel: 'Delete',
      danger: true
    }))) return;
    busy = true;
    try {
      await api.stacks.delete(name);
      toast.success('Deleted', name);
      goto('/stacks');
    } catch (err) {
      toast.error('Delete failed', err instanceof ApiError ? err.message : undefined);
      busy = false;
    }
  }

  $effect(() => {
    if (name) {
      load();
      stream.start();
    }
    return stream.stop;
  });
</script>

<section class="space-y-5">
  <!-- Breadcrumb -->
  <a href="/stacks" class="inline-flex items-center gap-1 text-sm text-[var(--fg-muted)] hover:text-[var(--fg)]">
    <ChevronLeft class="w-4 h-4" />
    Stacks
  </a>

  <!-- Header + actions -->
  <div class="flex items-center justify-between flex-wrap gap-3">
    <div class="flex items-center gap-3 min-w-0">
      <h2 class="text-2xl font-semibold tracking-tight truncate">{name}</h2>
      {#if isRemote}
        <span class="inline-flex items-center gap-1.5 text-xs px-2 py-0.5 rounded border border-[var(--color-brand-500)]/40 bg-[color-mix(in_srgb,var(--color-brand-500)_10%,transparent)] text-[var(--color-brand-400)]">
          <Server class="w-3 h-3" />
          {hosts.selected?.name}
        </span>
      {/if}
      {#if services.length > 0}
        <Badge variant={services.every((s) => s.state === 'running') ? 'success' : 'warning'} dot>
          {services.filter((s) => s.state === 'running').length}/{services.length} running
        </Badge>
      {/if}
    </div>
    <div class="flex gap-2 flex-wrap">
      {#if canDeploy}
        <Button variant="primary" onclick={deploy} loading={busy} disabled={busy}>
          <Play class="w-4 h-4" />
          Deploy
        </Button>
        {#if hosts.available.length > 1}
          <Button variant="secondary" onclick={openMigrate} disabled={busy}>
            <ArrowRightLeft class="w-4 h-4" />
            Migrate
          </Button>
        {/if}
        <Button variant="secondary" onclick={stop} disabled={busy}>
          <Square class="w-4 h-4" />
          Stop
        </Button>
      {/if}
      {#if canWrite}
        <Button variant="secondary" onclick={save} disabled={busy || !dirty}>
          <Save class="w-4 h-4" />
          Save
        </Button>
        <Button variant="danger" onclick={del} disabled={busy}>
          <Trash2 class="w-4 h-4" />
          Delete
        </Button>
      {/if}
    </div>
  </div>

  {#if externalChange}
    <div class="dm-card p-4 border-[color-mix(in_srgb,var(--color-warning-500)_40%,transparent)] flex items-start gap-3">
      <AlertTriangle class="w-5 h-5 text-[var(--color-warning-400)] shrink-0 mt-0.5" />
      <div class="flex-1 text-sm">
        <div class="font-medium text-[var(--color-warning-400)]">
          {externalChange.file || 'Stack directory'} was {externalChange.type} outside Dockmesh
        </div>
        {#if dirty}
          <div class="text-xs text-[var(--color-danger-400)] mt-1">
            You have unsaved edits — reloading will discard them.
          </div>
        {:else}
          <div class="text-xs text-[var(--fg-muted)] mt-1">
            Reload to pick up the external change.
          </div>
        {/if}
      </div>
      <div class="flex gap-2 shrink-0">
        <Button size="sm" variant="secondary" onclick={load}>
          <RefreshCw class="w-3.5 h-3.5" />
          Reload
        </Button>
        <Button size="sm" variant="ghost" onclick={() => (externalChange = null)}>Ignore</Button>
      </div>
    </div>
  {/if}

  <!-- Git source (P.11.11) -->
  {#if !gitLoading}
    {#if gitSource}
      <Card class="p-4">
        <div class="flex items-start gap-3 flex-wrap">
          <GitBranch class="w-4 h-4 text-[var(--fg-muted)] mt-0.5 shrink-0" />
          <div class="flex-1 min-w-0">
            <div class="flex items-center gap-2 flex-wrap">
              <span class="text-sm font-medium truncate">{gitSource.repo_url}</span>
              <Badge variant="default">{gitSource.branch}</Badge>
              {#if gitSource.auto_deploy}
                <Badge variant="success">auto-deploy</Badge>
              {/if}
              {#if gitSource.has_webhook_secret}
                <Badge variant="info">webhook</Badge>
              {/if}
            </div>
            <div class="text-xs text-[var(--fg-muted)] mt-1 font-mono">
              {#if gitSource.last_sync_sha}
                {gitSource.last_sync_sha.slice(0, 7)}
                {#if gitSource.last_sync_at}
                  · synced {new Date(gitSource.last_sync_at).toLocaleString()}
                {/if}
              {:else}
                never synced
              {/if}
            </div>
            {#if gitSource.last_sync_error}
              <div class="text-xs text-[var(--color-danger-400)] mt-1">
                <AlertTriangle class="w-3 h-3 inline mr-1" />
                {gitSource.last_sync_error}
              </div>
            {/if}
          </div>
          {#if canWrite}
            <div class="flex items-center gap-1 shrink-0">
              <Button variant="secondary" onclick={syncNow} disabled={gitBusy}>
                <RefreshCw class="w-3.5 h-3.5 {gitBusy ? 'animate-spin' : ''}" />
                Sync now
              </Button>
              <Button variant="ghost" onclick={openGitDialog} disabled={gitBusy}>Edit</Button>
              <Button variant="ghost" onclick={disconnectGit} disabled={gitBusy}>
                <Unlink class="w-3.5 h-3.5" />
              </Button>
            </div>
          {/if}
        </div>
      </Card>
    {:else if canWrite}
      <div class="flex items-center gap-2 text-xs text-[var(--fg-muted)]">
        <GitBranch class="w-3.5 h-3.5" />
        No git source.
        <button class="underline hover:text-[var(--fg)]" onclick={openGitDialog}>Connect a repository</button>
      </div>
    {/if}
  {/if}

  <!-- Active migration banner -->
  {#if activeMigration && !activeMigration.completed_at}
    <Card class="p-4 border-[var(--color-brand-500)]/30">
      <div class="flex items-center gap-3">
        <Loader2 class="w-5 h-5 text-[var(--color-brand-400)] animate-spin shrink-0" />
        <div class="flex-1 min-w-0">
          <div class="text-sm font-medium">
            Migrating to {activeMigration.target_host_id}
          </div>
          <div class="text-xs text-[var(--fg-muted)]">
            Phase: {activeMigration.phase ?? activeMigration.status}
            {#if activeMigration.progress?.current_volume}
              — Volume {activeMigration.progress.volume_index}/{activeMigration.progress.volumes_total}: {activeMigration.progress.current_volume}
            {/if}
          </div>
        </div>
        <Badge variant="info" dot>{activeMigration.status}</Badge>
      </div>
    </Card>
  {/if}

  <!-- Tabs (P.12.6 — added to host History; future home for Logs / Events / Migrations) -->
  <div class="border-b border-[var(--border)]">
    <div class="flex gap-1" role="tablist" aria-label="Stack sections">
      <button
        role="tab"
        aria-selected={activeTab === 'overview'}
        class="px-4 py-2 text-sm font-medium border-b-2 -mb-px transition-colors {activeTab === 'overview' ? 'border-[var(--color-brand-500)] text-[var(--fg)]' : 'border-transparent text-[var(--fg-muted)] hover:text-[var(--fg)]'}"
        onclick={() => (activeTab = 'overview')}
      >
        Overview
      </button>
      <button
        role="tab"
        aria-selected={activeTab === 'history'}
        class="px-4 py-2 text-sm font-medium border-b-2 -mb-px inline-flex items-center gap-1.5 transition-colors {activeTab === 'history' ? 'border-[var(--color-brand-500)] text-[var(--fg)]' : 'border-transparent text-[var(--fg-muted)] hover:text-[var(--fg)]'}"
        onclick={() => (activeTab = 'history')}
      >
        <History class="w-3.5 h-3.5" />
        History
      </button>
    </div>
  </div>

  {#if activeTab === 'overview'}
  <!-- Services -->
  {#if loading}
    <Card class="p-5 space-y-3">
      <Skeleton width="30%" height="1rem" />
      <Skeleton width="100%" height="3rem" />
    </Card>
  {:else if services.length > 0}
    <Card>
      <div class="px-5 py-3 border-b border-[var(--border)] text-xs font-medium text-[var(--fg-muted)] uppercase tracking-wider">
        Services
      </div>
      <div class="divide-y divide-[var(--border)]">
        {#each services as s}
          {@const count = replicaCounts.get(s.service) ?? 0}
          <div class="flex items-center gap-3 px-5 py-3 hover:bg-[var(--surface-hover)] transition-colors">
            <a href={`/containers/${s.container_id}`} class="flex items-center gap-3 flex-1 min-w-0">
              <Badge variant={s.state === 'running' ? 'success' : 'default'} dot>{s.state}</Badge>
              <div class="min-w-0 flex-1">
                <div class="font-mono text-sm flex items-center gap-1.5">
                  {s.service}
                  {#if count > 1}
                    <span class="text-[10px] font-semibold px-1.5 py-0.5 rounded-full bg-[color-mix(in_srgb,var(--color-brand-500)_15%,transparent)] text-[var(--color-brand-400)]">
                      x{count}
                    </span>
                  {/if}
                </div>
                <div class="text-xs text-[var(--fg-muted)] truncate">{s.image}</div>
              </div>
              <div class="text-xs text-[var(--fg-subtle)] text-right">{s.status}</div>
            </a>
            {#if canDeploy}
              <button
                class="p-1.5 rounded-md text-[var(--fg-muted)] hover:text-[var(--fg)] hover:bg-[var(--surface-hover)] shrink-0"
                title="Scale {s.service}"
                aria-label="Scale {s.service}"
                onclick={() => openScale(s.service)}
              >
                <Maximize2 class="w-3.5 h-3.5" />
              </button>
            {/if}
          </div>
        {/each}
      </div>
    </Card>
  {/if}

  <!-- Environment overrides (P.12.8) -->
  {#if envs && envs.available.length > 0}
    <Card class="p-4">
      <div class="flex items-start gap-3">
        <Layers class="w-4 h-4 text-[var(--fg-muted)] mt-0.5 shrink-0" />
        <div class="flex-1 min-w-0 space-y-2">
          <div class="flex items-center justify-between gap-2 flex-wrap">
            <div class="text-xs font-medium uppercase tracking-wider text-[var(--fg-muted)]">Environment</div>
            {#if envs.active}
              <Badge variant="info">{envs.active}</Badge>
            {:else}
              <Badge variant="default">base</Badge>
            {/if}
          </div>
          <div class="text-xs text-[var(--fg-muted)]">
            This stack has {envs.available.length} overlay{envs.available.length > 1 ? 's' : ''} next to <span class="font-mono">compose.yaml</span>.
            {#if envs.active}
              Deploys merge <span class="font-mono">compose.{envs.active}.yaml</span> on top.
            {:else}
              Deploys use the base <span class="font-mono">compose.yaml</span> as-is.
            {/if}
          </div>
          {#if canWrite}
            <div class="flex items-center gap-1.5 flex-wrap pt-1">
              <button
                class="px-2 py-0.5 rounded border text-xs {envs.active === '' ? 'border-[var(--color-brand-500)] bg-[color-mix(in_srgb,var(--color-brand-500)_15%,transparent)]' : 'border-[var(--border)] hover:bg-[var(--surface-hover)]'}"
                onclick={() => setActiveEnv('')}
                disabled={envBusy}
              >base</button>
              {#each envs.available as e}
                <button
                  class="px-2 py-0.5 rounded border text-xs font-mono {envs.active === e ? 'border-[var(--color-brand-500)] bg-[color-mix(in_srgb,var(--color-brand-500)_15%,transparent)]' : 'border-[var(--border)] hover:bg-[var(--surface-hover)]'}"
                  onclick={() => setActiveEnv(e)}
                  disabled={envBusy}
                >{e}</button>
              {/each}
            </div>
          {/if}
        </div>
      </div>
    </Card>
  {/if}

  <!-- Dependencies (P.12.7) -->
  {#if deps && (deps.depends_on.length > 0 || deps.dependents.length > 0 || canWrite)}
    <Card class="p-4">
      <div class="flex items-start gap-3">
        <Network class="w-4 h-4 text-[var(--fg-muted)] mt-0.5 shrink-0" />
        <div class="flex-1 min-w-0 space-y-2">
          <div class="flex items-center justify-between gap-2 flex-wrap">
            <div class="text-xs font-medium uppercase tracking-wider text-[var(--fg-muted)]">Dependencies</div>
            {#if canWrite}
              <button
                class="text-xs text-[var(--fg-muted)] hover:text-[var(--fg)] underline"
                onclick={openDepsEditor}
              >Edit</button>
            {/if}
          </div>
          <div class="text-xs">
            {#if deps.depends_on.length > 0}
              <div class="flex items-center gap-1.5 flex-wrap">
                <span class="text-[var(--fg-muted)]">Needs:</span>
                {#each deps.depends_on as d}
                  <a href={`/stacks/${encodeURIComponent(d)}`} class="inline-flex items-center gap-1 px-2 py-0.5 rounded border border-[var(--border)] font-mono hover:bg-[var(--surface-hover)]">
                    {d}
                  </a>
                {/each}
              </div>
            {:else}
              <div class="text-[var(--fg-muted)]">No prerequisites.</div>
            {/if}
          </div>
          {#if deps.dependents.length > 0}
            <div class="text-xs flex items-center gap-1.5 flex-wrap">
              <span class="text-[var(--fg-muted)]">Needed by:</span>
              {#each deps.dependents as d}
                <a href={`/stacks/${encodeURIComponent(d)}`} class="inline-flex items-center gap-1 px-2 py-0.5 rounded border border-[var(--border)] font-mono hover:bg-[var(--surface-hover)]">
                  {d}
                </a>
              {/each}
            </div>
          {/if}
        </div>
      </div>
    </Card>
  {/if}

  <!-- Editor -->
  <Card>
    <div class="px-5 py-3 border-b border-[var(--border)] text-xs font-medium text-[var(--fg-muted)] uppercase tracking-wider flex items-center justify-between">
      <span>compose.yaml</span>
      {#if dirty}<span class="text-[var(--color-warning-400)] normal-case">unsaved</span>{/if}
    </div>
    <textarea
      class="dm-input rounded-none border-0 border-t-0 font-mono text-xs h-96 resize-y"
      style="border: none; background: transparent;"
      bind:value={compose}
      oninput={() => (dirty = true)}
    ></textarea>
  </Card>

  <Card>
    <div class="px-5 py-3 border-b border-[var(--border)] text-xs font-medium text-[var(--fg-muted)] uppercase tracking-wider">
      .env (optional)
    </div>
    <textarea
      class="dm-input rounded-none border-0 font-mono text-xs h-24 resize-y"
      style="border: none; background: transparent;"
      bind:value={env}
      oninput={() => (dirty = true)}
      placeholder="KEY=value"
    ></textarea>
  </Card>
  {/if}

  <!-- History tab (P.12.6) -->
  {#if activeTab === 'history'}
    {#if historyLoading && historyEntries.length === 0}
      <Card class="p-5 space-y-3">
        <Skeleton width="40%" height="1rem" />
        <Skeleton width="100%" height="2.5rem" />
        <Skeleton width="100%" height="2.5rem" />
      </Card>
    {:else if historyEntries.length === 0}
      <Card class="p-8 text-center">
        <History class="w-8 h-8 text-[var(--fg-subtle)] mx-auto mb-2" />
        <div class="text-sm font-medium">No deploy history yet</div>
        <div class="text-xs text-[var(--fg-muted)] mt-1">
          The next successful deploy will show up here and you'll be able to roll back to it.
        </div>
      </Card>
    {:else}
      <Card>
        <div class="px-5 py-3 border-b border-[var(--border)] text-xs font-medium text-[var(--fg-muted)] uppercase tracking-wider flex items-center justify-between">
          <span>Deploy history</span>
          <button
            class="inline-flex items-center gap-1 normal-case text-[var(--fg-muted)] hover:text-[var(--fg)]"
            onclick={loadHistory}
            disabled={historyLoading}
            title="Refresh"
            aria-label="Refresh history"
          >
            <RefreshCw class="w-3.5 h-3.5 {historyLoading ? 'animate-spin' : ''}" />
          </button>
        </div>
        <div class="divide-y divide-[var(--border)]">
          {#each historyEntries as entry, i}
            <div class="px-5 py-3 flex items-start gap-3 hover:bg-[var(--surface-hover)] transition-colors">
              <div class="flex-1 min-w-0">
                <div class="flex items-center gap-2 flex-wrap">
                  <span class="text-sm font-medium" title={new Date(entry.deployed_at).toLocaleString()}>
                    {relTime(entry.deployed_at)}
                  </span>
                  {#if i === 0}
                    <Badge variant="success">current</Badge>
                  {/if}
                  {#if entry.note}
                    <Badge variant="info">{entry.note}</Badge>
                  {/if}
                </div>
                <div class="text-xs text-[var(--fg-muted)] mt-1 flex items-center gap-3 flex-wrap">
                  {#if entry.deployed_by_name}
                    <span class="inline-flex items-center gap-1">
                      <User class="w-3 h-3" />
                      {entry.deployed_by_name}
                    </span>
                  {/if}
                  <span class="font-mono">#{entry.id}</span>
                  {#if entry.services && entry.services.length > 0}
                    <span class="truncate">
                      {entry.services.length} service{entry.services.length > 1 ? 's' : ''}:
                      <span class="font-mono">{entry.services.map(s => s.image).join(', ')}</span>
                    </span>
                  {/if}
                </div>
              </div>
              <div class="flex items-center gap-1 shrink-0">
                <button
                  class="p-1.5 rounded-md text-[var(--fg-muted)] hover:text-[var(--fg)] hover:bg-[var(--surface-hover)]"
                  title="View compose.yaml"
                  aria-label="View compose.yaml"
                  onclick={() => openYaml(entry.id)}
                >
                  <FileText class="w-3.5 h-3.5" />
                </button>
                {#if canDeploy && i !== 0}
                  <button
                    class="p-1.5 rounded-md text-[var(--fg-muted)] hover:text-[var(--color-warning-400)] hover:bg-[var(--surface-hover)]"
                    title="Roll back to this version"
                    aria-label="Roll back to this version"
                    onclick={() => openRollbackConfirm(entry.id)}
                  >
                    <RotateCcw class="w-3.5 h-3.5" />
                  </button>
                {/if}
              </div>
            </div>
          {/each}
        </div>
      </Card>
    {/if}
  {/if}
</section>

<!-- View YAML snapshot modal (P.12.6) -->
<Modal bind:open={showYaml} title={yamlEntry ? `Deploy #${yamlEntry.id} — ${new Date(yamlEntry.deployed_at).toLocaleString()}` : 'Deploy snapshot'} maxWidth="max-w-3xl">
  {#if yamlEntry}
    <div class="space-y-3">
      <div class="text-xs text-[var(--fg-muted)] flex items-center gap-3 flex-wrap">
        {#if yamlEntry.deployed_by_name}
          <span class="inline-flex items-center gap-1">
            <User class="w-3 h-3" />
            {yamlEntry.deployed_by_name}
          </span>
        {/if}
        {#if yamlEntry.note}
          <Badge variant="info">{yamlEntry.note}</Badge>
        {/if}
      </div>
      {#if yamlEntry.services && yamlEntry.services.length > 0}
        <div class="text-xs space-y-0.5 border border-[var(--border)] rounded-md p-3 bg-[var(--surface)]">
          <div class="font-medium text-[var(--fg-muted)] uppercase tracking-wider text-[10px] mb-1">Resolved images</div>
          {#each yamlEntry.services as svc}
            <div class="font-mono flex gap-2">
              <span class="text-[var(--fg-muted)]">{svc.service}</span>
              <span>{svc.image}</span>
            </div>
          {/each}
        </div>
      {/if}
      <pre class="border border-[var(--border)] rounded-md p-3 bg-[var(--surface)] text-xs font-mono overflow-auto max-h-96 whitespace-pre-wrap">{yamlEntry.compose_yaml}</pre>
    </div>
  {/if}
  <svelte:fragment slot="footer">
    <Button variant="secondary" onclick={() => (showYaml = false)}>Close</Button>
  </svelte:fragment>
</Modal>

<!-- Dependencies editor modal (P.12.7) -->
<Modal bind:open={showDepsEditor} title="Dependencies for {name}" maxWidth="max-w-lg">
  <div class="space-y-4 text-sm">
    <div class="text-xs text-[var(--fg-muted)]">
      Stacks listed here will be deployed first (if they aren't already running) whenever you deploy
      <span class="font-mono">{name}</span>. Deep chains deploy bottom-up. Cycles are rejected.
    </div>
    <div class="space-y-2">
      {#if depsEditList.length === 0}
        <div class="text-xs text-[var(--fg-muted)] italic">No prerequisites yet.</div>
      {:else}
        <div class="flex flex-wrap gap-1.5">
          {#each depsEditList as d}
            <div class="inline-flex items-center gap-1 px-2 py-0.5 rounded border border-[var(--border)] font-mono text-xs">
              <span>{d}</span>
              <button
                class="text-[var(--fg-muted)] hover:text-[var(--color-danger-400)]"
                onclick={() => depRemove(d)}
                title="Remove"
                aria-label="Remove {d}"
              >
                <X class="w-3 h-3" />
              </button>
            </div>
          {/each}
        </div>
      {/if}
    </div>
    <div class="space-y-1">
      <label class="text-xs font-medium text-[var(--fg-muted)]" for="dep-picker">Add a dependency</label>
      <div class="flex gap-2">
        <input
          id="dep-picker"
          class="dm-input flex-1"
          list="dep-stack-options"
          bind:value={depsNewEntry}
          placeholder="Pick a stack…"
          onkeydown={(e) => { if (e.key === 'Enter') { e.preventDefault(); depAdd(depsNewEntry); } }}
        />
        <datalist id="dep-stack-options">
          {#each allStackNames as s}
            <option value={s}></option>
          {/each}
        </datalist>
        <Button variant="secondary" onclick={() => depAdd(depsNewEntry)} disabled={!depsNewEntry.trim()}>
          <Plus class="w-3.5 h-3.5" />
          Add
        </Button>
      </div>
      <div class="text-[11px] text-[var(--fg-muted)]">
        Unknown stack names are accepted — declaring an edge for a stack you haven't created yet is fine.
      </div>
    </div>
  </div>
  <svelte:fragment slot="footer">
    <Button variant="secondary" onclick={() => (showDepsEditor = false)} disabled={depsBusy}>Cancel</Button>
    <Button variant="primary" onclick={saveDeps} loading={depsBusy} disabled={depsBusy}>Save</Button>
  </svelte:fragment>
</Modal>

<!-- Rollback confirm modal (P.12.6) -->
<Modal bind:open={showRollbackConfirm} title="Roll back to deploy #{rollbackEntry?.id}" maxWidth="max-w-lg">
  {#if rollbackEntry}
    <div class="space-y-4 text-sm">
      <div class="flex items-start gap-3 p-3 rounded-md border border-[color-mix(in_srgb,var(--color-warning-500)_40%,transparent)] bg-[color-mix(in_srgb,var(--color-warning-500)_8%,transparent)]">
        <AlertTriangle class="w-4 h-4 text-[var(--color-warning-400)] shrink-0 mt-0.5" />
        <div class="space-y-1">
          <div>
            This will overwrite <span class="font-mono">compose.yaml</span> with the snapshot from
            <span class="font-medium">{new Date(rollbackEntry.deployed_at).toLocaleString()}</span>
            and redeploy the stack.
          </div>
          <div class="text-xs text-[var(--fg-muted)]">
            Your current <span class="font-mono">.env</span> is kept as-is — secrets added or changed
            since this deploy will still use their current values. If you need to roll env back too,
            restore it manually after rollback.
          </div>
        </div>
      </div>
      {#if rollbackEntry.services && rollbackEntry.services.length > 0}
        <div class="text-xs space-y-0.5 border border-[var(--border)] rounded-md p-3 bg-[var(--surface)]">
          <div class="font-medium text-[var(--fg-muted)] uppercase tracking-wider text-[10px] mb-1">Images that will be redeployed</div>
          {#each rollbackEntry.services as svc}
            <div class="font-mono flex gap-2">
              <span class="text-[var(--fg-muted)]">{svc.service}</span>
              <span>{svc.image}</span>
            </div>
          {/each}
        </div>
      {/if}
    </div>
  {/if}
  <svelte:fragment slot="footer">
    <Button variant="secondary" onclick={() => (showRollbackConfirm = false)} disabled={rollbackBusy}>Cancel</Button>
    <Button variant="primary" onclick={doRollback} loading={rollbackBusy} disabled={rollbackBusy}>
      <RotateCcw class="w-4 h-4" />
      Roll back
    </Button>
  </svelte:fragment>
</Modal>

<!-- Scale modal -->
<Modal bind:open={showScale} title="Scale {scaleTarget}" maxWidth="max-w-sm">
  <div class="space-y-4">
    <div>
      <label for="scale-slider" class="block text-xs font-medium text-[var(--fg-muted)] mb-2">
        Replicas: <span class="text-[var(--fg)] font-bold text-lg">{scaleValue}</span>
      </label>
      <input
        id="scale-slider"
        type="range"
        min="0"
        max="10"
        step="1"
        bind:value={scaleValue}
        class="w-full accent-[var(--color-brand-500)]"
      />
      <div class="flex justify-between text-[10px] text-[var(--fg-subtle)] mt-1">
        <span>0</span><span>5</span><span>10</span>
      </div>
    </div>

    {#if scaleCheck?.has_container_name}
      <div class="p-3 rounded-lg bg-[color-mix(in_srgb,var(--color-danger-500)_10%,transparent)] border border-[color-mix(in_srgb,var(--color-danger-500)_30%,transparent)] text-xs text-[var(--color-danger-400)] flex items-start gap-2">
        <AlertTriangle class="w-4 h-4 shrink-0 mt-0.5" />
        <div>This service has <code class="font-mono">container_name</code> set. Remove it in the compose file to allow scaling beyond 1.</div>
      </div>
    {/if}

    {#if scaleCheck?.has_hard_port}
      <div class="p-3 rounded-lg bg-[color-mix(in_srgb,var(--color-danger-500)_10%,transparent)] border border-[color-mix(in_srgb,var(--color-danger-500)_30%,transparent)] text-xs text-[var(--color-danger-400)] flex items-start gap-2">
        <AlertTriangle class="w-4 h-4 shrink-0 mt-0.5" />
        <div>Hard-coded host port <code class="font-mono">{scaleCheck.hard_port_detail}</code>. Use a port range or remove the binding to scale beyond 1.</div>
      </div>
    {/if}

    {#if scaleCheck?.is_stateful && scaleValue > 1}
      <div class="p-3 rounded-lg bg-[color-mix(in_srgb,var(--color-warning-500)_10%,transparent)] border border-[color-mix(in_srgb,var(--color-warning-500)_30%,transparent)] text-xs text-[var(--color-warning-400)]">
        <div class="flex items-start gap-2">
          <AlertTriangle class="w-4 h-4 shrink-0 mt-0.5" />
          <div>
            This service looks like a database (<strong>{scaleCheck.stateful_image}</strong>) with mounted volumes.
            Scaling may cause data corruption.
          </div>
        </div>
        <label class="flex items-center gap-2 mt-2 cursor-pointer">
          <input type="checkbox" bind:checked={scaleForce} class="rounded" />
          <span>I understand the risk — proceed anyway</span>
        </label>
      </div>
    {/if}
  </div>

  {#snippet footer()}
    <Button variant="secondary" onclick={() => (showScale = false)}>Cancel</Button>
    <Button
      variant="primary"
      loading={scaleBusy}
      disabled={scaleBusy || (scaleValue > 1 && (scaleCheck?.has_container_name || scaleCheck?.has_hard_port)) || (scaleCheck?.is_stateful && scaleValue > 1 && !scaleForce)}
      onclick={doScale}
    >
      Scale to {scaleValue}
    </Button>
  {/snippet}
</Modal>

<!-- Migrate modal -->
<Modal bind:open={showMigrate} title="Migrate {name}" maxWidth="max-w-lg">
  <div class="space-y-4">
    <div>
      <label for="migrate-target" class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">Target host</label>
      <select
        id="migrate-target"
        class="dm-input text-sm"
        bind:value={migrateTarget}
        onchange={() => { migratePreflight = null; if (migrateTarget) runPreflight(); }}
      >
        <option value="">Select a host…</option>
        {#each hosts.available.filter(h => h.id !== stackHost && h.id !== 'all') as h}
          <option value={h.id} disabled={h.status !== 'online'}>{h.name} {h.status !== 'online' ? `(${h.status})` : ''}</option>
        {/each}
      </select>
    </div>

    {#if migratePreflightLoading}
      <div class="flex items-center gap-2 text-sm text-[var(--fg-muted)]">
        <Loader2 class="w-4 h-4 animate-spin" /> Running pre-flight checks…
      </div>
    {/if}

    {#if migratePreflight}
      <div class="space-y-1.5">
        <div class="text-xs font-medium text-[var(--fg-muted)] uppercase tracking-wider">Pre-flight checks</div>
        {#each migratePreflight.checks as check}
          <div class="flex items-center gap-2 text-xs">
            {#if check.passed}
              <CheckCircle2 class="w-3.5 h-3.5 text-[var(--color-success-400)] shrink-0" />
            {:else}
              <XCircle class="w-3.5 h-3.5 text-[var(--color-danger-400)] shrink-0" />
            {/if}
            <span class="font-medium">{check.name.replace(/_/g, ' ')}</span>
            {#if check.detail}
              <span class="text-[var(--fg-muted)] truncate">{check.detail}</span>
            {/if}
          </div>
        {/each}
      </div>

      {#if !migratePreflight.passed}
        <div class="p-3 rounded-lg bg-[color-mix(in_srgb,var(--color-danger-500)_10%,transparent)] border border-[color-mix(in_srgb,var(--color-danger-500)_30%,transparent)] text-xs text-[var(--color-danger-400)]">
          Pre-flight checks failed. Fix the issues above before migrating.
        </div>
      {/if}
    {/if}

    <p class="text-xs text-[var(--fg-muted)]">
      The stack will be <strong>stopped</strong> on the current host during transfer (Safe Mode).
      Downtime depends on volume size.
    </p>
  </div>

  {#snippet footer()}
    <Button variant="secondary" onclick={() => (showMigrate = false)}>Cancel</Button>
    <Button
      variant="primary"
      loading={migrateBusy}
      disabled={migrateBusy || !migrateTarget || migratePreflightLoading || (migratePreflight && !migratePreflight.passed)}
      onclick={startMigration}
    >
      <ArrowRightLeft class="w-4 h-4" />
      Start migration
    </Button>
  {/snippet}
</Modal>

<!-- Git source configure dialog (P.11.11) -->
<Modal bind:open={showGitDialog} title={gitSource ? 'Edit git source' : 'Connect a git repository'} maxWidth="max-w-lg">
  <form onsubmit={saveGitSource} id="git-form" class="space-y-4">
    <div>
      <label class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5" for="git-repo">Repository URL</label>
      <input id="git-repo" class="dm-input" placeholder="https://github.com/acme/stack.git" bind:value={gitForm.repo_url} />
    </div>
    <div class="grid grid-cols-2 gap-3">
      <div>
        <label class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5" for="git-branch">Branch</label>
        <input id="git-branch" class="dm-input" bind:value={gitForm.branch as any} />
      </div>
      <div>
        <label class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5" for="git-path">Path in repo</label>
        <input id="git-path" class="dm-input" placeholder="." bind:value={gitForm.path_in_repo as any} />
      </div>
    </div>
    <div>
      <label class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5" for="git-auth">Authentication</label>
      <select id="git-auth" class="dm-input" bind:value={gitForm.auth_kind as any}>
        <option value="none">None (public repo)</option>
        <option value="http">HTTPS username + password / token</option>
        <option value="ssh">SSH private key</option>
      </select>
    </div>
    {#if gitForm.auth_kind === 'http'}
      <div>
        <label class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5" for="git-user">Username</label>
        <input id="git-user" class="dm-input" bind:value={gitForm.username as any} />
      </div>
      <div>
        <label class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5" for="git-pass">
          Password / Token
          {#if gitSource?.has_password}<span class="font-normal normal-case">— leave blank to keep existing</span>{/if}
        </label>
        <input id="git-pass" type="password" class="dm-input" bind:value={gitForm.password as any} />
      </div>
    {:else if gitForm.auth_kind === 'ssh'}
      <div>
        <label class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5" for="git-sshuser">SSH user</label>
        <input id="git-sshuser" class="dm-input" placeholder="git" bind:value={gitForm.username as any} />
      </div>
      <div>
        <label class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5" for="git-sshkey">
          Private key (PEM)
          {#if gitSource?.has_ssh_key}<span class="font-normal normal-case">— leave blank to keep existing</span>{/if}
        </label>
        <textarea id="git-sshkey" class="dm-input font-mono text-xs" rows="5" bind:value={gitForm.ssh_key as any}></textarea>
      </div>
    {/if}
    <div class="grid grid-cols-2 gap-3">
      <div>
        <label class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5" for="git-poll">Poll interval (sec)</label>
        <input id="git-poll" type="number" min="60" class="dm-input" bind:value={gitForm.poll_interval_sec as any} />
        <p class="text-xs text-[var(--fg-muted)] mt-1">60+ sec, or 0 for manual / webhook-only.</p>
      </div>
      <div>
        <label class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5" for="git-webhook">
          Webhook secret (HMAC)
          {#if gitSource?.has_webhook_secret}<span class="font-normal normal-case">— set to keep</span>{/if}
        </label>
        <input id="git-webhook" type="password" class="dm-input" bind:value={gitForm.webhook_secret as any} />
      </div>
    </div>
    <label class="flex items-center gap-2 text-sm">
      <input type="checkbox" bind:checked={gitForm.auto_deploy as any} />
      Auto-deploy on new commits
    </label>
    {#if gitSource?.has_webhook_secret || (gitSource && !gitSource.has_webhook_secret)}
      <div class="text-xs text-[var(--fg-muted)] bg-[var(--bg-muted)] rounded p-2 border border-[var(--border)]">
        <p class="font-medium text-[var(--fg)] mb-1">Webhook URL</p>
        <code class="text-[11px] font-mono break-all">POST /api/v1/stacks/{name}/git/webhook</code>
      </div>
    {/if}
  </form>
  {#snippet footer()}
    <Button variant="ghost" onclick={() => (showGitDialog = false)}>Cancel</Button>
    <Button variant="primary" onclick={saveGitSource} disabled={gitBusy || !gitForm.repo_url}>
      <LinkIcon class="w-3.5 h-3.5" />
      {gitBusy ? 'Saving…' : gitSource ? 'Save' : 'Connect & sync'}
    </Button>
  {/snippet}
</Modal>
