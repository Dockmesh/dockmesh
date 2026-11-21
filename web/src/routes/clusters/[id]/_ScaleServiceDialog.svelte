<script lang="ts">
  import { api, ApiError, type SwarmService } from '$lib/api';
  import { Button, Modal } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { m } from '$lib/paraglide/messages.js';

  let { open = $bindable(false), stack, service, onscaled }: { open?: boolean; stack: string; service: SwarmService; onscaled?: () => void } = $props();

  const short = $derived(service.name.slice(stack.length + 1));
  let replicas = $state(1);
  let busy = $state(false);
  let error = $state<string | null>(null);
  let stateful = $state<string | null>(null);

  $effect(() => {
    if (!open) return;
    replicas = service.replicas ?? 1;
    error = null;
    stateful = null;
  });

  async function apply(force = false) {
    busy = true;
    error = null;
    try {
      const res = await api.stacks.scale(stack, short, replicas, force);
      toast.success(m.cl_scale_done({ name: service.name, n: res.current }), res.file_replicas != null && res.file_replicas !== res.current ? m.cl_scale_file({ n: res.file_replicas }) : undefined);
      open = false;
      onscaled?.();
    } catch (err) {
      if (err instanceof ApiError && err.body?.warning === 'stateful_service') stateful = String(err.body.message ?? '');
      else error = err instanceof ApiError ? err.message : String(err);
    } finally {
      busy = false;
    }
  }
</script>

<Modal bind:open title={m.cl_scale_title({ name: service.name })} maxWidth="max-w-md">
  <div class="space-y-3 text-sm" data-testid="scale-dialog">
    <p class="text-[var(--fg-muted)]">{m.cl_scale_intro()}</p>
    <label class="flex items-center gap-2 text-xs">
      <span class="text-[var(--fg-muted)] w-24">{m.cl_col_replicas()}</span>
      <input type="number" class="dm-input w-24" min="0" max="100" bind:value={replicas} />
      <span class="text-[var(--fg-muted)]">{m.cl_scale_now({ n: service.running_tasks, total: service.desired_tasks })}</span>
    </label>
    {#if error}<p class="text-xs text-[var(--color-danger-400)]">{error}</p>{/if}
    {#if stateful}
      <p class="text-xs text-[var(--color-warning-400)]">{stateful}</p>
      <div class="flex justify-end"><Button variant="danger" onclick={() => apply(true)} disabled={busy}>{m.cl_scale_force()}</Button></div>
    {:else}
      <div class="flex justify-end"><Button variant="primary" onclick={() => apply()} disabled={busy || replicas < 0 || replicas > 100}>{m.cl_scale_apply()}</Button></div>
    {/if}
  </div>
</Modal>
