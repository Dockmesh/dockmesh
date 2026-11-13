<script lang="ts">
  import { api, ApiError, type StackCleanupPlan } from '$lib/api';
  import { Button, Modal } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { stackOps } from '$lib/stores/stackOps.svelte';
  import { AlertTriangle, Trash2 } from 'lucide-svelte';
  import { m } from '$lib/paraglide/messages.js';

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

  async function confirmDelete() {
    if (busy || typed.trim() !== name) return;
    busy = true;
    try {
      const res = await stackOps.run(host, name, () => api.stacks.delete(name, { stop, networks, volumes, images }));
      if (res?.pending_host_id) {
        const h = res.pending_host_name || res.pending_host_id;
        toast.warning(m.stacks_del_pending_title({ host: h }), m.stacks_del_pending_body({ host: h }));
        open = false;
        ondeleted?.();
        return;
      }
      const parts: string[] = [];
      if (stop && containers > 0) parts.push(m.stacks_n_containers({ count: containers }));
      if (res?.cleanup?.networks?.length) parts.push(m.stacks_n_networks({ count: res.cleanup.networks.length }));
      if (res?.cleanup?.volumes?.length) parts.push(m.stacks_n_volumes({ count: res.cleanup.volumes.length }));
      if (res?.cleanup?.images?.length) parts.push(m.stacks_n_images({ count: res.cleanup.images.length }));
      toast.success(m.stacks_del_deleted(), parts.length > 0 ? m.stacks_del_removed({ list: parts.join(', ') }) : name);
      if (res?.cleanup_error) toast.error(m.stacks_del_cleanup_partial(), res.cleanup_error);
      open = false;
      ondeleted?.();
    } catch (err) {
      toast.error(m.stacks_del_failed(), err instanceof ApiError ? err.message : undefined);
    } finally {
      busy = false;
    }
  }
</script>

