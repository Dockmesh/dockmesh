<script lang="ts">
  import { api, ApiError } from '$lib/api';
  import { goto } from '$app/navigation';
  import { Button, Card, Modal, EmptyState, Input, Skeleton } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { allowed } from '$lib/rbac';
  import { Layers, Plus, FileCode2, Terminal } from 'lucide-svelte';

  const canWrite = $derived(allowed('stack.write'));

  let stacks = $state<Array<{ name: string }>>([]);
  let loading = $state(true);
  let showCreate = $state(false);
  let showImport = $state(false);

  let newName = $state('');
  let newCompose = $state('services:\n  web:\n    image: nginx:alpine\n    ports:\n      - "8080:80"\n');
  let newEnv = $state('');
  let creating = $state(false);

  let runCommand = $state('');
  let convertWarnings = $state<string[]>([]);
  let converting = $state(false);

  async function load() {
    loading = true;
    try {
      stacks = await api.stacks.list();
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

  $effect(() => { load(); });
</script>

<section class="space-y-6">
  <div class="flex items-center justify-between flex-wrap gap-3">
    <div>
      <h2 class="text-2xl font-semibold tracking-tight">Stacks</h2>
      <p class="text-sm text-[var(--fg-muted)] mt-0.5">
        Compose definitions stored on disk under <code class="font-mono text-xs">stacks/</code>
      </p>
    </div>
    {#if canWrite}
      <Button variant="primary" onclick={() => (showCreate = true)}>
        <Plus class="w-4 h-4" />
        New Stack
      </Button>
    {/if}
  </div>

  {#if loading}
    <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4">
      {#each Array(3) as _}
        <Card class="p-5">
          <Skeleton width="60%" height="1.25rem" />
          <Skeleton class="mt-3" width="40%" height="0.85rem" />
        </Card>
      {/each}
    </div>
  {:else if stacks.length === 0}
    <Card>
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
    </Card>
  {:else}
    <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4">
      {#each stacks as s}
        <Card hover onclick={() => goto(`/stacks/${s.name}`)} class="p-5">
          <div class="flex items-start gap-3">
            <div class="w-10 h-10 rounded-lg bg-[color-mix(in_srgb,var(--color-brand-500)_15%,transparent)] text-[var(--color-brand-400)] flex items-center justify-center shrink-0">
              <Layers class="w-5 h-5" />
            </div>
            <div class="min-w-0 flex-1">
              <div class="font-semibold text-[var(--fg)] truncate">{s.name}</div>
              <div class="text-xs text-[var(--fg-muted)] mt-0.5">filesystem-backed</div>
            </div>
          </div>
        </Card>
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
      <div class="dm-card p-3 text-xs border-[color-mix(in_srgb,var(--color-warning-500)_30%,transparent)]">
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
