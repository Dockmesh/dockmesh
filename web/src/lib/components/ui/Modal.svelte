<script lang="ts">
  import type { Snippet } from 'svelte';
  import { X } from 'lucide-svelte';
  import { trapFocus, portal } from '$lib/a11y';

  interface Props {
    open: boolean;
    title?: string;
    onclose?: () => void;
    // Tailwind max-width class; dialogs stay within 1280x800 because the
    // body scrolls inside a panel capped at the viewport height.
    maxWidth?: string;
    children?: Snippet;
    footer?: Snippet;
    header?: Snippet;
  }

  let { open = $bindable(false), title, onclose, maxWidth = 'max-w-2xl', children, footer, header }: Props = $props();

  const titleId = `dlg-${Math.random().toString(36).slice(2, 9)}`;

  function close() {
    open = false;
    onclose?.();
  }

  function onBackdropClick(e: MouseEvent) {
    if (e.target === e.currentTarget) close();
  }

  function onKeydown(e: KeyboardEvent) {
    if (e.key === 'Escape') {
      e.stopPropagation();
      close();
    }
  }
</script>

{#if open}
  <div class="dlg-overlay" use:portal onmousedown={onBackdropClick} role="presentation">
    <div
      class="dlg w-full {maxWidth}"
      role="dialog"
      aria-modal="true"
      aria-labelledby={title ? titleId : undefined}
      tabindex="-1"
      use:trapFocus
      onkeydown={onKeydown}
    >
      {#if title || header}
        <div class="dlg-h">
          {#if header}
            {@render header()}
          {:else}
            <h2 id={titleId}>{title}</h2>
          {/if}
          <button type="button" onclick={close} class="dlg-x" aria-label="Close">
            <X size={15} strokeWidth={1.8} />
          </button>
        </div>
      {/if}
      <div class="dlg-b">
        {@render children?.()}
      </div>
      {#if footer}
        <div class="dlg-f">
          {@render footer?.()}
        </div>
      {/if}
    </div>
  </div>
{/if}

<style>
  .dlg-overlay {
    position: fixed;
    inset: 0;
    z-index: 100;
    background: var(--overlay);
    display: flex;
    align-items: flex-start;
    justify-content: center;
    padding: 8vh 16px 16px;
  }
  .dlg {
    background: var(--menu);
    border: 1px solid var(--border);
    border-radius: var(--r-xl);
    box-shadow: var(--pop);
    display: flex;
    flex-direction: column;
    max-height: calc(100vh - 8vh - 24px);
    outline: none;
  }
  .dlg-h {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 14px 16px;
    border-bottom: 1px solid var(--border);
  }
  .dlg-h h2 {
    margin: 0;
    font-size: var(--fs-md);
    font-weight: 600;
    flex: 1;
    min-width: 0;
  }
  .dlg-x {
    margin-left: auto;
    width: 28px;
    height: 28px;
    border-radius: var(--r-sm);
    border: 1px solid transparent;
    background: none;
    color: var(--muted);
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    justify-content: center;
  }
  .dlg-x:hover { border-color: var(--border); color: var(--fg); }
  .dlg-b {
    padding: 16px;
    overflow: auto;
    flex: 1;
    min-height: 0;
  }
  .dlg-f {
    display: flex;
    justify-content: flex-end;
    align-items: center;
    gap: 8px;
    padding: 12px 16px;
    border-top: 1px solid var(--border);
  }
</style>
