<script lang="ts">
  import { Info } from 'lucide-svelte';

  // Helper text longer than one sentence, behind an info icon. Shown on
  // hover and keyboard focus.
  let { text, label = 'More information' }: { text: string; label?: string } = $props();
  const id = `tip-${Math.random().toString(36).slice(2, 8)}`;
</script>

<span class="it">
  <button type="button" class="ib" aria-label={label} aria-describedby={id}><Info size={13} strokeWidth={1.8} /></button>
  <span class="tip" role="tooltip" {id}>{text}</span>
</span>

<style>
  .it { position: relative; display: inline-flex; vertical-align: middle; }
  .ib { display: inline-flex; padding: 2px; border: 0; background: none; color: var(--subtle); cursor: help; border-radius: 50%; }
  .ib:hover, .ib:focus-visible { color: var(--fg); }
  .ib:focus-visible { outline: 2px solid var(--accent); outline-offset: 1px; }
  .tip {
    position: absolute;
    left: 50%;
    bottom: calc(100% + 6px);
    transform: translateX(-50%);
    width: max-content;
    max-width: 320px;
    padding: 8px 10px;
    border-radius: var(--r);
    background: var(--menu);
    border: 1px solid var(--border);
    box-shadow: var(--pop);
    color: var(--fg);
    font-size: var(--fs-xs);
    font-weight: 400;
    line-height: 1.45;
    white-space: normal;
    text-align: left;
    z-index: 70;
    visibility: hidden;
    opacity: 0;
    transition: opacity 0.1s;
  }
  .it:hover .tip, .ib:focus-visible + .tip { visibility: visible; opacity: 1; }
</style>
