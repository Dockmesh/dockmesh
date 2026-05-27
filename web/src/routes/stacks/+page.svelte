<script lang="ts">
  import { untrack } from 'svelte';
  import {
    api, ApiError, isFanOut,
    type StackDeployment, type DiscoveredStack, type StackGitSourceInput, type EnvDrift,
  } from '$lib/api';
  import { Button, Modal, Input, Skeleton } from '$lib/components/ui';
  import { Eyebrow, EdRow, StatusPill } from '$lib/components/editorial';
  import { toast } from '$lib/stores/toast.svelte';
  import { stackOps } from '$lib/stores/stackOps.svelte';
  import { allowed } from '$lib/rbac.svelte';
  import { autoRefresh } from '$lib/autorefresh';
  import { hosts } from '$lib/stores/host.svelte';
  import {
    Plus,
    FileCode2,
    Terminal,
    Search,
    Server,
    RefreshCw,
    Clock,
    AlertTriangle,
    Anchor,
    ArrowRight,
  } from 'lucide-svelte';

  const canWrite = $derived(allowed('stacks.create'));
  const canDeploy = $derived(allowed('stacks.deploy'));
  const canAdopt = $derived(allowed('stacks.adopt'));
  const isRemote = $derived(hosts.id !== 'local');

  type SortMode = 'name' | 'state' | 'deployed';
  let sortMode = $state<SortMode>('name');

  type StackState = 'running' | 'stopped' | 'partial' | 'unhealthy';
  interface StackCard {
    name: string;
    state: StackState;
    services: Array<{ name: string; state: string; status: string }>;
    hosts: Array<{ id: string; name: string }>;
    deployment?: StackDeployment;
    status?: 'ok' | 'needs_recovery';
  }

  let stackCards = $state<StackCard[]>([]);
  let loading = $state(true);
  let showCreate = $state(false);
  let showImport = $state(false);

  let filter = $state<'all' | StackState>('all');
  let search = $state('');

  let newName = $state('');
  let newCompose = $state(
    'services:\n  web:\n    image: nginx:alpine\n    ports:\n      - "8080:80"\n'
  );
  let newEnv = $state('');
  let creating = $state(false);

  // Create-modal source toggle. "editor" = paste compose + env. "git" =
  // clone from a repository (the backend's POST /stacks/from-git path
  // does configure + initial sync in one shot).
  type CreateMode = 'editor' | 'git';
  let createMode = $state<CreateMode>('editor');
  let newGit = $state<StackGitSourceInput>({
    repo_url: '',
    branch: 'main',
    path_in_repo: '.',
    auth_kind: 'none',
    username: '',
    password: '',
    ssh_key: '',
    auto_deploy: false,
    poll_interval_sec: 300,
    webhook_secret: '',
  });
  // After a successful git import, show the drift summary so the user
  // immediately sees that new env vars need filling in.
  let lastGitDrift = $state<EnvDrift | null>(null);

  let runCommand = $state('');
  let convertWarnings = $state<string[]>([]);
  let converting = $state(false);

  let discoveredStacks = $state<DiscoveredStack[]>([]);
  let showAdopt = $state(false);
  let adoptTarget = $state<DiscoveredStack | null>(null);
  let adoptCompose = $state('');
  let adopting = $state(false);

  async function load() {
    // Skeleton flicker fix — flip `loading` only on the FIRST load.
    // The 5s autoRefresh polling re-runs this function; if we toggled
    // `loading=true` every poll, the user saw the list flash to skeletons
    // every 5 seconds. Subsequent loads silently update stackCards in
    // place, no visual jitter. (Same pattern the dashboard uses.)
    const isFirstLoad = untrack(() => stackCards.length === 0);
    if (isFirstLoad) loading = true;
    try {
      const [stackList, containersRaw, discovered] = await Promise.all([
        api.stacks.list(),
        api.containers.list(true, hosts.id).catch(() => []),
        canAdopt
          ? api.stacks.discovered(hosts.id).catch(() => [] as DiscoveredStack[])
          : Promise.resolve([] as DiscoveredStack[]),
      ]);
      discoveredStacks = discovered;
      const containers: any[] = isFanOut(containersRaw) ? containersRaw.items : containersRaw;

      const byStack = new Map<string, any[]>();
      for (const c of containers) {
        const proj: string | undefined = c.Labels?.['com.docker.compose.project'];
        if (!proj) continue;
        if (!byStack.has(proj)) byStack.set(proj, []);
        byStack.get(proj)!.push(c);
      }

      stackCards = stackList.map((s) => {
        const cs = byStack.get(s.name) ?? [];
        const running = cs.filter((c) => c.State === 'running').length;
        const unhealthy = cs.filter((c) =>
          (c.Status ?? '').toLowerCase().includes('unhealthy')
        ).length;
        let state: StackState;
        if (cs.length === 0) state = 'stopped';
        else if (unhealthy > 0) state = 'unhealthy';
        else if (running === cs.length) state = 'running';
        else if (running === 0) state = 'stopped';
        else state = 'partial';

        const seenHost = new Map<string, string>();
        for (const c of cs) {
          const id = c.host_id ?? 'local';
          const name = c.host_name ?? 'Local';
          if (!seenHost.has(id)) seenHost.set(id, name);
        }

        return {
          name: s.name,
          state,
          services: cs.map((c) => ({
            name:
              c.Labels?.['com.docker.compose.service'] ??
              (c.Names?.[0] ?? '').replace(/^\//, ''),
            state: c.State,
            status: c.Status ?? '',
          })),
          hosts: [...seenHost.entries()].map(([id, name]) => ({ id, name })),
          deployment: s.deployment,
          status: s.status ?? 'ok',
        };
      });
    } catch (err) {
      toast.error('Failed to load stacks', err instanceof ApiError ? err.message : undefined);
    } finally {
      loading = false;
    }
  }

  async function create(e: Event) {
    e.preventDefault();
    creating = true;
    lastGitDrift = null;
    try {
      if (createMode === 'git') {
        const res = await api.stacks.createFromGit(newName, normalizeGitInput(newGit));
        const driftCount =
          (res.sync?.env_drift?.new_from_repo?.length ?? 0) +
          (res.sync?.env_drift?.new_from_compose?.length ?? 0);
        toast.success(
          'Stack imported',
          driftCount > 0
            ? `${newName} · ${driftCount} env var${driftCount === 1 ? '' : 's'} need values`
            : `${newName} · synced ${res.sync?.new_sha?.slice(0, 7) ?? ''}`,
        );
        lastGitDrift = res.sync?.env_drift ?? null;
      } else {
        await api.stacks.create(newName, newCompose, newEnv || undefined);
        toast.success('Stack created', newName);
      }
      showCreate = false;
      newName = '';
      resetGitForm();
      await load();
    } catch (err) {
      toast.error('Create failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      creating = false;
    }
  }

  function resetGitForm() {
    newGit = {
      repo_url: '',
      branch: 'main',
      path_in_repo: '.',
      auth_kind: 'none',
      username: '',
      password: '',
      ssh_key: '',
      auto_deploy: false,
      poll_interval_sec: 300,
      webhook_secret: '',
    };
    createMode = 'editor';
  }

  // Strip secrets the user didn't fill in so the backend doesn't store
  // empty-string auth (which would shadow a missing field). 5-minute
  // poll is the backend's enforced minimum — clamp anything lower.
  function normalizeGitInput(g: StackGitSourceInput): StackGitSourceInput {
    const out: StackGitSourceInput = {
      repo_url: g.repo_url.trim(),
      branch: g.branch?.trim() || 'main',
      path_in_repo: g.path_in_repo?.trim() || '.',
      auth_kind: g.auth_kind ?? 'none',
      auto_deploy: g.auto_deploy ?? false,
      poll_interval_sec: Math.max(60, g.poll_interval_sec ?? 300),
    };
    if (out.auth_kind === 'http') {
      out.username = (g.username ?? '').trim();
      out.password = g.password ?? '';
    } else if (out.auth_kind === 'ssh') {
      out.username = (g.username ?? 'git').trim() || 'git';
      out.ssh_key = g.ssh_key ?? '';
    }
    if (g.webhook_secret && g.webhook_secret.trim().length > 0) {
      out.webhook_secret = g.webhook_secret.trim();
    }
    return out;
  }

  function openAdopt(ds: DiscoveredStack) {
    adoptTarget = ds;
    adoptCompose = `# Paste the compose.yaml that describes this running project.\n# The service names below must match what's running.\n#\nservices:\n${ds.services.map((s) => `  ${s.name}:\n    image: ${s.image ?? ''}`).join('\n')}\n`;
    showAdopt = true;
  }

  async function submitAdopt(e: Event) {
    e.preventDefault();
    if (!adoptTarget) return;
    adopting = true;
    try {
      const res = await api.stacks.adopt({
        name: adoptTarget.project_name,
        host_id: adoptTarget.host_id,
        compose: adoptCompose,
        accepted_warnings: ['metadata-only-adoption'],
      });
      toast.success(
        'Adopted',
        `${res.name} (${res.bound_containers} container${res.bound_containers === 1 ? '' : 's'})`
      );
      showAdopt = false;
      adoptTarget = null;
      adoptCompose = '';
      await load();
    } catch (err) {
      toast.error('Adopt failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      adopting = false;
    }
  }

  async function convertRun() {
    converting = true;
    convertWarnings = [];
    try {
      const res = await api.convert.runToCompose(runCommand);
      newCompose = res.yaml;
      convertWarnings = res.warnings ?? [];
      showImport = false;
      if (convertWarnings.length > 0) {
        toast.warning('Converted with warnings', `${convertWarnings.length} unsupported flag(s)`);
      } else {
        toast.success('Converted', 'compose.yaml populated');
      }
    } catch (err) {
      toast.error('Convert failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      converting = false;
    }
  }

  $effect(() => {
    hosts.id;
    load();
  });

  $effect(() => autoRefresh(load, 5_000));

  const counts = $derived({
    all: stackCards.length,
    running: stackCards.filter((s) => s.state === 'running').length,
    stopped: stackCards.filter((s) => s.state === 'stopped').length,
    unhealthy: stackCards.filter((s) => s.state === 'unhealthy' || s.state === 'partial').length,
  });

  const needsRecovery = $derived(stackCards.filter((s) => s.status === 'needs_recovery'));

  const visible = $derived(
    stackCards
      .filter((s) => s.status !== 'needs_recovery')
      .filter((s) => {
        if (filter === 'all') return true;
        if (filter === 'unhealthy') return s.state === 'unhealthy' || s.state === 'partial';
        return s.state === filter;
      })
      .filter((s) => {
        if (!search.trim()) return true;
        const q = search.toLowerCase();
        return (
          s.name.toLowerCase().includes(q) ||
          s.services.some((svc) => svc.name.toLowerCase().includes(q))
        );
      })
      .sort((a, b) => {
        if (sortMode === 'state') {
          const order: Record<string, number> = {
            running: 0, partial: 1, unhealthy: 2, stopped: 3,
          };
          return (order[a.state] ?? 9) - (order[b.state] ?? 9);
        }
        if (sortMode === 'deployed') {
          const aTime = a.deployment?.deployed_at ?? '';
          const bTime = b.deployment?.deployed_at ?? '';
          return bTime.localeCompare(aTime);
        }
        return a.name.localeCompare(b.name);
      })
  );

  function fmtRelTime(ts?: string): string {
    if (!ts) return '';
    const secs = Math.floor((Date.now() - new Date(ts).getTime()) / 1000);
    if (secs < 60) return 'just now';
    if (secs < 3600) return `${Math.floor(secs / 60)}m ago`;
    if (secs < 86400) return `${Math.floor(secs / 3600)}h ago`;
    return `${Math.floor(secs / 86400)}d ago`;
  }

  function rowStatus(s: StackState): 'running' | 'degraded' | 'failing' | 'stopped' {
    if (s === 'running') return 'running';
    if (s === 'unhealthy') return 'failing';
    if (s === 'partial') return 'degraded';
    return 'stopped';
  }

  function pillStatus(s: StackState): 'running' | 'degraded' | 'failing' | 'stopped' {
    return rowStatus(s);
  }

  // Title is the page identity, not a narrative. Inline mono counter
  // appended ("· 10 on 3 hosts") echoes the mockup's restraint — the
  // narrative-italic-accent style is reserved for the dashboard.
  const subtitleLine = $derived.by(() => {
    const totalHosts = hosts.available.filter((h) => h.kind !== 'all').length;
    const base = `${counts.all} stack${counts.all === 1 ? '' : 's'}`;
    return totalHosts > 1 ? `${base} on ${totalHosts} hosts` : base;
  });
</script>

<section class="stacks-frame">
  <!-- Header -->
  <header class="stacks-header">
    <div class="stacks-header-text">
      <h1 class="ed-title stacks-title">Stacks</h1>
      <p class="ed-subtitle stacks-subtitle">{subtitleLine}</p>
    </div>
    <div class="ed-actions">
      {#if canWrite}
        <button
          type="button"
          class="dm-btn dm-btn-primary dm-btn-sm"
          onclick={() => (showCreate = true)}
        >
          <Plus size={13} strokeWidth={1.5} />
          New stack
        </button>
        <button
          type="button"
          class="dm-btn dm-btn-secondary dm-btn-sm"
          onclick={() => (showImport = true)}
        >
          <Terminal size={13} strokeWidth={1.5} />
          Import compose
        </button>
      {/if}
      <button
        type="button"
        class="dm-btn dm-btn-ghost dm-btn-sm"
        onclick={load}
        disabled={loading}
        aria-label="Refresh"
      >
        <RefreshCw size={13} strokeWidth={1.5} class={loading ? 'animate-spin' : ''} />
        Refresh
      </button>
    </div>
  </header>

  <!-- Needs-recovery banner -->
  {#if needsRecovery.length > 0}
    <div class="stacks-banner stacks-banner-warn">
      <AlertTriangle size={14} strokeWidth={1.5} class="stacks-banner-icon-warn" />
      <div>
        <div class="stacks-banner-title">
          {needsRecovery.length} stack{needsRecovery.length === 1 ? '' : 's'}
          need{needsRecovery.length === 1 ? 's' : ''} <em class="ed-accent">attention</em>
        </div>
        <p class="stacks-banner-body">
          The compose file is missing or empty on disk, but a deployment record (or running
          containers carrying the project label) still exist. Open each stack to recover from
          the running containers, restore from a backup, or remove the dockmesh record.
        </p>
        <ul class="stacks-banner-list">
          {#each needsRecovery as s (s.name)}
            <li>
              <a
                href="/stacks/{encodeURIComponent(s.name)}"
                class="stacks-banner-row"
              >
                <span class="stacks-banner-row-name">{s.name}</span>
                <span class="stacks-banner-row-meta">
                  {#if s.deployment}
                    last deployed {fmtRelTime(s.deployment.deployed_at)} on {s.deployment.host_name || s.deployment.host_id}
                  {:else}
                    containers found via project label
                  {/if}
                </span>
                <span class="stacks-banner-row-cta">Open recovery <ArrowRight size={11} strokeWidth={1.5} /></span>
              </a>
            </li>
          {/each}
        </ul>
      </div>
    </div>
  {/if}

  <!-- Discovered (unmanaged) banner -->
  {#if canAdopt && discoveredStacks.length > 0}
    <div class="stacks-banner stacks-banner-info">
      <Anchor size={14} strokeWidth={1.5} class="stacks-banner-icon-info" />
      <div>
        <div class="stacks-banner-title">
          {discoveredStacks.length} unmanaged compose project{discoveredStacks.length === 1 ? '' : 's'}
          detected on this host
        </div>
        <p class="stacks-banner-body">
          These are running via plain <code class="stacks-code">docker compose up</code> — Dockmesh
          can take over without restarting containers. For stacks with build contexts or relative
          bind mounts, prefer <code class="stacks-code">dmctl stack adopt &lt;path&gt;</code>
          from the host shell.
        </p>
        <ul class="stacks-banner-list">
          {#each discoveredStacks as ds (ds.project_name)}
            <li>
              <div class="stacks-banner-row">
                <span class="stacks-banner-row-name">{ds.project_name}</span>
                <span class="stacks-banner-row-meta">
                  {ds.service_count} service{ds.service_count === 1 ? '' : 's'} on {ds.host_name}: {ds.services.map((s) => s.name).join(', ')}
                </span>
                <button
                  type="button"
                  class="dm-btn dm-btn-secondary dm-btn-xs"
                  onclick={() => openAdopt(ds)}
                >
                  Adopt
                </button>
              </div>
            </li>
          {/each}
        </ul>
      </div>
    </div>
  {/if}

  <!-- Filter bar — tabs LEFT, search + sort RIGHT, all on the same line.
       Matches the mockup's pattern (tabs are the primary filter, search
       is the secondary refinement).  -->
  {#if !loading && stackCards.length > 0}
    <div class="ed-tabs stacks-tabs">
      <button
        class="ed-tab"
        class:active={filter === 'all'}
        onclick={() => (filter = 'all')}
      >All <span class="count">{counts.all}</span></button>
      <button
        class="ed-tab"
        class:active={filter === 'running'}
        onclick={() => (filter = 'running')}
      >Running <span class="count">{counts.running}</span></button>
      <button
        class="ed-tab"
        class:active={filter === 'unhealthy'}
        onclick={() => (filter = 'unhealthy')}
      >Issues <span class="count">{counts.unhealthy}</span></button>
      <button
        class="ed-tab"
        class:active={filter === 'stopped'}
        onclick={() => (filter = 'stopped')}
      >Stopped <span class="count">{counts.stopped}</span></button>

      <div class="stacks-tabs-right">
        <div class="stacks-search">
          <Search size={13} strokeWidth={1.5} class="stacks-search-icon" />
          <input
            type="search"
            placeholder="filter…"
            bind:value={search}
            class="ed-input ed-input-mono stacks-search-input"
          />
        </div>
        <div class="stacks-sort">
          <span class="stacks-sort-label">sort</span>
          <select class="stacks-sort-select" bind:value={sortMode}>
            <option value="name">name</option>
            <option value="state">status</option>
            <option value="deployed">last deployed</option>
          </select>
        </div>
      </div>
    </div>
  {/if}

  <!-- Stacks list -->
  {#if loading}
    <div class="stacks-skeleton">
      {#each Array(5) as _}
        <Skeleton width="100%" height="2.5rem" />
      {/each}
    </div>
  {:else if stackCards.length === 0}
    <div class="stacks-empty">
      <div class="stacks-empty-title">No stacks yet.</div>
      <p class="stacks-empty-body">
        Create your first stack by pasting a <em class="ed-accent">compose.yaml</em>
        or importing a <code class="stacks-code">docker run</code> command.
      </p>
      {#if canWrite}
        <div class="stacks-empty-actions">
          <button
            type="button"
            class="dm-btn dm-btn-primary dm-btn-sm"
            onclick={() => (showCreate = true)}
          >
            <Plus size={13} strokeWidth={1.5} /> Create stack
          </button>
          <button
            type="button"
            class="dm-btn dm-btn-secondary dm-btn-sm"
            onclick={() => (showImport = true)}
          >
            <Terminal size={13} strokeWidth={1.5} /> Import from docker run
          </button>
        </div>
      {/if}
    </div>
  {:else if visible.length === 0}
    <div class="stacks-empty">
      <p class="stacks-empty-body">No stacks match this filter.</p>
    </div>
  {:else}
    <div class="stacks-list">
      {#each visible as s (s.name)}
        <EdRow
          status={rowStatus(s.state)}
          href={`/stacks/${encodeURIComponent(s.name)}`}
          columns="6px minmax(0, 1.6fr) minmax(0, 1.4fr) minmax(0, 0.9fr) auto minmax(80px, auto) auto"
        >
          <span class="stacks-row-name">
            <span class="stacks-row-id">{s.name}</span>
            <span class="stacks-row-meta">
              {s.services.length} service{s.services.length === 1 ? '' : 's'}
            </span>
          </span>

          <span class="stacks-row-services">
            {#if s.services.length > 0}
              {#each s.services.slice(0, 5) as svc (svc.name)}
                <span
                  class="stacks-row-service"
                  class:running={svc.state === 'running'}
                  title={svc.status}
                >{svc.name}</span>
              {/each}
              {#if s.services.length > 5}
                <span class="stacks-row-service-more">+{s.services.length - 5}</span>
              {/if}
            {:else}
              <span class="stacks-row-service-empty">no containers</span>
            {/if}
          </span>

          <span class="stacks-row-host">
            {#if s.deployment}
              <span class="stacks-row-host-pill">
                <Server size={10} strokeWidth={1.5} />
                {s.deployment.host_name || s.deployment.host_id}
              </span>
            {:else if hosts.isAll && s.hosts.length > 0}
              {#each s.hosts as h (h.id)}
                <span class="stacks-row-host-pill">
                  <Server size={10} strokeWidth={1.5} />
                  {h.name}
                </span>
              {/each}
            {/if}
          </span>

          <StatusPill status={pillStatus(s.state)} />

          <span class="stacks-row-uptime">
            {#if s.deployment?.deployed_at && s.state === 'running'}
              <Clock size={10} strokeWidth={1.5} class="stacks-row-uptime-icon" />
              <span>up {fmtRelTime(s.deployment.deployed_at).replace(' ago', '')}</span>
            {:else if s.deployment?.deployed_at}
              <span class="stacks-row-uptime-muted">{fmtRelTime(s.deployment.deployed_at)}</span>
            {:else}
              <span class="stacks-row-uptime-muted">—</span>
            {/if}
          </span>

          <span class="stacks-row-arrow" aria-hidden="true">
            <ArrowRight size={13} strokeWidth={1.5} />
          </span>
        </EdRow>
      {/each}
    </div>
  {/if}
</section>

<!-- Modals — using the existing Modal primitive so the auth/RBAC + state
     wiring stays untouched. Page-level conversion of these popovers to
     EditorialModal will land in a follow-up slice; the important part for
     this slice is that the LIST page reads editorial. -->
<Modal bind:open={showCreate} title="Create stack" maxWidth="max-w-3xl">
  <form onsubmit={create} class="space-y-4" id="create-stack-form">
    <div class="flex items-center justify-between">
      <div class="text-xs text-[var(--fg-muted)]">
        Name must match <code class="font-mono">[a-z0-9][a-z0-9-]*[a-z0-9]</code>, 2-63 chars.
      </div>
      <button
        type="button"
        class="dm-btn dm-btn-ghost dm-btn-xs"
        onclick={() => (showImport = true)}
      >
        <Terminal class="w-3.5 h-3.5" />
        Import from docker run
      </button>
    </div>

    <Input label="Name" placeholder="my-stack" bind:value={newName} disabled={creating} />

    <!-- Source picker: paste-it or pull-it-from-a-repo. -->
    <div class="flex gap-1 p-1 rounded bg-[var(--bg-muted,rgba(0,0,0,0.04))] w-fit">
      <button
        type="button"
        class="px-3 py-1.5 text-xs rounded transition"
        class:bg-[var(--bg)]={createMode === 'editor'}
        class:font-medium={createMode === 'editor'}
        class:text-[var(--fg-muted)]={createMode !== 'editor'}
        onclick={() => (createMode = 'editor')}
      >
        Compose editor
      </button>
      <button
        type="button"
        class="px-3 py-1.5 text-xs rounded transition"
        class:bg-[var(--bg)]={createMode === 'git'}
        class:font-medium={createMode === 'git'}
        class:text-[var(--fg-muted)]={createMode !== 'git'}
        onclick={() => (createMode = 'git')}
      >
        From git repository
      </button>
    </div>

    {#if createMode === 'editor'}
      <div>
        <label for="compose" class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">
          <span class="inline-flex items-center gap-1"><FileCode2 class="w-3 h-3" /> compose.yaml</span>
        </label>
        <textarea
          id="compose"
          class="dm-input font-mono text-xs h-64 resize-y"
          bind:value={newCompose}
          disabled={creating}
        ></textarea>
      </div>

      <div>
        <label for="env" class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">.env (optional)</label>
        <textarea
          id="env"
          class="dm-input font-mono text-xs h-20 resize-y"
          bind:value={newEnv}
          disabled={creating}
          placeholder="KEY=value"
        ></textarea>
      </div>
    {:else}
      <!-- ───── git import form ───── -->
      <Input
        label="Repository URL"
        placeholder="https://github.com/acme/stack.git"
        bind:value={newGit.repo_url}
        disabled={creating}
      />
      <div class="grid grid-cols-2 gap-3">
        <Input
          label="Branch"
          placeholder="main"
          bind:value={newGit.branch as any}
          disabled={creating}
        />
        <Input
          label="Path in repo"
          placeholder="."
          bind:value={newGit.path_in_repo as any}
          disabled={creating}
        />
      </div>
      <div>
        <label for="git-auth" class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">Authentication</label>
        <select id="git-auth" class="dm-input" bind:value={newGit.auth_kind as any} disabled={creating}>
          <option value="none">None (public repo)</option>
          <option value="http">HTTPS · username + token</option>
          <option value="ssh">SSH key</option>
        </select>
      </div>
      {#if newGit.auth_kind === 'http'}
        <div class="grid grid-cols-2 gap-3">
          <Input
            label="Username"
            placeholder="your-github-username"
            bind:value={newGit.username as any}
            disabled={creating}
          />
          <Input
            label="Personal access token"
            type="password"
            placeholder="ghp_… or github_pat_…"
            bind:value={newGit.password as any}
            disabled={creating}
          />
        </div>
        {#if newGit.repo_url && /github\.com/.test(newGit.repo_url)}
          <div class="dm-card p-3 text-xs text-[var(--fg-muted)] space-y-1">
            <p>
              <strong class="text-[var(--fg)]">GitHub requires a Personal Access Token</strong>,
              not your account password (deprecated since 2021-08-13).
            </p>
            <p>
              Create one at
              <a href="https://github.com/settings/personal-access-tokens" target="_blank" rel="noopener" class="underline">
                github.com/settings/personal-access-tokens
              </a>
              with <code class="font-mono">Contents: Read-only</code> permission
              on the repo (fine-grained), or
              <a href="https://github.com/settings/tokens" target="_blank" rel="noopener" class="underline">
                classic tokens
              </a>
              with the <code class="font-mono">repo</code> scope.
            </p>
          </div>
        {/if}
      {:else if newGit.auth_kind === 'ssh'}
        <Input
          label="SSH user"
          placeholder="git"
          bind:value={newGit.username as any}
          disabled={creating}
        />
        <div>
          <label for="git-ssh-key" class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">
            OpenSSH private key
          </label>
          <textarea
            id="git-ssh-key"
            class="dm-input font-mono text-xs h-24 resize-y"
            bind:value={newGit.ssh_key as any}
            disabled={creating}
            placeholder="-----BEGIN OPENSSH PRIVATE KEY-----"
          ></textarea>
        </div>
      {/if}

      <div class="grid grid-cols-2 gap-3">
        <Input
          label="Poll interval (seconds)"
          type="number"
          min={60}
          bind:value={newGit.poll_interval_sec as any}
          disabled={creating}
        />
        <Input
          label="Webhook secret (optional)"
          type="password"
          bind:value={newGit.webhook_secret as any}
          disabled={creating}
        />
      </div>

      <label class="flex items-center gap-2 text-xs">
        <input
          type="checkbox"
          bind:checked={newGit.auto_deploy as any}
          disabled={creating}
        />
        Auto-deploy on new commits
      </label>

      <div class="dm-card p-3 text-xs text-[var(--fg-muted)] space-y-1">
        <p>The first sync seeds <code class="font-mono">.env</code> from the repo. Subsequent syncs preserve your filled-in values; new keys from the repo or new <code class="font-mono">${`{VAR}`}</code> references in compose are appended at the end.</p>
      </div>
    {/if}

    {#if convertWarnings.length > 0}
      <div class="dm-card p-3 text-xs border border-[color-mix(in_srgb,var(--color-warning-500)_30%,transparent)]">
        <div class="font-medium text-[var(--color-warning-400)] mb-1">Converter warnings</div>
        <ul class="list-disc list-inside text-[var(--fg-muted)] space-y-0.5">
          {#each convertWarnings as w}<li>{w}</li>{/each}
        </ul>
      </div>
    {/if}
  </form>

  {#snippet footer()}
    <Button variant="secondary" onclick={() => (showCreate = false)}>Cancel</Button>
    <Button
      variant="primary"
      type="submit"
      form="create-stack-form"
      loading={creating}
      disabled={creating || !newName ||
        (createMode === 'editor' ? !newCompose : !newGit.repo_url)}
    >
      {createMode === 'git' ? 'Import & sync' : 'Create'}
    </Button>
  {/snippet}
</Modal>

{#if lastGitDrift && ((lastGitDrift.new_from_repo?.length ?? 0) + (lastGitDrift.new_from_compose?.length ?? 0) > 0)}
  <div class="fixed bottom-4 right-4 max-w-md dm-card p-4 text-xs space-y-2 border border-[color-mix(in_srgb,var(--color-warning-500)_30%,transparent)] z-50">
    <div class="font-medium">Env drift detected</div>
    {#if lastGitDrift.new_from_repo?.length}
      <div>
        <span class="text-[var(--fg-muted)]">New from repo:</span>
        <code class="font-mono">{lastGitDrift.new_from_repo.join(', ')}</code>
      </div>
    {/if}
    {#if lastGitDrift.new_from_compose?.length}
      <div>
        <span class="text-[var(--fg-muted)]">Needed by compose:</span>
        <code class="font-mono">{lastGitDrift.new_from_compose.join(', ')}</code>
      </div>
    {/if}
    <button
      type="button"
      class="dm-btn dm-btn-ghost dm-btn-xs"
      onclick={() => (lastGitDrift = null)}
    >
      Dismiss
    </button>
  </div>
{/if}

<Modal bind:open={showImport} title="Import from docker run" maxWidth="max-w-xl">
  <p class="text-sm text-[var(--fg-muted)] mb-4">
    Paste a complete <code class="font-mono">docker run</code> command. We convert
    it into compose YAML. Supports ports, volumes, env, networks, restart,
    labels, capabilities and the common flags.
  </p>
  <textarea
    class="dm-input font-mono text-xs h-32"
    placeholder="docker run -d --name web -p 8080:80 nginx:alpine"
    bind:value={runCommand}
  ></textarea>

  {#snippet footer()}
    <Button variant="secondary" onclick={() => (showImport = false)}>Cancel</Button>
    <Button
      variant="primary"
      loading={converting}
      disabled={converting || !runCommand.trim()}
      onclick={convertRun}
    >
      Convert
    </Button>
  {/snippet}
</Modal>

<Modal bind:open={showAdopt} title={adoptTarget ? `Adopt '${adoptTarget.project_name}'` : 'Adopt'} maxWidth="max-w-3xl">
  {#if adoptTarget}
    <form onsubmit={submitAdopt} class="space-y-4">
      <div class="rounded-md border border-[var(--border)] bg-[var(--bg-card)] px-3 py-2 text-xs text-[var(--fg-muted)]">
        <div class="text-[var(--fg)] font-medium mb-1">This is a metadata-only adoption.</div>
        Dockmesh will write the compose.yaml below into <code class="font-mono">stacks/{adoptTarget.project_name}/</code> and bind to
        <strong>{adoptTarget.service_count}</strong> running container{adoptTarget.service_count === 1 ? '' : 's'}. No containers are
        restarted. For stacks that reference local files (build contexts, <code class="font-mono">./config.yml</code> bind mounts, …)
        use <code class="font-mono">dmctl stack adopt &lt;path&gt;</code> from the host shell — the CLI ships the full folder so restarts keep working.
      </div>
      <label class="block text-xs font-medium">compose.yaml
        <textarea
          class="dm-input font-mono text-xs mt-1 h-64 w-full"
          required
          bind:value={adoptCompose}
        ></textarea>
      </label>
    </form>
  {/if}
  {#snippet footer()}
    <Button variant="secondary" onclick={() => (showAdopt = false)}>Cancel</Button>
    <Button
      variant="primary"
      loading={adopting}
      disabled={adopting || !adoptCompose.trim()}
      onclick={submitAdopt}
    >
      Adopt
    </Button>
  {/snippet}
</Modal>

<style>
  .stacks-frame {
    display: flex;
    flex-direction: column;
    gap: 28px;
    max-width: 1480px;
    padding-bottom: 48px;
  }

  /* ─────── Header ────── */
  .stacks-header {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
    gap: 24px;
    flex-wrap: wrap;
  }
  .stacks-header-text { min-width: 0; max-width: 80ch; flex: 1 1 40ch; }
  .stacks-title {
    /* `display: flex` (not inline-flex) forces the title onto its own
       line below the eyebrow — matches the editorial pattern where
       eyebrow and title stack vertically. */
    display: flex;
    align-items: baseline;
    flex-wrap: wrap;
    gap: 8px;
    font-size: 26px;
    line-height: 1.2;
    letter-spacing: -0.02em;
    margin-top: 10px;
  }
  .stacks-title-meta {
    font-family: var(--font-mono);
    font-size: 13px;
    color: var(--fg-subtle);
    font-weight: 400;
    letter-spacing: 0.02em;
  }
  .stacks-code {
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: var(--fg-muted);
    background: var(--surface);
    border: 1px solid var(--border);
    padding: 1px 5px;
    border-radius: 3px;
  }

  /* ─────── Banners ────── */
  .stacks-banner {
    display: grid;
    grid-template-columns: auto 1fr;
    gap: 14px;
    padding: 14px 16px;
    border-radius: 6px;
  }
  .stacks-banner-warn {
    border: 1px solid color-mix(in srgb, var(--color-warning-500) 40%, var(--border));
    background: color-mix(in srgb, var(--color-warning-500) 6%, transparent);
  }
  .stacks-banner-info {
    border: 1px solid color-mix(in srgb, var(--color-brand-500) 30%, var(--border));
    background: color-mix(in srgb, var(--color-brand-500) 5%, transparent);
  }
  :global(.stacks-banner-icon-warn) { color: var(--color-warning-400); margin-top: 4px; flex-shrink: 0; }
  :global(.stacks-banner-icon-info) { color: var(--color-brand-400); margin-top: 4px; flex-shrink: 0; }
  .stacks-banner-title {
    font-size: 13.5px;
    color: var(--fg);
    font-weight: 500;
  }
  .stacks-banner-body {
    margin: 6px 0 0;
    font-size: 12.5px;
    color: var(--fg-muted);
    line-height: 1.55;
    max-width: 80ch;
  }
  .stacks-banner-list {
    margin: 12px 0 0;
    padding: 0;
    list-style: none;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .stacks-banner-row {
    display: flex;
    align-items: center;
    gap: 14px;
    padding: 8px 12px;
    border: 1px solid var(--border);
    border-radius: 5px;
    background: var(--surface);
    color: inherit;
    text-decoration: none;
  }
  .stacks-banner-row:hover { border-color: var(--border-strong); }
  .stacks-banner-row-name {
    font-family: var(--font-mono);
    font-size: 12.5px;
    color: var(--fg);
    font-weight: 500;
    flex-shrink: 0;
  }
  .stacks-banner-row-meta {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fg-subtle);
    flex: 1;
    min-width: 0;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .stacks-banner-row-cta {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--accent-fg);
    display: inline-flex;
    align-items: center;
    gap: 4px;
    flex-shrink: 0;
  }

  /* ─────── Filter bar — tabs left, search/sort pushed right ────── */
  .stacks-tabs {
    align-items: center;
    border-bottom: 1px solid var(--border);
    flex-wrap: wrap;
  }
  .stacks-tabs-right {
    margin-left: auto;
    display: inline-flex;
    align-items: center;
    gap: 18px;
    padding: 6px 0;
  }
  .stacks-search {
    position: relative;
    display: inline-flex;
    align-items: center;
    width: 200px;
  }
  :global(.stacks-search-icon) {
    position: absolute;
    left: 0;
    top: 50%;
    transform: translateY(-50%);
    color: var(--fg-subtle);
    pointer-events: none;
  }
  .stacks-search-input {
    padding: 4px 0 4px 22px;
    border-bottom: 1px solid var(--border);
    font-size: 12px;
    width: 100%;
  }
  .stacks-search-input:focus { border-bottom-color: var(--color-brand-500); }
  .stacks-sort {
    display: inline-flex;
    align-items: center;
    gap: 8px;
  }
  .stacks-sort-label {
    font-family: var(--font-mono);
    font-size: 10px;
    letter-spacing: 0.14em;
    text-transform: uppercase;
    color: var(--fg-subtle);
  }
  .stacks-sort-select {
    background: transparent;
    border: 0;
    border-bottom: 1px solid var(--border);
    color: var(--fg);
    font-family: var(--font-mono);
    font-size: 12px;
    padding: 4px 18px 4px 0;
    cursor: pointer;
  }
  .stacks-sort-select:focus { outline: none; border-bottom-color: var(--color-brand-500); }

  /* ─────── List — transparent surface, hairline-only row dividers ────── */
  .stacks-list {
    margin-top: 8px;
    border-top: 1px solid var(--border);
  }
  .stacks-skeleton {
    padding: 16px 0;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .stacks-empty {
    padding: 32px 0;
    display: flex;
    flex-direction: column;
    gap: 12px;
    align-items: flex-start;
    border-top: 1px solid var(--border);
  }
  .stacks-empty-title {
    font-size: 14px;
    color: var(--fg);
    font-weight: 500;
  }
  .stacks-empty-body {
    margin: 0;
    color: var(--fg-muted);
    font-size: 13px;
    line-height: 1.6;
    max-width: 60ch;
  }
  .stacks-empty-actions {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
  }

  .stacks-row-name { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
  .stacks-row-id {
    font-size: 13.5px;
    color: var(--fg);
    font-weight: 500;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .stacks-row-meta {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
    letter-spacing: 0.04em;
  }
  .stacks-row-services {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    align-items: center;
    min-width: 0;
  }
  .stacks-row-service {
    font-family: var(--font-mono);
    font-size: 10.5px;
    padding: 1px 6px;
    border-radius: 3px;
    border: 1px solid var(--border);
    color: var(--fg-subtle);
  }
  .stacks-row-service.running { color: var(--fg-muted); border-color: var(--border-strong); }
  .stacks-row-service-more {
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
  }
  .stacks-row-service-empty {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fg-subtle);
    font-style: normal;
  }

  .stacks-row-host {
    display: inline-flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 4px;
    min-width: 0;
  }
  .stacks-row-host-pill {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    padding: 1px 6px 1px 5px;
    border: 1px solid var(--border);
    border-radius: 3px;
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-muted);
  }

  .stacks-row-uptime {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fg-muted);
    white-space: nowrap;
  }
  :global(.stacks-row-uptime-icon) { color: var(--color-success-400); flex-shrink: 0; }
  .stacks-row-uptime-muted { color: var(--fg-subtle); }

  .stacks-row-arrow { color: var(--fg-subtle); display: inline-flex; align-items: center; }

  :global(.animate-spin) { animation: stacks-spin 0.9s linear infinite; }
  @keyframes stacks-spin { to { transform: rotate(360deg); } }
</style>
