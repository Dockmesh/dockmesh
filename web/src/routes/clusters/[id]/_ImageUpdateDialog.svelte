<script lang="ts">
  import { api, ApiError, type SwarmDeployResult, type SwarmImageCheck } from '$lib/api';
  import { Button, Modal } from '$lib/components/ui';
  import { m } from '$lib/paraglide/messages.js';

  let { open = $bindable(false), cluster, stack, onupdated }: { open?: boolean; cluster: string; stack: string; onupdated?: () => void } = $props();

  let check = $state<SwarmImageCheck | null>(null);
  let error = $state<string | null>(null);
  let busy = $state(false);
  let result = $state<SwarmDeployResult | null>(null);
  const updates = $derived(check?.services.filter((s) => s.update) ?? []);
  const short = (d?: string) => (d ? d.replace('sha256:', '').slice(0, 12) : '—');

  $effect(() => {
    if (!open) return;
    check = null;
    result = null;
    error = null;
    busy = true;
    api.stacks
      .swarmImageUpdates(stack)
      .then((c) => (check = c))
      .catch((err) => (error = err instanceof ApiError ? err.message : String(err)))
      .finally(() => (busy = false));
  });

  async function update() {
    busy = true;
    error = null;
    try {
      const p = await api.stacks.swarmPreview(stack, cluster);
      if (!p.deployable || !p.plan_hash) {
        error = m.cl_img_not_deployable();
        return;
      }
      if (p.modified_outside.length > 0) {
        error = m.cl_img_modified();
        return;
      }
      result = await api.stacks.swarmDeploy(stack, cluster, p.plan_hash, false);
      onupdated?.();
    } catch (err) {
      if (err instanceof ApiError && err.body?.outcome) result = err.body as SwarmDeployResult;
      else error = err instanceof ApiError ? err.message : String(err);
    } finally {
      busy = false;
    }
  }
</script>

<Modal bind:open title={m.cl_img_title({ name: stack })} maxWidth="max-w-3xl">
  <div class="space-y-3 text-sm" data-testid="image-dialog">
    <p class="text-[var(--fg-muted)]">{m.cl_img_intro()}</p>
    {#if busy && !check}<p class="text-xs text-[var(--fg-muted)]">{m.cl_img_checking()}</p>{/if}
    {#if error}<p class="text-xs text-[var(--color-danger-400)]">{error}</p>{/if}
    {#if check}
      <table class="w-full text-xs border-collapse">
        <thead>
          <tr class="text-left text-[var(--fg-muted)] border-b border-[var(--border)]">
            <th class="py-1 pr-3 font-medium">{m.cl_col_service()}</th>
            <th class="py-1 pr-3 font-medium">{m.cl_col_image()}</th>
            <th class="py-1 pr-3 font-medium">{m.cl_img_running()}</th>
            <th class="py-1 pr-3 font-medium">{m.cl_img_registry()}</th>
            <th class="py-1 font-medium"></th>
          </tr>
        </thead>
        <tbody>
          {#each check.services as s (s.service)}
            <tr class="border-b border-[var(--border)] align-top">
              <td class="py-1 pr-3 dm-mono">{s.service}</td>
              <td class="py-1 pr-3 dm-mono">{s.image}</td>
              <td class="py-1 pr-3 dm-mono">{short(s.running_digest)}</td>
              <td class="py-1 pr-3 dm-mono">{short(s.registry_digest)}</td>
              <td class="py-1">
                {#if s.error}<span class="text-[var(--color-danger-400)]">{s.error}</span>
                {:else if s.update}<span class="text-[var(--color-warning-400)]">{m.cl_img_update()}</span>
                {:else}<span class="text-[var(--fg-muted)]">{m.cl_img_current()}</span>{/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
      {#if result}
        <p class="text-xs {result.outcome === 'applied' ? '' : 'text-[var(--color-danger-400)]'}">
          {result.outcome === 'applied' ? m.cl_img_started() : m.cl_img_failed()}{result.error ? ` ${result.error}` : ''}
        </p>
      {:else if updates.length > 0}
        <div class="flex justify-end">
          <Button variant="primary" onclick={update} disabled={busy}>{m.cl_img_apply({ n: updates.length })}</Button>
        </div>
      {/if}
    {/if}
  </div>
</Modal>
