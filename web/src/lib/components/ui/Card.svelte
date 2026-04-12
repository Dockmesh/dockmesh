<script lang="ts">
  import type { Snippet } from 'svelte';
  interface Props {
    hover?: boolean;
    class?: string;
    children?: Snippet;
    onclick?: (e: MouseEvent) => void;
    href?: string;
  }
  let { hover = false, class: klass = '', children, onclick, href }: Props = $props();
</script>

{#if href}
  <a {href} class="dm-card {hover ? 'dm-card-hover' : ''} block {klass}">
    {@render children?.()}
  </a>
{:else if onclick}
  <button type="button" {onclick} class="dm-card {hover ? 'dm-card-hover' : ''} text-left w-full {klass}">
    {@render children?.()}
  </button>
{:else}
  <div class="dm-card {hover ? 'dm-card-hover' : ''} {klass}">
    {@render children?.()}
  </div>
{/if}
