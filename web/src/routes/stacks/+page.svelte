<script lang="ts">
  import { api, ApiError, isFanOut, type StackDeployment, type DiscoveredStack } from '$lib/api';
  import { goto } from '$app/navigation';
  import { Button, Modal, EmptyState, Input, Skeleton, Badge } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { stackOps } from '$lib/stores/stackOps.svelte';
  import { allowed } from '$lib/rbac';
  import { autoRefresh } from '$lib/autorefresh';
  import { hosts } from '$lib/stores/host.svelte';
  import { Layers, Plus, FileCode2, Terminal, Search, Server, RefreshCw, Play, Square, ArrowUpDown, Clock, ArrowRightLeft, Anchor, Loader2 } from 'lucide-svelte';

  const canWrite = $derived(allowed('stack.write'));
  const canDeploy = $derived(allowed('stack.deploy'));
  const canAdopt = $derived(allowed('stack.adopt'));
  const isRemote = $derived(hosts.id !== 'local');

  // Sort
  type SortMode = 'name' | 'state' | 'deployed';
  let sortMode = $state<SortMode>('name');

  type StackState = 'running' | 'stopped' | 'partial' | 'unhealthy';
  interface StackCard {
    name: string;
    state: StackState;
    services: Array<{ name: string; state: string; status: string }>;
    // Hosts this stack's containers are currently running on. In
    // single-host mode this is always one entry; in all-mode we collect
    // every host that has matching compose-project labels.
    hosts: Array<{ id: string; name: string }>;
    // P.7: database-backed deployment association (host + status).
    deployment?: StackDeployment;
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

  let runCommand = $state('');
  let convertWarnings = $state<string[]>([]);
  let converting = $state(false);

  // Adoption: compose projects running on the current host that don't
  // have a stack dir on disk. Surfaced as a banner above the main grid
  // so first-time migrators from plain `docker compose up` see them
  // immediately, without an extra click.
  let discoveredStacks = $state<DiscoveredStack[]>([]);
  let showAdopt = $state(false);
  let adoptTarget = $state<DiscoveredStack | null>(null);
  let adoptCompose = $state('');
  let adopting = $state(false);

  async function load() {
    loading = true;
    try {
      const [stackList, containersRaw, discovered] = await Promise.all([
        api.stacks.list(),
        api.containers.list(true, hosts.id).catch(() => []),
        canAdopt ? api.stacks.discovered(hosts.id).catch(() => [] as DiscoveredStack[]) : Promise.resolve([] as DiscoveredStack[])
      ]);
      discoveredStacks = discovered;
      // Normalize containers to a bare array: if the picker is on 'all'
      // we get a FanOutResponse back, otherwise a plain array.
      const containers: any[] = isFanOut(containersRaw) ? containersRaw.items : containersRaw;

      // Group containers by stack name via the compose project label.
      // This avoids fanning out one /stacks/{name}/status call per stack.
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

        // Collect distinct hosts from the container rows. In single-host
        // mode containers have no host_id set, so we default to 'local'.
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
            status: c.Status ?? ''
          })),
          hosts: [...seenHost.entries()].map(([id, name]) => ({ id, name })),
          deployment: s.deployment
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
    try {
      await api.stacks.create(newName, newCompose, newEnv || undefined);
      toast.success('Stack created', newName);
      showCreate = false;
      newName = '';
      await load();
    } catch (err) {
      toast.error('Create failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      creating = false;
    }
  }

  function openAdopt(ds: DiscoveredStack) {
    adoptTarget = ds;
    // Pre-fill a skeleton so the textarea isn't a blank abyss. The
    // user is expected to paste their actual compose.yaml — we can't
    // reconstruct it from the running containers reliably.
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
        accepted_warnings: ['metadata-only-adoption']
      });
      toast.success('Adopted', `${res.name} (${res.bound_containers} container${res.bound_containers === 1 ? '' : 's'})`);
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
    // Re-load when the host switcher changes.
    hosts.id;
    load();
  });

  // Poll every 5s so deploy/stop actions on other tabs, remote-host
  // state changes, and auto-deploy git syncs show up without manually
  // hitting Refresh.
  $effect(() => autoRefresh(load, 5_000));

  const counts = $derived({
    all: stackCards.length,
    running: stackCards.filter((s) => s.state === 'running').length,
    stopped: stackCards.filter((s) => s.state === 'stopped').length,
    unhealthy: stackCards.filter((s) => s.state === 'unhealthy' || s.state === 'partial').length
  });

  const visible = $derived(
    stackCards
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
          const order: Record<string, number> = { running: 0, partial: 1, unhealthy: 2, stopped: 3 };
          return (order[a.state] ?? 9) - (order[b.state] ?? 9);
        }
        if (sortMode === 'deployed') {
          const aTime = a.deployment?.deployed_at ?? '';
          const bTime = b.deployment?.deployed_at ?? '';
          return bTime.localeCompare(aTime); // newest first
        }
        return a.name.localeCompare(b.name);
      })
  );

  // Quick deploy/stop from the card. Guard duplicates through the global
  // stackOps lock so a deploy in flight (even one started on the detail
  // page) keeps the card button busy here too.
  let actionBusy = $state<string | null>(null);
  async function quickDeploy(name: string) {
    if (stackOps.isBusy(hosts.id, name)) return;
    actionBusy = name;
    try {
      const res = await stackOps.run(hosts.id, name, () => api.stacks.deploy(name, hosts.id));
      toast.success('Deployed', `${res.services.length} service(s)`);
      await load();
    } catch (err) {
      toast.error('Deploy failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      actionBusy = null;
    }
  }
  async function quickStop(name: string) {
    if (stackOps.isBusy(hosts.id, name)) return;
    actionBusy = name;
    try {
      await stackOps.run(hosts.id, name, () => api.stacks.stop(name, hosts.id));
      toast.info('Stopped', name);
      await load();
    } catch (err) {
      toast.error('Stop failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      actionBusy = null;
    }
  }

  function fmtRelTime(ts?: string): string {
    if (!ts) return '';
    const secs = Math.floor((Date.now() - new Date(ts).getTime()) / 1000);
    if (secs < 60) return 'just now';
    if (secs < 3600) return `${Math.floor(secs / 60)}m ago`;
    if (secs < 86400) return `${Math.floor(secs / 3600)}h ago`;
    return `${Math.floor(secs / 86400)}d ago`;
  }

  function badgeVariant(state: StackState): 'success' | 'warning' | 'default' {
    if (state === 'running') return 'success';
    if (state === 'unhealthy' || state === 'partial') return 'warning';
    return 'default';
  }

  function stateLabel(state: StackState): string {
    if (state === 'partial') return 'partial';
    return state;
  }
</script>

<section class="space-y-6">
  <!-- Header -->
  <div class="flex items-start justify-between flex-wrap gap-3">
    <div>
      <div class="flex items-center gap-2">
        <h1 class="text-[22px] font-semibold tracking-tight">Stacks</h1>
        {#if isRemote}
          <span
            class="inline-flex items-center gap-1 text-[11px] px-2 py-0.5 rounded-full border border-[var(--color-brand-500)]/30 bg-[color-mix(in_srgb,var(--color-brand-500)_10%,transparent)] text-[var(--color-brand-400)]"
          >
            <Server class="w-3 h-3" />
            {hosts.selected?.name}
          </span>
        {/if}
      </div>
      <p class="text-sm text-[var(--fg-muted)] mt-0.5">
        Compose definitions stored on disk under
        <code class="font-mono text-xs text-[var(--fg)]">stacks/</code>
      </p>
    </div>
    <div class="flex items-center gap-2">
      <button
        onclick={load}
        class="dm-btn dm-btn-secondary dm-btn-sm"
        disabled={loading}
        aria-label="Refresh"
      >
        <RefreshCw class="w-3.5 h-3.5 {loading ? 'animate-spin' : ''}" />
        Refresh
      </button>
      {#if canWrite}
        <Button variant="primary" onclick={() => (showCreate = true)}>
          <Plus class="w-4 h-4" />
          New stack
        </Button>
      {/if}
    </div>
  </div>

  <!-- Discovered (unmanaged) section -->
  {#if canAdopt && discoveredStacks.length > 0}
    <div class="rounded-lg border border-[var(--color-brand-500)]/30 bg-[color-mix(in_srgb,var(--color-brand-500)_7%,transparent)] p-4 space-y-3">
      <div class="flex items-start gap-3">
        <Anchor class="w-4 h-4 text-[var(--color-brand-400)] mt-0.5 flex-shrink-0" />
        <div class="flex-1 min-w-0">
          <div class="text-sm font-medium">
            {discoveredStacks.length} unmanaged compose project{discoveredStacks.length === 1 ? '' : 's'} detected on this host
          </div>
          <p class="text-xs text-[var(--fg-muted)] mt-0.5">
            These are running via plain <code class="font-mono">docker compose up</code> — dockmesh can take over without restarting containers.
            For stacks with build contexts or relative-path bind mounts, prefer <code class="font-mono">dmctl stack adopt &lt;path&gt;</code> from the host shell.
          </p>
        </div>
      </div>
      <div class="space-y-2">
        {#each discoveredStacks as ds (ds.project_name)}
          <div class="flex items-center justify-between gap-3 rounded-md bg-[var(--bg-card)] border border-[var(--border)] px-3 py-2">
            <div class="min-w-0">
              <div class="font-mono text-sm truncate">{ds.project_name}</div>
              <div class="text-xs text-[var(--fg-muted)] truncate">
                {ds.service_count} service{ds.service_count === 1 ? '' : 's'} on {ds.host_name}:
                {ds.services.map((s) => s.name).join(', ')}
              </div>
            </div>
            <Button variant="secondary" onclick={() => openAdopt(ds)}>Adopt</Button>
          </div>
        {/each}
      </div>
    </div>
  {/if}

  <!-- Filter bar: search + state pills -->
  {#if !loading && stackCards.length > 0}
    <div class="flex flex-wrap items-center gap-3">
      <div class="relative flex-1 min-w-[200px] max-w-sm">
        <Search class="w-3.5 h-3.5 text-[var(--fg-subtle)] absolute left-2.5 top-1/2 -translate-y-1/2 pointer-events-none" />
        <input
          type="search"
          placeholder="Search stacks or services…"
          bind:value={search}
          class="dm-input pl-8 pr-3 py-1.5 text-sm w-full"
        />
      </div>
      <!-- Sort dropdown -->
      <div class="flex items-center gap-1.5 text-xs text-[var(--fg-muted)]">
        <ArrowUpDown class="w-3 h-3" />
        <select class="dm-input !py-0.5 !px-2 !w-auto text-xs" bind:value={sortMode}>
          <option value="name">Name</option>
          <option value="state">Status</option>
          <option value="deployed">Last deployed</option>
        </select>
      </div>

      <div class="flex gap-1 text-xs">
        {#snippet pill(key: 'all' | StackState, label: string, n: number)}
          <button
            class="px-2.5 py-1 rounded-full border transition-colors {filter === key
              ? 'bg-[var(--surface)] border-[var(--border-strong)] text-[var(--fg)]'
              : 'border-[var(--border)] text-[var(--fg-muted)] hover:bg-[var(--surface-hover)] hover:text-[var(--fg)]'}"
            onclick={() => (filter = key)}
          >
            {label} <span class="tabular-nums">{n}</span>
          </button>
        {/snippet}
        {@render pill('all', 'All', counts.all)}
        {@render pill('running', 'Running', counts.running)}
        {@render pill('stopped', 'Stopped', counts.stopped)}
        {@render pill('unhealthy', 'Issues', counts.unhealthy)}
      </div>
    </div>
  {/if}

  <!-- Stack grid -->
  {#if loading}
    <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
      {#each Array(6) as _}
        <div class="dm-card p-4 space-y-2">
          <Skeleton width="60%" height="1rem" />
          <Skeleton width="40%" height="0.85rem" />
          <Skeleton width="100%" height="1.25rem" />
        </div>
      {/each}
    </div>
  {:else if stackCards.length === 0}
    <div class="dm-card">
      <EmptyState
        icon={Layers}
        title="No stacks yet"
        description="Create your first stack by pasting a compose.yaml or importing a docker run command."
      >
        {#snippet action()}
          {#if canWrite}
            <Button variant="primary" onclick={() => (showCreate = true)}>
              <Plus class="w-4 h-4" />
              Create stack
            </Button>
          {/if}
        {/snippet}
      </EmptyState>
    </div>
  {:else if visible.length === 0}
    <div class="dm-card p-8 text-center text-sm text-[var(--fg-muted)]">
      No stacks match this filter.
    </div>
  {:else}
    <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
      {#each visible as s}
        <div class="dm-card dm-card-hover p-4 group relative">
          <a href="/stacks/{s.name}" class="block">
            <div class="flex items-start justify-between gap-2 mb-2.5">
              <div class="min-w-0 flex-1">
                <div class="font-semibold text-[15px] text-[var(--fg)] truncate">{s.name}</div>
                <div class="text-[11px] text-[var(--fg-subtle)] mt-0.5 flex items-center gap-2">
                  <span>{s.services.length} service{s.services.length === 1 ? '' : 's'}</span>
                  {#if s.deployment?.deployed_at}
                    <span class="inline-flex items-center gap-0.5">
                      <Clock class="w-2.5 h-2.5" />
                      {fmtRelTime(s.deployment.deployed_at)}
                    </span>
                  {/if}
                </div>
              </div>
              <Badge variant={badgeVariant(s.state)} dot>{stateLabel(s.state)}</Badge>
            </div>
            {#if s.services.length > 0}
              <div class="flex flex-wrap gap-1">
                {#each s.services.slice(0, 6) as svc}
                  <span
                    class="font-mono text-[10px] px-1.5 py-0.5 rounded border border-[var(--border)] text-[var(--fg-muted)]"
                    class:text-[var(--fg)]={svc.state === 'running'}
                    title={svc.status}
                  >
                    {svc.name}
                  </span>
                {/each}
                {#if s.services.length > 6}
                  <span class="font-mono text-[10px] px-1.5 py-0.5 text-[var(--fg-subtle)]">
                    +{s.services.length - 6}
                  </span>
                {/if}
              </div>
            {/if}
          </a>
          <!-- Footer: host + quick actions -->
          <div class="flex items-center justify-between mt-2 pt-2 border-t border-[var(--border)]">
            <div class="flex items-center gap-1.5">
              {#if s.deployment}
                <span class="inline-flex items-center gap-1 font-mono text-[10px] px-1.5 py-0.5 rounded border border-[var(--border)] text-[var(--fg-muted)]">
                  <Server class="w-2.5 h-2.5" />
                  {s.deployment.host_name || s.deployment.host_id}
                </span>
              {:else if hosts.isAll && s.hosts.length > 0}
                {#each s.hosts as h}
                  <span class="inline-flex items-center gap-0.5 font-mono text-[10px] px-1.5 py-0.5 rounded border border-[var(--border)] text-[var(--fg-muted)]">
                    <Server class="w-2.5 h-2.5" />
                    {h.name}
                  </span>
                {/each}
              {/if}
            </div>
            {#if canDeploy}
              <div class="flex gap-1">
                {#if hosts.available.length > 1}
                  <a
                    href="/stacks/{s.name}"
                    class="p-1 rounded-md text-[var(--fg-muted)] hover:text-[var(--color-brand-400)] hover:bg-[var(--surface-hover)]"
                    title="Migrate {s.name}"
                    onclick={(e) => e.stopPropagation()}
                  >
                    <ArrowRightLeft class="w-3.5 h-3.5" />
                  </a>
                {/if}
                {#if stackOps.isBusy(hosts.id, s.name)}
                  <span
                    class="inline-flex items-center gap-1 text-[10px] px-1.5 py-0.5 rounded border border-[var(--color-brand-500)]/40 bg-[color-mix(in_srgb,var(--color-brand-500)_10%,transparent)] text-[var(--color-brand-400)]"
                    title="Operation in progress"
                  >
                    <Loader2 class="w-3 h-3 animate-spin" />
                    working
                  </span>
                {:else if s.state === 'running' || s.state === 'partial' || s.state === 'unhealthy'}
                  <button
                    class="p-1 rounded-md text-[var(--fg-muted)] hover:text-[var(--fg)] hover:bg-[var(--surface-hover)] disabled:opacity-50"
                    title="Stop {s.name}"
                    disabled={actionBusy === s.name}
                    onclick={(e) => { e.preventDefault(); e.stopPropagation(); quickStop(s.name); }}
                  >
                    <Square class="w-3.5 h-3.5" />
                  </button>
                {:else}
                  <button
                    class="p-1 rounded-md text-[var(--color-success-400)] hover:bg-[color-mix(in_srgb,var(--color-success-500)_10%,transparent)] disabled:opacity-50"
                    title="Deploy {s.name}"
                    disabled={actionBusy === s.name}
                    onclick={(e) => { e.preventDefault(); e.stopPropagation(); quickDeploy(s.name); }}
                  >
                    <Play class="w-3.5 h-3.5" />
                  </button>
                {/if}
              </div>
            {/if}
          </div>
        </div>
      {/each}
    </div>
  {/if}
</section>

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
      disabled={creating || !newName || !newCompose}
    >
      Create
    </Button>
  {/snippet}
</Modal>

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
