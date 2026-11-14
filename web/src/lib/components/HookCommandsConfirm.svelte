<script lang="ts">
  import { AlertTriangle } from 'lucide-svelte';
  import * as m from '$lib/paraglide/messages.js';
  import type { HookCommands } from '$lib/api';

  // One row per stack whose migration would run hook commands.
  let { items, confirmed = $bindable(false) }: { items: Array<{ stack: string; hooks: HookCommands }>; confirmed?: boolean } = $props();
</script>

{#if items.length > 0}
  <div class="space-y-2 p-3 rounded-lg border border-[color-mix(in_srgb,var(--color-warning-500)_35%,transparent)] bg-[color-mix(in_srgb,var(--color-warning-500)_8%,transparent)] text-xs">
    <div class="flex items-start gap-2">
      <AlertTriangle class="w-3.5 h-3.5 mt-0.5 shrink-0 text-[var(--color-warning-400)]" />
      <span>{m.migration_hooks_hint()}</span>
    </div>
    {#each items as it (it.stack)}
      <div class="space-y-1">
        {#if items.length > 1}<div class="font-medium dm-mono">{it.stack}</div>{/if}
        {#if it.hooks.pre_dump}
          <div class="text-[var(--fg-muted)]">{m.migration_hooks_pre_dump()}</div>
          <pre class="dm-mono whitespace-pre-wrap break-all p-2 rounded bg-[var(--surface)] border border-[var(--border)] max-h-32 overflow-auto">{it.hooks.pre_dump}</pre>
        {/if}
        {#if it.hooks.post_restore}
          <div class="text-[var(--fg-muted)]">{m.migration_hooks_post_restore()}</div>
          <pre class="dm-mono whitespace-pre-wrap break-all p-2 rounded bg-[var(--surface)] border border-[var(--border)] max-h-32 overflow-auto">{it.hooks.post_restore}</pre>
        {/if}
      </div>
    {/each}
    <label class="flex items-center gap-2 font-medium cursor-pointer">
      <input type="checkbox" bind:checked={confirmed} />
      {m.migration_hooks_confirm()}
    </label>
  </div>
{/if}
