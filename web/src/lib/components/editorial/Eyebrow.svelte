<script lang="ts">
  import type { Snippet } from 'svelte';

  interface Props {
    active?: boolean;
    children?: Snippet;
  }

  let { active = false, children }: Props = $props();
</script>

<span class="ed-eyebrow" class:ed-eyebrow-active={active}>
  {@render children?.()}
  {#if active}
    <span class="ed-cursor" aria-hidden="true"></span>
  {/if}
</span>
