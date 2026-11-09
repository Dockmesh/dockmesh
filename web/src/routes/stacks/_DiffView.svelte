<script lang="ts">
  import type { DiffLine } from '$lib/diffLines';

  let { lines, maxHeight = 'max-h-96' }: { lines: DiffLine[]; maxHeight?: string } = $props();
</script>

<div class="border border-[var(--border)] rounded-md bg-[var(--surface)] text-xs font-mono overflow-auto {maxHeight}" data-testid="diff-view">
  {#each lines as ln, i (i)}
    <div
      class="px-3 py-0 whitespace-pre-wrap"
      class:diff-context={ln.kind === ' '}
      class:diff-add={ln.kind === '+'}
      class:diff-del={ln.kind === '-'}
    ><span class="diff-marker">{ln.kind}</span> {ln.text}</div>
  {/each}
</div>

<style>
  .diff-context { color: var(--muted); }
  .diff-add { background: color-mix(in srgb, var(--ok) 12%, transparent); color: var(--fg); }
  .diff-del { background: color-mix(in srgb, var(--bad) 12%, transparent); color: var(--fg); }
  .diff-marker { display: inline-block; width: 12px; color: var(--subtle); user-select: none; }
  .diff-add .diff-marker { color: var(--ok); }
  .diff-del .diff-marker { color: var(--bad); }
</style>
