<script lang="ts">
  // Shared wizard frame — centered modal with a numbered step strip,
  // body slot, and Back / Continue / Finish footer. Used by both
  // JobWizard and TargetWizard. Pattern from the install-wizard
  // (`Dockmesh Wizard (6)/wizard.jsx`) but compressed: no left-rail
  // section markers, no big watermark — backups are the everyday use
  // case, the install wizard is once-per-host so it gets the bigger
  // chrome.
  import { Eyebrow } from '$lib/components/editorial';
  import { X, ArrowLeft, ArrowRight, Check } from 'lucide-svelte';
  import type { Snippet } from 'svelte';

  interface Props {
    open: boolean;
    eyebrow: string;
    title: string;
    subtitle?: string;
    steps: string[];
    stepIdx: number;
    width?: number;
    nextDisabled?: boolean;
    finishLabel?: string;
    busy?: boolean;
    onclose: () => void;
    onstep?: (i: number) => void;
    onback: () => void;
    onnext: () => void;
    onfinish: () => void;
    children: Snippet;
  }
  let {
    open,
    eyebrow,
    title,
    subtitle,
    steps,
    stepIdx,
    width = 720,
    nextDisabled = false,
    finishLabel = 'Create',
    busy = false,
    onclose,
    onstep,
    onback,
    onnext,
    onfinish,
    children
  }: Props = $props();

  const isLast = $derived(stepIdx === steps.length - 1);

  function onKey(e: KeyboardEvent) {
    if (!open) return;
    if (e.key === 'Escape') onclose();
  }
</script>

<svelte:window onkeydown={onKey} />

