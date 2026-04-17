<script lang="ts">
  import { page } from '$app/stores';
  import { api, ApiError, type VolumeEntry, type VolumeFileResult } from '$lib/api';
  import { Card, Badge, Skeleton, Button, EmptyState } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { allowed } from '$lib/rbac';
  import {
    ChevronLeft, Folder, File, FileText, Link2, Download, RefreshCw, HardDrive, AlertTriangle
  } from 'lucide-svelte';

  const name = $derived(decodeURIComponent($page.params.name));
  const hostId = $derived($page.url.searchParams.get('host') || 'local');

  type Tab = 'overview' | 'files';
  let tab = $state<Tab>('overview');

  // --- Overview state ---
  let inspect = $state<any>(null);
  let inspectLoading = $state(false);
  async function loadInspect() {
    inspectLoading = true;
    try {
      inspect = await api.volumes.inspect(name, hostId);
    } catch (err) {
      toast.error('Failed to load volume', err instanceof ApiError ? err.message : undefined);
    } finally {
      inspectLoading = false;
    }
  }

  // --- Files state (P.11.8) ---
  // `currentPath` is the path inside the volume, always starts with "/"
  // for display. Backend takes the stripped form via URLSearchParams.
  let currentPath = $state('/');
  let entries = $state<VolumeEntry[]>([]);
  let entriesLoading = $state(false);
  let browseError = $state<string | null>(null);

  let selectedFile = $state<string | null>(null);
  let fileResult = $state<VolumeFileResult | null>(null);
  let fileLoading = $state(false);
  let fileError = $state<string | null>(null);

  async function loadDir(path: string) {
    entriesLoading = true;
    browseError = null;
    // Clear any preview — stale content when navigating is worse than empty.
    selectedFile = null;
    fileResult = null;
    fileError = null;
    try {
      const apiPath = path === '/' ? '' : path;
      entries = await api.volumes.browse(name, apiPath, hostId);
      currentPath = path;
    } catch (err) {
      browseError = err instanceof ApiError ? err.message : 'failed to browse';
      entries = [];
    } finally {
      entriesLoading = false;
    }
  }

  function joinPath(base: string, child: string): string {
    if (base === '/' || base === '') return '/' + child;
    return base.replace(/\/$/, '') + '/' + child;
  }

  function parentPath(p: string): string {
    if (p === '/' || p === '') return '/';
    const trimmed = p.replace(/\/$/, '');
    const i = trimmed.lastIndexOf('/');
    return i <= 0 ? '/' : trimmed.slice(0, i);
  }

  function onEntryClick(e: VolumeEntry) {
    if (e.type === 'dir') {
      loadDir(joinPath(currentPath, e.name));
    } else if (e.type === 'file') {
      loadFile(joinPath(currentPath, e.name));
    }
    // symlinks: ignore click — the UX of "where does it point?" needs
    // thought; show link_dest inline is enough for v1.
  }

  async function loadFile(path: string) {
    selectedFile = path;
    fileResult = null;
    fileError = null;
    fileLoading = true;
    try {
      fileResult = await api.volumes.readFile(name, path, hostId);
    } catch (err) {
      fileError = err instanceof ApiError ? err.message : 'failed to read';
    } finally {
      fileLoading = false;
    }
  }

  // Breadcrumb segments — the root is rendered separately so clicking
  // "/" always returns to the volume root even when deeply nested.
  const breadcrumb = $derived.by(() => {
    if (currentPath === '/' || currentPath === '') return [];
    return currentPath.replace(/^\//, '').split('/').filter(Boolean);
  });

  function goToBreadcrumb(i: number) {
    const segments = breadcrumb.slice(0, i + 1);
    loadDir('/' + segments.join('/'));
  }

  function fmtSize(n: number): string {
    if (n < 1024) return `${n} B`;
    const units = ['KB', 'MB', 'GB', 'TB'];
    let v = n / 1024;
    let i = 0;
    while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
    return `${v.toFixed(1)} ${units[i]}`;
  }

  function fmtMtime(iso: string): string {
    const d = new Date(iso);
    return d.toISOString().slice(0, 19).replace('T', ' ');
  }

  function entryIcon(e: VolumeEntry) {
    if (e.type === 'dir') return Folder;
    if (e.type === 'symlink') return Link2;
    return File;
  }

  // Decoded text for preview — happens lazily in the template so we
  // don't crash on invalid UTF-8 for binary files.
  const previewText = $derived.by(() => {
    if (!fileResult || fileResult.binary) return '';
    try {
      return atob(fileResult.content);
    } catch {
      return '';
    }
  });

  function downloadBlob() {
    if (!fileResult || !selectedFile) return;
    const bytes = Uint8Array.from(atob(fileResult.content), (c) => c.charCodeAt(0));
    const blob = new Blob([bytes]);
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = selectedFile.split('/').filter(Boolean).pop() || 'file';
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    URL.revokeObjectURL(url);
  }

  $effect(() => {
    if (tab === 'overview') loadInspect();
    else if (tab === 'files') loadDir('/');
  });
</script>

<section class="space-y-4">
  <div class="flex items-center gap-3">
    <a href="/volumes" class="p-1.5 rounded hover:bg-[var(--bg-hover)] text-[var(--fg-muted)] hover:text-[var(--fg)]" aria-label="Back to volumes">
      <ChevronLeft class="w-4 h-4" />
    </a>
    <HardDrive class="w-5 h-5 text-[var(--fg-muted)]" />
    <div class="flex-1 min-w-0">
      <h2 class="text-xl font-semibold truncate">{name}</h2>
      <p class="text-xs text-[var(--fg-muted)]">
        Host: <code class="font-mono">{hostId}</code>
      </p>
    </div>
  </div>

  <div class="border-b border-[var(--border)] flex gap-1">
    <button
      class="px-4 py-2 text-sm border-b-2 transition-colors {tab === 'overview' ? 'border-[var(--color-brand-500)] text-[var(--fg)]' : 'border-transparent text-[var(--fg-muted)] hover:text-[var(--fg)]'}"
      onclick={() => (tab = 'overview')}
    >Overview</button>
    {#if allowed('user.manage')}
      <button
        class="px-4 py-2 text-sm border-b-2 transition-colors {tab === 'files' ? 'border-[var(--color-brand-500)] text-[var(--fg)]' : 'border-transparent text-[var(--fg-muted)] hover:text-[var(--fg)]'}"
        onclick={() => (tab = 'files')}
      >Files</button>
    {/if}
  </div>

  {#if tab === 'overview'}
    <Card>
      {#if inspectLoading}
        <Skeleton class="h-32" />
      {:else if inspect}
        <dl class="grid grid-cols-[140px_1fr] gap-y-2 gap-x-4 text-sm">
          <dt class="text-[var(--fg-muted)]">Name</dt>
          <dd class="font-mono">{inspect.Name}</dd>
          <dt class="text-[var(--fg-muted)]">Driver</dt>
          <dd>{inspect.Driver}</dd>
          <dt class="text-[var(--fg-muted)]">Scope</dt>
          <dd><Badge variant={inspect.Scope === 'local' ? 'default' : 'info'}>{inspect.Scope}</Badge></dd>
          <dt class="text-[var(--fg-muted)]">Mountpoint</dt>
          <dd class="font-mono text-xs break-all">{inspect.Mountpoint || '—'}</dd>
          <dt class="text-[var(--fg-muted)]">Created at</dt>
          <dd class="text-[var(--fg-muted)]">{inspect.CreatedAt || '—'}</dd>
          {#if inspect.Labels && Object.keys(inspect.Labels).length > 0}
            <dt class="text-[var(--fg-muted)]">Labels</dt>
            <dd>
              <div class="flex flex-wrap gap-1">
                {#each Object.entries(inspect.Labels) as [k, v]}
                  <span class="text-[11px] px-1.5 py-0.5 rounded border border-[var(--border)] font-mono">
                    {k}={v}
                  </span>
                {/each}
              </div>
            </dd>
          {/if}
          {#if inspect.Options && Object.keys(inspect.Options).length > 0}
            <dt class="text-[var(--fg-muted)]">Options</dt>
            <dd>
              <pre class="text-xs font-mono bg-[var(--bg-muted)] p-2 rounded border border-[var(--border)] overflow-x-auto">{JSON.stringify(inspect.Options, null, 2)}</pre>
            </dd>
          {/if}
        </dl>
      {/if}
    </Card>
  {/if}

  {#if tab === 'files' && allowed('user.manage')}
    <div class="grid md:grid-cols-2 gap-4">
      <Card>
        <!-- Breadcrumb -->
        <div class="flex items-center gap-1 text-xs text-[var(--fg-muted)] mb-3 flex-wrap">
          <button class="hover:text-[var(--fg)] font-mono" onclick={() => loadDir('/')}>/</button>
          {#each breadcrumb as seg, i}
            <span>/</span>
            <button class="hover:text-[var(--fg)] font-mono" onclick={() => goToBreadcrumb(i)}>{seg}</button>
          {/each}
          <button
            class="ml-auto p-1 hover:bg-[var(--bg-hover)] rounded"
            onclick={() => loadDir(currentPath)}
            title="Refresh"
            aria-label="Refresh"
          >
            <RefreshCw class="w-3.5 h-3.5" />
          </button>
        </div>

        {#if entriesLoading}
          <Skeleton class="h-32" />
        {:else if browseError}
          <div class="p-3 text-xs rounded border border-[var(--color-danger-400)] text-[var(--color-danger-500)] bg-[color-mix(in_srgb,var(--color-danger-500)_5%,transparent)]">
            <AlertTriangle class="w-4 h-4 inline mr-1" />
            {browseError}
          </div>
        {:else if entries.length === 0 && currentPath === '/'}
          <EmptyState
            icon={Folder}
            title="Volume is empty"
            description="No files or directories at the volume root."
          />
        {:else}
          <ul class="text-sm divide-y divide-[var(--border)]">
            {#if currentPath !== '/'}
              <li>
                <button
                  class="w-full flex items-center gap-2 px-2 py-1.5 text-left hover:bg-[var(--bg-hover)] rounded"
                  onclick={() => loadDir(parentPath(currentPath))}
                >
                  <Folder class="w-3.5 h-3.5 text-[var(--fg-muted)]" />
                  <span class="text-[var(--fg-muted)]">..</span>
                </button>
              </li>
            {/if}
            {#each entries as e (e.name)}
              {@const Icon = entryIcon(e)}
              <li>
                <button
                  class="w-full flex items-center gap-2 px-2 py-1.5 text-left hover:bg-[var(--bg-hover)] rounded {selectedFile === joinPath(currentPath, e.name) ? 'bg-[var(--bg-hover)]' : ''}"
                  onclick={() => onEntryClick(e)}
                >
                  <Icon class="w-3.5 h-3.5 text-[var(--fg-muted)] flex-shrink-0" />
                  <span class="font-mono text-xs truncate flex-1">{e.name}{e.type === 'dir' ? '/' : ''}</span>
                  {#if e.type === 'file'}
                    <span class="text-xs text-[var(--fg-muted)] tabular-nums">{fmtSize(e.size)}</span>
                  {:else if e.type === 'symlink'}
                    <span class="text-[10px] text-[var(--fg-muted)] font-mono truncate max-w-[120px]" title={e.link_dest}>→ {e.link_dest}</span>
                  {/if}
                  <span class="text-[10px] text-[var(--fg-muted)] tabular-nums hidden sm:inline">{fmtMtime(e.mod_time)}</span>
                </button>
              </li>
            {/each}
          </ul>
        {/if}
      </Card>

      <Card>
        {#if !selectedFile}
          <EmptyState
            icon={FileText}
            title="Select a file"
            description="Click a file on the left to preview its content. Text files up to 1 MiB display inline; larger or binary files are offered as a download."
          />
        {:else if fileLoading}
          <Skeleton class="h-48" />
        {:else if fileError}
          <div class="p-3 text-xs rounded border border-[var(--color-danger-400)] text-[var(--color-danger-500)]">
            <AlertTriangle class="w-4 h-4 inline mr-1" />
            {fileError}
          </div>
        {:else if fileResult}
          <div class="flex items-start justify-between mb-3 gap-3">
            <div class="min-w-0">
              <p class="font-mono text-xs truncate" title={selectedFile}>{selectedFile}</p>
              <p class="text-[10px] text-[var(--fg-muted)] mt-0.5">
                {fmtSize(fileResult.size)}{fileResult.truncated ? ' · truncated preview' : ''}{fileResult.binary ? ' · binary' : ''}
              </p>
            </div>
            <Button variant="secondary" onclick={downloadBlob}>
              <Download class="w-3.5 h-3.5" />
              Download
            </Button>
          </div>
          {#if fileResult.binary}
            <div class="p-6 text-center text-sm text-[var(--fg-muted)] bg-[var(--bg-muted)] rounded border border-[var(--border)]">
              Binary file — preview hidden. Use the Download button to inspect offline.
            </div>
          {:else}
            <pre class="text-xs font-mono bg-[var(--bg-muted)] p-3 rounded border border-[var(--border)] overflow-auto max-h-[500px] whitespace-pre-wrap break-all">{previewText}</pre>
            {#if fileResult.truncated}
              <p class="text-[10px] text-[var(--fg-muted)] mt-2">
                Only the first 1 MiB is shown. Download for the full file.
              </p>
            {/if}
          {/if}
        {/if}
      </Card>
    </div>

    <div class="text-xs text-[var(--fg-muted)] bg-[var(--bg-muted)] rounded-md p-3 border border-[var(--border)]">
      <p>
        <strong class="text-[var(--fg)]">Read-only.</strong>
        Browsing volumes is audited — every directory listing and file read is recorded in the audit log.
        Intentionally no write, rename, or delete operations here; use a deployed container shell for those.
      </p>
    </div>
  {/if}
</section>
