<script lang="ts">
  import { api, ApiError, type SwarmAdoptPreview, type SwarmAdoptResult, type SwarmPreviewFinding } from '$lib/api';
  import { Button, Modal } from '$lib/components/ui';
  import { AlertTriangle, CheckCircle2, XCircle } from 'lucide-svelte';
  import { m } from '$lib/paraglide/messages.js';

  let { open = $bindable(false), cluster, stack, onadopted }: { open?: boolean; cluster: string; stack: string; onadopted?: () => void } = $props();

  let preview = $state<SwarmAdoptPreview | null>(null);
  let error = $state<string | null>(null);
  let busy = $state(false);
  let result = $state<SwarmAdoptResult | null>(null);
  const changed = $derived(preview?.services.filter((s) => s.changes.length > 0) ?? []);

  $effect(() => {
    if (!open) return;
    preview = null;
    result = null;
    error = null;
    busy = true;
    api.clusters
      .adoptPreview(cluster, stack)
      .then((p) => (preview = p))
      .catch((err) => (error = err instanceof ApiError ? err.message : String(err)))
      .finally(() => (busy = false));
  });

  async function adopt() {
    if (!preview) return;
    busy = true;
    error = null;
    try {
      result = await api.clusters.adopt(cluster, stack, preview.plan_hash);
      onadopted?.();
    } catch (err) {
      if (err instanceof ApiError && err.body?.outcome) result = err.body as SwarmAdoptResult;
      else error = err instanceof ApiError ? err.message : String(err);
    } finally {
      busy = false;
    }
  }
</script>

{#snippet findings(rows: SwarmPreviewFinding[], tone: 'danger' | 'warning')}
  <table class="w-full text-xs border-collapse">
    <thead>
      <tr class="text-left text-[var(--fg-muted)] border-b border-[var(--border)]">
        <th class="py-1 pr-3 font-medium w-32">{m.stacks_swp_col_service()}</th>
        <th class="py-1 pr-3 font-medium w-56">{m.stacks_swp_col_key()}</th>
        <th class="py-1 font-medium">{m.stacks_swp_col_reason()}</th>
      </tr>
    </thead>
    <tbody>
      {#each rows as f (f.service + '/' + f.key + '/' + f.message)}
        <tr class="border-b border-[var(--border)] align-top">
          <td class="py-1 pr-3 dm-mono">{f.service ?? '—'}</td>
          <td class="py-1 pr-3 dm-mono break-all {tone === 'danger' ? 'text-[var(--color-danger-400)]' : 'text-[var(--color-warning-400)]'}">{f.key}</td>
          <td class="py-1">{f.message}</td>
        </tr>
      {/each}
    </tbody>
  </table>
{/snippet}

<Modal bind:open title={m.cl_adopt_title({ name: stack })} maxWidth="max-w-4xl">
  <div class="space-y-3 text-sm" data-testid="adopt-dialog">
    <p class="text-[var(--fg-muted)]">{m.cl_adopt_intro()}</p>

    {#if busy && !preview}
      <p class="text-xs text-[var(--fg-muted)]">{m.cl_adopt_reading()}</p>
    {/if}
    {#if error}
      <div class="p-2.5 rounded-md border border-[color-mix(in_srgb,var(--color-danger-500)_40%,transparent)] text-xs text-[var(--color-danger-400)]">{error}</div>
    {/if}

    {#if result}
      <div class="flex items-center gap-2 font-medium">
        {#if result.outcome === 'applied'}
          <CheckCircle2 class="w-4 h-4 text-[var(--color-success-400)]" /> {m.cl_adopt_done({ name: stack })}
          <a class="ml-auto underline" href="/stacks/{encodeURIComponent(stack)}">{m.cl_adopt_open()}</a>
        {:else}
          <XCircle class="w-4 h-4 text-[var(--color-danger-400)]" /> {m.cl_adopt_failed()}
        {/if}
      </div>
      {#if result.error}<p class="text-xs text-[var(--color-danger-400)]">{result.error}</p>{/if}
    {:else if preview}
      {#if preview.refusals.length > 0}
        <div class="flex items-center gap-2 font-medium"><XCircle class="w-4 h-4 text-[var(--color-danger-400)]" /> {m.cl_adopt_refused()}</div>
        {@render findings(preview.refusals, 'danger')}
      {:else}
        <div class="flex items-center">
          <Button class="ml-auto" onclick={adopt} disabled={busy}>{m.cl_adopt_confirm()}</Button>
        </div>
      {/if}

      <section class="space-y-1">
        <h3 class="text-xs font-medium">{m.cl_adopt_changes()}</h3>
        {#if changed.length === 0}
          <p class="text-xs text-[var(--fg-muted)]">{m.cl_adopt_no_changes()}</p>
        {:else}
          <table class="w-full text-xs border-collapse">
            <tbody>
              {#each changed as s (s.id)}
                <tr class="border-b border-[var(--border)] align-top">
                  <td class="py-1 pr-3 dm-mono w-48">{s.name}</td>
                  <td class="py-1 dm-mono text-[var(--color-warning-400)]">{s.changes.join(', ')}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        {/if}
      </section>

      {#if preview.not_carried.length > 0}
        <section class="space-y-1">
          <h3 class="text-xs font-medium flex items-center gap-1"><AlertTriangle class="w-3.5 h-3.5 text-[var(--color-warning-400)]" /> {m.cl_adopt_not_carried()}</h3>
          <p class="text-xs text-[var(--fg-muted)]">{m.cl_adopt_not_carried_hint()}</p>
          {@render findings(preview.not_carried, 'warning')}
        </section>
      {/if}

      {#if preview.warnings.length > 0}
        {@render findings(preview.warnings, 'warning')}
      {/if}

      {#if preview.kept_networks.length > 0}
        <section class="space-y-1">
          <h3 class="text-xs font-medium">{m.cl_adopt_kept()}</h3>
          <p class="text-xs text-[var(--fg-muted)]">{m.cl_adopt_kept_hint()} <span class="dm-mono">{preview.kept_networks.join(', ')}</span></p>
        </section>
      {/if}

      <section class="space-y-1">
        <h3 class="text-xs font-medium">{m.cl_adopt_file()}</h3>
        <p class="text-xs text-[var(--fg-muted)]">{m.cl_adopt_visible()}</p>
        <pre class="dm-mono text-xs max-h-72 overflow-auto p-2 rounded-md border border-[var(--border)] bg-[var(--bg-subtle)]">{preview.compose}</pre>
      </section>
      {#if preview.env_keys.length > 0}
        <section class="space-y-1">
          <h3 class="text-xs font-medium">{m.cl_adopt_env()}</h3>
          <p class="dm-mono text-xs text-[var(--fg-muted)]">{preview.env_keys.join(', ')}</p>
        </section>
      {/if}
    {/if}
  </div>
</Modal>
