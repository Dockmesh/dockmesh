<script lang="ts">
  import { m } from '$lib/paraglide/messages.js';
  import { api, type Operation } from '$lib/api';
  import { operations, kindLabel } from '$lib/stores/operations.svelte';
  import { canCancel, cancelConsequence, cancelPerm } from '$lib/operationsProgress';
  import { allowed } from '$lib/rbac.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { toast } from '$lib/stores/toast.svelte';

  // Cancel for running work, shown only while the server says the current
  // step may be stopped and the user holds the permission that started it.
  let { op, size = 'sm' }: { op: Operation; size?: 'sm' | 'md' } = $props();

  const perm = $derived(cancelPerm(op.kind));
  const show = $derived(!!perm && allowed(perm) && (canCancel(op) || !!op.cancel_requested));
  let busy = $state(false);

  async function cancel(e: MouseEvent) {
    e.stopPropagation();
    e.preventDefault();
    const ok = await confirm.ask({
      title: m.ops_cancel_title({ kind: kindLabel(op.kind), kindLower: kindLabel(op.kind).toLowerCase(), target: op.target }),
      message: cancelConsequence(op),
      confirmLabel: m.ops_cancel_confirm(),
      cancelLabel: m.ops_cancel_keep(),
      danger: true
    });
    if (!ok) return;
    busy = true;
    try {
      operations.track(await api.operations.cancel(op.id));
    } catch (err) {
      toast.error(m.ops_cancel_failed(), (err as Error).message);
    } finally {
      busy = false;
    }
  }
</script>

{#if show}
  <button
    type="button"
    class="dm-btn {size === 'sm' ? 'dm-btn-sm' : ''} dm-btn-ghost"
    disabled={busy || !!op.cancel_requested}
    title={op.cancel_requested ? undefined : cancelConsequence(op)}
    onclick={cancel}
  >{op.cancel_requested ? m.ops_cancelling() : m.common_cancel()}</button>
{/if}
