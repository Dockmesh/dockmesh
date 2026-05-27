<script lang="ts">
  import type { Snippet } from 'svelte';

  interface Props {
    label: string;
    hint?: string;
    right?: Snippet;
    children?: Snippet;
  }

  let { label, hint, right, children }: Props = $props();
</script>

<label class="ed-field">
  <span class="ed-field-row">
    <span>{label}</span>
    {#if right}{@render right()}{/if}
  </span>
  {@render children?.()}
  {#if hint}
    <span class="ed-field-hint">{hint}</span>
  {/if}
</label>

<style>
  .ed-field {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .ed-field-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    font-family: var(--font-mono);
    font-size: 10.5px;
    letter-spacing: 0.14em;
    text-transform: uppercase;
    color: var(--fg-subtle);
  }
  .ed-field-hint {
    font-size: 11.5px;
    color: var(--fg-subtle);
    line-height: 1.5;
  }
</style>
