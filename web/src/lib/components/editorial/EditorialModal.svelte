<script lang="ts">
  import type { Snippet } from 'svelte';
  import { X } from 'lucide-svelte';
  import Eyebrow from './Eyebrow.svelte';

  interface Props {
    open: boolean;
    eyebrow?: string;
    title?: Snippet | string;
    width?: number;
    onclose?: () => void;
    /** Custom Esc handler — return true if Esc has been intercepted (e.g.
        to show a sub-confirmation instead of closing). */
    onescape?: () => boolean | void;
    children?: Snippet;
    footer?: Snippet;
  }

  let {
    open = $bindable(false),
    eyebrow,
    title,
    width = 520,
    onclose,
    onescape,
    children,
    footer,
  }: Props = $props();

  function close() {
    open = false;
    onclose?.();
  }

  function onBackdropMouseDown(e: MouseEvent) {
    if (e.target === e.currentTarget) close();
  }

  function onKeydown(e: KeyboardEvent) {
    if (!open) return;
    if (e.key !== 'Escape') return;
    const handled = onescape?.();
    if (handled !== true) close();
  }
</script>

<svelte:window onkeydown={onKeydown} />

{#if open}
  <div
    class="ed-modal-scrim dm-fade-in"
    onmousedown={onBackdropMouseDown}
    role="presentation"
  >
    <div
      class="ed-modal-card"
      role="dialog"
      aria-modal="true"
      style="width: {width}px;"
    >
      <button
        type="button"
        class="ed-modal-close"
        onclick={close}
        aria-label="Close"
      >
        <X size={14} strokeWidth={1.5} />
      </button>

      {#if eyebrow || title}
        <div class="ed-modal-header">
          {#if eyebrow}<Eyebrow>{eyebrow}</Eyebrow>{/if}
          {#if title}
            <h2 class="ed-title ed-modal-title">
              {#if typeof title === 'string'}{title}{:else}{@render title()}{/if}
            </h2>
          {/if}
        </div>
      {/if}

      <div class="ed-modal-body">
        {@render children?.()}
      </div>

      {#if footer}
        <div class="ed-modal-footer">
          {@render footer()}
        </div>
      {/if}
    </div>
  </div>
{/if}

<style>
  .ed-modal-scrim {
    position: fixed;
    inset: 0;
    background: rgba(2, 6, 23, 0.66);
    z-index: 100;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 24px;
  }
  .ed-modal-card {
    max-width: 100%;
    max-height: calc(100vh - 48px);
    background: var(--bg-elevated);
    border: 1px solid var(--border-strong);
    border-radius: 6px;
    overflow: auto;
    position: relative;
  }
  .ed-modal-close {
    position: absolute;
    top: 12px;
    right: 12px;
    width: 26px;
    height: 26px;
    background: transparent;
    border: 0;
    color: var(--fg-subtle);
    display: inline-flex;
    align-items: center;
    justify-content: center;
    cursor: pointer;
    border-radius: 4px;
  }
  .ed-modal-close:hover { color: var(--fg); background: var(--surface-hover); }
  .ed-modal-header { padding: 26px 28px 4px; }
  .ed-modal-title { font-size: 22px; margin-top: 10px; }
  .ed-modal-body { padding: 10px 28px 22px; }
  .ed-modal-footer {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 14px 22px;
    border-top: 1px solid var(--border-subtle);
    background: var(--surface);
  }
</style>
