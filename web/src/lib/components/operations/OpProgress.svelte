<script lang="ts">
  import type { Operation } from '$lib/api';
  import { operations, currentStep, overallProgress, isActive, hasWarning } from '$lib/stores/operations.svelte';
  import StatusChip from '$lib/components/ui/StatusChip.svelte';

  // Inline progress for a list row: the step that runs now and a thin bar.
  // Clicking opens the progress dialog with all steps and the live log.
  let { op, compact = false }: { op: Operation; compact?: boolean } = $props();

  const step = $derived(currentStep(op));
  const pct = $derived(overallProgress(op));
</script>

<button type="button" class="opp" class:compact title="Show steps and log" onclick={(e) => { e.stopPropagation(); operations.open(op.id); }}>
  {#if isActive(op)}
    <span class="txt">
      {op.status === 'queued' ? 'Queued' : op.cancel_requested ? 'Cancelling…' : (step?.label ?? 'Running')}{#if step?.progress != null}<span class="dm-num"> · {Math.round(step.progress * 100)} %</span>{/if}
    </span>
    <span class="bar" class:indet={pct == null}><i style={pct != null ? `width:${Math.max(4, Math.round(pct * 100))}%` : ''}></i></span>
  {:else if op.status === 'failed'}
    <StatusChip tone="bad" label="failed" title={op.error} />
    {#if !compact}<span class="txt err" title={op.error}>{step?.label ?? op.error}</span>{/if}
  {:else if op.status === 'cancelled'}
    <StatusChip tone="off" label="cancelled" title={op.error} />
  {:else if hasWarning(op)}
    <StatusChip tone="warn" label="warning" title={op.steps.find((s) => s.status === 'warning')?.detail} />
  {:else}
    <StatusChip tone="ok" label="done" />
  {/if}
</button>

<style>
  .opp { all: unset; cursor: pointer; display: inline-flex; flex-direction: column; justify-content: center; gap: 4px; min-width: 0; max-width: 100%; }
  .opp:has(:global(.chip)) { flex-direction: row; align-items: center; gap: 8px; }
  .opp:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
  .txt { font-size: var(--fs-xs); color: var(--muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .txt.err { color: var(--bad); }
  .bar { display: block; height: 3px; width: 100%; min-width: 80px; background: var(--field); position: relative; overflow: hidden; }
  .bar i { display: block; height: 100%; background: var(--accent); transition: width 0.3s ease; }
  .bar.indet i { position: absolute; width: 30%; animation: slide 1.4s ease-in-out infinite; }
  @keyframes slide { 0% { left: -30%; } 100% { left: 100%; } }
  @media (prefers-reduced-motion: reduce) { .bar.indet i { animation: none; width: 100%; opacity: 0.4; } }
</style>
