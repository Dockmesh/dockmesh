<script lang="ts">
  import { goto } from '$app/navigation';
  import { auth } from '$lib/stores/auth.svelte';
  import { notices } from '$lib/stores/notices.svelte';
  import { toast } from '$lib/stores/toast.svelte';
  import { ApiError, type Notice } from '$lib/api';
  import { StatusPill } from '$lib/components/editorial';
  import { ArrowRight, X } from 'lucide-svelte';

  $effect(() => {
    if (!auth.isAuthenticated) {
      notices.clear();
      return;
    }
    notices.refresh();
    const iv = setInterval(notices.refresh, 60_000);
    return () => clearInterval(iv);
  });

  async function dismiss(n: Notice) {
    try {
      await notices.dismiss(n);
    } catch (err) {
      toast.error('Dismiss failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  const pill = { critical: 'failing', warning: 'warn', info: 'neutral' } as const;
  const tone = {
    critical: 'border-[color-mix(in_srgb,var(--color-danger-500)_40%,var(--border))] bg-[color-mix(in_srgb,var(--color-danger-500)_8%,transparent)]',
    warning: 'border-[color-mix(in_srgb,var(--color-warning-500)_40%,var(--border))] bg-[color-mix(in_srgb,var(--color-warning-500)_8%,transparent)]',
    info: 'border-[var(--border)] bg-[var(--surface)]',
  };
</script>

{#each notices.banner as n (n.id)}
  <div class="border-b {tone[n.severity]}" role={n.severity === 'critical' ? 'alert' : 'status'}>
    <div class="max-w-7xl mx-auto px-5 md:px-8 py-2.5 flex items-center gap-3 text-sm">
      <StatusPill status={pill[n.severity]} label={n.severity === 'info' ? 'action' : 'action required'} />
      <div class="flex-1 min-w-0">
        <span class="font-medium text-[var(--fg)]">{n.title}</span>
        {#if n.body}
          <span class="text-[var(--fg-muted)] ml-1">· {n.body}</span>
        {/if}
      </div>
      {#if n.link}
        <button
          type="button"
          onclick={() => goto(n.link!)}
          class="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-md border border-[var(--border)] text-[var(--fg)] hover:bg-[var(--surface-hover)] text-xs font-medium transition-colors"
        >
          Fix it
          <ArrowRight class="w-3 h-3" />
        </button>
      {/if}
      {#if n.dismissible}
        <button
          type="button"
          onclick={() => dismiss(n)}
          class="p-1.5 rounded-md text-[var(--fg-muted)] hover:bg-[var(--surface-hover)] hover:text-[var(--fg)] transition-colors"
          aria-label="Hide this notice"
          title="Hide from banner (stays in notifications until resolved)"
        >
          <X class="w-4 h-4" />
        </button>
      {/if}
    </div>
  </div>
{/each}
