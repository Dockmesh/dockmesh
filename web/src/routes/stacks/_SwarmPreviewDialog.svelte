<script lang="ts">
  import { api, ApiError, type Cluster, type SwarmPreview, type SwarmPreviewFinding } from '$lib/api';
  import { Button, Modal } from '$lib/components/ui';
  import { AlertTriangle, CheckCircle2, XCircle } from 'lucide-svelte';
  import { m } from '$lib/paraglide/messages.js';

  let { open = $bindable(false), name }: { open?: boolean; name: string } = $props();

  let clusters = $state<Cluster[] | null>(null);
  let cluster = $state('');
  let preview = $state<SwarmPreview | null>(null);
  let error = $state<string | null>(null);
  let busy = $state(false);

  $effect(() => {
    if (!open) return;
    preview = null;
    error = null;
    api.clusters
      .list()
      .then((cs) => {
        clusters = cs ?? [];
        if (!clusters.some((c) => c.id === cluster)) cluster = clusters[0]?.id ?? '';
      })
      .catch((err) => {
        clusters = [];
        error = err instanceof ApiError ? err.message : String(err);
      });
  });

  async function run() {
    if (!cluster) return;
    busy = true;
    error = null;
    try {
      preview = await api.stacks.swarmPreview(name, cluster);
    } catch (err) {
      preview = null;
      error = err instanceof ApiError ? err.message : String(err);
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
          <td class="py-1">
            {f.message}
            {#if f.hint}<span class="block text-[var(--fg-muted)]">{m.stacks_swp_use({ hint: f.hint })}</span>{/if}
          </td>
        </tr>
      {/each}
    </tbody>
  </table>
{/snippet}

<Modal bind:open title={m.stacks_swp_title({ name })} maxWidth="max-w-4xl">
  <div class="space-y-3 text-sm">
    <p class="text-[var(--fg-muted)]">{m.stacks_swp_intro()}</p>

    {#if clusters && clusters.length === 0 && !error}
      <p class="text-xs text-[var(--fg-muted)]">{m.stacks_swp_no_clusters()}</p>
    {:else if clusters}
      <div class="flex items-end gap-2">
        <label class="flex flex-col gap-1 text-xs">
          <span class="text-[var(--fg-muted)]">{m.stacks_swp_cluster()}</span>
          <select class="dm-input min-w-56" bind:value={cluster}>
            {#each clusters as c (c.id)}
              <option value={c.id}>{c.name || c.id}</option>
            {/each}
          </select>
        </label>
        <Button onclick={run} disabled={busy || !cluster}>{m.stacks_swp_run()}</Button>
      </div>
    {/if}

    {#if error}
      <div class="p-2.5 rounded-md border border-[color-mix(in_srgb,var(--color-danger-500)_40%,transparent)] text-xs text-[var(--color-danger-400)]">{error}</div>
    {/if}

    {#if preview}
      <div class="flex items-center gap-2 font-medium">
        {#if preview.deployable}
          <CheckCircle2 class="w-4 h-4 text-[var(--color-success-400)]" /> {m.stacks_swp_deployable()}
        {:else}
          <XCircle class="w-4 h-4 text-[var(--color-danger-400)]" /> {m.stacks_swp_refused()}
        {/if}
      </div>

      {#if preview.modified_outside.length > 0}
        <div class="flex items-start gap-2 text-xs text-[var(--color-warning-400)]">
          <AlertTriangle class="w-3.5 h-3.5 shrink-0 mt-0.5" />
          {m.stacks_swp_modified({ files: preview.modified_outside.join(', ') })}
        </div>
      {/if}

      <section>
        <h3 class="text-xs font-semibold mb-1">{m.stacks_swp_refusals({ count: preview.refusals.length })}</h3>
        {#if preview.refusals.length > 0}
          {@render findings(preview.refusals, 'danger')}
        {:else}
          <p class="text-xs text-[var(--fg-muted)]">{m.stacks_swp_none()}</p>
        {/if}
      </section>

      <section>
        <h3 class="text-xs font-semibold mb-1">{m.stacks_swp_warnings({ count: preview.warnings.length })}</h3>
        {#if preview.warnings.length > 0}
          {@render findings(preview.warnings, 'warning')}
        {:else}
          <p class="text-xs text-[var(--fg-muted)]">{m.stacks_swp_none()}</p>
        {/if}
      </section>

      {#if preview.external_networks.length > 0}
        <p class="text-xs">{m.stacks_swp_external({ names: preview.external_networks.join(', ') })}</p>
      {/if}

      {#if preview.services.length > 0}
        <section>
          <h3 class="text-xs font-semibold mb-1">{m.stacks_swp_services({ count: preview.services.length })}</h3>
          <p class="text-xs text-[var(--fg-muted)] mb-1">{m.stacks_swp_env_note()}</p>
          {#each preview.services as s, i (s.Name ?? i)}
            <details class="border border-[var(--border)] rounded-md mb-1">
              <summary class="px-2 py-1 cursor-pointer dm-mono text-xs">{s.Name}</summary>
              <pre class="px-2 py-1 text-[11px] overflow-auto max-h-80">{JSON.stringify(s, null, 2)}</pre>
            </details>
          {/each}
        </section>
      {/if}

      {#if preview.networks.length > 0}
        <section>
          <h3 class="text-xs font-semibold mb-1">{m.stacks_swp_networks({ count: preview.networks.length })}</h3>
          {#each preview.networks as n, i (n.Name ?? i)}
            <details class="border border-[var(--border)] rounded-md mb-1">
              <summary class="px-2 py-1 cursor-pointer dm-mono text-xs">{n.Name}</summary>
              <pre class="px-2 py-1 text-[11px] overflow-auto max-h-60">{JSON.stringify(n, null, 2)}</pre>
            </details>
          {/each}
        </section>
      {/if}
    {/if}
  </div>
</Modal>
