<script lang="ts">
  import { api, ApiError, type Cluster, type SwarmDeployResult, type SwarmPreview, type SwarmPreviewFinding } from '$lib/api';
  import { Button, Modal } from '$lib/components/ui';
  import { AlertTriangle, CheckCircle2, XCircle } from 'lucide-svelte';
  import { m } from '$lib/paraglide/messages.js';

  let { open = $bindable(false), name }: { open?: boolean; name: string } = $props();

  let clusters = $state<Cluster[] | null>(null);
  let cluster = $state('');
  let preview = $state<SwarmPreview | null>(null);
  let error = $state<string | null>(null);
  let busy = $state(false);
  let result = $state<SwarmDeployResult | null>(null);
  let prune = $state(false);
  const leftovers = $derived(preview?.prune ? [...preview.prune.services, ...preview.prune.networks] : []);

  $effect(() => {
    if (!open) return;
    preview = null;
    result = null;
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
    result = null;
    prune = false;
    try {
      preview = await api.stacks.swarmPreview(name, cluster);
    } catch (err) {
      preview = null;
      error = err instanceof ApiError ? err.message : String(err);
    } finally {
      busy = false;
    }
  }

  async function deploy() {
    if (!preview?.plan_hash) return;
    busy = true;
    error = null;
    try {
      result = await api.stacks.swarmDeploy(name, preview.cluster_id, preview.plan_hash, prune, preview.modified_outside.length > 0);
    } catch (err) {
      if (err instanceof ApiError && err.body?.outcome) {
        result = err.body as SwarmDeployResult;
      } else {
        error = err instanceof ApiError ? err.message : String(err);
      }
    } finally {
      busy = false;
    }
  }

  const outcomeText = (o: SwarmDeployResult['outcome']) =>
    o === 'applied' ? m.stacks_swd_applied() : o === 'partial' ? m.stacks_swd_partial() : m.stacks_swd_unknown();
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
          {#if preview.plan_hash && !result}
            <Button class="ml-auto" onclick={deploy} disabled={busy}>{m.stacks_swd_deploy({ cluster: preview.cluster_id })}</Button>
          {/if}
        {:else}
          <XCircle class="w-4 h-4 text-[var(--color-danger-400)]" /> {m.stacks_swp_refused()}
        {/if}
      </div>

      {#if preview.prune_unknown}
        <div class="flex items-start gap-2 text-xs text-[var(--color-warning-400)]">
          <AlertTriangle class="w-3.5 h-3.5 shrink-0 mt-0.5" />
          {preview.prune_unknown}
        </div>
      {/if}

      {#if preview.prune && leftovers.length > 0}
        <section class="border border-[color-mix(in_srgb,var(--color-warning-500)_40%,transparent)] rounded-md p-2 space-y-1">
          <h3 class="text-xs font-semibold">{m.stacks_swd_prune_title({ count: leftovers.length })}</h3>
          <p class="text-xs text-[var(--fg-muted)]">{m.stacks_swd_prune_intro()}</p>
          {#if preview.prune.services.length > 0}
            <p class="text-xs">{m.stacks_swd_prune_services()} <span class="dm-mono">{preview.prune.services.join(', ')}</span></p>
          {/if}
          {#if preview.prune.networks.length > 0}
            <p class="text-xs">{m.stacks_swd_prune_networks()} <span class="dm-mono">{preview.prune.networks.join(', ')}</span></p>
          {/if}
          <label class="flex items-center gap-2 text-xs">
            <input type="checkbox" bind:checked={prune} disabled={busy || !!result} />
            {m.stacks_swd_prune_confirm({ count: leftovers.length })}
          </label>
        </section>
      {/if}

      {#if result}
        <section class="border border-[var(--border)] rounded-md p-2 space-y-1">
          <h3 class="text-xs font-semibold">{outcomeText(result.outcome)}</h3>
          <p class="text-xs text-[var(--fg-muted)]">
            {m.stacks_swd_via({ manager: result.manager })}{#if result.checked_via} · {m.stacks_swd_checked({ host: result.checked_via })}{/if}
          </p>
          {#if result.error}<p class="text-xs text-[var(--color-danger-400)]">{result.error}</p>{/if}
          {#if result.prune_error}<p class="text-xs text-[var(--color-danger-400)]">{m.stacks_swd_prune_failed({ reason: result.prune_error })}</p>{/if}
          {#if result.pruned}
            <p class="text-xs">
              {m.stacks_swd_pruned()}
              <span class="dm-mono">{[...result.pruned.services, ...result.pruned.networks].map((x) => `${x.name} (${x.action})`).join(', ')}</span>
            </p>
          {/if}
          {#if result.not_pruned && !result.prune_error}
            <p class="text-xs text-[var(--fg-muted)]">
              {m.stacks_swd_not_pruned()} <span class="dm-mono">{[...result.not_pruned.services, ...result.not_pruned.networks].join(', ')}</span>
            </p>
          {/if}
          <table class="w-full text-xs border-collapse">
            <thead>
              <tr class="text-left text-[var(--fg-muted)] border-b border-[var(--border)]">
                <th class="py-1 pr-3 font-medium">{m.stacks_swp_col_service()}</th>
                <th class="py-1 pr-3 font-medium w-28">{m.stacks_swd_col_action()}</th>
                <th class="py-1 pr-3 font-medium w-24">{m.stacks_swd_col_tasks()}</th>
                <th class="py-1 font-medium">{m.stacks_swd_col_state()}</th>
              </tr>
            </thead>
            <tbody>
              {#each result.services as s (s.name)}
                <tr class="border-b border-[var(--border)] align-top">
                  <td class="py-1 pr-3 dm-mono">{s.name}</td>
                  <td class="py-1 pr-3 {s.action === 'failed' || s.applied === false ? 'text-[var(--color-danger-400)]' : ''}">
                    {s.action}{#if s.applied !== undefined} · {s.applied ? m.stacks_swd_is_applied() : m.stacks_swd_not_applied()}{/if}
                  </td>
                  <td class="py-1 pr-3 tabular-nums">{s.running_tasks}/{s.desired_tasks}</td>
                  <td class="py-1">{s.error ?? s.update_state ?? ''}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </section>
      {/if}

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
