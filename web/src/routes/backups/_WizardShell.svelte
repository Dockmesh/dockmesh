<script lang="ts">
  // Dialog frame for the job and target wizards: title, step strip, body
  // and Back / Next / Finish. Completed steps can be revisited; later ones
  // open only by going forward, so each step's checks run in order.
  import { X } from 'lucide-svelte';
  import type { Snippet } from 'svelte';
  import { trapFocus, portal } from '$lib/a11y';

  interface Props {
    open: boolean;
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
  const titleId = `wiz-${Math.random().toString(36).slice(2, 9)}`;
</script>

{#if open}
  <div use:portal>
    <div class="ov" role="presentation" onmousedown={(e) => { if (e.target === e.currentTarget) onclose(); }}>
      <div
        class="wiz"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabindex="-1"
        style="width: min({width}px, 96vw)"
        use:trapFocus
        onkeydown={(e) => { if (e.key === 'Escape') { e.stopPropagation(); onclose(); } }}
      >
        <header class="h">
          <div class="ht">
            <h2 id={titleId}>{title}</h2>
            {#if subtitle}<p>{subtitle}</p>{/if}
          </div>
          <span class="dm-num dm-dim">Step {stepIdx + 1} of {steps.length}</span>
          <button type="button" class="x" onclick={onclose} aria-label="Close"><X size={15} strokeWidth={1.8} /></button>
        </header>

        <ol class="steps" aria-label="Steps">
          {#each steps as label, i (i)}
            <li class:done={i < stepIdx} class:on={i === stepIdx}>
              <button
                type="button"
                aria-current={i === stepIdx ? 'step' : undefined}
                disabled={i >= stepIdx}
                onclick={() => onstep?.(i)}
              >
                <span class="n">{i + 1}</span><span>{label}</span>
              </button>
            </li>
          {/each}
        </ol>

        <div class="b">{@render children()}</div>

        <footer class="f">
          {#if stepIdx > 0}
            <button type="button" class="dm-btn" onclick={onback} disabled={busy}>Back</button>
          {:else}
            <button type="button" class="dm-btn" onclick={onclose}>Cancel</button>
          {/if}
          {#if !isLast}
            <button type="button" class="dm-btn dm-btn-primary" onclick={onnext} disabled={nextDisabled || busy}>Next</button>
          {:else}
            <button type="button" class="dm-btn dm-btn-primary" onclick={onfinish} disabled={nextDisabled || busy}>{busy ? 'Saving…' : finishLabel}</button>
          {/if}
        </footer>
      </div>
    </div>
  </div>
{/if}

<style>
  .ov { position: fixed; inset: 0; z-index: 100; background: var(--overlay); display: flex; align-items: flex-start; justify-content: center; padding: 6vh 16px 16px; }
  .wiz { background: var(--menu); border: 1px solid var(--border); border-radius: var(--r-xl); box-shadow: var(--pop); display: flex; flex-direction: column; max-height: calc(100vh - 6vh - 24px); outline: none; }
  .h { display: flex; align-items: center; gap: 12px; padding: 14px 16px; border-bottom: 1px solid var(--border); }
  .ht { flex: 1; min-width: 0; }
  .ht h2 { margin: 0; font-size: var(--fs-md); font-weight: 600; }
  .ht p { margin: 2px 0 0; color: var(--subtle); font-size: var(--fs-sm); }
  .x { width: 28px; height: 28px; border-radius: var(--r-sm); border: 1px solid transparent; background: none; color: var(--muted); cursor: pointer; display: inline-flex; align-items: center; justify-content: center; }
  .x:hover { border-color: var(--border); color: var(--fg); }
  .steps { display: flex; gap: 6px; list-style: none; margin: 0; padding: 10px 16px; border-bottom: 1px solid var(--border); flex-wrap: wrap; }
  .steps li { display: flex; align-items: center; gap: 6px; }
  .steps li + li::before { content: ''; width: 18px; height: 1px; background: var(--border); }
  .steps button { all: unset; display: inline-flex; align-items: center; gap: 6px; color: var(--subtle); font-size: var(--fs-sm); }
  .steps .done button { cursor: pointer; color: var(--muted); }
  .steps .done button:hover { color: var(--fg); }
  .steps button:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
  .steps .on button { color: var(--fg); font-weight: 600; }
  .n { width: 20px; height: 20px; border-radius: 10px; border: 1px solid var(--border); display: inline-flex; align-items: center; justify-content: center; font-size: 11px; font-family: var(--font-mono); }
  .on .n { border-color: var(--accent); color: var(--accent); }
  .done .n { background: var(--accent); border-color: var(--accent); color: var(--on-accent); }
  .b { padding: 16px; overflow: auto; flex: 1; min-height: 0; }
  .f { display: flex; justify-content: flex-end; align-items: center; gap: 8px; padding: 12px 16px; border-top: 1px solid var(--border); }
</style>
