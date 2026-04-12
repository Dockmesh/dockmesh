<script lang="ts">
  function prune() {
    // TODO: POST /api/v1/images/prune
  }
</script>

<section class="space-y-4">
  <div class="flex justify-between items-center">
    <h2 class="text-xl font-semibold">Images</h2>
    <button class="px-3 py-1 rounded border border-[var(--border)]" onclick={prune}>Prune</button>
  </div>
  <p class="text-[var(--muted)]">Placeholder — image list comes in Phase 1.</p>
</section>
