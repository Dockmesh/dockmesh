<script lang="ts">
  import { Button, Modal } from '$lib/components/ui';
  import { diffLines } from '$lib/diffLines';
  import { m } from '$lib/paraglide/messages.js';
  import type { StackDetail } from '$lib/api';

  interface Props {
    conflict: { files: string[]; current: StackDetail } | null;
    mine: { compose: string; env: string };
    onusedisk: () => void;
    onforce: () => void;
    oncancel: () => void;
  }

  let { conflict, mine, onusedisk, onforce, oncancel }: Props = $props();

  // Browsers normalise textarea line ends, which would mark every line of a
  // CRLF file as changed.
  const lf = (s: string) => s.replace(/\r\n/g, '\n');

  const diffs = $derived(
    (conflict?.files ?? []).map((file) => ({
      file,
      lines: diffLines(
        lf(file === '.env' ? (conflict?.current.env ?? '') : (conflict?.current.compose ?? '')),
        lf(file === '.env' ? mine.env : mine.compose)
      )
    }))
  );
</script>

{#if conflict}
  <Modal open title={m.stacks_d_conflict_title()} maxWidth="max-w-4xl" onclose={oncancel}>
    <div class="space-y-3 text-sm" data-testid="save-conflict">
      <p>{m.stacks_d_conflict_body({ files: conflict.files.join(', ') })}</p>
      <p class="text-xs text-[var(--fg-muted)]">
        <span class="legend del">- {m.stacks_d_conflict_on_disk()}</span>
        <span class="legend add">+ {m.stacks_d_conflict_yours()}</span>
      </p>
      {#each diffs as d (d.file)}
        <div>
          <div class="font-mono text-xs mb-1">{d.file}</div>
          <div class="border border-[var(--border)] rounded-md bg-[var(--surface)] text-xs font-mono overflow-auto max-h-80" data-testid="save-conflict-diff">
            {#each d.lines as ln, i (i)}
              <div
                class="px-3 py-0 whitespace-pre-wrap"
                class:diff-context={ln.kind === ' '}
                class:diff-add={ln.kind === '+'}
                class:diff-del={ln.kind === '-'}
              ><span class="diff-marker">{ln.kind}</span> {ln.text}</div>
            {/each}
          </div>
        </div>
      {/each}
    </div>
    {#snippet footer()}
      <Button variant="ghost" onclick={oncancel} data-testid="save-conflict-cancel">{m.common_cancel()}</Button>
      <Button variant="secondary" onclick={onusedisk} data-testid="save-conflict-use-disk">{m.stacks_d_conflict_use_disk()}</Button>
      <Button variant="danger" onclick={onforce} data-testid="save-conflict-force">{m.stacks_d_conflict_force()}</Button>
    {/snippet}
  </Modal>
{/if}

<style>
  .legend { margin-right: 12px; font-family: var(--font-mono, monospace); }
  .legend.add { color: var(--ok); }
  .legend.del { color: var(--bad); }
  .diff-context { color: var(--muted); }
  .diff-add { background: color-mix(in srgb, var(--ok) 12%, transparent); color: var(--fg); }
  .diff-del { background: color-mix(in srgb, var(--bad) 12%, transparent); color: var(--fg); }
  .diff-marker { display: inline-block; width: 12px; color: var(--subtle); user-select: none; }
  .diff-add .diff-marker { color: var(--ok); }
  .diff-del .diff-marker { color: var(--bad); }
</style>
