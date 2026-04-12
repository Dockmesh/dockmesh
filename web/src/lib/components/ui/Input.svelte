<script lang="ts">
  import type { HTMLInputAttributes } from 'svelte/elements';

  interface Props extends Omit<HTMLInputAttributes, 'class'> {
    class?: string;
    label?: string;
    hint?: string;
    error?: string;
    value?: string;
  }

  let {
    class: klass = '',
    label,
    hint,
    error,
    value = $bindable(''),
    ...rest
  }: Props = $props();
</script>

<label class="block {klass}">
  {#if label}
    <span class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">{label}</span>
  {/if}
  <input class="dm-input" bind:value {...rest} />
  {#if error}
    <span class="block text-xs text-[var(--color-danger-400)] mt-1">{error}</span>
  {:else if hint}
    <span class="block text-xs text-[var(--fg-subtle)] mt-1">{hint}</span>
  {/if}
</label>
