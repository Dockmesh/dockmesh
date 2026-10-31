<script lang="ts">
  import { noticeBlocks } from '$lib/notices';

  let { body }: { body?: string } = $props();
  const blocks = $derived(noticeBlocks(body));
</script>

<div class="space-y-1.5 text-[var(--fg-muted)] leading-relaxed break-words">
  {#each blocks as b, i (i)}
    {#if b.kind === 'text'}
      <p>{b.text}</p>
    {:else}
      <ul class="list-disc pl-5 space-y-0.5">
        {#each b.items as item, j (j)}
          <li>{item}</li>
        {/each}
      </ul>
    {/if}
  {/each}
</div>
