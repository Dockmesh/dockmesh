<script lang="ts">
  import { api, ApiError } from '$lib/api';
  import { Card, Button, EmptyState, Skeleton, Badge } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { Image as ImageIcon, Trash2, RefreshCw, Sparkles } from 'lucide-svelte';

  interface ImageSummary {
    Id: string;
    RepoTags: string[] | null;
    Size: number;
    Created: number;
  }

  let images = $state<ImageSummary[]>([]);
  let loading = $state(true);

  async function load() {
    loading = true;
    try {
      images = await api.images.list();
    } catch (err) {
      toast.error('Failed to load', err instanceof ApiError ? err.message : undefined);
    } finally {
      loading = false;
    }
  }

  async function prune() {
    if (!confirm('Prune dangling images?')) return;
    try {
      const r = await api.images.prune();
      toast.success('Pruned', `reclaimed ${formatSize(r.SpaceReclaimed)}`);
      await load();
    } catch (err) {
      toast.error('Prune failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function removeImage(id: string) {
    if (!confirm('Remove this image?')) return;
    try {
      await api.images.remove(id, true);
      toast.success('Removed');
      await load();
    } catch (err) {
      toast.error('Remove failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  function formatSize(bytes: number): string {
    const mb = bytes / 1024 / 1024;
    if (mb < 1024) return `${mb.toFixed(1)} MB`;
    return `${(mb / 1024).toFixed(2)} GB`;
  }

  function fmtAge(unix: number): string {
    const d = (Date.now() / 1000 - unix) / 86400;
    if (d < 1) return 'today';
    if (d < 7) return `${Math.floor(d)}d ago`;
    if (d < 30) return `${Math.floor(d / 7)}w ago`;
    if (d < 365) return `${Math.floor(d / 30)}mo ago`;
    return `${Math.floor(d / 365)}y ago`;
  }

  const totalSize = $derived(images.reduce((sum, i) => sum + i.Size, 0));

  $effect(() => { load(); });
</script>

<section class="space-y-6">
  <div class="flex items-center justify-between flex-wrap gap-3">
    <div>
      <h2 class="text-2xl font-semibold tracking-tight">Images</h2>
      <p class="text-sm text-[var(--fg-muted)] mt-0.5">
        {images.length} {images.length === 1 ? 'image' : 'images'} · {formatSize(totalSize)} total
      </p>
    </div>
    <div class="flex gap-2">
      <Button variant="secondary" size="sm" onclick={load}>
        <RefreshCw class="w-3.5 h-3.5 {loading ? 'animate-spin' : ''}" />
        Refresh
      </Button>
      <Button variant="secondary" size="sm" onclick={prune}>
        <Sparkles class="w-3.5 h-3.5" />
        Prune
      </Button>
    </div>
  </div>

  {#if loading && images.length === 0}
    <Card>
      <div class="divide-y divide-[var(--border)]">
        {#each Array(4) as _}
          <div class="px-5 py-4 flex items-center gap-4">
            <Skeleton width="2.5rem" height="2.5rem" />
            <div class="flex-1 space-y-1.5">
              <Skeleton width="40%" height="0.85rem" />
              <Skeleton width="25%" height="0.75rem" />
            </div>
          </div>
        {/each}
      </div>
    </Card>
  {:else if images.length === 0}
    <Card>
      <EmptyState
        icon={ImageIcon}
        title="No images"
        description="Pull an image or deploy a stack to populate your local image store."
      />
    </Card>
  {:else}
    <Card>
      <div class="divide-y divide-[var(--border)]">
        {#each images as img}
          <div class="flex items-center gap-4 px-5 py-3 hover:bg-[var(--surface-hover)] transition-colors">
            <div class="w-10 h-10 rounded-lg bg-[color-mix(in_srgb,#a855f7_15%,transparent)] text-[#c084fc] flex items-center justify-center shrink-0">
              <ImageIcon class="w-5 h-5" />
            </div>
            <div class="flex-1 min-w-0">
              <div class="font-mono text-sm truncate">{img.RepoTags?.[0] ?? '<untagged>'}</div>
              <div class="flex gap-3 mt-0.5 text-xs text-[var(--fg-muted)]">
                <span>{formatSize(img.Size)}</span>
                <span>·</span>
                <span class="font-mono">{img.Id.slice(7, 19)}</span>
                <span>·</span>
                <span>{fmtAge(img.Created)}</span>
              </div>
            </div>
            <Button size="xs" variant="ghost" onclick={() => removeImage(img.Id)} aria-label="Remove">
              <Trash2 class="w-3.5 h-3.5 text-[var(--color-danger-400)]" />
            </Button>
          </div>
        {/each}
      </div>
    </Card>
  {/if}
</section>
