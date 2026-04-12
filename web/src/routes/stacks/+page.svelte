<script lang="ts">
  // TODO: load stacks from /api/v1/stacks
  const stacks: Array<{ name: string }> = [];
</script>

<section>
  <h2 class="text-xl font-semibold mb-4">Stacks</h2>
  {#if stacks.length === 0}
    <p class="text-[var(--muted)]">No stacks yet.</p>
  {:else}
    <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4">
      {#each stacks as s}
        <a
          href={`/stacks/${s.name}`}
          class="p-4 rounded border border-[var(--border)] bg-[var(--panel)] hover:border-brand-500"
        >
          <div class="font-semibold">{s.name}</div>
        </a>
      {/each}
    </div>
  {/if}
</section>
