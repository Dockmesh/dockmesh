<script lang="ts">
  // Volume detail — editorial rebuild. Three tabs (Overview · Files ·
  // Containers) plus a sticky header with back-link to /resources and
  // the standard editorial action row. The file browser preserves the
  // existing P.11.8 audit-logged behaviour — every directory listing
  // and file read is recorded server-side. Read-only on purpose; no
  // rename / write / delete in here. Operators who need that have a
  // shell-into-container path.
  import { fmtBytes } from '$lib/format';
  import { untrack } from 'svelte';
  import { latest } from '$lib/latest';
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import { api, ApiError, isFanOut, type VolumeEntry, type VolumeFileResult } from '$lib/api';
  import { Skeleton, EmptyState } from '$lib/components/ui';
  import { Eyebrow } from '$lib/components/editorial';
  import { toast } from '$lib/stores/toast.svelte';
  import { copyWithToast } from '$lib/clipboard';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { allowed } from '$lib/rbac.svelte';
  import { hosts } from '$lib/stores/host.svelte';
  import { pageContext } from '$lib/stores/pageContext.svelte';
  import { fmtDateTime } from '$lib/i18n';
  import { m } from '$lib/paraglide/messages.js';
  import { containerStateLabel } from '$lib/labels';
  import {
    ChevronLeft, Folder, File as FileIcon, FileText, Link2, Download,
    RefreshCw, HardDrive, AlertTriangle, Trash2, Copy, Box
  } from 'lucide-svelte';

  const name = $derived(decodeURIComponent(page.params.name));
  const hostId = $derived(page.url.searchParams.get('host') || 'local');

  $effect(() => {
    pageContext.set(name);
    return () => pageContext.clear();
  });

  type Tab = 'overview' | 'files' | 'containers';
  let tab = $state<Tab>(((new URLSearchParams(page.url.search).get('tab')) as Tab) || 'overview');

  // ───────── Inspect
  let inspect = $state<any>(null);
  let inspectLoading = $state(true);
  let inspectError = $state<string | null>(null);
  const nextInspect = latest();
  async function loadInspect() {
    const current = nextInspect();
    inspectLoading = true;
    try {
      const r = await api.volumes.inspect(name, hostId);
      if (!current()) return;
      inspect = r;
      inspectError = null;
    } catch (err) {
      if (!current()) return;
      inspectError = err instanceof ApiError ? err.message : m.volumes_load_failed();
      toast.error(m.volumes_load_failed(), inspectError);
    } finally {
      if (current()) inspectLoading = false;
    }
  }

  // ───────── Containers using this volume
  interface AttachedContainer {
    id: string;
    name: string;
    state: string;
    image: string;
    mountTarget: string;
    rw: boolean;
  }
  let attached = $state<AttachedContainer[]>([]);
  let attachedLoading = $state(false);
  async function loadAttached() {
    attachedLoading = true;
    try {
      const r = await api.containers.list(true, hostId);
      const list: any[] = isFanOut(r) ? r.items : (r as any[]);
      const out: AttachedContainer[] = [];
      for (const c of list) {
        for (const mt of (c.Mounts ?? [])) {
          if (mt.Type === 'volume' && mt.Name === name) {
            out.push({
              id: c.Id,
              name: (c.Names?.[0] ?? c.Id).replace(/^\//, ''),
              state: c.State,
              image: c.Image,
              mountTarget: mt.Destination ?? '?',
              rw: mt.RW ?? true
            });
            break;
          }
        }
      }
      attached = out;
    } catch (err) {
      toast.error(m.volumes_containers_load_failed(), err instanceof ApiError ? err.message : undefined);
    } finally {
      attachedLoading = false;
    }
  }

  // ───────── Files (P.11.8)
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
    selectedFile = null;
    fileResult = null;
    fileError = null;
    try {
      const apiPath = path === '/' ? '' : path;
      entries = await api.volumes.browse(name, apiPath, hostId);
      currentPath = path;
    } catch (err) {
      browseError = err instanceof ApiError ? err.message : m.volumes_browse_failed();
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
    if (e.type === 'dir') loadDir(joinPath(currentPath, e.name));
    else if (e.type === 'file') loadFile(joinPath(currentPath, e.name));
  }
  async function loadFile(path: string) {
    selectedFile = path;
    fileResult = null;
    fileError = null;
    fileLoading = true;
    try {
      fileResult = await api.volumes.readFile(name, path, hostId);
    } catch (err) {
      fileError = err instanceof ApiError ? err.message : m.volumes_read_failed();
    } finally {
      fileLoading = false;
    }
  }
  const breadcrumb = $derived.by(() => {
    if (currentPath === '/' || currentPath === '') return [];
    return currentPath.replace(/^\//, '').split('/').filter(Boolean);
  });
  function goToBreadcrumb(i: number) {
    const segments = breadcrumb.slice(0, i + 1);
    loadDir('/' + segments.join('/'));
  }

  function fmtMtime(iso: string): string {
    const d = new Date(iso);
    if (isNaN(d.getTime())) return iso;
    return fmtDateTime(d, { timeZone: 'UTC', year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit' });
  }
  function fmtDay(iso?: string): string {
    if (!iso) return '—';
    const d = new Date(iso);
    if (isNaN(d.getTime())) return iso;
    return fmtDateTime(d, { timeZone: 'UTC', year: 'numeric', month: '2-digit', day: '2-digit' });
  }
  function entryIcon(e: VolumeEntry) {
    if (e.type === 'dir') return Folder;
    if (e.type === 'symlink') return Link2;
    return FileIcon;
  }
  const previewText = $derived.by(() => {
    if (!fileResult || fileResult.binary) return '';
    try { return atob(fileResult.content); } catch { return ''; }
  });
  async function downloadFile() {
    if (!fileResult || !selectedFile) return;
    if (fileResult.truncated) {
      try {
        await api.volumes.downloadFile(name, selectedFile, hostId);
      } catch (err) {
        toast.error(m.volumes_download_failed(), err instanceof ApiError ? err.message : undefined);
      }
      return;
    }
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

  function copyMountpoint() {
    if (!inspect?.Mountpoint) return;
    void copyWithToast(inspect.Mountpoint, m.common_copied(), m.volumes_mountpoint_copied());
  }

  async function deleteVolume() {
    if (!(await confirm.ask({
      title: m.volumes_delete_title(),
      message: m.volumes_delete_message({ name }),
      body: m.volumes_delete_body(),
      confirmLabel: m.common_delete(), danger: true
    }))) return;
    try {
      await api.volumes.remove(name, true, hostId);
      toast.success(m.volumes_deleted(), name);
      goto('/resources?tab=volumes');
    } catch (err) {
      toast.error(m.volumes_delete_failed(), err instanceof ApiError ? err.message : undefined);
    }
  }

  // Tab orchestrator. Containers list comes from /containers + filtering
  // by Mounts; loaded lazily so the Overview tab doesn't pay for it.
  // Inspect feeds the header on every tab, so it loads once per volume
  // rather than per tab.
  $effect(() => {
    name; hostId;
    inspect = null;
    inspectError = null;
    untrack(loadInspect);
  });
  $effect(() => {
    name; hostId;
    if (tab === 'files') untrack(() => loadDir('/'));
    else if (tab === 'containers') untrack(loadAttached);
  });

  const canWrite = $derived(allowed('volumes.delete'));
  const canBrowse = $derived(allowed('volumes.browse'));
  const owner = $derived(inspect?.Labels?.['com.docker.compose.project'] ?? null);
  const labelEntries = $derived(inspect?.Labels ? Object.entries(inspect.Labels) : []);
  const optionsEntries = $derived(inspect?.Options ? Object.entries(inspect.Options) : []);
</script>

<section class="vol-detail">
  <header class="vol-detail-header">
    <div class="vol-detail-back-row">
      <a href="/resources?tab=volumes" class="vol-detail-back" aria-label={m.volumes_back_aria()}>
        <ChevronLeft size={14} strokeWidth={1.5} /> {m.volumes_back_crumb()}
      </a>
    </div>
    <div class="vol-detail-title-row">
      <div class="vol-detail-title-text">
        <h1 class="ed-title vol-detail-title">{name}</h1>
        <p class="ed-subtitle vol-detail-subtitle">
          {inspect?.Driver ?? '—'} · {inspect?.Scope ?? '—'} · {m.volumes_sub_host({ host: hostId })}{owner ? ` · ${m.volumes_sub_stack({ stack: owner })}` : ''}
        </p>
      </div>
      <div class="ed-actions">
        {#if canWrite}
          <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm vol-detail-danger" onclick={deleteVolume}>
            <Trash2 size={12} strokeWidth={1.5} /> {m.volumes_delete_button()}
          </button>
        {/if}
      </div>
    </div>
  </header>

  <!-- ─── Summary tiles ─── -->
  <div class="vol-detail-summary">
    <div class="ed-metric">
      <span class="ed-metric-label">{m.volumes_driver()}</span>
      <div class="ed-metric-value vol-detail-tile-text">{inspect?.Driver ?? '—'}</div>
      <span class="ed-metric-meta">{inspect?.Scope ?? ''}</span>
    </div>
    <div class="ed-metric">
      <span class="ed-metric-label">{m.volumes_in_use()}</span>
      <div class="ed-metric-value">{attached.length || (attachedLoading ? '…' : '—')}</div>
      <span class="ed-metric-meta">{m.volumes_containers_attached({ count: attached.length })}</span>
    </div>
    <div class="ed-metric">
      <span class="ed-metric-label">{m.common_created()}</span>
      <div class="ed-metric-value vol-detail-tile-text">{fmtDay(inspect?.CreatedAt)}</div>
      <span class="ed-metric-meta">{m.volumes_utc()}</span>
    </div>
    <div class="ed-metric">
      <span class="ed-metric-label">{m.volumes_labels()}</span>
      <div class="ed-metric-value">{labelEntries.length}</div>
      <span class="ed-metric-meta">{m.volumes_kv_pairs({ count: labelEntries.length })}</span>
    </div>
  </div>

  <!-- ─── Tabs ─── -->
  <div class="ed-tabs vol-detail-tabs">
    <button type="button" class="ed-tab" class:active={tab === 'overview'} onclick={() => (tab = 'overview')}>
      {m.common_overview()}
    </button>
    <button type="button" class="ed-tab" class:active={tab === 'containers'} onclick={() => (tab = 'containers')}>
      {m.common_containers()} <span class="count">{attached.length}</span>
    </button>
    {#if canBrowse}
      <button type="button" class="ed-tab" class:active={tab === 'files'} onclick={() => (tab = 'files')}>
        {m.volumes_tab_files()}
      </button>
    {/if}
  </div>

  <!-- ============================================================== -->
  <!--  OVERVIEW                                                       -->
  <!-- ============================================================== -->
  {#if tab === 'overview'}
    {#if inspectLoading && !inspect}
      <div class="dm-card vol-detail-card-pad"><Skeleton width="80%" height="6rem" /></div>
    {:else if inspectError && !inspect}
      <div class="dm-card vol-detail-card-pad">
        <EmptyState icon={AlertTriangle} title={m.volumes_not_available()} description={inspectError} />
      </div>
    {:else if inspect}
      <div class="dm-card vol-detail-overview">
        <dl class="vol-detail-dl">
          <dt>{m.common_name()}</dt>
          <dd class="font-mono">{inspect.Name}</dd>
          <dt>{m.volumes_driver()}</dt>
          <dd class="font-mono">{inspect.Driver}</dd>
          <dt>{m.volumes_scope()}</dt>
          <dd>
            <span class="dm-pill {inspect.Scope === 'global' ? 'dm-pill-success' : 'dm-pill-neutral'} vol-detail-pill">{inspect.Scope}</span>
          </dd>
          <dt>{m.volumes_mountpoint()}</dt>
          <dd class="vol-detail-mountpoint">
            <span class="font-mono">{inspect.Mountpoint || '—'}</span>
            {#if inspect.Mountpoint}
              <button type="button" class="dm-btn dm-btn-ghost dm-btn-xs" onclick={copyMountpoint} title={m.common_copy()}>
                <Copy size={11} strokeWidth={1.5} />
              </button>
            {/if}
          </dd>
          <dt>{m.volumes_created_at()}</dt>
          <dd class="font-mono">{inspect.CreatedAt || '—'}</dd>
          {#if labelEntries.length > 0}
            <dt>{m.volumes_labels()}</dt>
            <dd>
              <div class="vol-detail-labels">
                {#each labelEntries as [k, v] (k)}
                  <span class="vol-detail-label-chip font-mono">
                    <span class="vol-detail-label-k">{k}</span>=<span class="vol-detail-label-v">{v}</span>
                  </span>
                {/each}
              </div>
            </dd>
          {/if}
          {#if optionsEntries.length > 0}
            <dt>{m.volumes_options()}</dt>
            <dd>
              <pre class="vol-detail-opts font-mono">{JSON.stringify(inspect.Options, null, 2)}</pre>
            </dd>
          {/if}
        </dl>
      </div>
    {/if}
    <p class="vol-detail-foot font-mono">
      {m.volumes_size_note_before()} <code>du -s</code>{m.volumes_size_note_du_after()}
      {m.volumes_size_note_mid()} <strong>{m.volumes_size_note_files()}</strong> {m.volumes_size_note_after()}
    </p>
  {/if}

  <!-- ============================================================== -->
  <!--  CONTAINERS                                                     -->
  <!-- ============================================================== -->
  {#if tab === 'containers'}
    {#if attachedLoading && attached.length === 0}
      <div class="dm-card vol-detail-card-pad"><Skeleton width="80%" height="5rem" /></div>
    {:else if attached.length === 0}
      <div class="dm-card vol-detail-card-pad">
        <EmptyState icon={Box} title={m.volumes_not_mounted()} description={m.volumes_not_mounted_desc()} />
      </div>
    {:else}
      <div class="vol-detail-container-table">
        <div class="vol-detail-container-row vol-detail-container-row--head">
          <span>{m.common_name()}</span>
          <span>{m.common_state()}</span>
          <span>{m.common_image()}</span>
          <span>{m.volumes_col_mounted_at()}</span>
          <span>{m.volumes_col_mode()}</span>
        </div>
        {#each attached as c (c.id)}
          <a href={`/containers/${c.id}`} class="vol-detail-container-row vol-detail-container-row--data">
            <span class="font-mono vol-detail-container-name">{c.name}</span>
            <span>
              {#if c.state === 'running'}
                <span class="dm-pill dm-pill-success vol-detail-pill"><span class="dm-pill-dot"></span> {m.containers_status_running()}</span>
              {:else}
                <span class="dm-pill dm-pill-neutral vol-detail-pill">{containerStateLabel(c.state)}</span>
              {/if}
            </span>
            <span class="font-mono vol-detail-container-image">{c.image}</span>
            <span class="font-mono">{c.mountTarget}</span>
            <span class="font-mono">{c.rw ? 'rw' : 'ro'}</span>
          </a>
        {/each}
      </div>
    {/if}
  {/if}

  <!-- ============================================================== -->
  <!--  FILES                                                          -->
  <!-- ============================================================== -->
  {#if tab === 'files' && canBrowse}
    <div class="vol-detail-files-grid">
      <div class="dm-card vol-detail-card-pad">
        <div class="vol-detail-files-crumb">
          <button class="vol-detail-files-crumb-seg font-mono" onclick={() => loadDir('/')}>/</button>
          {#each breadcrumb as seg, i (seg + i)}
            <span class="vol-detail-files-crumb-sep">/</span>
            <button class="vol-detail-files-crumb-seg font-mono" onclick={() => goToBreadcrumb(i)}>{seg}</button>
          {/each}
          <button class="vol-detail-files-refresh" onclick={() => loadDir(currentPath)} title={m.common_refresh()} aria-label={m.common_refresh()}>
            <RefreshCw size={11} strokeWidth={1.5} />
          </button>
        </div>

        {#if entriesLoading}
          <Skeleton width="100%" height="6rem" />
        {:else if browseError}
          <div class="vol-detail-error">
            <AlertTriangle size={12} strokeWidth={1.5} /> {browseError}
          </div>
        {:else if entries.length === 0 && currentPath === '/'}
          <EmptyState icon={Folder} title={m.volumes_empty()} description={m.volumes_empty_desc()} />
        {:else}
          <ul class="vol-detail-files-list">
            {#if currentPath !== '/'}
              <li>
                <button class="vol-detail-files-row" onclick={() => loadDir(parentPath(currentPath))}>
                  <Folder size={12} strokeWidth={1.5} />
                  <span class="font-mono vol-detail-files-row-name muted">..</span>
                </button>
              </li>
            {/if}
            {#each entries as e (e.name)}
              {@const Icon = entryIcon(e)}
              {@const fullPath = joinPath(currentPath, e.name)}
              <li>
                <button
                  class="vol-detail-files-row"
                  class:active={selectedFile === fullPath}
                  onclick={() => onEntryClick(e)}
                >
                  <Icon size={12} strokeWidth={1.5} />
                  <span class="font-mono vol-detail-files-row-name">{e.name}{e.type === 'dir' ? '/' : ''}</span>
                  {#if e.type === 'file'}
                    <span class="vol-detail-files-row-size font-mono">{fmtBytes(e.size)}</span>
                  {:else if e.type === 'symlink'}
                    <span class="vol-detail-files-row-link font-mono" title={e.link_dest}>→ {e.link_dest}</span>
                  {/if}
                  <span class="vol-detail-files-row-mtime font-mono">{fmtMtime(e.mod_time)}</span>
                </button>
              </li>
            {/each}
          </ul>
        {/if}
      </div>

      <div class="dm-card vol-detail-card-pad">
        {#if !selectedFile}
          <EmptyState icon={FileText} title={m.volumes_select_file()} description={m.volumes_select_file_desc()} />
        {:else if fileLoading}
          <Skeleton width="100%" height="8rem" />
        {:else if fileError}
          <div class="vol-detail-error">
            <AlertTriangle size={12} strokeWidth={1.5} /> {fileError}
          </div>
        {:else if fileResult}
          <div class="vol-detail-file-head">
            <div class="vol-detail-file-info">
              <p class="font-mono vol-detail-file-path" title={selectedFile}>{selectedFile}</p>
              <p class="vol-detail-file-meta font-mono">
                {fmtBytes(fileResult.size)}{fileResult.truncated ? ` · ${m.volumes_truncated_preview()}` : ''}{fileResult.binary ? ` · ${m.volumes_binary()}` : ''}
              </p>
            </div>
            <button type="button" class="dm-btn dm-btn-secondary dm-btn-sm" onclick={downloadFile}>
              <Download size={12} strokeWidth={1.5} /> {m.common_download()}
            </button>
          </div>
          {#if fileResult.binary}
            <div class="vol-detail-binary">{m.volumes_binary_notice()}</div>
          {:else}
            <pre class="vol-detail-preview font-mono">{previewText}</pre>
            {#if fileResult.truncated}
              <p class="vol-detail-truncated font-mono">{m.volumes_truncated_notice()}</p>
            {/if}
          {/if}
        {/if}
      </div>
    </div>

    <p class="vol-detail-foot font-mono">
      <strong>{m.volumes_readonly()}</strong>
      {m.volumes_readonly_note()}
    </p>
  {/if}
</section>

<style>
  .vol-detail { display: block; }

  /* ─── Header ─── */
  .vol-detail-header { margin-bottom: 22px; }
  .vol-detail-back-row { margin-bottom: 12px; }
  .vol-detail-back {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 11.5px;
    color: var(--fg-subtle);
    text-decoration: none;
    letter-spacing: 0.04em;
  }
  .vol-detail-back:hover { color: var(--fg); }
  .vol-detail-title-row {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
    gap: 24px;
    flex-wrap: wrap;
  }
  .vol-detail-title-text { min-width: 0; max-width: 70ch; }
  .vol-detail-title { font-size: 26px; }
  .vol-detail-subtitle {
    margin-top: 6px;
    font-size: 12px;
    color: var(--fg-subtle);
  }
  .vol-detail-danger { color: var(--color-danger-400); }

  /* ─── Summary ─── */
  .vol-detail-summary {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: 10px;
    margin: 18px 0 14px;
  }
  @media (max-width: 720px) {
    .vol-detail-summary { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  }
  .vol-detail-tile-text {
    font-size: 14px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .vol-detail-tabs { margin-top: 14px; }

  /* ─── Overview card ─── */
  .vol-detail-card-pad { padding: 22px 22px 24px; margin-top: 18px; }
  .vol-detail-overview { padding: 22px 24px 24px; margin-top: 18px; }
  .vol-detail-dl {
    display: grid;
    grid-template-columns: 140px 1fr;
    gap: 10px 18px;
    font-size: 13px;
    margin: 0;
  }
  .vol-detail-dl dt {
    font-size: 11.5px;
    color: var(--fg-subtle);
    padding-top: 4px;
  }
  .vol-detail-dl dd { margin: 0; color: var(--fg); }
  .vol-detail-pill { font-size: 11.5px; padding: 1px 6px; }
  .vol-detail-mountpoint {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
    word-break: break-all;
  }
  .vol-detail-labels { display: flex; flex-wrap: wrap; gap: 4px; }
  .vol-detail-label-chip {
    display: inline-flex;
    align-items: center;
    gap: 1px;
    padding: 2px 6px;
    border: 1px solid var(--border);
    border-radius: 3px;
    font-size: 11.5px;
    color: var(--fg-muted);
    background: var(--surface);
  }
  .vol-detail-label-k { color: var(--fg-subtle); }
  .vol-detail-label-v { color: var(--fg); }
  .vol-detail-opts {
    margin: 0;
    padding: 10px 12px;
    background: var(--bg-elevated);
    border: 1px solid var(--border);
    border-radius: 4px;
    font-size: 12px;
    overflow: auto;
    color: var(--fg-muted);
  }
  .vol-detail-foot {
    margin-top: 16px;
    font-size: 11.5px;
    color: var(--fg-subtle);
    line-height: 1.6;
  }
  .vol-detail-foot code {
    color: var(--accent-fg);
    background: var(--bg-elevated);
    padding: 0 4px;
    border-radius: 3px;
  }

  /* ─── Containers tab ─── */
  .vol-detail-container-table {
    border: 1px solid var(--border);
    border-radius: 6px;
    margin-top: 18px;
    overflow: hidden;
  }
  .vol-detail-container-row {
    display: grid;
    grid-template-columns: minmax(180px, 1.4fr) 110px minmax(180px, 1.6fr) minmax(140px, 1fr) 80px;
    gap: 14px;
    align-items: center;
    padding: 10px 14px;
    border-bottom: 1px solid var(--border-subtle);
    text-decoration: none;
    color: var(--fg);
  }
  .vol-detail-container-row:last-child { border-bottom: 0; }
  .vol-detail-container-row--head {
    background: var(--bg-elevated);
    color: var(--fg-subtle);
    font-size: 11.5px;
  }
  .vol-detail-container-row--data:hover { background: var(--surface-hover); }
  .vol-detail-container-name {
    font-size: 12.5px;
    color: var(--fg);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .vol-detail-container-image {
    font-size: 11.5px;
    color: var(--fg-muted);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  /* ─── Files tab ─── */
  .vol-detail-files-grid {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
    gap: 14px;
    margin-top: 18px;
  }
  @media (max-width: 900px) {
    .vol-detail-files-grid { grid-template-columns: 1fr; }
  }
  .vol-detail-files-crumb {
    display: flex;
    align-items: center;
    gap: 4px;
    flex-wrap: wrap;
    font-size: 11px;
    color: var(--fg-subtle);
    margin-bottom: 12px;
  }
  .vol-detail-files-crumb-seg {
    background: transparent;
    border: 0;
    padding: 0;
    cursor: pointer;
    color: var(--fg-muted);
    font-size: 11px;
  }
  .vol-detail-files-crumb-seg:hover { color: var(--fg); }
  .vol-detail-files-crumb-sep { color: var(--border-strong); }
  .vol-detail-files-refresh {
    margin-left: auto;
    background: transparent;
    border: 0;
    padding: 4px;
    color: var(--fg-subtle);
    cursor: pointer;
    border-radius: 3px;
  }
  .vol-detail-files-refresh:hover { color: var(--fg); background: var(--surface-hover); }
  .vol-detail-files-list {
    list-style: none;
    margin: 0;
    padding: 0;
    border-top: 1px solid var(--border-subtle);
  }
  .vol-detail-files-list li { border-bottom: 1px solid var(--border-subtle); }
  .vol-detail-files-list li:last-child { border-bottom: 0; }
  .vol-detail-files-row {
    display: grid;
    grid-template-columns: 16px minmax(0, 1fr) 60px 110px;
    gap: 10px;
    align-items: center;
    width: 100%;
    background: transparent;
    border: 0;
    padding: 6px 4px;
    cursor: pointer;
    color: var(--fg);
    text-align: left;
  }
  .vol-detail-files-row:hover { background: var(--surface-hover); }
  .vol-detail-files-row.active { background: color-mix(in srgb, var(--color-brand-500) 8%, transparent); }
  .vol-detail-files-row-name {
    font-size: 12px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .vol-detail-files-row-name.muted { color: var(--fg-subtle); }
  .vol-detail-files-row-size {
    font-size: 11px;
    color: var(--fg-subtle);
    text-align: right;
  }
  .vol-detail-files-row-mtime { font-size: 11.5px; color: var(--fg-subtle); white-space: nowrap; }
  .vol-detail-files-row-link {
    font-size: 11.5px;
    color: var(--fg-subtle);
    overflow: hidden;
    text-overflow: ellipsis;
    grid-column: 3 / span 2;
  }

  .vol-detail-file-head {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 12px;
    margin-bottom: 12px;
  }
  .vol-detail-file-info { min-width: 0; flex: 1; }
  .vol-detail-file-path {
    font-size: 12px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .vol-detail-file-meta {
    font-size: 11.5px;
    color: var(--fg-subtle);
    margin-top: 3px;
  }
  .vol-detail-binary {
    padding: 18px;
    text-align: center;
    font-size: 12px;
    color: var(--fg-subtle);
    background: var(--bg-elevated);
    border: 1px solid var(--border);
    border-radius: 4px;
  }
  .vol-detail-preview {
    margin: 0;
    padding: 12px 14px;
    background: var(--bg-elevated);
    border: 1px solid var(--border);
    border-radius: 4px;
    font-size: 12px;
    line-height: 1.55;
    overflow: auto;
    max-height: 480px;
    white-space: pre-wrap;
    word-break: break-all;
    color: var(--fg-muted);
  }
  .vol-detail-truncated {
    margin-top: 6px;
    font-size: 11.5px;
    color: var(--fg-subtle);
  }
  .vol-detail-error {
    display: flex;
    align-items: center;
    gap: 6px;
    padding: 8px 12px;
    border: 1px solid color-mix(in srgb, var(--color-danger-500) 30%, var(--border));
    background: color-mix(in srgb, var(--color-danger-500) 8%, transparent);
    color: var(--color-danger-400);
    border-radius: 4px;
    font-size: 12px;
  }

  .muted { color: var(--fg-subtle); }
</style>
