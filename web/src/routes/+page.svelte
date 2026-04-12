<script lang="ts">
  // TODO: load host metrics + container overview from /api/v1
</script>

<section class="space-y-4">
  <h2 class="text-xl font-semibold">Dashboard</h2>
  <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4">
    <div class="p-4 rounded border border-[var(--border)] bg-[var(--panel)]">
      <div class="text-sm text-[var(--muted)]">Containers</div>
      <div class="text-2xl font-bold">—</div>
    </div>
    <div class="p-4 rounded border border-[var(--border)] bg-[var(--panel)]">
      <div class="text-sm text-[var(--muted)]">Stacks</div>
      <div class="text-2xl font-bold">—</div>
    </div>
    <div class="p-4 rounded border border-[var(--border)] bg-[var(--panel)]">
      <div class="text-sm text-[var(--muted)]">Images</div>
      <div class="text-2xl font-bold">—</div>
    </div>
    <div class="p-4 rounded border border-[var(--border)] bg-[var(--panel)]">
      <div class="text-sm text-[var(--muted)]">CPU</div>
      <div class="text-2xl font-bold">—</div>
    </div>
  </div>
  <p class="text-[var(--muted)] text-sm">Placeholder — metrics wiring comes in Phase 1.</p>
</section>
