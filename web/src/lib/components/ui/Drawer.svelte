<script lang="ts">
  import type { Snippet } from 'svelte';
  import { X } from 'lucide-svelte';
  import { trapFocus, portal } from '$lib/a11y';

  // Side drawer for a quick view or short edit while the list stays visible.
  let {
    open = $bindable(false),
    title,
    width = 480,
    onclose,
    children,
    footer
  }: {
    open: boolean;
    title: string;
    width?: number;
    onclose?: () => void;
    children?: Snippet;
    footer?: Snippet;
  } = $props();

  const titleId = `drw-${Math.random().toString(36).slice(2, 9)}`;

  function close() {
    open = false;
    onclose?.();
  }
</script>

{#if open}
  <div use:portal>
    <div class="scrim" role="presentation" onmousedown={close}></div>
    <div
      class="drw"
      style="width: min({width}px, 96vw)"
      role="dialog"
      aria-modal="true"
      aria-labelledby={titleId}
      tabindex="-1"
      use:trapFocus
      onkeydown={(e) => {
        if (e.key === 'Escape') {
          e.stopPropagation();
          close();
        }
      }}
    >
      <div class="h">
        <h2 id={titleId}>{title}</h2>
        <button type="button" class="x" onclick={close} aria-label="Close"><X size={15} strokeWidth={1.8} /></button>
      </div>
      <div class="b">{@render children?.()}</div>
      {#if footer}<div class="f">{@render footer()}</div>{/if}
    </div>
  </div>
{/if}

<style>
  .scrim { position: fixed; inset: 0; background: var(--overlay); z-index: 100; }
  .drw {
    position: fixed;
    top: 0;
    right: 0;
    bottom: 0;
    background: var(--menu);
    border-left: 1px solid var(--border);
    box-shadow: var(--pop);
    z-index: 101;
    display: flex;
    flex-direction: column;
    outline: none;
  }
  .h { display: flex; align-items: center; gap: 10px; padding: 14px 16px; border-bottom: 1px solid var(--border); }
  .h h2 { margin: 0; font-size: var(--fs-md); font-weight: 600; flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .x {
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
  .x:hover { border-color: var(--border); color: var(--fg); }
  .b { flex: 1; overflow: auto; padding: 16px; min-height: 0; }
  .f { display: flex; justify-content: flex-end; gap: 8px; padding: 12px 16px; border-top: 1px solid var(--border); }
</style>
