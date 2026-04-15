<script lang="ts">
  import { api, ApiError } from '$lib/api';
  import { goto } from '$app/navigation';
  import { Button, Modal, EmptyState, Input, Skeleton, Badge } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { allowed } from '$lib/rbac';
  import { hosts } from '$lib/stores/host.svelte';
  import { Layers, Plus, FileCode2, Terminal, Search, Server, RefreshCw } from 'lucide-svelte';

  const canWrite = $derived(allowed('stack.write'));
  const isRemote = $derived(hosts.id !== 'local');

  type StackState = 'running' | 'stopped' | 'partial' | 'unhealthy';
  interface StackCard {
    name: string;
    state: StackState;
    services: Array<{ name: string; state: string; status: string }>;
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

  async function load() {
    loading = true;
    try {
      const [stackList, containers] = await Promise.all([
        api.stacks.list(),
        api.containers.list(true, hosts.id).catch(() => [])
      ]);

      // Group containers by stack name via the compose project label.
      // This avoids fanning out one /stacks/{name}/status call per stack.
      const byStack = new Map<string, any[]>();
      for (const c of containers) {
        const proj: string | undefined = c.Labels?.['com.docker.compose.project'];
        if (!proj) continue;
        if (!byStack.has(proj)) byStack.set(proj, []);
        byStack.get(proj)!.push(c);
      }

      stackCards = stackList.map((s: { name: string }) => {
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
        return {
          name: s.name,
          state,
          services: cs.map((c) => ({
            name:
              c.Labels?.['com.docker.compose.service'] ??
              (c.Names?.[0] ?? '').replace(/^\//, ''),
            state: c.State,
            status: c.Status ?? ''
          }))
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
  );

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
        <a
          href="/stacks/{s.name}"
          class="dm-card dm-card-hover p-4 block group"
        >
          <div class="flex items-start justify-between gap-2 mb-2.5">
            <div class="min-w-0 flex-1">
              <div class="font-semibold text-[15px] text-[var(--fg)] truncate">{s.name}</div>
              <div class="text-[11px] text-[var(--fg-subtle)] mt-0.5">
                {s.services.length} service{s.services.length === 1 ? '' : 's'}
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
