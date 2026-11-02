<script lang="ts">
  import { api, ApiError, type StackCleanupPlan } from '$lib/api';
  import { Button, Modal } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { stackOps } from '$lib/stores/stackOps.svelte';
  import { AlertTriangle, Trash2 } from 'lucide-svelte';

  let {
    open = $bindable(false),
    name,
    host,
    containers,
    ondeleted
  }: {
    open?: boolean;
    name: string;
    host: string;
    containers: number;
    ondeleted?: () => void;
  } = $props();

  // Volumes and images are opt-in: one loses data for good, the other
  // costs a re-pull. The preview lists what each box would remove, with
  // external volumes and shared images already left out by the server.
  let busy = $state(false);
  let stop = $state(true);
  let networks = $state(true);
  let volumes = $state(false);
  let images = $state(false);
  let typed = $state('');
  let plan = $state<StackCleanupPlan | null>(null);
  let planError = $state<string | null>(null);
  let planLoading = $state(false);

  let loadedFor = '';
  $effect(() => {
    if (!open) {
      loadedFor = '';
      return;
    }
    if (loadedFor === name) return;
    loadedFor = name;
    reset();
  });

  async function reset() {
    stop = containers > 0;
    networks = true;
    volumes = false;
    images = false;
    typed = '';
    plan = null;
    planError = null;
    planLoading = true;
    try {
      const p = await api.stacks.cleanupPreview(name);
      // Empty lists come back as null.
      plan = { ...p, networks: p.networks ?? [], volumes: p.volumes ?? [], images: p.images ?? [], skipped_in_use: p.skipped_in_use ?? [] };
    } catch (err) {
      planError = err instanceof ApiError ? err.message : String(err);
    } finally {
      planLoading = false;
    }
  }

  const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? '' : 's'}`;

  async function confirmDelete() {
    if (busy || typed.trim() !== name) return;
    busy = true;
    try {
      const res = await stackOps.run(host, name, () => api.stacks.delete(name, { stop, networks, volumes, images }));
      const parts: string[] = [];
      if (stop && containers > 0) parts.push(plural(containers, 'container'));
      if (res?.cleanup?.networks?.length) parts.push(plural(res.cleanup.networks.length, 'network'));
      if (res?.cleanup?.volumes?.length) parts.push(plural(res.cleanup.volumes.length, 'volume'));
      if (res?.cleanup?.images?.length) parts.push(plural(res.cleanup.images.length, 'image'));
      toast.success('Deleted', parts.length > 0 ? `Removed: ${parts.join(', ')}` : name);
      if (res?.cleanup_error) toast.error('Cleanup partial', res.cleanup_error);
      open = false;
      ondeleted?.();
    } catch (err) {
      toast.error('Delete failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      busy = false;
    }
  }
</script>

<Modal bind:open title="Delete stack {name}" maxWidth="max-w-lg">
  <div class="space-y-3 text-sm">
    <p>Removes the stack's <span class="dm-mono">compose.yaml</span>. Choose what else goes with it:</p>

    {#if planError}
      <div class="p-2.5 rounded-md border border-[color-mix(in_srgb,var(--color-warning-500)_40%,transparent)] bg-[color-mix(in_srgb,var(--color-warning-500)_8%,transparent)] text-xs">
        <div class="font-medium text-[var(--color-warning-400)]">Resource cleanup unavailable for this host</div>
        <div class="text-[var(--fg-muted)] mt-1">{planError}</div>
        <div class="text-[var(--fg-muted)] mt-1">Only the compose.yaml is removed; clean up the rest on the host.</div>
      </div>
    {/if}

    <label class="flex items-start gap-2 p-2.5 rounded-md border border-[var(--border)] bg-[var(--surface)] cursor-pointer">
      <input type="checkbox" bind:checked={stop} class="accent-[var(--color-brand-500)] mt-0.5" />
      <span class="flex-1">
        <span class="font-medium">Stop and remove containers</span>
        <span class="block text-xs text-[var(--fg-muted)] mt-0.5">
          {#if containers > 0}
            {plural(containers, 'container')}; unchecked, they keep running and can be adopted again.
          {:else}
            No containers are running for this stack.
          {/if}
        </span>
      </span>
    </label>

    <label class="flex items-start gap-2 p-2.5 rounded-md border border-[var(--border)] bg-[var(--surface)] cursor-pointer {planError ? 'opacity-50 pointer-events-none' : ''}">
      <input type="checkbox" bind:checked={networks} disabled={!!planError} class="accent-[var(--color-brand-500)] mt-0.5" />
      <span class="flex-1">
        <span class="font-medium">Remove project networks</span>
        <span class="block text-xs text-[var(--fg-muted)] mt-0.5">
          {#if planLoading}
            Loading…
          {:else if plan && plan.networks.length > 0}
            Removes {plural(plan.networks.length, 'network')}: <span class="font-mono">{plan.networks.join(', ')}</span>
          {:else}
            No project networks to remove.
          {/if}
        </span>
      </span>
    </label>

    <label class="flex items-start gap-2 p-2.5 rounded-md border border-[color-mix(in_srgb,var(--color-danger-500)_30%,transparent)] bg-[color-mix(in_srgb,var(--color-danger-500)_5%,transparent)] cursor-pointer {planError ? 'opacity-50 pointer-events-none' : ''}">
      <input type="checkbox" bind:checked={volumes} disabled={!!planError} class="accent-[var(--color-danger-500)] mt-0.5" />
      <span class="flex-1">
        <span class="font-medium flex items-center gap-1.5">
          <AlertTriangle class="w-3.5 h-3.5 text-[var(--color-danger-400)]" />
          Remove volumes
          <span class="text-[10px] px-1.5 py-0.5 rounded bg-[color-mix(in_srgb,var(--color-danger-500)_15%,transparent)] text-[var(--color-danger-400)] font-normal">Unrecoverable</span>
        </span>
        <span class="block text-xs text-[var(--fg-muted)] mt-0.5">
          Deletes the data in these volumes for good. External volumes are never touched.
          {#if planLoading}
            Loading…
          {:else if plan && plan.volumes.length > 0}
            <span class="block mt-1">Removes {plural(plan.volumes.length, 'volume')}: <span class="font-mono">{plan.volumes.join(', ')}</span></span>
          {:else if plan}
            <span class="block mt-1">No project volumes to remove.</span>
          {/if}
        </span>
      </span>
    </label>
    {#if volumes && plan && plan.volumes.length > 0}
      <div class="px-2.5 py-2 rounded-md bg-[color-mix(in_srgb,var(--color-danger-500)_12%,transparent)] border border-[color-mix(in_srgb,var(--color-danger-500)_40%,transparent)] text-xs text-[var(--color-danger-400)] flex items-start gap-2">
        <AlertTriangle class="w-4 h-4 shrink-0 mt-0.5" />
        <span>The data in {plural(plan.volumes.length, 'volume')} is deleted for good. Take a backup first if it matters.</span>
      </div>
    {/if}

    <label class="flex items-start gap-2 p-2.5 rounded-md border border-[var(--border)] bg-[var(--surface)] cursor-pointer {planError ? 'opacity-50 pointer-events-none' : ''}">
      <input type="checkbox" bind:checked={images} disabled={!!planError} class="accent-[var(--color-brand-500)] mt-0.5" />
      <span class="flex-1">
        <span class="font-medium">Remove images</span>
        <span class="block text-xs text-[var(--fg-muted)] mt-0.5">
          {#if planLoading}
            Loading…
          {:else if plan && plan.images.length > 0}
            Removes {plural(plan.images.length, 'image')}: <span class="font-mono break-all">{plan.images.join(', ')}</span>
          {:else if plan}
            No images to remove, or all are used by other projects.
          {/if}
          {#if plan && plan.skipped_in_use && plan.skipped_in_use.length > 0}
            <span class="block mt-1">Kept, still used elsewhere: {plural(plan.skipped_in_use.length, 'image')}</span>
          {/if}
        </span>
      </span>
    </label>
    <div class="dm-field">
      <label for="del-typed">Type <b class="dm-mono">{name}</b> to confirm</label>
      <input id="del-typed" class="dm-input" bind:value={typed} autocomplete="off" spellcheck="false" />
    </div>
  </div>
  {#snippet footer()}
    <Button variant="secondary" onclick={() => (open = false)} disabled={busy}>Cancel</Button>
    <Button variant="danger" onclick={confirmDelete} loading={busy} disabled={busy || typed.trim() !== name}>
      <Trash2 class="w-4 h-4" />
      Delete
    </Button>
  {/snippet}
</Modal>
