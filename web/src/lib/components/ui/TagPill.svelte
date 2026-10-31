<script lang="ts">
  // Filled pill for tags and labels. Never styled like a status chip.
  let {
    label,
    active = false,
    onclick,
    title
  }: { label: string; active?: boolean; onclick?: (label: string) => void; title?: string } = $props();
</script>

{#if onclick}
  <button
    type="button"
    class="tp click"
    class:on={active}
    title={title ?? `Filter by ${label}`}
    onclick={(e) => {
      e.stopPropagation();
      onclick(label);
    }}>{label}</button
  >
{:else}
  <span class="tp" class:on={active} {title}>{label}</span>
{/if}

<style>
  .tp {
    display: inline-flex;
    align-items: center;
    font: inherit;
    font-size: 12px;
    line-height: 20px;
    padding: 0 8px;
    border: 0;
    border-radius: 999px;
    background: color-mix(in srgb, var(--fg) 9%, transparent);
    color: var(--muted);
    white-space: nowrap;
    max-width: 100%;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .click { cursor: pointer; }
  .click:hover,
  .on {
    color: var(--fg);
    background: color-mix(in srgb, var(--accent) 18%, transparent);
  }
</style>
