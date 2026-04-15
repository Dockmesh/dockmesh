<script lang="ts">
  import { api, ApiError, isFanOut, type ScanReport, type Severity } from '$lib/api';
  import { Card, Button, EmptyState, Skeleton, Badge, Modal } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { allowed } from '$lib/rbac';
  import { hosts } from '$lib/stores/host.svelte';
  import { Image as ImageIcon, Trash2, RefreshCw, Sparkles, Shield, ShieldAlert, AlertTriangle, Server, Layers } from 'lucide-svelte';

  const canWrite = $derived(allowed('image.write'));
  const canScan = $derived(allowed('image.scan'));
  const isAll = $derived(hosts.isAll);
  const isRemote = $derived(hosts.id !== 'local' && hosts.id !== 'all');

  interface ImageSummary {
    Id: string;
    RepoTags: string[] | null;
    Size: number;
    Created: number;
    host_id?: string;
    host_name?: string;
  }

  let images = $state<ImageSummary[]>([]);
  let unreachable = $state<Array<{ host_id: string; host_name: string; reason: string }>>([]);
  let loading = $state(true);

  // Scan state
  let scanOpen = $state(false);
  let scanBusy = $state(false);
  let scanReport = $state<ScanReport | null>(null);
  let scanImageRef = $state('');
  let severityFilter = $state<Severity | 'all'>('all');

  async function load() {
    loading = true;
    try {
      const res = await api.images.list(false, hosts.id);
      if (isFanOut(res)) {
        images = res.items as ImageSummary[];
        unreachable = res.unreachable_hosts;
      } else {
        images = res as ImageSummary[];
        unreachable = [];
      }
    } catch (err) {
      toast.error('Failed to load', err instanceof ApiError ? err.message : undefined);
    } finally {
      loading = false;
    }
  }

  // Reload when the host picker changes.
  let prevHost = hosts.id;
  $effect(() => {
    const cur = hosts.id;
    if (cur !== prevHost) {
      prevHost = cur;
      load();
    }
  });

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

  async function scanImage(img: ImageSummary) {
    scanImageRef = img.RepoTags?.[0] ?? img.Id.slice(7, 19);
    scanOpen = true;
    scanBusy = true;
    scanReport = null;
    severityFilter = 'all';

    // Try cached result first for instant display.
    try {
      const cached = await api.images.getScan(img.Id);
      scanReport = cached;
    } catch { /* 404 is fine */ }

    try {
      scanReport = await api.images.scan(img.Id);
      toast.success('Scan complete', `${scanReport.vulnerabilities.length} findings`);
    } catch (err) {
      if (err instanceof ApiError) {
        toast.error('Scan failed', err.message);
      }
    } finally {
      scanBusy = false;
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

  function sevColor(s: Severity): 'danger' | 'warning' | 'info' | 'default' {
    if (s === 'critical' || s === 'high') return 'danger';
    if (s === 'medium') return 'warning';
    if (s === 'low') return 'info';
    return 'default';
  }

  const totalSize = $derived(images.reduce((sum, i) => sum + i.Size, 0));

  const sum = $derived(scanReport?.summary ?? { critical: 0, high: 0, medium: 0, low: 0, negligible: 0, unknown: 0 });

  const filteredVulns = $derived(
    scanReport
      ? scanReport.vulnerabilities
          .filter((v) => severityFilter === 'all' || v.severity === severityFilter)
          .sort((a, b) => {
            const rank = { critical: 5, high: 4, medium: 3, low: 2, negligible: 1, unknown: 0 };
            return rank[b.severity] - rank[a.severity];
          })
      : []
  );

  $effect(() => { load(); });
</script>

<section class="space-y-6">
  <div class="flex items-center justify-between flex-wrap gap-3">
    <div>
      <h2 class="text-2xl font-semibold tracking-tight">Images</h2>
      <p class="text-sm text-[var(--fg-muted)] mt-0.5 flex items-center gap-2">
        {#if isAll}
          <Layers class="w-3.5 h-3.5 text-[var(--color-brand-400)]" />
          <span>Aggregated across all online hosts</span>
          <span>·</span>
        {:else if isRemote}
          <Server class="w-3.5 h-3.5 text-[var(--color-brand-400)]" />
          <span>Showing remote host <span class="font-mono text-[var(--fg)]">{hosts.selected?.name}</span></span>
          <span>·</span>
        {/if}
        <span>{images.length} {images.length === 1 ? 'image' : 'images'} · {formatSize(totalSize)} total</span>
      </p>
    </div>
    <div class="flex gap-2">
      <Button variant="secondary" size="sm" onclick={load}>
        <RefreshCw class="w-3.5 h-3.5 {loading ? 'animate-spin' : ''}" />
        Refresh
      </Button>
      {#if canWrite}
        <Button variant="secondary" size="sm" onclick={prune}>
          <Sparkles class="w-3.5 h-3.5" />
          Prune
        </Button>
      {/if}
    </div>
  </div>

  {#if unreachable.length > 0}
    <div class="dm-card p-3 flex items-start gap-2.5 border border-[color-mix(in_srgb,var(--color-warning-500)_30%,transparent)]">
      <AlertTriangle class="w-4 h-4 text-[var(--color-warning-400)] shrink-0 mt-0.5" />
      <div class="text-xs flex-1">
        <div class="font-medium text-[var(--color-warning-400)]">
          Partial results — {unreachable.length} host{unreachable.length === 1 ? '' : 's'} did not respond
        </div>
        <div class="text-[var(--fg-muted)] mt-0.5">
          {#each unreachable as u, i}<span class="font-mono">{u.host_name}</span>{#if u.reason} ({u.reason}){/if}{#if i < unreachable.length - 1}, {/if}{/each}
        </div>
      </div>
    </div>
  {/if}

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
              <div class="flex items-center gap-2 flex-wrap">
                <div class="font-mono text-sm truncate">{img.RepoTags?.[0] ?? '<untagged>'}</div>
                {#if isAll && img.host_name}
                  <span class="inline-flex items-center gap-1 text-[10px] px-1.5 py-0.5 rounded border border-[var(--border)] text-[var(--fg-muted)] font-mono">
                    <Server class="w-2.5 h-2.5" />
                    {img.host_name}
                  </span>
                {/if}
              </div>
              <div class="flex gap-3 mt-0.5 text-xs text-[var(--fg-muted)]">
                <span>{formatSize(img.Size)}</span>
                <span>·</span>
                <span class="font-mono">{img.Id.slice(7, 19)}</span>
                <span>·</span>
                <span>{fmtAge(img.Created)}</span>
              </div>
            </div>
            {#if canScan}
              <Button size="xs" variant="ghost" onclick={() => scanImage(img)} aria-label="Scan for vulnerabilities" title="Scan">
                <Shield class="w-3.5 h-3.5" />
              </Button>
            {/if}
            {#if canWrite}
              <Button size="xs" variant="ghost" onclick={() => removeImage(img.Id)} aria-label="Remove">
                <Trash2 class="w-3.5 h-3.5 text-[var(--color-danger-400)]" />
              </Button>
            {/if}
          </div>
        {/each}
      </div>
    </Card>
  {/if}
</section>

<Modal bind:open={scanOpen} title="Vulnerability scan" maxWidth="max-w-4xl">
  <div class="mb-4">
    <div class="text-xs text-[var(--fg-muted)]">Image</div>
    <div class="font-mono text-sm truncate">{scanImageRef}</div>
    {#if scanReport}
      <div class="text-xs text-[var(--fg-subtle)] mt-1">
        {scanReport.scanner}
        {scanReport.scanner_version ?? ''}
        · scanned {new Date(scanReport.scanned_at).toLocaleString()}
      </div>
    {/if}
  </div>

  {#if scanBusy && !scanReport}
    <div class="flex items-center gap-3 py-8 justify-center text-[var(--fg-muted)]">
      <Shield class="w-5 h-5 animate-pulse" />
      Running grype scan — this can take a minute for large images…
    </div>
  {:else if scanReport}
    {#snippet sevCard(label: string, count: number, key: Severity | 'all', color: string)}
      <button
        type="button"
        class="dm-card p-3 text-left transition-colors {severityFilter === key ? 'border-[var(--color-brand-500)]' : ''}"
        onclick={() => (severityFilter = key)}
      >
        <div class="text-xs {color}">{label}</div>
        <div class="text-2xl font-bold font-mono tabular-nums">{count}</div>
      </button>
    {/snippet}

    <!-- Severity summary pills -->
    <div class="grid grid-cols-3 sm:grid-cols-6 gap-2 mb-4">
      {@render sevCard('Total', sum.critical + sum.high + sum.medium + sum.low + sum.negligible + sum.unknown, 'all', 'text-[var(--fg-muted)]')}
      {@render sevCard('Critical', sum.critical, 'critical', 'text-[var(--color-danger-400)]')}
      {@render sevCard('High', sum.high, 'high', 'text-[var(--color-danger-400)]')}
      {@render sevCard('Medium', sum.medium, 'medium', 'text-[var(--color-warning-400)]')}
      {@render sevCard('Low', sum.low, 'low', 'text-[var(--color-brand-400)]')}
      {@render sevCard('Negligible', sum.negligible, 'negligible', 'text-[var(--fg-subtle)]')}
    </div>

    {#if filteredVulns.length === 0}
      <div class="py-8 text-center text-sm text-[var(--fg-muted)]">
        {#if scanReport.vulnerabilities.length === 0}
          <ShieldAlert class="w-5 h-5 mx-auto mb-2 text-[var(--color-success-400)]" />
          No vulnerabilities found. Nice.
        {:else}
          No entries match the current filter.
        {/if}
      </div>
    {:else}
      <div class="border border-[var(--border)] rounded-lg overflow-hidden">
        <table class="w-full text-sm">
          <thead class="text-left text-xs text-[var(--fg-muted)] uppercase tracking-wider bg-[var(--bg-elevated)]">
            <tr>
              <th class="px-3 py-2 font-medium">Severity</th>
              <th class="px-3 py-2 font-medium">CVE</th>
              <th class="px-3 py-2 font-medium">Package</th>
              <th class="px-3 py-2 font-medium">Version</th>
              <th class="px-3 py-2 font-medium">Fix</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-[var(--border)]">
            {#each filteredVulns.slice(0, 200) as v}
              <tr class="hover:bg-[var(--surface-hover)]">
                <td class="px-3 py-1.5">
                  <Badge variant={sevColor(v.severity)} dot>{v.severity}</Badge>
                </td>
                <td class="px-3 py-1.5 font-mono text-xs">
                  {#if v.url}
                    <a href={v.url} target="_blank" rel="noopener" class="text-[var(--color-brand-400)] hover:underline">{v.id}</a>
                  {:else}
                    {v.id}
                  {/if}
                </td>
                <td class="px-3 py-1.5 font-mono text-xs">{v.package}</td>
                <td class="px-3 py-1.5 font-mono text-xs text-[var(--fg-muted)]">{v.version}</td>
                <td class="px-3 py-1.5 font-mono text-xs text-[var(--color-success-400)]">{v.fixed_in ?? '—'}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
      {#if filteredVulns.length > 200}
        <div class="text-xs text-[var(--fg-subtle)] mt-2 text-center">
          showing 200 of {filteredVulns.length} — refine the filter for more detail
        </div>
      {/if}
    {/if}
  {/if}

  {#snippet footer()}
    <Button variant="secondary" onclick={() => (scanOpen = false)}>Close</Button>
  {/snippet}
</Modal>
