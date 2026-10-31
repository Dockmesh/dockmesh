<script lang="ts" module>
  export interface SegOption {
    key: string;
    label: string;
    count?: number | string | null;
    title?: string;
  }
</script>

<script lang="ts">
  // Segmented control for filter pills and view switches.
  let {
    options,
    value = $bindable(),
    onchange,
    label,
    size = 'md'
  }: {
    options: SegOption[];
    value: string;
    onchange?: (key: string) => void;
    label: string;
    size?: 'sm' | 'md';
  } = $props();

  function pick(k: string) {
    value = k;
    onchange?.(k);
  }
</script>

<div class="seg {size}" role="group" aria-label={label}>
  {#each options as o (o.key)}
    <button type="button" class:on={value === o.key} aria-pressed={value === o.key} title={o.title} onclick={() => pick(o.key)}>
      {o.label}{#if o.count != null && o.count !== ''}<span class="c">{o.count}</span>{/if}
    </button>
  {/each}
</div>

<style>
  .seg {
    display: inline-flex;
    background: var(--field);
    border-radius: 9px;
    padding: 2px;
    flex: none;
    max-width: 100%;
    overflow-x: auto;
  }
  button {
    border: 0;
    font: inherit;
    font-size: var(--fs-sm);
    font-weight: 500;
    padding: 0 12px;
    min-height: 28px;
    border-radius: 7px;
    cursor: pointer;
    color: var(--muted);
    background: transparent;
    white-space: nowrap;
  }
  .sm button { min-height: 24px; padding: 0 9px; font-size: var(--fs-xs); }
  button:hover { color: var(--fg); }
  button.on {
    color: var(--fg);
    background: var(--surface);
    font-weight: 600;
    box-shadow: 0 1px 2px rgba(0, 0, 0, 0.18);
  }
  .c { color: var(--subtle); font-weight: 400; margin-left: 4px; }
</style>
