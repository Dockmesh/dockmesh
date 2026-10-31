<script lang="ts">
  import TagPill from './TagPill.svelte';
  // At most `max` pills inline, the rest as a quiet "+N" with the full list
  // as tooltip, so rows keep one line.
  let {
    items,
    max = 2,
    onpick,
    active = null
  }: { items: string[]; max?: number; onpick?: (label: string) => void; active?: string | null } = $props();
  const shown = $derived(items.slice(0, max));
  const rest = $derived(items.slice(max));
</script>

<span class="cl">
  {#each shown as it (it)}
    <TagPill label={it} onclick={onpick} active={active === it} />
  {/each}
  {#if rest.length}
    <span class="more" title={items.join(', ')}>+{rest.length}</span>
  {/if}
</span>

<style>
  .cl { display: inline-flex; gap: 4px; align-items: center; min-width: 0; max-width: 100%; overflow: hidden; vertical-align: middle; }
  .more { color: var(--subtle); font-size: 12px; padding: 0 4px; white-space: nowrap; cursor: default; }
</style>
