<script lang="ts">
  import { page } from '$app/stores';
  import { goto } from '$app/navigation';
  import { api, ApiError } from '$lib/api';

  const name = $derived($page.params.name);

  let compose = $state('');
  let env = $state('');
  let services = $state<Array<{ service: string; container_id: string; state: string; status: string; image: string }>>([]);
  let loading = $state(true);
  let error = $state('');
  let busy = $state(false);

  async function load() {
    loading = true;
    error = '';
    try {
      const detail = await api.stacks.get(name);
      compose = detail.compose;
      env = detail.env ?? '';
      try {
        services = await api.stacks.status(name);
      } catch {
        services = [];
      }
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Load failed';
    } finally {
      loading = false;
    }
  }

  async function save() {
    busy = true;
    error = '';
    try {
      await api.stacks.update(name, compose, env || undefined);
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Save failed';
    } finally {
      busy = false;
    }
  }

  async function deploy() {
    busy = true;
    error = '';
    try {
      await api.stacks.deploy(name);
      services = await api.stacks.status(name);
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Deploy failed';
    } finally {
      busy = false;
    }
  }

  async function stop() {
    busy = true;
    error = '';
    try {
      await api.stacks.stop(name);
      services = [];
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Stop failed';
    } finally {
      busy = false;
    }
  }

  async function del() {
    if (!confirm(`Delete stack "${name}"? This removes the compose file.`)) return;
    busy = true;
    try {
      await api.stacks.delete(name);
      goto('/stacks');
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Delete failed';
      busy = false;
    }
  }

  $effect(() => {
    if (name) load();
  });
</script>

<section class="space-y-4">
  <div class="flex items-center gap-3">
    <a href="/stacks" class="text-[var(--muted)] hover:text-[var(--fg)]">← Stacks</a>
    <h2 class="text-xl font-semibold">{name}</h2>
  </div>

  {#if error}
    <div class="p-3 rounded border border-red-500/30 bg-red-500/10 text-red-500 text-sm">{error}</div>
  {/if}

  {#if loading}
    <p class="text-[var(--muted)]">Loading…</p>
  {:else}
    <div class="flex gap-2 flex-wrap">
      <button class="px-4 py-2 rounded bg-brand-500 text-white font-semibold disabled:opacity-50" onclick={deploy} disabled={busy}>
        Deploy
      </button>
      <button class="px-4 py-2 rounded border border-[var(--border)] disabled:opacity-50" onclick={stop} disabled={busy}>
        Stop
      </button>
      <button class="px-4 py-2 rounded border border-[var(--border)] disabled:opacity-50" onclick={save} disabled={busy}>
        Save
      </button>
      <button class="px-4 py-2 rounded border border-red-500/50 text-red-500 disabled:opacity-50 ml-auto" onclick={del} disabled={busy}>
        Delete
      </button>
    </div>

    {#if services.length > 0}
      <div>
        <h3 class="text-sm font-semibold text-[var(--muted)] mb-2">Services</h3>
        <div class="space-y-2">
          {#each services as s}
            <div class="p-3 rounded border border-[var(--border)] bg-[var(--panel)] flex justify-between items-center">
              <div>
                <div class="font-mono text-sm">{s.service}</div>
                <div class="text-xs text-[var(--muted)]">{s.image}</div>
              </div>
              <div class="text-right">
                <span class="text-xs px-2 py-0.5 rounded {s.state === 'running' ? 'bg-green-500/20 text-green-500' : 'bg-[var(--bg)] text-[var(--muted)]'}">
                  {s.state}
                </span>
                <div class="text-xs text-[var(--muted)] mt-1">{s.status}</div>
              </div>
            </div>
          {/each}
        </div>
      </div>
    {/if}

    <div>
      <label for="compose" class="block text-sm mb-1 text-[var(--muted)]">compose.yaml</label>
      <textarea
        id="compose"
        class="w-full h-80 px-3 py-2 font-mono text-sm rounded border border-[var(--border)] bg-[var(--bg)]"
        bind:value={compose}
      ></textarea>
    </div>

    <div>
      <label for="env" class="block text-sm mb-1 text-[var(--muted)]">.env (optional)</label>
      <textarea
        id="env"
        class="w-full h-24 px-3 py-2 font-mono text-sm rounded border border-[var(--border)] bg-[var(--bg)]"
        bind:value={env}
      ></textarea>
    </div>
  {/if}
</section>
