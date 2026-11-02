<script lang="ts">
  import { m } from '$lib/paraglide/messages.js';
  import Menu, { type MenuItem } from '$lib/components/ui/Menu.svelte';
  import { operations, kindLabel, currentStep, hasWarning } from '$lib/stores/operations.svelte';
  import { LoaderCircle, History } from 'lucide-svelte';

  const ST: Record<string, () => string> = {
    queued: m.ops_st_queued,
    running: m.ops_st_running,
    succeeded: m.ops_st_succeeded,
    failed: m.ops_st_failed,
    cancelled: m.ops_st_cancelled
  };

  // Top-bar entry to running work: shows how many operations run and
  // lists them with the recent ones, so leaving a page never loses them.
  const active = $derived(operations.active);
  const recent = $derived(operations.recent.slice(0, 6));

  const items = $derived.by<MenuItem[]>(() => {
    const out: MenuItem[] = [];
    if (active.length) {
      out.push({ heading: m.common_running() });
      for (const op of active) {
        const s = currentStep(op);
        out.push({
          label: `${kindLabel(op.kind)} · ${op.target}`,
          kbd: op.status === 'queued' ? m.ops_st_queued() : s?.progress != null ? `${Math.round(s.progress * 100)} %` : (s?.label ?? ''),
          onclick: () => operations.open(op.id)
        });
      }
    }
    if (recent.length) {
      if (out.length) out.push({ separator: true });
      out.push({ heading: m.ops_recently_finished() });
      for (const op of recent) {
        out.push({
          label: `${kindLabel(op.kind)} · ${op.target}`,
          kbd: op.status === 'succeeded' && hasWarning(op) ? m.ops_st_warning() : (ST[op.status]?.() ?? op.status),
          danger: op.status === 'failed',
          onclick: () => operations.open(op.id)
        });
      }
    }
    return out;
  });
</script>

{#if active.length || recent.length}
  <Menu label={m.ops_running_label()} {items} width={340} triggerClass="tb-btn ops">
    {#snippet trigger()}
      {#if active.length}
        <LoaderCircle size={14} class="spin" />
        <span>{m.ops_n_running({ count: active.length })}</span>
      {:else}
        <History size={14} />
        <span class="sr-only">{m.ops_recent_label()}</span>
      {/if}
    {/snippet}
  </Menu>
{/if}

<style>
  :global(.tb-btn.ops) { gap: 6px; font-size: var(--fs-sm); }
  :global(.tb-btn.ops .spin) { color: var(--accent); animation: opspin 1s linear infinite; }
  @keyframes opspin { to { transform: rotate(360deg); } }
  @media (prefers-reduced-motion: reduce) { :global(.tb-btn.ops .spin) { animation: none; } }
  .sr-only { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); }
</style>
