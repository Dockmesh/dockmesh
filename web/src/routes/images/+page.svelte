<script lang="ts">
  import { api, ApiError } from '$lib/api';

  interface ImageSummary {
    Id: string;
    RepoTags: string[] | null;
    Size: number;
    Created: number;
  }

  let images = $state<ImageSummary[]>([]);
  let loading = $state(true);
  let error = $state('');

  async function load() {
    loading = true;
    error = '';
    try {
      images = await api.images.list();
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Load failed';
    } finally {
      loading = false;
    }
  }

  async function prune() {
    if (!confirm('Prune dangling images?')) return;
    try {
      const r = await api.images.prune();
      alert(`Reclaimed ${Math.round(r.SpaceReclaimed / 1024 / 1024)} MB`);
      await load();
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Prune failed';
    }
  }

  async function removeImage(id: string) {
    if (!confirm('Remove this image?')) return;
    try {
      await api.images.remove(id, true);
      await load();
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Remove failed';
    }
  }

  function formatSize(bytes: number): string {
    const mb = bytes / 1024 / 1024;
    if (mb < 1024) return `${mb.toFixed(1)} MB`;
    return `${(mb / 1024).toFixed(2)} GB`;
  }

  $effect(() => { load(); });
</script>

<section class="space-y-4">
  <div class="flex justify-between items-center">
    <h2 class="text-xl font-semibold">Images</h2>
    <div class="flex gap-2">
      <button class="px-3 py-1 text-sm border border-[var(--border)] rounded" onclick={load}>Refresh</button>
      <button class="px-3 py-1 text-sm border border-[var(--border)] rounded" onclick={prune}>Prune</button>
    </div>
  </div>

  {#if error}
    <div class="p-3 rounded border border-red-500/30 bg-red-500/10 text-red-500 text-sm">{error}</div>
  {/if}

  {#if loading}
    <p class="text-[var(--muted)]">Loading…</p>
  {:else if images.length === 0}
    <p class="text-[var(--muted)]">No images.</p>
  {:else}
    <div class="space-y-2">
      {#each images as img}
        <div class="p-3 rounded border border-[var(--border)] bg-[var(--panel)] flex items-center gap-3">
          <div class="flex-1 min-w-0">
            <div class="font-mono text-sm truncate">{img.RepoTags?.[0] ?? '<untagged>'}</div>
            <div class="text-xs text-[var(--muted)]">{formatSize(img.Size)} · {img.Id.slice(7, 19)}</div>
          </div>
          <button class="px-2 py-1 text-xs border border-red-500/50 text-red-500 rounded" onclick={() => removeImage(img.Id)}>
            Remove
          </button>
        </div>
      {/each}
    </div>
  {/if}
</section>
