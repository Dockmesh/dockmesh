<script lang="ts">
  import type { Snippet } from 'svelte';
  import { X } from 'lucide-svelte';

  interface Props {
    open: boolean;
    title?: string;
    onclose?: () => void;
    maxWidth?: string;
    children?: Snippet;
    footer?: Snippet;
  }

  let { open = $bindable(false), title, onclose, maxWidth = 'max-w-2xl', children, footer }: Props = $props();

  function close() {
    open = false;
    onclose?.();
  }

  function onBackdropClick(e: MouseEvent) {
    if (e.target === e.currentTarget) close();
  }

  function onKeydown(e: KeyboardEvent) {
    if (e.key === 'Escape') close();
  }
</script>

<svelte:window onkeydown={onKeydown} />

{#if open}
  <div
    class="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/60 backdrop-blur-sm dm-fade-in"
    onclick={onBackdropClick}
    role="presentation"
  >
    <div class="w-full {maxWidth} dm-card shadow-2xl overflow-hidden flex flex-col max-h-[90vh]">
      {#if title}
        <div class="flex items-center justify-between px-5 py-4 border-b border-[var(--border)]">
          <h3 class="text-base font-semibold">{title}</h3>
          <button onclick={close} class="dm-btn-ghost p-1 rounded-md" aria-label="Close">
            <X class="w-4 h-4" />
          </button>
        </div>
      {/if}
      <div class="p-5 overflow-auto flex-1">
        {@render children?.()}
      </div>
      {#if footer}
        <div class="px-5 py-4 border-t border-[var(--border)] flex justify-end gap-2">
          {@render footer?.()}
        </div>
      {/if}
    </div>
  </div>
{/if}
