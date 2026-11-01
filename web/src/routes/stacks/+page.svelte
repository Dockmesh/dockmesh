<script lang="ts">
  import { untrack } from 'svelte';
  import {
    api, ApiError, isFanOut,
    type StackDeployment, type DiscoveredStack, type StackGitSourceInput, type EnvDrift,
  } from '$lib/api';
  import { goto } from '$app/navigation';
  import { Button, Modal, Input, Skeleton, Segmented, StatBar, LiveIndicator, Menu, EmptyState } from '$lib/components/ui';
  import StackHealth from '$lib/components/ui/StackHealth.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { toast } from '$lib/stores/toast.svelte';
  import { stackOps } from '$lib/stores/stackOps.svelte';
  import { allowed } from '$lib/rbac.svelte';
  import { autoRefresh } from '$lib/autorefresh';
  import { latest } from '$lib/latest';
  import { hosts } from '$lib/stores/host.svelte';
  import { Plus, Terminal, AlertTriangle, ArrowRight, FileCode2 } from 'lucide-svelte';

  const canWrite = $derived(allowed('stacks.create'));
  const canDeploy = $derived(allowed('stacks.deploy'));
  const canAdopt = $derived(allowed('stacks.adopt'));

  type SortMode = 'name' | 'state' | 'deployed';
  let sortMode = $state<SortMode>('state');

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
  let refreshing = $state(false);
  let lastLoaded = $state<number | null>(null);
  let showAdoptPicker = $state(false);
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
  // Host the new stack should deploy onto when the operator picks
  // "Create & deploy". Defaults to whatever the sidebar host switcher
  // is currently on; ignored when only `local` is available. The picker
  // is hidden unless multi-host is configured to keep the modal lean for
  // single-host installs.
  let newHostId = $state('local');

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

  const nextLoad = latest();
  async function load() {
    const current = nextLoad();
    // Skeleton only on the first load; polls update the list in place.
    const isFirstLoad = untrack(() => stackCards.length === 0);
    if (isFirstLoad) loading = true;
    refreshing = true;
    try {
      const [stackList, containersRaw, discovered] = await Promise.all([
        api.stacks.list(),
        api.containers.list(true, hosts.id).catch(() => []),
        canAdopt
          ? api.stacks.discovered(hosts.id).catch(() => [] as DiscoveredStack[])
          : Promise.resolve([] as DiscoveredStack[]),
      ]);
      if (!current()) return;
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
      lastLoaded = Date.now();
    } catch (err) {
      if (current()) toast.error('Failed to load stacks', err instanceof ApiError ? err.message : undefined);
    } finally {
      if (current()) {
        loading = false;
        refreshing = false;
      }
    }
  }

  async function create(e: Event) {
    e.preventDefault();
    await runCreate(false);
  }

  async function createAndDeploy() {
    await runCreate(true);
  }

  // Shared create flow. `deploy` controls whether we follow the create
  // call with a deploy on the picked host — for the git path the deploy
  // is unconditional because the import endpoint already syncs but
  // doesn't bring up containers.
  async function runCreate(deploy: boolean) {
    creating = true;
    lastGitDrift = null;
    const stackName = newName;
    const targetHost = newHostId || 'local';
    try {
      if (createMode === 'git') {
        const res = await api.stacks.createFromGit(stackName, normalizeGitInput(newGit));
        const driftCount =
          (res.sync?.env_drift?.new_from_repo?.length ?? 0) +
          (res.sync?.env_drift?.new_from_compose?.length ?? 0);
        toast.success(
          'Stack imported',
          driftCount > 0
            ? `${stackName} · ${driftCount} env var${driftCount === 1 ? '' : 's'} need values`
            : `${stackName} · synced ${res.sync?.new_sha?.slice(0, 7) ?? ''}`,
        );
        lastGitDrift = res.sync?.env_drift ?? null;
      } else {
        await api.stacks.create(stackName, newCompose, newEnv || undefined);
        toast.success('Stack created', stackName);
      }
      if (deploy) {
        try {
          await api.stacks.deploy(stackName, targetHost);
          toast.success('Deploy started', `${stackName} → ${targetHost === 'local' ? 'local' : targetHost}`);
        } catch (err) {
          toast.error('Deploy failed', err instanceof ApiError ? err.message : undefined);
        }
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
    untrack(load);
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
          // Attention first, then running, then stopped.
          const order: Record<string, number> = {
            unhealthy: 0, partial: 1, running: 2, stopped: 3,
          };
          return (order[a.state] ?? 9) - (order[b.state] ?? 9) || a.name.localeCompare(b.name);
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

  const COLS = 'minmax(240px, 2.4fr) 210px minmax(100px, 1fr) 110px 40px';

  function stateLabel(s: StackState): string {
    return { running: 'running', partial: 'degraded', unhealthy: 'unhealthy', stopped: 'stopped' }[s];
  }
  const isUnhealthy = (svc: { status: string }) => svc.status.toLowerCase().includes('unhealthy');
  function stackHost(s: StackCard): string {
    return s.deployment?.host_id ?? s.hosts[0]?.id ?? (hosts.isAll ? 'local' : hosts.id);
  }
  function hostLabel(s: StackCard): string {
    if (s.deployment) return s.deployment.host_name || s.deployment.host_id;
    if (s.hosts.length) return s.hosts.map((h) => h.name).join(', ');
    return '—';
  }
  const hostCount = $derived(
    new Set(stackCards.map((s) => s.deployment?.host_id ?? s.hosts[0]?.id).filter(Boolean)).size
  );

  async function confirmExternalChanges(err: unknown, name: string): Promise<boolean> {
    if (!(err instanceof ApiError) || err.status !== 409 || err.body?.code !== 'modified_outside') return false;
    const files: string = (err.body.files as string[] | undefined)?.join(', ') || 'compose.yaml or .env';
    return confirm.ask({
      title: 'Stack files changed outside Dockmesh',
      message: `${files} of "${name}" changed since Dockmesh last wrote them.`,
      body: 'Something other than Dockmesh changed these files. Review them in the editor before you deploy them as they are.',
      confirmLabel: 'Deploy anyway',
      danger: true,
    });
  }

  async function deployStack(name: string, host: string) {
    try {
      const res = await stackOps.run(host, name, async () => {
        try {
          return await api.stacks.deploy(name, host);
        } catch (err) {
          if (!(await confirmExternalChanges(err, name))) throw err;
          return api.stacks.deploy(name, host, undefined, true);
        }
      });
      toast.success('Deployed', `${name} · ${res.services.length} service(s)`);
      await load();
    } catch (err) {
      toast.error('Deploy failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function stopStack(name: string, host: string) {
    const ok = await confirm.ask({
      title: `Stop ${name}?`,
      message: 'All containers of this stack are stopped. Volumes and the compose file are kept.',
      confirmLabel: 'Stop',
    });
    if (!ok) return;
    try {
      await stackOps.run(host, name, () => api.stacks.stop(name, host));
      toast.info('Stopped', name);
      await load();
    } catch (err) {
      toast.error('Stop failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  const subtitleLine = $derived.by(() => {
    const totalHosts = hosts.available.filter((h) => h.kind !== 'all').length;
    const base = `${counts.all} stack${counts.all === 1 ? '' : 's'}`;
    return totalHosts > 1 ? `${base} on ${totalHosts} hosts` : base;
  });
</script>

<section class="dm-page">
  <div class="dm-ph">
    <h1>Stacks</h1>
    <span class="dm-sub">{subtitleLine}</span>
    <span class="dm-sp"></span>
    <LiveIndicator updatedAt={lastLoaded} onrefresh={load} busy={refreshing} />
    {#if canAdopt}
      <button type="button" class="dm-btn" onclick={() => (showAdoptPicker = true)}>
        Adopt existing…{#if discoveredStacks.length}<span class="dm-num dm-dim">{discoveredStacks.length}</span>{/if}
      </button>
    {/if}
    {#if canWrite}
      <button
        type="button"
        class="dm-btn dm-btn-primary"
        onclick={() => { newHostId = hosts.isAll ? 'local' : hosts.id; showCreate = true; }}
      >
        <Plus size={14} strokeWidth={2} /> New stack
      </button>
    {/if}
  </div>

  {#if needsRecovery.length > 0}
    <div class="st-attn" role="status">
      <div class="st-attn-h">
        <AlertTriangle size={14} strokeWidth={1.8} />
        <b>{needsRecovery.length} stack{needsRecovery.length === 1 ? '' : 's'} need{needsRecovery.length === 1 ? 's' : ''} recovery</b>
        <span class="dm-dim">· compose file missing on disk, containers or a deploy record still exist</span>
      </div>
      {#each needsRecovery as s (s.name)}
        <a class="st-attn-row" href="/stacks/{encodeURIComponent(s.name)}">
          <span class="dm-mono">{s.name}</span>
          <span class="dm-dim dm-trunc">
            {#if s.deployment}
              last deployed {fmtRelTime(s.deployment.deployed_at)} on {s.deployment.host_name || s.deployment.host_id}
            {:else}
              containers found via project label
            {/if}
          </span>
          <span class="st-attn-cta">Open recovery <ArrowRight size={12} /></span>
        </a>
      {/each}
    </div>
  {/if}

  {#if !loading && stackCards.length > 0}
    <div class="dm-toolbar">
      <Segmented
        label="Filter stacks"
        bind:value={filter}
        options={[
          { key: 'all', label: 'All', count: counts.all },
          { key: 'running', label: 'Running', count: counts.running },
          { key: 'unhealthy', label: 'Attention', count: counts.unhealthy },
          { key: 'stopped', label: 'Stopped', count: counts.stopped }
        ]}
      />
      <span class="dm-sp"></span>
      <input type="search" class="dm-filter" placeholder="Filter stacks or services…" aria-label="Filter stacks" bind:value={search} style="width: 240px" />
    </div>

    <StatBar
      items={[
        { label: 'Running', value: `${counts.running} of ${counts.all}` },
        {
          label: 'Needs attention',
          value: counts.unhealthy + needsRecovery.length,
          note: [...stackCards.filter((s) => s.state === 'unhealthy' || s.state === 'partial').map((s) => `${s.name} ${stateLabel(s.state)}`), ...needsRecovery.map((s) => `${s.name} needs recovery`)].join(' · '),
          tone: counts.unhealthy + needsRecovery.length ? 'warn' : ''
        },
        { label: 'Stopped', value: counts.stopped },
        { label: 'Hosts', value: hostCount }
      ]}
    />
  {/if}

  {#if loading}
    <div class="dm-tbl">
      {#each Array(5) as _, i (i)}
        <div class="dm-tr" style="grid-template-columns: 1fr"><Skeleton width="60%" height="0.9rem" /></div>
      {/each}
    </div>
  {:else if stackCards.length === 0}
    <EmptyState title="No stacks yet" description="Create a stack from a compose.yaml, a git repository or a docker run command.">
      {#snippet action()}
        {#if canWrite}
          <button type="button" class="dm-btn dm-btn-primary" onclick={() => { newHostId = hosts.isAll ? 'local' : hosts.id; showCreate = true; }}>
            <Plus size={14} strokeWidth={2} /> New stack
          </button>
        {/if}
      {/snippet}
    </EmptyState>
  {:else}
    <div class="dm-tbl" role="table" aria-label="Stacks">
      <div class="dm-tr dm-th" role="row" style="grid-template-columns: {COLS}">
        <span role="columnheader"><button type="button" onclick={() => (sortMode = 'name')}>Name · services{sortMode === 'name' ? ' ↓' : ''}</button></span>
        <span role="columnheader"><button type="button" onclick={() => (sortMode = 'state')}>Status{sortMode === 'state' ? ' ↓' : ''}</button></span>
        <span role="columnheader">Host</span>
        <span role="columnheader"><button type="button" onclick={() => (sortMode = 'deployed')}>Last deploy{sortMode === 'deployed' ? ' ↓' : ''}</button></span>
        <span role="columnheader"></span>
      </div>
      {#each visible as s (s.name)}
        {@const running = s.services.filter((x) => x.state === 'running').length}
        {@const host = stackHost(s)}
        <div
          class="dm-tr dm-click"
          role="row"
          style="grid-template-columns: {COLS}"
          onclick={() => goto(`/stacks/${encodeURIComponent(s.name)}`)}
          onkeydown={(e) => { if (e.key === 'Enter' && e.target === e.currentTarget) goto(`/stacks/${encodeURIComponent(s.name)}`); }}
          tabindex="0"
        >
          <span role="cell" class="dm-nm">
            <a href="/stacks/{encodeURIComponent(s.name)}" onclick={(e) => e.stopPropagation()}>{s.name}</a>
            <small title={s.services.map((x) => `${x.name}: ${x.status || x.state}`).join('\n')}>
              {#if s.services.length}{s.services.map((x) => x.name).join(' · ')}{:else}No containers{/if}
            </small>
          </span>
          <span role="cell">
            <StackHealth
              {running}
              total={s.services.length}
              unhealthy={s.services.filter(isUnhealthy).length}
              members={s.services.map((x) => ({ name: x.name, state: x.state, unhealthy: isUnhealthy(x) }))}
            />
          </span>
          <span role="cell" class="dm-dim" title={hostLabel(s)}>{hostLabel(s)}</span>
          <span role="cell" class="dm-num dm-dim" title={s.deployment?.deployed_at ? new Date(s.deployment.deployed_at).toLocaleString() : ''}>
            {s.deployment?.deployed_at ? fmtRelTime(s.deployment.deployed_at) : '—'}
          </span>
          <span role="cell" class="dm-tr-menu">
            <Menu
              label="Actions for {s.name}"
              items={[
                { label: 'Open', kbd: '↵', href: `/stacks/${encodeURIComponent(s.name)}` },
                ...(canDeploy
                  ? [
                      { separator: true },
                      { label: s.state === 'stopped' ? 'Start' : 'Redeploy', disabled: stackOps.isBusy(host, s.name), onclick: () => deployStack(s.name, host) },
                      ...(s.state !== 'stopped' ? [{ label: 'Stop', disabled: stackOps.isBusy(host, s.name), onclick: () => stopStack(s.name, host) }] : [])
                    ]
                  : [])
              ]}
            />
          </span>
        </div>
      {:else}
        <div class="dm-empty">No stacks match this filter.</div>
      {/each}
    </div>
  {/if}
</section>

<Modal bind:open={showAdoptPicker} title="Adopt an existing compose project" maxWidth="max-w-xl">
  <div class="st-adopt">
    <p class="dm-sub">
      Projects started with plain <code class="dm-mono">docker compose up</code> can be taken over without
      restarting their containers. For projects with build contexts or relative bind mounts, use
      <code class="dm-mono">dmctl stack adopt &lt;path&gt;</code> on the host instead.
    </p>
    {#if discoveredStacks.length === 0}
      <EmptyState title="Nothing to adopt" description="No unmanaged compose projects were found on {hosts.isAll ? 'any host' : (hosts.selected?.name ?? 'this host')}." />
    {:else}
      <div class="dm-tbl">
        {#each discoveredStacks as ds (ds.host_id + ds.project_name)}
          <div class="dm-tr" style="grid-template-columns: minmax(0, 1fr) auto">
            <span class="dm-nm">
              <span class="dm-t">{ds.project_name}</span>
              <small>{ds.service_count} service{ds.service_count === 1 ? '' : 's'} on {ds.host_name}: {ds.services.map((x) => x.name).join(', ')}</small>
            </span>
            <button type="button" class="dm-btn dm-btn-sm" onclick={() => { showAdoptPicker = false; openAdopt(ds); }}>Adopt…</button>
          </div>
        {/each}
      </div>
    {/if}
  </div>
</Modal>

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

    <!-- Host picker — only meaningful when more than one host is wired up.
         The compose.yaml itself is host-neutral on disk; this picks the
         target host for the optional "Create & deploy" action. Falls
         back to whatever the sidebar host switcher had selected. -->
    {#if hosts.available.filter((h) => h.kind !== 'all').length > 1}
      <div>
        <label for="new-host" class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">
          Deploy on host
        </label>
        <select id="new-host" class="dm-input text-sm w-full" bind:value={newHostId} disabled={creating}>
          {#each hosts.available.filter((h) => h.kind !== 'all') as h (h.id)}
            <option value={h.id}>{h.name}{h.id === 'local' ? ' (server)' : ''}</option>
          {/each}
        </select>
        <p class="text-[10.5px] text-[var(--fg-subtle)] mt-1">
          Only used when you pick <em>Create &amp; deploy</em>. Plain <em>Create</em> saves the compose host-neutrally.
        </p>
      </div>
    {/if}

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
          label="Webhook secret (needed for push webhooks)"
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
      variant="secondary"
      type="submit"
      form="create-stack-form"
      loading={creating}
      disabled={creating || !newName ||
        (createMode === 'editor' ? !newCompose : !newGit.repo_url)}
    >
      {createMode === 'git' ? 'Import & sync' : 'Create'}
    </Button>
    <Button
      variant="primary"
      loading={creating}
      disabled={creating || !newName || !canDeploy ||
        (createMode === 'editor' ? !newCompose : !newGit.repo_url)}
      onclick={createAndDeploy}
    >
      Create & deploy
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
  .st-attn {
    border: 1px solid var(--warn-bd);
    background: var(--warn-bg);
    border-radius: 9px;
    padding: 4px 14px;
  }
  .st-attn-h {
    display: flex;
    align-items: center;
    gap: 8px;
    min-height: 36px;
    color: var(--warn);
    font-size: 13px;
  }
  .st-attn-h b { color: var(--fg); font-weight: 600; }
  .st-attn-row {
    display: grid;
    grid-template-columns: 200px minmax(0, 1fr) auto;
    gap: 12px;
    align-items: center;
    min-height: 36px;
    border-top: 1px solid var(--warn-bd);
    color: var(--fg);
    text-decoration: none;
    font-size: 13px;
  }
  .st-attn-row:hover .st-attn-cta { text-decoration: underline; }
  .st-attn-cta { color: var(--accent); display: inline-flex; align-items: center; gap: 4px; white-space: nowrap; }
  .st-adopt { display: flex; flex-direction: column; gap: 12px; }
  .st-adopt p { margin: 0; }
</style>
