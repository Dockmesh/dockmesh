<script lang="ts">
  import { api, ApiError, isFanOut, type ScanReport, type Severity } from '$lib/api';
  import { Card, Button, EmptyState, Skeleton, Badge, Modal, Input } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { allowed } from '$lib/rbac';
  import { autoRefresh } from '$lib/autorefresh';
  import { hosts } from '$lib/stores/host.svelte';
  import {
    Image as ImageIcon, Trash2, RefreshCw, Shield, ShieldAlert, AlertTriangle,
    Server, Layers, Search, ChevronUp, ChevronDown, Download, Check, Package
  } from 'lucide-svelte';

  const canWrite = $derived(allowed('image.write'));
  const canScan = $derived(allowed('image.scan'));
  const isAll = $derived(hosts.isAll);

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
  let search = $state('');

  // In-use: set of image IDs that have at least one container referencing them
  let usedImageIds = $state<Set<string>>(new Set());

  // Sort
  type SortKey = 'tag' | 'size' | 'created' | 'used';
  let sortKey = $state<SortKey>('tag');
  let sortAsc = $state(true);

  // Bulk
  let selected = $state<Set<string>>(new Set());
  let bulkBusy = $state(false);

  // Pull modal
  let showPull = $state(false);
  let pullImage = $state('');
  let pullBusy = $state(false);
  let hubResults = $state<Array<{ repo_name: string; short_description: string; star_count: number; is_official: boolean }>>([]);
  let hubSearchTimer: ReturnType<typeof setTimeout> | null = null;

  // Scan state
  let scanOpen = $state(false);
  let scanBusy = $state(false);
  let scanReport = $state<ScanReport | null>(null);
  let scanImageRef = $state('');
  let severityFilter = $state<Severity | 'all'>('all');

  async function load() {
    loading = true;
    try {
      const [imgRes, ctrRes] = await Promise.all([
        api.images.list(false, hosts.id),
        api.containers.list(true, hosts.id).catch(() => [])
      ]);
      if (isFanOut(imgRes)) {
        images = imgRes.items as ImageSummary[];
        unreachable = imgRes.unreachable_hosts;
      } else {
        images = imgRes as ImageSummary[];
        unreachable = [];
      }
      // Build in-use set from container image IDs
      const containers: any[] = isFanOut(ctrRes) ? ctrRes.items : (ctrRes as any[]);
      const used = new Set<string>();
      for (const c of containers) {
        if (c.ImageID) used.add(c.ImageID);
      }
      usedImageIds = used;
    } catch (err) {
      toast.error('Failed to load', err instanceof ApiError ? err.message : undefined);
    } finally {
      loading = false;
    }
  }

  $effect(() => { hosts.id; load(); });
  $effect(() => autoRefresh(load, 10_000));

  // Filtering + sorting
  const visible = $derived(
    images
      .filter(img => {
        if (!search.trim()) return true;
        const q = search.toLowerCase();
        const tag = (img.RepoTags?.[0] ?? '').toLowerCase();
        return tag.includes(q) || img.Id.toLowerCase().includes(q);
      })
      .sort((a, b) => {
        let cmp = 0;
        switch (sortKey) {
          case 'tag': cmp = tagOf(a).localeCompare(tagOf(b)); break;
          case 'size': cmp = a.Size - b.Size; break;
          case 'created': cmp = a.Created - b.Created; break;
          case 'used': cmp = (isUsed(a) ? 1 : 0) - (isUsed(b) ? 1 : 0); break;
        }
        return sortAsc ? cmp : -cmp;
      })
  );

  const allSelected = $derived(visible.length > 0 && visible.every(i => selected.has(i.Id)));
  function toggleAll() {
    if (allSelected) { selected = new Set(); }
    else { selected = new Set(visible.map(i => i.Id)); }
  }
  function toggleOne(id: string) {
    const next = new Set(selected);
    if (next.has(id)) next.delete(id); else next.add(id);
    selected = next;
  }
  function toggleSort(key: SortKey) {
    if (sortKey === key) { sortAsc = !sortAsc; }
    else { sortKey = key; sortAsc = true; }
  }

  // Helpers
  function tagOf(img: ImageSummary): string { return img.RepoTags?.[0] ?? '<untagged>'; }
  function isUsed(img: ImageSummary): boolean { return usedImageIds.has(img.Id); }
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
          .filter(v => severityFilter === 'all' || v.severity === severityFilter)
          .sort((a, b) => {
            const rank: Record<string, number> = { critical: 5, high: 4, medium: 3, low: 2, negligible: 1, unknown: 0 };
            return (rank[b.severity] ?? 0) - (rank[a.severity] ?? 0);
          })
      : []
  );

  // Actions
  async function removeImage(id: string) {
    if (!(await confirm.ask({ title: 'Remove image', message: 'Remove this image?', body: 'Docker refuses the remove if any container is still using it — stop those containers first.', confirmLabel: 'Remove', danger: true }))) return;
    try {
      await api.images.remove(id, true);
      toast.success('Removed');
      await load();
    } catch (err) {
      toast.error('Remove failed', err instanceof ApiError ? err.message : undefined);
    }
  }
  async function bulkRemove() {
    if (!(await confirm.ask({ title: 'Remove images', message: `Remove ${selected.size} image(s)?`, body: 'Images in use by containers are skipped with an error; the rest are removed.', confirmLabel: 'Remove', danger: true }))) return;
    bulkBusy = true;
    let ok = 0, fail = 0;
    for (const img of images.filter(i => selected.has(i.Id))) {
      try { await api.images.remove(img.Id, true); ok++; } catch { fail++; }
    }
    toast.success(`Removed: ${ok}${fail ? `, ${fail} failed` : ''}`);
    selected = new Set();
    bulkBusy = false;
    await load();
  }
  async function prune() {
    if (!(await confirm.ask({ title: 'Prune images', message: 'Remove all dangling images?', body: 'Dangling images are layers no tagged image references. Safe to remove — Docker re-pulls what\u2019s needed.', confirmLabel: 'Prune', danger: true }))) return;
    try {
      const r = await api.images.prune();
      toast.success('Pruned', `reclaimed ${formatSize(r.SpaceReclaimed)}`);
      await load();
    } catch (err) {
      toast.error('Prune failed', err instanceof ApiError ? err.message : undefined);
    }
  }
  async function doPull() {
    if (!pullImage.trim()) return;
    pullBusy = true;
    try {
      await api.images.pull(pullImage.trim());
      toast.success('Pulled', pullImage);
      showPull = false;
      pullImage = '';
      hubResults = [];
      await load();
    } catch (err) {
      toast.error('Pull failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      pullBusy = false;
    }
  }
  async function scanImage(img: ImageSummary) {
    // Use the tag for scanning — the sha256 ID works but tags give
    // cleaner cache keys and avoid URL-encoding issues with the colon.
    const ref = img.RepoTags?.[0] ?? img.Id;
    scanImageRef = ref;
    scanOpen = true;
    scanBusy = true;
    scanReport = null;
    severityFilter = 'all';
    try {
      // Run fresh scan (POST). Skip the cached-result GET to avoid
      // console 404 noise — the POST always returns the latest result.
      scanReport = await api.images.scan(ref);
      toast.success('Scan complete', `${scanReport.vulnerabilities.length} findings`);
    } catch (err) {
      if (err instanceof ApiError) {
        toast.error('Scan failed', err.message);
      } else {
        toast.error('Scan failed', 'Scanner unavailable — is Grype installed?');
      }
    } finally {
      scanBusy = false;
    }
  }

  // Docker Hub search
  function onPullInput() {
    if (hubSearchTimer) clearTimeout(hubSearchTimer);
    const q = pullImage.trim();
    if (q.length < 2) { hubResults = []; return; }
    hubSearchTimer = setTimeout(async () => {
      try {
        const res = await fetch(`https://hub.docker.com/v2/search/repositories/?query=${encodeURIComponent(q)}&page_size=8`);
        if (res.ok) {
          const data = await res.json();
          hubResults = data.results ?? [];
        }
      } catch { /* ignore — Hub not reachable */ }
    }, 400);
  }
</script>

<section class="space-y-4">
  <!-- Header -->
  <div class="flex items-center justify-between flex-wrap gap-3">
    <div>
      <h2 class="text-2xl font-semibold tracking-tight">Images</h2>
      <p class="text-sm text-[var(--fg-muted)] mt-0.5">
        {images.length} image{images.length === 1 ? '' : 's'} · {formatSize(totalSize)} total
        {#if isAll}across all hosts{/if}
      </p>
    </div>
    <div class="flex items-center gap-2">
      {#if canWrite}
        <Button variant="secondary" size="sm" onclick={prune}>
          <Trash2 class="w-3.5 h-3.5" /> Prune
        </Button>
        <Button variant="primary" size="sm" onclick={() => { showPull = true; pullImage = ''; hubResults = []; }}>
          <Download class="w-3.5 h-3.5" /> Pull
        </Button>
      {/if}
      <Button variant="secondary" size="sm" onclick={load}>
        <RefreshCw class="w-3.5 h-3.5 {loading ? 'animate-spin' : ''}" /> Refresh
      </Button>
    </div>
  </div>

  <!-- Search -->
  {#if images.length > 0}
    <div class="relative max-w-sm">
      <Search class="w-3.5 h-3.5 text-[var(--fg-subtle)] absolute left-2.5 top-1/2 -translate-y-1/2 pointer-events-none" />
      <input type="search" placeholder="Search by tag or ID…" bind:value={search} class="dm-input pl-8 pr-3 py-1.5 text-sm w-full" />
    </div>
  {/if}

  <!-- Bulk action bar -->
  {#if selected.size > 0 && canWrite}
    <div class="flex items-center gap-3 px-4 py-2.5 rounded-lg bg-[var(--surface)] border border-[var(--border)]">
      <span class="text-sm font-medium">{selected.size} selected</span>
      <div class="flex gap-1.5 ml-auto">
        <Button size="xs" variant="danger" onclick={bulkRemove} disabled={bulkBusy}>
          <Trash2 class="w-3.5 h-3.5" /> Remove
        </Button>
        <button class="text-xs text-[var(--fg-muted)] hover:text-[var(--fg)] ml-2" onclick={() => (selected = new Set())}>Clear</button>
      </div>
    </div>
  {/if}

  <!-- Unreachable banner -->
  {#if unreachable.length > 0}
    <div class="dm-card p-3 flex items-start gap-2.5 border border-[color-mix(in_srgb,var(--color-warning-500)_30%,transparent)]">
      <AlertTriangle class="w-4 h-4 text-[var(--color-warning-400)] shrink-0 mt-0.5" />
      <div class="text-xs">
        <span class="font-medium text-[var(--color-warning-400)]">{unreachable.length} host(s) unreachable</span>
        <span class="text-[var(--fg-muted)]"> — {unreachable.map(u => u.host_name).join(', ')}</span>
      </div>
    </div>
  {/if}

  <!-- Table -->
  {#if loading && images.length === 0}
    <Card>
      <div class="divide-y divide-[var(--border)]">
        {#each Array(5) as _}
          <div class="px-5 py-3.5 flex items-center gap-4">
            <Skeleton width="1rem" height="1rem" />
            <Skeleton width="35%" height="0.85rem" />
            <Skeleton width="15%" height="0.75rem" />
          </div>
        {/each}
      </div>
    </Card>
  {:else if images.length === 0}
    <Card>
      <EmptyState icon={ImageIcon} title="No images" description="Pull an image or deploy a stack to get started." />
    </Card>
  {:else if visible.length === 0}
    <Card class="p-8 text-center text-sm text-[var(--fg-muted)]">No images match this search.</Card>
  {:else}
    <Card>
      <div class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead>
            <tr class="border-b border-[var(--border)] text-[var(--fg-muted)] text-xs uppercase tracking-wider">
              {#if canWrite}
                <th class="w-10 px-3 py-3">
                  <input type="checkbox" checked={allSelected} onchange={toggleAll} class="accent-[var(--color-brand-500)]" />
                </th>
              {/if}
              {#snippet sortHeader(key: SortKey, label: string)}
                <th class="text-left px-3 py-3 cursor-pointer select-none hover:text-[var(--fg)]" onclick={() => toggleSort(key)}>
                  <span class="inline-flex items-center gap-1">
                    {label}
                    {#if sortKey === key}
                      {#if sortAsc}<ChevronUp class="w-3 h-3" />{:else}<ChevronDown class="w-3 h-3" />{/if}
                    {/if}
                  </span>
                </th>
              {/snippet}
              {@render sortHeader('tag', 'Tag')}
              <th class="text-left px-3 py-3">ID</th>
              {@render sortHeader('size', 'Size')}
              {@render sortHeader('created', 'Created')}
              {@render sortHeader('used', 'In Use')}
              {#if isAll}
                <th class="text-left px-3 py-3">Host</th>
              {/if}
              <th class="text-right px-3 py-3 w-28">Actions</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-[var(--border)]">
            {#each visible as img (img.Id + (img.host_id ?? ''))}
              {@const used = isUsed(img)}
              <tr class="hover:bg-[var(--surface-hover)] transition-colors {selected.has(img.Id) ? 'bg-[color-mix(in_srgb,var(--color-brand-500)_5%,transparent)]' : ''}">
                {#if canWrite}
                  <td class="w-10 px-3 py-2.5">
                    <input type="checkbox" checked={selected.has(img.Id)} onchange={() => toggleOne(img.Id)} class="accent-[var(--color-brand-500)]" />
                  </td>
                {/if}
                <td class="px-3 py-2.5">
                  <span class="font-mono text-sm truncate block max-w-[280px]" title={tagOf(img)}>{tagOf(img)}</span>
                </td>
                <td class="px-3 py-2.5 text-[10px] text-[var(--fg-muted)] font-mono">{img.Id.slice(7, 19)}</td>
                <td class="px-3 py-2.5 text-xs text-[var(--fg-muted)] tabular-nums">{formatSize(img.Size)}</td>
                <td class="px-3 py-2.5 text-xs text-[var(--fg-muted)]">{fmtAge(img.Created)}</td>
                <td class="px-3 py-2.5">
                  {#if used}
                    <Badge variant="success" dot>in use</Badge>
                  {:else}
                    <span class="text-xs text-[var(--fg-subtle)]">unused</span>
                  {/if}
                </td>
                {#if isAll}
                  <td class="px-3 py-2.5">
                    <span class="inline-flex items-center gap-1 font-mono text-[10px] px-1.5 py-0.5 rounded border border-[var(--border)] text-[var(--fg-muted)]">
                      <Server class="w-2.5 h-2.5" />
                      {img.host_name || 'local'}
                    </span>
                  </td>
                {/if}
                <td class="px-3 py-2.5">
                  <div class="flex gap-0.5 justify-end">
                    {#if canScan}
                      <button class="p-1.5 rounded-md text-[var(--fg-muted)] hover:text-[var(--fg)] hover:bg-[var(--surface-hover)]" title="Scan for vulnerabilities" onclick={() => scanImage(img)}>
                        <Shield class="w-3.5 h-3.5" />
                      </button>
                    {/if}
                    {#if canWrite}
                      <button class="p-1.5 rounded-md text-[var(--color-danger-400)] hover:bg-[color-mix(in_srgb,var(--color-danger-500)_10%,transparent)]" title="Remove" onclick={() => removeImage(img.Id)}>
                        <Trash2 class="w-3.5 h-3.5" />
                      </button>
                    {/if}
                  </div>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    </Card>
  {/if}
</section>

<!-- Pull modal with Docker Hub search -->
<Modal bind:open={showPull} title="Pull image" maxWidth="max-w-lg">
  <div class="space-y-3">
    <div>
      <label for="pull-input" class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">Image name</label>
      <input
        id="pull-input"
        type="text"
        class="dm-input text-sm"
        placeholder="nginx:alpine, postgres:16, ghcr.io/org/app:latest"
        bind:value={pullImage}
        oninput={onPullInput}
        disabled={pullBusy}
      />
    </div>
    {#if hubResults.length > 0 && !pullBusy}
      <div class="border border-[var(--border)] rounded-lg max-h-48 overflow-auto divide-y divide-[var(--border)]">
        {#each hubResults as r}
          <button
            class="w-full text-left px-3 py-2 hover:bg-[var(--surface-hover)] flex items-start gap-2"
            onclick={() => { pullImage = r.repo_name; hubResults = []; }}
          >
            <Package class="w-4 h-4 text-[var(--fg-muted)] shrink-0 mt-0.5" />
            <div class="min-w-0 flex-1">
              <div class="text-sm font-mono flex items-center gap-1.5">
                {r.repo_name}
                {#if r.is_official}
                  <Badge variant="info">official</Badge>
                {/if}
              </div>
              <div class="text-[10px] text-[var(--fg-muted)] truncate">{r.short_description}</div>
            </div>
            <span class="text-[10px] text-[var(--fg-subtle)] shrink-0">★ {r.star_count}</span>
          </button>
        {/each}
      </div>
    {/if}
    <p class="text-xs text-[var(--fg-muted)]">
      Type to search Docker Hub, or enter a full image reference including registry and tag.
    </p>
  </div>
  {#snippet footer()}
    <Button variant="secondary" onclick={() => (showPull = false)}>Cancel</Button>
    <Button variant="primary" loading={pullBusy} disabled={pullBusy || !pullImage.trim()} onclick={doPull}>
      <Download class="w-4 h-4" /> Pull
    </Button>
  {/snippet}
</Modal>

<!-- Scan modal (kept from original) -->
<Modal bind:open={scanOpen} title="Vulnerability scan" maxWidth="max-w-4xl">
  <div class="space-y-4">
    <div class="flex items-center gap-3">
      <div class="font-mono text-sm">{scanImageRef}</div>
      {#if scanBusy}
        <Badge variant="warning" dot>scanning…</Badge>
      {:else if scanReport}
        <Badge variant={scanReport.vulnerabilities.length === 0 ? 'success' : 'danger'} dot>
          {scanReport.vulnerabilities.length} finding{scanReport.vulnerabilities.length === 1 ? '' : 's'}
        </Badge>
      {/if}
    </div>

    {#if scanReport}
      <div class="grid grid-cols-3 sm:grid-cols-6 gap-2">
        {#each [['critical', sum.critical], ['high', sum.high], ['medium', sum.medium], ['low', sum.low], ['negligible', sum.negligible], ['unknown', sum.unknown]] as [sev, count]}
          <button
            class="dm-card p-2 text-center cursor-pointer hover:border-[var(--color-brand-500)] {severityFilter === sev ? 'border-[var(--color-brand-500)]' : ''}"
            onclick={() => (severityFilter = severityFilter === sev ? 'all' : sev as Severity)}
          >
            <div class="text-lg font-bold tabular-nums">{count}</div>
            <div class="text-[10px] uppercase text-[var(--fg-muted)]">{sev}</div>
          </button>
        {/each}
      </div>

      {#if filteredVulns.length > 0}
        <div class="overflow-x-auto max-h-[50vh] overflow-y-auto">
          <table class="w-full text-xs">
            <thead class="sticky top-0 bg-[var(--bg-elevated)]">
              <tr class="border-b border-[var(--border)] text-[var(--fg-muted)] uppercase tracking-wider">
                <th class="text-left px-3 py-2">Severity</th>
                <th class="text-left px-3 py-2">Package</th>
                <th class="text-left px-3 py-2">Version</th>
                <th class="text-left px-3 py-2">Fixed In</th>
                <th class="text-left px-3 py-2">CVE</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-[var(--border)]">
              {#each filteredVulns as v}
                <tr class="hover:bg-[var(--surface-hover)]">
                  <td class="px-3 py-1.5"><Badge variant={sevColor(v.severity)}>{v.severity}</Badge></td>
                  <td class="px-3 py-1.5 font-mono">{v.package}</td>
                  <td class="px-3 py-1.5 font-mono">{v.version}</td>
                  <td class="px-3 py-1.5 font-mono">{v.fixed_in || '—'}</td>
                  <td class="px-3 py-1.5 font-mono">{v.id}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {:else}
        <div class="text-sm text-[var(--fg-muted)] text-center py-4">
          {scanReport.vulnerabilities.length === 0 ? 'No vulnerabilities found.' : 'No matches for this severity filter.'}
        </div>
      {/if}
    {:else if scanBusy}
      <div class="text-center py-8 text-sm text-[var(--fg-muted)]">
        <RefreshCw class="w-5 h-5 animate-spin inline mb-2" /><br />
        Scanning with Grype…
      </div>
    {/if}
  </div>
  {#snippet footer()}
    <Button variant="secondary" onclick={() => (scanOpen = false)}>Close</Button>
  {/snippet}
</Modal>
