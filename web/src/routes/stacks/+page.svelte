<script lang="ts">
  import { api, ApiError } from '$lib/api';
  import { goto } from '$app/navigation';

  let stacks = $state<Array<{ name: string }>>([]);
  let loading = $state(true);
  let error = $state('');
  let showCreate = $state(false);
  let newName = $state('');
  let newCompose = $state('services:\n  web:\n    image: nginx:alpine\n    ports:\n      - "8080:80"\n');
  let newEnv = $state('');
  let creating = $state(false);

  // docker run → compose converter
  let showImport = $state(false);
  let runCommand = $state('');
  let convertWarnings = $state<string[]>([]);
  let converting = $state(false);

  async function convertRun() {
    converting = true;
    convertWarnings = [];
    try {
      const res = await api.convert.runToCompose(runCommand);
      newCompose = res.yaml;
      convertWarnings = res.warnings ?? [];
      showImport = false;
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'convert failed';
    } finally {
      converting = false;
    }
  }

  async function load() {
    loading = true;
    try {
      stacks = await api.stacks.list();
    } catch (err: any) {
      error = err.message;
    } finally {
      loading = false;
    }
  }

  async function create(e: Event) {
    e.preventDefault();
    creating = true;
    error = '';
    try {
      await api.stacks.create(newName, newCompose, newEnv || undefined);
      showCreate = false;
      newName = '';
      await load();
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Create failed';
    } finally {
      creating = false;
    }
  }

  $effect(() => { load(); });
</script>

<section class="space-y-4">
  <div class="flex justify-between items-center">
    <h2 class="text-xl font-semibold">Stacks</h2>
    <button class="px-3 py-1 rounded bg-brand-500 text-white text-sm" onclick={() => (showCreate = true)}>
      + New Stack
    </button>
  </div>

  {#if error}
    <div class="p-3 rounded border border-red-500/30 bg-red-500/10 text-red-500 text-sm">{error}</div>
  {/if}

  {#if loading}
    <p class="text-[var(--muted)]">Loading…</p>
  {:else if stacks.length === 0}
    <p class="text-[var(--muted)]">No stacks yet. Create one to get started.</p>
  {:else}
    <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4">
      {#each stacks as s}
        <button
          class="text-left p-4 rounded border border-[var(--border)] bg-[var(--panel)] hover:border-brand-500"
          onclick={() => goto(`/stacks/${s.name}`)}
        >
          <div class="font-semibold">{s.name}</div>
          <div class="text-xs text-[var(--muted)] mt-1">Filesystem-backed</div>
        </button>
      {/each}
    </div>
  {/if}
</section>

{#if showCreate}
  <div class="fixed inset-0 bg-black/50 flex items-center justify-center p-4 z-10">
    <form
      onsubmit={create}
      class="w-full max-w-2xl p-6 rounded border border-[var(--border)] bg-[var(--panel)] space-y-3"
    >
      <div class="flex justify-between items-center">
        <h3 class="text-lg font-semibold">Create Stack</h3>
        <button
          type="button"
          class="text-sm px-2 py-1 rounded border border-[var(--border)] hover:border-brand-500"
          onclick={() => (showImport = true)}
        >
          Import from docker run
        </button>
      </div>
      <input
        class="w-full px-3 py-2 rounded border border-[var(--border)] bg-[var(--bg)]"
        placeholder="Name (lowercase, digits, hyphens)"
        bind:value={newName}
        disabled={creating}
      />
      <textarea
        class="w-full h-48 px-3 py-2 font-mono text-sm rounded border border-[var(--border)] bg-[var(--bg)]"
        placeholder="compose.yaml"
        bind:value={newCompose}
        disabled={creating}
      ></textarea>
      <textarea
        class="w-full h-20 px-3 py-2 font-mono text-sm rounded border border-[var(--border)] bg-[var(--bg)]"
        placeholder=".env (optional)"
        bind:value={newEnv}
        disabled={creating}
      ></textarea>
      {#if convertWarnings.length > 0}
        <div class="p-2 rounded border border-yellow-500/30 bg-yellow-500/10 text-yellow-400 text-xs">
          <strong>Warnings from converter:</strong>
          <ul class="list-disc list-inside">
            {#each convertWarnings as w}<li>{w}</li>{/each}
          </ul>
        </div>
      {/if}
      <div class="flex justify-end gap-2">
        <button type="button" class="px-4 py-2 rounded border border-[var(--border)]" onclick={() => (showCreate = false)}>
          Cancel
        </button>
        <button type="submit" class="px-4 py-2 rounded bg-brand-500 text-white font-semibold" disabled={creating || !newName || !newCompose}>
          {creating ? 'Creating…' : 'Create'}
        </button>
      </div>
    </form>
  </div>
{/if}

{#if showImport}
  <div class="fixed inset-0 bg-black/70 flex items-center justify-center p-4 z-20">
    <div class="w-full max-w-2xl p-6 rounded border border-[var(--border)] bg-[var(--panel)] space-y-3">
      <h3 class="text-lg font-semibold">Import from docker run</h3>
      <p class="text-sm text-[var(--muted)]">
        Paste a complete <code class="font-mono">docker run</code> command. We'll convert
        it into compose YAML. Supports ports, volumes, env, networks, restart,
        labels, and the common flags.
      </p>
      <textarea
        class="w-full h-32 px-3 py-2 font-mono text-sm rounded border border-[var(--border)] bg-[var(--bg)]"
        placeholder="docker run -d --name web -p 8080:80 nginx:alpine"
        bind:value={runCommand}
      ></textarea>
      <div class="flex justify-end gap-2">
        <button class="px-4 py-2 rounded border border-[var(--border)]" onclick={() => (showImport = false)}>Cancel</button>
        <button
          class="px-4 py-2 rounded bg-brand-500 text-white font-semibold disabled:opacity-50"
          onclick={convertRun}
          disabled={converting || !runCommand.trim()}
        >
          {converting ? 'Converting…' : 'Convert'}
        </button>
      </div>
    </div>
  </div>
{/if}