<Modal bind:open title={m.stacks_del_title({ name })} maxWidth="max-w-lg">
  <div class="space-y-3 text-sm">
    <p>{m.stacks_del_intro_before()} <span class="dm-mono">compose.yaml</span>{m.stacks_del_intro_after()}</p>

    {#if planError}
      <div class="p-2.5 rounded-md border border-[color-mix(in_srgb,var(--color-warning-500)_40%,transparent)] bg-[color-mix(in_srgb,var(--color-warning-500)_8%,transparent)] text-xs">
        <div class="font-medium text-[var(--color-warning-400)]">{m.stacks_del_cleanup_unavailable()}</div>
        <div class="text-[var(--fg-muted)] mt-1">{planError}</div>
        <div class="text-[var(--fg-muted)] mt-1">{m.stacks_del_only_compose()}</div>
      </div>
    {/if}

    <label class="flex items-start gap-2 p-2.5 rounded-md border border-[var(--border)] bg-[var(--surface)] cursor-pointer">
      <input type="checkbox" bind:checked={stop} class="accent-[var(--color-brand-500)] mt-0.5" />
      <span class="flex-1">
        <span class="font-medium">{m.stacks_del_stop_containers()}</span>
        <span class="block text-xs text-[var(--fg-muted)] mt-0.5">
          {#if containers > 0}
            {m.stacks_del_containers_keep({ containers: m.stacks_n_containers({ count: containers }) })}
          {:else}
            {m.stacks_del_no_containers()}
          {/if}
        </span>
      </span>
    </label>

    <label class="flex items-start gap-2 p-2.5 rounded-md border border-[var(--border)] bg-[var(--surface)] cursor-pointer {planError ? 'opacity-50 pointer-events-none' : ''}">
      <input type="checkbox" bind:checked={networks} disabled={!!planError} class="accent-[var(--color-brand-500)] mt-0.5" />
      <span class="flex-1">
        <span class="font-medium">{m.stacks_del_remove_networks()}</span>
        <span class="block text-xs text-[var(--fg-muted)] mt-0.5">
          {#if planLoading}
            {m.common_loading()}
          {:else if plan && plan.networks.length > 0}
            {m.stacks_del_removes({ what: m.stacks_n_networks({ count: plan.networks.length }) })} <span class="font-mono">{plan.networks.join(', ')}</span>
          {:else}
            {m.stacks_del_no_networks()}
          {/if}
        </span>
      </span>
    </label>

    <label class="flex items-start gap-2 p-2.5 rounded-md border border-[color-mix(in_srgb,var(--color-danger-500)_30%,transparent)] bg-[color-mix(in_srgb,var(--color-danger-500)_5%,transparent)] cursor-pointer {planError ? 'opacity-50 pointer-events-none' : ''}">
      <input type="checkbox" bind:checked={volumes} disabled={!!planError} class="accent-[var(--color-danger-500)] mt-0.5" />
      <span class="flex-1">
        <span class="font-medium flex items-center gap-1.5">
          <AlertTriangle class="w-3.5 h-3.5 text-[var(--color-danger-400)]" />
          {m.stacks_del_remove_volumes()}
          <span class="text-[10px] px-1.5 py-0.5 rounded bg-[color-mix(in_srgb,var(--color-danger-500)_15%,transparent)] text-[var(--color-danger-400)] font-normal">{m.stacks_del_unrecoverable()}</span>
        </span>
        <span class="block text-xs text-[var(--fg-muted)] mt-0.5">
          {m.stacks_del_volumes_hint()}
          {#if planLoading}
            {m.common_loading()}
          {:else if plan && plan.volumes.length > 0}
            <span class="block mt-1">{m.stacks_del_removes({ what: m.stacks_n_volumes({ count: plan.volumes.length }) })} <span class="font-mono">{plan.volumes.join(', ')}</span></span>
          {:else if plan}
            <span class="block mt-1">{m.stacks_del_no_volumes()}</span>
          {/if}
        </span>
      </span>
    </label>
    {#if volumes && plan && plan.volumes.length > 0}
      <div class="px-2.5 py-2 rounded-md bg-[color-mix(in_srgb,var(--color-danger-500)_12%,transparent)] border border-[color-mix(in_srgb,var(--color-danger-500)_40%,transparent)] text-xs text-[var(--color-danger-400)] flex items-start gap-2">
        <AlertTriangle class="w-4 h-4 shrink-0 mt-0.5" />
        <span>{m.stacks_del_volumes_warn({ volumes: m.stacks_n_volumes({ count: plan.volumes.length }) })}</span>
      </div>
    {/if}

    <label class="flex items-start gap-2 p-2.5 rounded-md border border-[var(--border)] bg-[var(--surface)] cursor-pointer {planError ? 'opacity-50 pointer-events-none' : ''}">
      <input type="checkbox" bind:checked={images} disabled={!!planError} class="accent-[var(--color-brand-500)] mt-0.5" />
      <span class="flex-1">
        <span class="font-medium">{m.stacks_del_remove_images()}</span>
        <span class="block text-xs text-[var(--fg-muted)] mt-0.5">
          {#if planLoading}
            {m.common_loading()}
          {:else if plan && plan.images.length > 0}
            {m.stacks_del_removes({ what: m.stacks_n_images({ count: plan.images.length }) })} <span class="font-mono break-all">{plan.images.join(', ')}</span>
          {:else if plan}
            {m.stacks_del_no_images()}
          {/if}
          {#if plan && plan.skipped_in_use && plan.skipped_in_use.length > 0}
            <span class="block mt-1">{m.stacks_del_kept_images({ images: m.stacks_n_images({ count: plan.skipped_in_use.length }) })}</span>
          {/if}
        </span>
      </span>
    </label>
    <div class="dm-field">
      <label for="del-typed">{m.stacks_del_type_before()} <b class="dm-mono">{name}</b> {m.stacks_del_type_after()}</label>
      <input id="del-typed" class="dm-input" bind:value={typed} autocomplete="off" spellcheck="false" />
    </div>
  </div>
  {#snippet footer()}
    <Button variant="secondary" onclick={() => (open = false)} disabled={busy}>{m.common_cancel()}</Button>
    <Button variant="danger" onclick={confirmDelete} loading={busy} disabled={busy || typed.trim() !== name}>
      <Trash2 class="w-4 h-4" />
      {m.common_delete()}
    </Button>
  {/snippet}
</Modal>
