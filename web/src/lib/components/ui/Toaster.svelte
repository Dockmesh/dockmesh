<script lang="ts">
  import { toast } from '$lib/stores/toast.svelte';
  import { X } from 'lucide-svelte';
</script>

<div class="toasts">
  {#each toast.items as t (t.id)}
    <div
      class="toast {t.variant}"
      role={t.variant === 'error' ? 'alert' : 'status'}
      aria-live={t.variant === 'error' ? 'assertive' : 'polite'}
      onmouseenter={() => toast.pause(t.id)}
      onmouseleave={() => toast.resume(t.id)}
    >
      <div class="txt">
        <div class="t">{t.title}</div>
        {#if t.description}
          <div class="d">{t.description}</div>
        {/if}
      </div>
      <button type="button" class="x" onclick={() => toast.dismiss(t.id)} aria-label="Dismiss">
        <X size={13} strokeWidth={1.8} />
      </button>
    </div>
  {/each}
</div>

<style>
  .toasts {
    position: fixed;
    right: min(calc(18px + var(--drawer-w, 0px)), calc(100vw - min(380px, 100vw - 36px) - 18px));
    bottom: 18px;
    display: flex;
    flex-direction: column;
    gap: 8px;
    z-index: 200;
    pointer-events: none;
    width: min(380px, calc(100vw - 36px));
  }
  .toast {
    pointer-events: auto;
    display: flex;
    gap: 10px;
    align-items: flex-start;
    background: var(--menu);
    border: 1px solid var(--border);
    border-left: 3px solid var(--accent);
    border-radius: var(--r);
    padding: 9px 10px 9px 14px;
    box-shadow: var(--pop);
    font-size: 13px;
  }
  .toast.success { border-left-color: var(--ok); }
  .toast.warning { border-left-color: var(--warn); }
  .toast.error { border-left-color: var(--bad); }
  .txt { flex: 1; min-width: 0; }
  .t { font-weight: 500; color: var(--fg); }
  .d { color: var(--muted); font-size: var(--fs-xs); margin-top: 2px; overflow-wrap: anywhere; }
  .x {
    flex: none;
    width: 22px;
    height: 22px;
    border: 0;
    border-radius: 5px;
    background: none;
    color: var(--subtle);
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    justify-content: center;
  }
  .x:hover { background: var(--field); color: var(--fg); }
</style>