{#if open}
  <button type="button" class="bk-wiz-backdrop" onclick={onclose} aria-label="Close wizard"></button>
  <div class="bk-wiz" role="dialog" aria-modal="true" style="max-width: {width}px;">
    <header class="bk-wiz-head">
      <div class="bk-wiz-head-text">
        <Eyebrow>{eyebrow}</Eyebrow>
        <h2 class="ed-title bk-wiz-title">{title}</h2>
        {#if subtitle}
          <p class="bk-wiz-subtitle">{subtitle}</p>
        {/if}
      </div>
      <button type="button" onclick={onclose} class="bk-wiz-close" aria-label="Close">
        <X size={14} strokeWidth={1.5} />
      </button>
    </header>

    <!-- Step strip — clickable for completed steps so the operator can
         hop back without losing data they already filled in. Future
         steps stay disabled until reached forward, so the wizard
         enforces the linear validation chain. -->
    <div class="bk-wiz-steps" role="tablist">
      {#each steps as label, i (i)}
        <button
          type="button"
          role="tab"
          class="bk-wiz-step"
          class:current={i === stepIdx}
          class:done={i < stepIdx}
          aria-current={i === stepIdx ? 'step' : undefined}
          onclick={() => { if (onstep && i < stepIdx) onstep(i); }}
          disabled={i > stepIdx}
        >
          <span class="bk-wiz-step-n font-mono">{String(i + 1).padStart(2, '0')}</span>
          <span class="bk-wiz-step-l">{label}</span>
        </button>
      {/each}
    </div>

    <div class="bk-wiz-body">
      {@render children()}
    </div>

    <footer class="bk-wiz-foot">
      <span class="bk-wiz-foot-meta font-mono">
        ESC to cancel · step {stepIdx + 1} of {steps.length}
      </span>
      <span class="bk-wiz-foot-spacer"></span>
      {#if stepIdx > 0}
        <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={onback} disabled={busy}>
          <ArrowLeft size={12} strokeWidth={1.5} /> Back
        </button>
      {/if}
      {#if !isLast}
        <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" onclick={onnext} disabled={nextDisabled || busy}>
          Continue <ArrowRight size={12} strokeWidth={1.5} />
        </button>
      {:else}
        <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" onclick={onfinish} disabled={nextDisabled || busy}>
          <Check size={12} strokeWidth={1.5} /> {busy ? 'Saving…' : finishLabel}
        </button>
      {/if}
    </footer>
  </div>
{/if}

<style>
  .bk-wiz-backdrop {
    position: fixed;
    inset: 0;
    background: rgba(2, 6, 23, 0.66);
    backdrop-filter: blur(4px);
    border: 0;
    cursor: default;
    z-index: 100;
  }
  .bk-wiz {
    position: fixed;
    top: 50%;
    left: 50%;
    transform: translate(-50%, -50%);
    width: calc(100vw - 48px);
    max-height: calc(100vh - 48px);
    background: var(--bg-elevated);
    border: 1px solid var(--border-strong);
    border-radius: 8px;
    box-shadow: 0 25px 80px rgba(0, 0, 0, 0.5);
    z-index: 101;
    display: flex;
    flex-direction: column;
    animation: bk-wiz-in 200ms ease-out;
  }
  @keyframes bk-wiz-in {
    from { transform: translate(-50%, calc(-50% + 12px)); opacity: 0; }
    to { transform: translate(-50%, -50%); opacity: 1; }
  }

  .bk-wiz-head {
    display: flex;
    align-items: flex-start;
    gap: 12px;
    padding: 22px 24px 16px;
  }
  .bk-wiz-head-text { flex: 1; min-width: 0; }
  .bk-wiz-title {
    margin-top: 8px;
    font-size: 22px;
  }
  .bk-wiz-subtitle {
    margin-top: 6px;
    font-size: 12.5px;
    color: var(--fg-muted);
    line-height: 1.55;
  }
  .bk-wiz-close {
    background: transparent;
    border: 0;
    color: var(--fg-subtle);
    cursor: pointer;
    padding: 4px;
    border-radius: 3px;
  }
  .bk-wiz-close:hover { color: var(--fg); background: var(--surface-hover); }

  /* Step strip — sits under the header. Numbers in mono "01 / 02 / 03",
     accent colour on the current step, success-green check style on
     completed steps. Linear-only navigation (mockup pattern): clickable
     to GO BACK, future steps disabled until reached. */
  .bk-wiz-steps {
    display: flex;
    gap: 12px;
    padding: 0 24px 18px;
    border-bottom: 1px solid var(--border);
    flex-wrap: wrap;
  }
  .bk-wiz-step {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    padding: 6px 12px;
    border: 1px solid var(--border);
    border-radius: 4px;
    background: transparent;
    color: var(--fg-subtle);
    font-size: 12px;
    cursor: pointer;
    transition: border-color 120ms, color 120ms, background 120ms;
  }
  .bk-wiz-step:hover:not(:disabled):not(.current) {
    color: var(--fg-muted);
    border-color: var(--border-strong);
  }
  .bk-wiz-step:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }
  .bk-wiz-step.current {
    color: var(--accent-fg);
    border-color: color-mix(in srgb, var(--accent) 60%, var(--border));
    background: color-mix(in srgb, var(--accent) 10%, transparent);
  }
  .bk-wiz-step.done {
    color: var(--color-success-400);
    border-color: color-mix(in srgb, var(--color-success-500) 30%, var(--border));
  }
  .bk-wiz-step.done .bk-wiz-step-n {
    color: var(--color-success-400);
  }
  .bk-wiz-step-n {
    font-size: 10px;
    color: var(--fg-subtle);
    letter-spacing: 0.04em;
  }
  .bk-wiz-step.current .bk-wiz-step-n {
    color: var(--accent-fg);
  }
  .bk-wiz-step-l {
    font-size: 12.5px;
  }

  .bk-wiz-body {
    flex: 1;
    overflow-y: auto;
    padding: 22px 24px 24px;
  }

  .bk-wiz-foot {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 14px 24px;
    border-top: 1px solid var(--border);
    background: var(--bg-elevated);
    border-bottom-left-radius: 8px;
    border-bottom-right-radius: 8px;
    flex-shrink: 0;
  }
  .bk-wiz-foot-meta {
    font-size: 10.5px;
    color: var(--fg-subtle);
  }
  .bk-wiz-foot-spacer { flex: 1; }
</style>
