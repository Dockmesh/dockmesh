<script lang="ts">
  let {
    checked = $bindable(false),
    label,
    hideLabel = false,
    disabled = false,
    onchange
  }: {
    checked?: boolean;
    label: string;
    hideLabel?: boolean;
    disabled?: boolean;
    onchange?: (v: boolean) => void;
  } = $props();

  function toggle() {
    if (disabled) return;
    checked = !checked;
    onchange?.(checked);
  }
</script>

<button
  type="button"
  class="sw-wrap"
  role="switch"
  aria-checked={checked}
  aria-label={hideLabel ? label : undefined}
  {disabled}
  onclick={toggle}
>
  <span class="sw" class:on={checked}></span>
  {#if !hideLabel}<span class="lbl">{label}</span>{/if}
</button>

<style>
  .sw-wrap {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    border: 0;
    background: none;
    padding: 0;
    font: inherit;
    color: var(--muted);
    cursor: pointer;
    text-align: left;
  }
  .sw-wrap:disabled { opacity: 0.5; cursor: not-allowed; }
  .sw {
    display: inline-block;
    width: 34px;
    height: 20px;
    border-radius: 10px;
    background: var(--field);
    border: 1px solid var(--border-strong);
    position: relative;
    flex: none;
  }
  .sw::after {
    content: '';
    position: absolute;
    top: 2px;
    left: 2px;
    width: 14px;
    height: 14px;
    border-radius: 7px;
    background: var(--subtle);
    transition: left 0.12s;
  }
  .sw.on { background: var(--accent); border-color: var(--accent); }
  .sw.on::after { left: 16px; background: var(--on-accent); }
</style>
