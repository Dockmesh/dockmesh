<script lang="ts">
  import type { Snippet } from 'svelte';

  type Status = 'running' | 'ok' | 'degraded' | 'warn' | 'failing' | 'stopped' | 'pending';

  interface Props {
    status?: Status;
    /** Override the row's CSS grid-template-columns. Defaults to the
        6-column shape from shell.css (stripe + main + 4 columns). */
    columns?: string;
    href?: string;
    onclick?: (e: MouseEvent) => void;
    children?: Snippet;
  }

  let { status, columns, href, onclick, children }: Props = $props();

  // The visible "stripe" element is a child div coloured via [data-status].
  // EdRow always renders one as the first grid cell; consumers fill cells 2+.
</script>

{#if href}
  <a
    class="ed-row ed-row-link"
    data-status={status}
    style={columns ? `grid-template-columns: ${columns}` : undefined}
    {href}
    {onclick}
  >
    <span class="stripe"></span>
    {@render children?.()}
  </a>
{:else if onclick}
  <button
    type="button"
    class="ed-row ed-row-button"
    data-status={status}
    style={columns ? `grid-template-columns: ${columns}` : undefined}
    {onclick}
  >
    <span class="stripe"></span>
    {@render children?.()}
  </button>
{:else}
  <div
    class="ed-row"
    data-status={status}
    style={columns ? `grid-template-columns: ${columns}` : undefined}
  >
    <span class="stripe"></span>
    {@render children?.()}
  </div>
{/if}

<style>
  .ed-row-link,
  .ed-row-button {
    color: inherit;
    text-decoration: none;
    text-align: left;
    background: transparent;
    border: 0;
    border-bottom: 1px solid var(--border-subtle);
    cursor: pointer;
    font: inherit;
    width: 100%;
  }
  .ed-row-link:last-child,
  .ed-row-button:last-child { border-bottom: 0; }
</style>
