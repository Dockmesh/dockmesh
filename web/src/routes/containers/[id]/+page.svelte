<script lang="ts">
  import { page } from '$app/state';
  import { semverBump } from '$lib/version';
  import { goto, replaceState } from '$app/navigation';
  import { api, ApiError } from '$lib/api';
  import { tick } from 'svelte';
  import { Terminal as XTerm } from '@xterm/xterm';
  import { FitAddon } from '@xterm/addon-fit';
  import '@xterm/xterm/css/xterm.css';
  import {
    Skeleton,
    StatusChip,
    Tabs,
    Menu,
    EmptyState
  } from '$lib/components/ui';
  import type { MenuItem } from '$lib/components/ui/Menu.svelte';
  import { EdMetric } from '$lib/components/editorial';
  import ContainerMetrics from '$lib/components/ContainerMetrics.svelte';
  import type { ChartEvent } from '$lib/components/ui/MetricsChart.svelte';
  import {
    Image as FilesIcon,
    Search,
    Folder,
    File as FileIcon,
    Link2,
    Upload,
    ChevronRight,
    Save,
    X as XIcon,
    Pencil
  } from 'lucide-svelte';
  import { toast } from '$lib/stores/toast.svelte';
  import { copyWithToast } from '$lib/clipboard';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { allowed } from '$lib/rbac.svelte';
  import { fmtBytes } from '$lib/format';
  import { hosts, isAllHosts } from '$lib/stores/host.svelte';
  import { pageContext } from '$lib/stores/pageContext.svelte';
  import type { UpdatePreview, UpdateHistoryEntry } from '$lib/api';
  import {
    Play,
    Square,
    RotateCw,
    FileText,
    Terminal as TerminalIcon,
    Trash,
    Download,
    Undo2,
    ExternalLink,
    Package,
    PlayCircle,
    ArrowRight
  } from 'lucide-svelte';

  const id = $derived(page.params.id);
  // Resolve the host: prefer the URL ?host=… (set when the user navigated
  // here from a remote-host listing), otherwise the global selection.
  // "All hosts" is a list view; a single container without ?host= is the
  // server's own.
  const targetHost = $derived.by(() => {
    const h = page.url.searchParams.get('host') || hosts.id;
    return isAllHosts(h) ? 'local' : h;
  });
  const isRemote = $derived(targetHost !== 'local');

  let info = $state<any>(null);

  // Scope-aware perm gates: containers.update / .exec also require the
  // caller's role-scope to cover this container's host + stack (if any).
  const ctxStack = $derived(info?.Config?.Labels?.['com.docker.compose.project'] as string | undefined);
  const ctxService = $derived(info?.Config?.Labels?.['com.docker.compose.service'] as string | undefined);
  const ctxHost = $derived(targetHost);
  // Containers Dockmesh runs for itself (the reverse proxy) are managed
  // from their own page; changing them here would be undone or break it.
  const isSystem = $derived(!!info?.Config?.Labels?.['dockmesh.component']);
  const systemComponent = $derived(info?.Config?.Labels?.['dockmesh.component'] as string | undefined);
  const canControl = $derived(!isSystem && allowed('containers.update'));
  const canExec = $derived(!isSystem && allowed('containers.exec'));
  const canFiles = $derived(!isSystem && (allowed('containers.files') || canExec));
  const canRemove = $derived(!isSystem && allowed('containers.delete'));
  const canUpgrade = $derived(!isSystem && allowed('containers.upgrade'));
  let loading = $state(true);
  type TabKey = 'overview' | 'logs' | 'exec' | 'updates' | 'inspect' | 'network' | 'files';
  const TAB_KEYS: TabKey[] = ['overview', 'logs', 'exec', 'updates', 'inspect', 'network', 'files'];
  // ?tab= deep-links a section (row menus on the list link to logs and the
  // terminal); switching tabs rewrites it without a navigation.
  function tabFromUrl(): TabKey {
    const t = page.url.searchParams.get('tab') as TabKey | null;
    return t && TAB_KEYS.includes(t) ? t : 'overview';
  }
  let tab = $state<TabKey>(tabFromUrl());
  // A terminal opened from a link waits for Attach instead of starting a
  // session (and an audit entry) on page load.
  let autoAttach = tab !== 'exec';
  $effect(() => {
    const want = tab;
    const url = new URL(page.url.href);
    if ((url.searchParams.get('tab') ?? 'overview') === want) return;
    if (want === 'overview') url.searchParams.delete('tab');
    else url.searchParams.set('tab', want);
    replaceState(url, {});
  });

  let envExpanded = $state(false);
  let logFilter = $state<'all' | 'error' | 'warn' | 'info' | 'fatal'>('all');
  let logQuery = $state('');
  let inspectView = $state<'pretty' | 'raw'>('pretty');

  // -------- Files tab state -------------------------------------------
  // Browser through the container filesystem. Tar-stream parsing on
  // the backend means this works even on scratch / distroless images.
  type CtnFileEntry = {
    name: string;
    type: 'file' | 'dir' | 'symlink';
    size: number;
    mode: string;
    mod_time: string;
    link_dest?: string;
  };
  let filesPath = $state<string>('/');
  let filesEntries = $state<CtnFileEntry[]>([]);
  let filesLoading = $state(false);
  let filesError = $state<string | null>(null);
  let filesLoaded = $state(false); // track whether we've fetched at least once
  // Preview pane state — `null` means "nothing selected".
  let filesPreview = $state<{
    path: string;
    name: string;
    content: string;         // decoded UTF-8 text (or "" for binary)
    size: number;
    truncated: boolean;
    binary: boolean;
  } | null>(null);
  let filesPreviewLoading = $state(false);
  let filesEditing = $state(false);
  let filesEditValue = $state('');
  let filesSaving = $state(false);

  // Breadcrumb segments: ['', 'etc', 'nginx'] for "/etc/nginx".
  const filesCrumbs = $derived.by(() => {
    const segs = filesPath.split('/').filter(Boolean);
    const acc: { label: string; path: string }[] = [{ label: '/', path: '/' }];
    let cur = '';
    for (const s of segs) {
      cur += '/' + s;
      acc.push({ label: s, path: cur });
    }
    return acc;
  });

  // Sort: dirs first (alphabetical), then symlinks, then files.
  const filesSorted = $derived.by(() => {
    const rank: Record<string, number> = { dir: 0, symlink: 1, file: 2 };
    return [...filesEntries].sort((a, b) => {
      const r = (rank[a.type] ?? 9) - (rank[b.type] ?? 9);
      if (r !== 0) return r;
      return a.name.localeCompare(b.name);
    });
  });

  function joinPath(base: string, name: string): string {
    if (base === '/' || base === '') return '/' + name;
    return base.replace(/\/$/, '') + '/' + name;
  }
  function parentPath(p: string): string {
    if (p === '/' || p === '') return '/';
    const idx = p.lastIndexOf('/');
    if (idx <= 0) return '/';
    return p.slice(0, idx);
  }

  async function loadFiles(targetPath: string) {
    if (isRemote) return;
    filesLoading = true;
    filesError = null;
    try {
      const entries = await api.containers.files.browse(id, targetPath, targetHost);
      filesEntries = entries as CtnFileEntry[];
      filesPath = targetPath;
    } catch (e) {
      filesError = e instanceof Error ? e.message : String(e);
      filesEntries = [];
    } finally {
      // `filesLoaded` flips true on both success AND error so the auto-
      // load $effect below doesn't loop on an empty directory or a
      // permission denial.
      filesLoaded = true;
      filesLoading = false;
    }
  }

  async function openEntry(entry: CtnFileEntry) {
    if (entry.type === 'dir') {
      await loadFiles(joinPath(filesPath, entry.name));
      filesPreview = null;
      filesEditing = false;
      return;
    }
    if (entry.type === 'symlink') {
      // Resolve symlinks pragmatically: absolute → as-is, relative →
      // joined onto the current dir. We don't stat-follow; if the
      // target is a dir the browse will succeed, otherwise we treat
      // it like a regular file read.
      const dest = entry.link_dest ?? '';
      if (!dest) return;
      const resolved = dest.startsWith('/') ? dest : joinPath(filesPath, dest);
      try {
        await loadFiles(resolved);
        filesPreview = null;
        filesEditing = false;
        return;
      } catch {
        await openFilePreview(resolved, entry.name);
        return;
      }
    }
    await openFilePreview(joinPath(filesPath, entry.name), entry.name);
  }

  async function openFilePreview(fullPath: string, name: string) {
    filesPreviewLoading = true;
    filesEditing = false;
    try {
      const res = await api.containers.files.read(id, fullPath, targetHost);
      // Backend ships content as base64 (Go []byte). Decode to UTF-8
      // only when non-binary; binary preview shows a placeholder.
      let text = '';
      if (!res.binary) {
        try {
          text = decodeURIComponent(escape(atob(res.content || '')));
        } catch {
          text = atob(res.content || '');
        }
      }
      filesPreview = {
        path: fullPath,
        name,
        content: text,
        size: res.size,
        truncated: res.truncated,
        binary: res.binary
      };
      filesEditValue = text;
    } catch (e) {
      filesError = e instanceof Error ? e.message : String(e);
      filesPreview = null;
    } finally {
      filesPreviewLoading = false;
    }
  }

  async function downloadCurrentPreview() {
    if (!filesPreview) return;
    try {
      const blob = await api.containers.files.downloadBlob(id, filesPreview.path, targetHost);
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = filesPreview.name;
      document.body.appendChild(a);
      a.click();
      a.remove();
      setTimeout(() => URL.revokeObjectURL(url), 0);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : 'Download failed');
    }
  }

  async function downloadEntry(entry: CtnFileEntry) {
    const full = joinPath(filesPath, entry.name);
    try {
      const blob = await api.containers.files.downloadBlob(id, full, targetHost);
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = entry.name;
      document.body.appendChild(a);
      a.click();
      a.remove();
      setTimeout(() => URL.revokeObjectURL(url), 0);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : 'Download failed');
    }
  }

  async function saveEdit() {
    if (!filesPreview) return;
    filesSaving = true;
    try {
      // UTF-8 → base64. btoa requires latin-1; convert via TextEncoder.
      const bytes = new TextEncoder().encode(filesEditValue);
      let bin = '';
      for (const b of bytes) bin += String.fromCharCode(b);
      const b64 = btoa(bin);
      await api.containers.files.write(id, filesPreview.path, b64, 0, targetHost);
      filesPreview = {
        ...filesPreview,
        content: filesEditValue,
        size: bytes.length,
        truncated: false
      };
      filesEditing = false;
      toast.success('File saved');
    } catch (e) {
      toast.error(e instanceof Error ? e.message : 'Save failed');
    } finally {
      filesSaving = false;
    }
  }

  async function uploadFile(file: File) {
    const reader = new FileReader();
    reader.onload = async () => {
      const arr = new Uint8Array(reader.result as ArrayBuffer);
      let bin = '';
      for (const b of arr) bin += String.fromCharCode(b);
      const b64 = btoa(bin);
      const dest = joinPath(filesPath, file.name);
      try {
        await api.containers.files.write(id, dest, b64, 0, targetHost);
        toast.success(`Uploaded ${file.name}`);
        await loadFiles(filesPath);
      } catch (e) {
        toast.error(e instanceof Error ? e.message : 'Upload failed');
      }
    };
    reader.readAsArrayBuffer(file);
  }

  function pickAndUpload() {
    const inp = document.createElement('input');
    inp.type = 'file';
    inp.onchange = () => {
      if (inp.files && inp.files[0]) uploadFile(inp.files[0]);
    };
    inp.click();
  }

  function formatRelativeTime(iso: string): string {
    if (!iso) return '';
    const t = new Date(iso).getTime();
    if (!t) return '';
    const diff = Date.now() - t;
    if (diff < 0) return new Date(iso).toLocaleString();
    const sec = Math.floor(diff / 1000);
    if (sec < 60) return `${sec}s ago`;
    const min = Math.floor(sec / 60);
    if (min < 60) return `${min}m ago`;
    const hr = Math.floor(min / 60);
    if (hr < 24) return `${hr}h ago`;
    const days = Math.floor(hr / 24);
    if (days < 30) return `${days}d ago`;
    return new Date(iso).toLocaleDateString();
  }

  // Auto-load the root listing when the Files tab is opened for the
  // first time. Re-load when the targetHost changes — switching hosts
  // would otherwise show stale data. Depends only on `filesLoaded` so
  // an empty directory or a permission error doesn't trigger a loop.
  $effect(() => {
    if (tab !== 'files') return;
    if (!filesLoaded && !filesLoading) {
      loadFiles(filesPath);
    }
  });
  $effect(() => {
    // Reset on host switch — strip out cached state.
    targetHost; // dep
    filesLoaded = false;
    filesEntries = [];
    filesPreview = null;
    filesEditing = false;
    filesPath = '/';
  });

  function logLevelOf(line: string): 'fatal' | 'error' | 'warn' | 'info' | 'debug' | '' {
    const l = line.toLowerCase();
    if (/(fatal|panic)/.test(l)) return 'fatal';
    if (/(\berror\b|level=error|err\s*=)/.test(l)) return 'error';
    if (/(\bwarn\b|level=warn|warning)/.test(l)) return 'warn';
    if (/(\binfo\b|level=info)/.test(l)) return 'info';
    if (/(\bdebug\b|level=debug)/.test(l)) return 'debug';
    return '';
  }
  // Pull a leading ISO timestamp out of a docker log line and shorten
  // it to "HH:MM:SS.mmm" for the dedicated time column. Falls back to
  // empty + the raw line when no timestamp is present.
  function parseLogLine(line: string): { ts: string; body: string } {
    const m = line.match(/^(\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:\.\d+)?Z?)\s+(.*)$/);
    if (!m) return { ts: '', body: line };
    const t = m[1].match(/[T ](\d{2}:\d{2}:\d{2}(?:\.\d{1,3})?)/);
    return { ts: t ? t[1] : m[1], body: m[2] };
  }
  // logLineEntries / logCounts / filteredLogLines are declared further
  // down — see right after `let logs = $state<string[]>([])` to avoid
  // TDZ on the `logs` reference inside the derived expressions.

  // The image tag pill ("v3.2.0", "16-alpine", "latest") — derived from
  // `Config.Image`. If the image has no explicit tag the registry resolved
  // it to `latest`, so default to that.
  const imageTag = $derived.by<string | null>(() => {
    const ref: string | undefined = info?.Config?.Image;
    if (!ref) return null;
    const colon = ref.lastIndexOf(':');
    if (colon < 0 || ref.indexOf('/') > colon) return 'latest';
    return ref.slice(colon + 1) || 'latest';
  });
  // Strip a leading "v" so version arrows render cleanly: v3.2.1 → 3.2.1.
  function trimV(s?: string | null): string {
    return (s ?? '').replace(/^v/i, '');
  }
  // semver-ish bump classification when both sides parse cleanly.
  // updatePreview-dependent deriveds are declared further down, right
  // after `let updatePreview = …`, to avoid TDZ.

  // Updates state
  let updatePreview = $state<UpdatePreview | null>(null);
  let updateHistory = $state<UpdateHistoryEntry[]>([]);
  let previewLoading = $state(false);
  let updateBusy = $state(false);
  // Deriveds that read `updatePreview` — must come after the declaration.
  const newTag = $derived(trimV(updatePreview?.latest_release?.tag));
  const currentTagShort = $derived(trimV(imageTag));
  const versionBump = $derived.by<'major' | 'minor' | 'patch' | null>(() => {
    if (!newTag || !currentTagShort) return null;
    return semverBump(currentTagShort, newTag);
  });
  const updateAvailable = $derived(
    !!updatePreview?.latest_release && !!newTag && newTag !== currentTagShort
  );
  // Size delta in bytes. Positive = remote is larger. null when either side
  // is missing — we don't fake a delta we can't compute.
  const sizeDelta = $derived.by<number | null>(() => {
    const a = updatePreview?.local_size;
    const b = updatePreview?.remote_size;
    if (!a || !b) return null;
    return b - a;
  });
  const fmtSizeDelta = (d: number) => `${d >= 0 ? '+' : '−'}${fmtBytes(Math.abs(d))}`;

  // Logs
  let logs = $state<string[]>([]);
  // Logs derived state — depends on `logs` so must come after it.
  const logLineEntries = $derived(
    logs.map((raw) => {
      const { ts, body } = parseLogLine(raw);
      return { line: body, ts, lvl: logLevelOf(body) };
    })
  );
  const logCounts = $derived.by(() => {
    const c: Record<string, number> = { error: 0, warn: 0, info: 0, fatal: 0, debug: 0 };
    for (const e of logLineEntries) if (e.lvl) c[e.lvl] = (c[e.lvl] ?? 0) + 1;
    return c;
  });
  const filteredLogLines = $derived(
    logLineEntries.filter((e) => {
      if (logFilter !== 'all' && e.lvl !== logFilter) return false;
      if (logQuery && !e.line.toLowerCase().includes(logQuery.toLowerCase())) return false;
      return true;
    })
  );
  let wsConnected = $state(false);
  let autoScroll = $state(true);
  let logContainer: HTMLDivElement | null = $state(null);
  let ws: WebSocket | null = null;

  // Stats
  interface StatsSample {
    cpu_percent: number;
    mem_used: number;
    mem_limit: number;
    mem_unlimited?: boolean;
    mem_percent: number;
    net_rx: number;
    net_tx: number;
    blk_read: number;
    blk_write: number;
    pids_current: number;
  }
  let stats = $state<StatsSample | null>(null);
  let statsHistory = $state<StatsSample[]>([]);
  let statsWs: WebSocket | null = null;

  // Exec
  let execContainer: HTMLDivElement | null = $state(null);
  let execConnected = $state(false);
  let execShell = $state<'sh' | 'bash'>('sh');
  let term: XTerm | null = null;
  let fitAddon: FitAddon | null = null;
  let execWs: WebSocket | null = null;
  let resizeObserver: ResizeObserver | null = null;

  async function loadInfo() {
    loading = true;
    try {
      info = await api.containers.inspect(id, targetHost);
    } catch (err) {
      toast.error('Load failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      loading = false;
    }
  }

  // ---------- Updates ----------
  async function loadUpdateData() {
    previewLoading = true;
    try {
      const [p, h] = await Promise.all([
        api.containers.updateInfo(id).catch(() => null),
        api.containers.updateHistory(id).catch(() => [] as UpdateHistoryEntry[])
      ]);
      updatePreview = p;
      updateHistory = h;
    } finally {
      previewLoading = false;
    }
  }

  async function doUpdate() {
    if (!(await confirm.ask({ title: 'Update container', message: 'Pull the latest image and recreate this container?', body: 'The old image is kept as a rollback snapshot. Recreate preserves mounts and env vars.', confirmLabel: 'Update' }))) return;
    updateBusy = true;
    try {
      const res = await api.containers.doUpdate(id);
      if (!res.updated) {
        toast.info('Already up to date', res.image);
      } else {
        toast.success('Updated', res.image);
        // Container id changed — navigate to the new one.
        goto(`/containers/${res.container_id}`);
        return;
      }
      await loadUpdateData();
    } catch (err) {
      toast.error('Update failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      updateBusy = false;
    }
  }

  async function doRollback(historyId: number) {
    if (!(await confirm.ask({ title: 'Roll back container', message: 'Roll back this container to the previous image version?', body: 'The currently-running image is replaced with the snapshot captured during the last update.', confirmLabel: 'Roll back' }))) return;
    updateBusy = true;
    try {
      const res = await api.containers.rollback(id, historyId);
      toast.success('Rolled back', res.image);
      goto(`/containers/${res.container_id}`);
    } catch (err) {
      toast.error('Rollback failed', err instanceof ApiError ? err.message : undefined);
      updateBusy = false;
    }
  }

  function fmtRelTime(ts?: string | null): string {
    if (!ts) return '—';
    const d = (Date.now() - new Date(ts).getTime()) / 1000;
    if (d < 60) return 'just now';
    if (d < 3600) return `${Math.floor(d / 60)}m ago`;
    if (d < 86400) return `${Math.floor(d / 3600)}h ago`;
    if (d < 2592000) return `${Math.floor(d / 86400)}d ago`;
    return new Date(ts).toLocaleDateString();
  }

  function shortDigest(d?: string): string {
    if (!d) return '—';
    const m = d.match(/sha256:([a-f0-9]{12})/);
    return m ? m[0] : d.slice(0, 19);
  }

  // ---------- Logs ----------
  // Auto-reconnect state. `userClosed` distinguishes an intentional
  // disconnect (leaving the tab, clicking Disconnect) from a server-side
  // close (dockmesh restart, agent reconnect). Only the latter triggers
  // the backoff retry — otherwise leaving the page would kick off a
  // reconnect loop against a torn-down component.
  let logsUserClosed = false;
  // Bumped on every disconnect so a socket whose ticket arrives after the
  // tab was left (or the page destroyed) is never opened.
  let logsGen = 0;
  let statsGen = 0;
  let execGen = 0;
  let logsReconnectTimer: ReturnType<typeof setTimeout> | null = null;
  let logsReconnectAttempt = 0;
  let statsUserClosed = false;
  let statsReconnectTimer: ReturnType<typeof setTimeout> | null = null;
  let statsReconnectAttempt = 0;

  // Exponential backoff capped at 30s so a long outage doesn't spin
  // the browser, but a brief dockmesh restart reconnects in ~1s.
  function backoffMs(attempt: number): number {
    return Math.min(30_000, 500 * 2 ** Math.min(attempt, 6));
  }

  async function connectLogs() {
    disconnectLogs();
    logs = [];
    logsUserClosed = false;
    logsReconnectAttempt = 0;
    await openLogsSocket();
  }

  async function openLogsSocket() {
    const gen = logsGen;
    try {
      const { ticket } = await api.ws.ticket('containers.logs');
      if (gen !== logsGen) return;
      const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
      const hostQs = isRemote ? `&host=${encodeURIComponent(targetHost)}` : '';
      const sock = new WebSocket(`${proto}//${location.host}/api/v1/ws/logs/${id}?ticket=${ticket}&tail=200${hostQs}`);
      ws = sock;
      ws.onopen = () => {
        wsConnected = true;
        logsReconnectAttempt = 0;
      };
      ws.onmessage = async (ev) => {
        logs = [...logs, ev.data as string];
        if (logs.length > 5000) logs = logs.slice(-5000);
        if (autoScroll) {
          await tick();
          if (logContainer) logContainer.scrollTop = logContainer.scrollHeight;
        }
      };
      ws.onclose = () => {
        if (ws !== sock) return;
        wsConnected = false;
        scheduleLogsReconnect();
      };
      ws.onerror = () => {
        wsConnected = false;
      };
    } catch (err) {
      if (gen !== logsGen) return;
      if (logsReconnectAttempt === 0) {
        toast.error('Logs connect failed', err instanceof ApiError ? err.message : undefined);
      }
      scheduleLogsReconnect();
    }
  }

  function scheduleLogsReconnect() {
    if (logsUserClosed || tab !== 'logs') return;
    if (logsReconnectTimer) return;
    const delay = backoffMs(logsReconnectAttempt++);
    logsReconnectTimer = setTimeout(() => {
      logsReconnectTimer = null;
      if (!logsUserClosed && tab === 'logs') openLogsSocket();
    }, delay);
  }

  function disconnectLogs() {
    logsGen++;
    logsUserClosed = true;
    if (logsReconnectTimer) { clearTimeout(logsReconnectTimer); logsReconnectTimer = null; }
    if (ws) { ws.close(); ws = null; }
    wsConnected = false;
  }

  // ---------- Stats ----------
  async function connectStats() {
    disconnectStats();
    statsHistory = [];
    statsUserClosed = false;
    statsReconnectAttempt = 0;
    await openStatsSocket();
  }

  async function openStatsSocket() {
    const gen = statsGen;
    try {
      const { ticket } = await api.ws.ticket('containers.view');
      if (gen !== statsGen) return;
      const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
      const hostQs = isRemote ? `&host=${encodeURIComponent(targetHost)}` : '';
      const sock = new WebSocket(`${proto}//${location.host}/api/v1/ws/stats/${id}?ticket=${ticket}${hostQs}`);
      statsWs = sock;
      statsWs.onopen = () => {
        statsReconnectAttempt = 0;
      };
      statsWs.onmessage = (ev) => {
        try {
          const s = JSON.parse(ev.data);
          if (s.error) return;
          stats = s;
          statsHistory = [...statsHistory, s].slice(-60);
        } catch { /* ignore */ }
      };
      statsWs.onclose = () => {
        if (statsWs !== sock) return;
        scheduleStatsReconnect();
      };
    } catch {
      if (gen === statsGen) scheduleStatsReconnect();
    }
  }

  function scheduleStatsReconnect() {
    if (statsUserClosed || tab !== 'overview') return;
    if (statsReconnectTimer) return;
    const delay = backoffMs(statsReconnectAttempt++);
    statsReconnectTimer = setTimeout(() => {
      statsReconnectTimer = null;
      if (!statsUserClosed && tab === 'overview') openStatsSocket();
    }, delay);
  }

  function disconnectStats() {
    statsGen++;
    statsUserClosed = true;
    if (statsReconnectTimer) { clearTimeout(statsReconnectTimer); statsReconnectTimer = null; }
    if (statsWs) { statsWs.close(); statsWs = null; }
  }

  // ---------- Exec ----------
  async function connectExec() {
    disconnectExec();
    if (!execContainer) return;
    term = new XTerm({
      fontFamily: '"JetBrains Mono Variable", JetBrains Mono, Menlo, Consolas, monospace',
      fontSize: 13,
      lineHeight: 1.3,
      cursorBlink: true,
      theme: {
        background: '#0a0e1a',
        foreground: '#e7ecf5',
        cursor: '#06b6d4',
        selectionBackground: 'rgba(6, 182, 212, 0.35)',
        black: '#1f2940',
        red: '#ef4444',
        green: '#22c55e',
        yellow: '#eab308',
        blue: '#3b82f6',
        magenta: '#a855f7',
        cyan: '#06b6d4',
        white: '#e7ecf5'
      }
    });
    fitAddon = new FitAddon();
    term.loadAddon(fitAddon);
    term.open(execContainer);
    fitAddon.fit();

    const gen = execGen;
    try {
      const { ticket } = await api.ws.ticket('containers.exec');
      if (gen !== execGen || !term) return;
      const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
      const cmd = execShell === 'bash' ? '/bin/bash' : '/bin/sh';
      const hostQs = isRemote ? `&host=${encodeURIComponent(targetHost)}` : '';
      execWs = new WebSocket(`${proto}//${location.host}/api/v1/ws/exec/${id}?ticket=${ticket}&cmd=${encodeURIComponent(cmd)}${hostQs}`);
      execWs.binaryType = 'arraybuffer';

      execWs.onopen = () => {
        execConnected = true;
        const { cols, rows } = term!;
        execWs!.send(JSON.stringify({ type: 'resize', cols, rows }));
      };
      execWs.onmessage = (ev) => {
        if (typeof ev.data === 'string') {
          term!.write(`\r\n\x1b[31m${ev.data}\x1b[0m\r\n`);
        } else {
          term!.write(new Uint8Array(ev.data));
        }
      };
      execWs.onclose = () => {
        execConnected = false;
        term?.write('\r\n\x1b[33m[session closed]\x1b[0m\r\n');
      };

      term.onData((data) => {
        if (execWs?.readyState === WebSocket.OPEN) {
          execWs.send(new TextEncoder().encode(data));
        }
      });
      term.onResize(({ cols, rows }) => {
        if (execWs?.readyState === WebSocket.OPEN) {
          execWs.send(JSON.stringify({ type: 'resize', cols, rows }));
        }
      });

      resizeObserver = new ResizeObserver(() => {
        try { fitAddon?.fit(); } catch { /* ignore */ }
      });
      resizeObserver.observe(execContainer);
    } catch (err) {
      if (gen !== execGen) return;
      toast.error('Exec failed', err instanceof ApiError ? err.message : undefined);
    }
  }
  function disconnectExec() {
    execGen++;
    resizeObserver?.disconnect();
    resizeObserver = null;
    if (execWs) { execWs.close(); execWs = null; }
    if (term) { term.dispose(); term = null; }
    fitAddon = null;
    execConnected = false;
  }

  // ---------- Actions ----------
  async function action(op: 'start' | 'stop' | 'restart' | 'pause' | 'unpause') {
    try {
      if (op === 'start') await api.containers.start(id, targetHost);
      else if (op === 'stop') await api.containers.stop(id, targetHost);
      else if (op === 'restart') await api.containers.restart(id, targetHost);
      else if (op === 'pause') await api.containers.pause(id, targetHost);
      else if (op === 'unpause') await api.containers.unpause(id, targetHost);
      toast.success(op);
      await loadInfo();
      if (tab === 'logs') connectLogs();
    } catch (err) {
      toast.error(`${op} failed`, err instanceof ApiError ? err.message : undefined);
    }
  }

  // Kill is separate because it's destructive + carries a signal. Default
  // signal "" lets Docker pick SIGKILL.
  async function killContainer() {
    if (!(await confirm.ask({ title: 'Kill container', message: 'Send SIGKILL to this container?', body: 'In-flight writes may be lost. The container exits immediately without a graceful shutdown.', confirmLabel: 'Kill', danger: true }))) return;
    try {
      await api.containers.kill(id, '', targetHost);
      toast.success('Killed', 'SIGKILL sent');
      await loadInfo();
    } catch (err) {
      toast.error('Kill failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function remove() {
    if (!(await confirm.ask({ title: 'Remove container', message: 'Remove this container?', body: 'Volumes are kept. Image stays available for redeploy.', confirmLabel: 'Remove', danger: true }))) return;
    try {
      await api.containers.remove(id, true, targetHost);
      toast.success('Removed');
      goto('/containers');
    } catch (err) {
      toast.error('Remove failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  $effect(() => {
    if (id) loadInfo();
  });

  $effect(() => {
    if (tab === 'overview') {
      disconnectLogs();
      disconnectExec();
      connectStats();
      if (!isRemote) {
        api.containers.updateHistory(id).then((h) => (updateHistory = h)).catch(() => {});
      }
    } else if (tab === 'logs') {
      disconnectExec();
      disconnectStats();
      connectLogs();
    } else if (tab === 'exec') {
      disconnectLogs();
      disconnectStats();
      if (autoAttach) tick().then(connectExec);
      autoAttach = true;
    } else if (tab === 'updates') {
      disconnectLogs();
      disconnectExec();
      disconnectStats();
      loadUpdateData();
    } else {
      disconnectLogs();
      disconnectExec();
      disconnectStats();
    }
  });

  $effect(() => () => {
    disconnectLogs();
    disconnectExec();
    disconnectStats();
  });

  $effect(() => {
    if (info) {
      const svc = info.Config?.Labels?.['com.docker.compose.service'];
      const proj = info.Config?.Labels?.['com.docker.compose.project'];
      const fromStack = page.url.searchParams.get('from') === 'stack';
      const name = svc || containerName(info) || id.slice(0, 12);
      if (fromStack && proj) {
        pageContext.set(name, [
          { label: 'Stacks', href: '/stacks' },
          { label: proj, href: `/stacks/${encodeURIComponent(proj)}` },
        ]);
      } else {
        pageContext.set(name);
      }
    }
    return () => pageContext.clear();
  });

  // Helpers
  function containerName(inf: any): string {
    return (inf?.Name ?? '').replace(/^\//, '');
  }

  // JSON syntax highlighting for Inspect tab
  function highlightJSON(obj: any): string {
    const raw = JSON.stringify(obj, null, 2);
    return raw
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
      .replace(/"([^"]+)":/g, '<span class="json-key">"$1"</span>:')
      .replace(/: "([^"]*)"/g, ': <span class="json-string">"$1"</span>')
      .replace(/: (\d+\.?\d*)/g, ': <span class="json-number">$1</span>')
      .replace(/: (true|false)/g, ': <span class="json-bool">$1</span>')
      .replace(/: (null)/g, ': <span class="json-null">$1</span>');
  }

  // Log line colorization
  function colorizeLog(line: string): string {
    const escaped = line.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
    // Timestamp prefix (ISO or common formats)
    let result = escaped.replace(
      /^(\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}[.\d]*Z?)/,
      '<span class="log-ts">$1</span>'
    );
    // Log levels
    result = result
      .replace(/\b(ERROR|FATAL|CRIT(?:ICAL)?|PANIC)\b/gi, '<span class="log-error">$&</span>')
      .replace(/\b(WARN(?:ING)?)\b/gi, '<span class="log-warn">$&</span>')
      .replace(/\b(INFO)\b/gi, '<span class="log-info">$&</span>')
      .replace(/\b(DEBUG|TRACE)\b/gi, '<span class="log-debug">$&</span>');
    // HTTP status codes
    result = result
      .replace(/\b([45]\d{2})\b/g, '<span class="log-error">$1</span>')
      .replace(/\b([23]\d{2})\b/g, '<span class="log-ok">$1</span>');
    return result;
  }

  const statusTone = $derived.by<{ tone: 'ok' | 'warn' | 'bad' | 'off'; label: string }>(() => {
    const st = info?.State;
    if (!st) return { tone: 'off', label: 'unknown' };
    if (st.Paused) return { tone: 'warn', label: 'paused' };
    if (st.Running) {
      if (st.Health?.Status === 'unhealthy') return { tone: 'warn', label: 'unhealthy' };
      if (st.Restarting) return { tone: 'warn', label: 'restarting' };
      return { tone: 'ok', label: 'running' };
    }
    if (st.Status === 'dead' || (st.ExitCode ?? 0) !== 0) return { tone: 'bad', label: st.Status === 'dead' ? 'dead' : `exited ${st.ExitCode}` };
    return { tone: 'off', label: st.Status ?? 'stopped' };
  });

  const hostName = $derived(
    hosts.available.find((h) => h.id === targetHost)?.name ?? (targetHost === 'local' ? 'the server' : targetHost)
  );

  const chartEvents = $derived.by<ChartEvent[]>(() => {
    const out: ChartEvent[] = [];
    const started = info?.State?.StartedAt ? Date.parse(info.State.StartedAt) : NaN;
    if (isFinite(started)) out.push({ ts: started, label: 'container started', color: 'var(--warn)' });
    for (const e of updateHistory) {
      const t = Date.parse(e.applied_at);
      if (isFinite(t)) out.push({ ts: t, label: `image updated · ${e.image_ref}` });
      const rb = e.rolled_back_at ? Date.parse(e.rolled_back_at) : NaN;
      if (isFinite(rb)) out.push({ ts: rb, label: `rolled back · ${e.image_ref}`, color: 'var(--warn)' });
    }
    return out;
  });

  const liveSample = $derived(
    stats
      ? {
          cpu_percent: stats.cpu_percent,
          mem_used: stats.mem_used,
          mem_limit: stats.mem_limit,
          mem_unlimited: stats.mem_unlimited,
          net_rx: stats.net_rx,
          net_tx: stats.net_tx,
          blk_read: stats.blk_read,
          blk_write: stats.blk_write
        }
      : null
  );

  const headerMenu = $derived.by<MenuItem[]>(() => {
    if (!info) return [];
    const items: MenuItem[] = [];
    if (canControl && info.State?.Running && !info.State?.Paused) items.push({ label: 'Pause', onclick: () => action('pause') });
    if (canControl && (info.State?.Running || info.State?.Paused)) items.push({ label: 'Kill', onclick: killContainer });
    items.push({ label: 'Copy ID', onclick: () => void copyWithToast(info.Id ?? id, 'Copied container ID') });
    if (ctxStack) items.push({ label: `Open stack ${ctxStack}`, href: `/stacks/${encodeURIComponent(ctxStack)}` });
    if (canRemove) items.push({ separator: true }, { label: 'Remove…', danger: true, onclick: remove });
    return items;
  });

  $effect(() => {
    if (info && !tabItems.some((t) => t.key === tab)) tab = 'overview';
  });

  const tabItems = $derived([
    { key: 'overview', label: 'Overview' },
    { key: 'logs', label: 'Logs', count: wsConnected ? 'live' : null },
    ...(canExec ? [{ key: 'exec', label: 'Terminal', count: execConnected ? 'attached' : null }] : []),
    { key: 'inspect', label: 'Inspect' },
    {
      key: 'network',
      label: 'Network',
      count: info && Object.keys(info.NetworkSettings?.Networks ?? {}).length > 0 ? Object.keys(info.NetworkSettings?.Networks ?? {}).length : null
    },
    ...(canFiles ? [{ key: 'files', label: 'Files' }] : []),
    ...(canUpgrade && !isRemote ? [{ key: 'updates', label: 'Updates', count: updateHistory.length || null }] : [])
  ]);
</script>

<section class="dm-page ctn-frame">
  {#if loading}
    <div class="ctn-skeleton">
      <Skeleton width="40%" height="2rem" />
      <Skeleton width="70%" height="1rem" />
    </div>
  {:else if info}

    <header class="dm-ph ctn-ph">
      <h1>{ctxService || containerName(info) || id.slice(0, 12)}</h1>
      <StatusChip tone={statusTone.tone} label={statusTone.label} title={info.State?.Status} />
      {#if isSystem}<StatusChip tone="acc" label="System" title="Managed by Dockmesh" />{/if}
      <span class="dm-sub dm-mono ctn-ph-sub">
        {imageTag ? `${imageTag} · ` : ''}{info.State?.Running ? `up ${fmtRelTime(info.State?.StartedAt).replace(' ago', '')}` : 'not running'}{(info.RestartCount ?? 0) > 0 ? ` · ${info.RestartCount} restart${info.RestartCount === 1 ? '' : 's'}` : ''} · {hostName} · {id.slice(0, 12)}
      </span>
      <span class="dm-sp"></span>
      {#if canControl}
        {#if info.State?.Paused}
          <button type="button" class="dm-btn dm-btn-primary" onclick={() => action('unpause')}><PlayCircle size={14} /> Resume</button>
        {:else if info.State?.Running}
          <button type="button" class="dm-btn dm-btn-primary" onclick={() => action('restart')}><RotateCw size={14} /> Restart</button>
          <button type="button" class="dm-btn" onclick={() => action('stop')}><Square size={13} /> Stop</button>
        {:else}
          <button type="button" class="dm-btn dm-btn-primary" onclick={() => action('start')}><Play size={14} /> Start</button>
        {/if}
      {/if}
      <Menu label="More actions" items={headerMenu} />
    </header>
  {/if}

  {#if info && isSystem}
    <div class="ctn-remote">
      {#if systemComponent === 'proxy'}
        Dockmesh runs this container as its reverse proxy and keeps it in sync with your sites. Change it under
        <a href="/proxy">Reverse proxy</a>.
      {:else}
        Dockmesh runs this container for itself; it is shown read-only.
      {/if}
    </div>
  {/if}

  {#if isRemote}
    <div class="ctn-remote">
      <span class="ctn-remote-bullet">·</span>
      Remote host — Logs / Stats / Terminal / Inspect stream via the agent. Image updates stay
      local-only on the central daemon.
    </div>
  {/if}

  <Tabs label="Container sections" tabs={tabItems} active={tab} onchange={(k) => (tab = k as TabKey)} />

  <!-- Tab panels -->
  {#if tab === 'overview' && info}
    {@const envLines = (info.Config?.Env ?? []) as string[]}
    {@const portsMap = (info.NetworkSettings?.Ports ?? {}) as Record<string, Array<{HostIp: string; HostPort: string}> | null>}
    {@const portRows = Object.entries(portsMap).map(([k, v]) => ({
      port: k,
      bindings: (v ?? []) as Array<{HostIp: string; HostPort: string}>,
    }))}
    {@const networks = Object.entries(info.NetworkSettings?.Networks ?? {}) as Array<[string, any]>}
    {@const mounts = (info.Mounts ?? []) as any[]}
    {@const health = info.State?.Health}
    {@const cmdParts = [...(info.Config?.Entrypoint ?? []), ...(info.Config?.Cmd ?? [])]}
    {@const restartCount = info.RestartCount ?? info.State?.RestartCount ?? 0}

    <ContainerMetrics
      {id}
      host={targetHost}
      {hostName}
      running={!!info.State?.Running}
      latest={liveSample}
      events={chartEvents}
    />

    <div class="ctn-overview">
      <div class="ctn-overview-main">
        <!-- Identity -->
        <section class="ctn-block">
          <h3 class="ctn-h3">Identity</h3>
          <dl class="ctn-kv">
            <dt>ID</dt>
            <dd>{id.slice(0, 12)}</dd>

            <dt>Image</dt>
            <dd>{info.Config?.Image}</dd>

            {#if cmdParts.length > 0}
              <dt>Command</dt>
              <dd>{JSON.stringify(cmdParts)}</dd>
            {/if}

            {#if envLines.length > 0}
              <dt>Environment</dt>
              <dd class="ctn-kv-env">
                {#each (envExpanded ? envLines : envLines.slice(0, 3)) as e (e)}
                  {@const idx = e.indexOf('=')}
                  {@const k = idx >= 0 ? e.slice(0, idx) : e}
                  {@const v = idx >= 0 ? e.slice(idx + 1) : ''}
                  {@const isSec = /password|secret|token|api[_-]?key|credential/i.test(k)}
                  <span class="ctn-kv-env-pair">
                    <span class="ctn-kv-env-key">{k}</span>=<span class:secret={isSec}>{isSec ? '••••••' : (v.length > 50 ? v.slice(0, 50) + '…' : v)}</span>
                  </span>
                {/each}
                {#if envLines.length > 3}
                  <button
                    type="button"
                    class="ctn-kv-env-toggle"
                    onclick={() => (envExpanded = !envExpanded)}
                  >
                    {envExpanded
                      ? `hide ${envLines.length - 3}`
                      : `show ${envLines.length - 3} more`}
                  </button>
                {/if}
              </dd>
            {/if}

            {#if portRows.length > 0}
              <dt>Ports</dt>
              <dd>
                {#each portRows as p, i (p.port)}{#if i > 0} · {/if}{p.port}{/each}
              </dd>
            {/if}

            <dt>Created</dt>
            <dd>{info.Created?.slice(0, 19).replace('T', ' ')} · {fmtRelTime(info.Created)}</dd>

            {#if info.HostConfig?.RestartPolicy?.Name}
              <dt>Restart</dt>
              <dd>{info.HostConfig.RestartPolicy.Name}</dd>
            {/if}

            {#if info.Config?.User}
              <dt>User</dt>
              <dd>{info.Config.User}</dd>
            {/if}
          </dl>
        </section>

        <!-- Health — single bordered container wrapping the row(s).
             Probe meta-line shows the actual healthcheck command + interval
             ("CMD curl http://… · every 5s"), not a raw timestamp. Docker
             only exposes one healthcheck per container; we render exactly
             that one row, status-coloured. -->
        <section class="ctn-block">
          <h3 class="ctn-h3">Health</h3>
          {#if !health || health.Status === 'none'}
            <p class="ctn-empty-line">No <code>healthcheck</code> configured. Status comes from the container's running/stopped state only.</p>
          {:else}
            {@const probeTest = info.Config?.Healthcheck?.Test}
            {@const probeCmd = Array.isArray(probeTest) && probeTest.length > 1
              ? `${probeTest[0]} ${probeTest.slice(1).join(' ')}`.slice(0, 80)
              : 'CMD'}
            {@const probeIntervalNs = info.Config?.Healthcheck?.Interval ?? 0}
            {@const probeIntervalSec = Math.max(1, Math.round(probeIntervalNs / 1_000_000_000))}
            {@const probeMeta = `${probeCmd} · every ${probeIntervalSec}s`}
            <div class="ctn-health-box">
              {#if health.Status === 'unhealthy'}
                <div class="ed-row" data-status="failing" style="grid-template-columns: 6px 1fr auto auto">
                  <span class="stripe"></span>
                  <div class="ctn-health-namecell">
                    <span class="ctn-health-name">Healthcheck</span>
                    <div class="ctn-health-detail">{probeMeta}</div>
                  </div>
                  <div class="ctn-health-status">
                    <span class="dm-pill dm-pill-danger ctn-health-pill"><span class="dm-pill-dot"></span>exit {health.Log?.[0]?.ExitCode ?? '?'}</span>
                    {#if health.FailingStreak > 0}
                      <span class="ctn-health-streak">{health.FailingStreak} consecutive failures</span>
                    {/if}
                  </div>
                  <span class="ctn-health-when">since {fmtRelTime(health.Log?.[0]?.Start)}</span>
                </div>
                {#if health.Log?.[0]?.Output}
                  <pre class="ctn-health-output">{health.Log[0].Output.slice(0, 400)}</pre>
                {/if}
              {:else if health.Status === 'starting'}
                <div class="ed-row" data-status="degraded" style="grid-template-columns: 6px 1fr auto auto">
                  <span class="stripe"></span>
                  <div class="ctn-health-namecell">
                    <span class="ctn-health-name">Healthcheck</span>
                    <div class="ctn-health-detail">{probeMeta}</div>
                  </div>
                  <div class="ctn-health-status">
                    <span class="dm-pill dm-pill-warning ctn-health-pill"><span class="dm-pill-dot"></span>starting</span>
                  </div>
                  <span class="ctn-health-when">{fmtRelTime(info.State?.StartedAt)}</span>
                </div>
              {:else}
                <div class="ed-row" data-status="running" style="grid-template-columns: 6px 1fr auto auto">
                  <span class="stripe"></span>
                  <div class="ctn-health-namecell">
                    <span class="ctn-health-name">Healthcheck</span>
                    <div class="ctn-health-detail">{probeMeta}</div>
                  </div>
                  <div class="ctn-health-status">
                    <span class="dm-pill dm-pill-success ctn-health-pill"><span class="dm-pill-dot"></span>healthy</span>
                  </div>
                  <span class="ctn-health-when">last ok {fmtRelTime(health.Log?.[0]?.End)}</span>
                </div>
              {/if}
            </div>
          {/if}
        </section>

        <!-- Restart history (summarized — Docker doesn't expose individual
             restart events, so we show count + last-start + last-exit). -->
        <section class="ctn-block">
          <h3 class="ctn-h3">Restart history · {restartCount} restart{restartCount === 1 ? '' : 's'}</h3>
          {#if restartCount === 0 && info.State?.Running}
            <p class="ctn-empty-line">No restarts since this container was created. Running cleanly since <strong>{fmtRelTime(info.State?.StartedAt)}</strong>.</p>
          {:else}
            <ol class="ctn-restart-list">
              {#if info.State?.StartedAt}
                <li class="ed-feed-item">
                  <span class="ed-feed-time">{fmtRelTime(info.State.StartedAt)}</span>
                  <span class="ed-feed-text">
                    <strong>last start</strong> · {info.State?.Running ? 'currently running' : 'not running'}
                  </span>
                  <span class="ed-feed-actor">
                    {#if info.State?.ExitCode !== undefined && info.State.ExitCode !== 0}
                      exit {info.State.ExitCode}
                    {:else if !info.State?.Running}
                      stopped
                    {:else}
                      live
                    {/if}
                  </span>
                </li>
              {/if}
              {#if info.State?.FinishedAt && info.State.FinishedAt !== '0001-01-01T00:00:00Z'}
                <li class="ed-feed-item">
                  <span class="ed-feed-time">{fmtRelTime(info.State.FinishedAt)}</span>
                  <span class="ed-feed-text">
                    <strong>last finish</strong> · {info.State?.Error || 'clean exit'}
                  </span>
                  <span class="ed-feed-actor">
                    exit {info.State?.ExitCode ?? 0}
                  </span>
                </li>
              {/if}
            </ol>
            <p class="ctn-restart-note">
              Per-restart details (reason, signal, OOM kill) aren't surfaced by Docker — the count above is the lifetime restart count for this container id.
            </p>
          {/if}
        </section>
      </div>

      <aside class="ctn-overview-rail">
        <section class="dm-card ctn-rail-card">
          <h3 class="ctn-h3">Mounts · {mounts.length}</h3>
          {#if mounts.length === 0}
            <p class="ctn-empty-line">No volumes or bind mounts.</p>
          {:else}
            <ul class="ctn-mounts">
              {#each mounts as m (m.Destination)}
                <li>
                  <span class="ctn-mount-name">{m.Name || m.Source}</span>
                  <span class="ctn-mount-arrow">→</span>
                  <span class="ctn-mount-dest">{m.Destination}</span>
                  {#if m.RW === false}<span class="dm-pill dm-pill-neutral ctn-mount-ro">ro</span>{/if}
                </li>
              {/each}
            </ul>
          {/if}
        </section>

        <section class="dm-card ctn-rail-card">
          <h3 class="ctn-h3">Networks · {networks.length}</h3>
          {#if networks.length === 0}
            <p class="ctn-empty-line">Not attached to any user network.</p>
          {:else}
            <ul class="ctn-networks">
              {#each networks as [netName, cfg] (netName)}
                <li>
                  <span class="ctn-network-name">{netName}</span>
                  {#if cfg?.IPAddress}
                    <span class="ctn-network-sep">·</span>
                    <span class="ctn-network-ip">{cfg.IPAddress}</span>
                  {/if}
                </li>
              {/each}
            </ul>
          {/if}
        </section>

        {#if ctxStack}
          <section class="dm-card-flat ctn-rail-card">
            <h3 class="ctn-h3">Stack context</h3>
            <p class="ctn-stack-blurb">
              Service <em class="ed-accent">{ctxService || '—'}</em> of stack
              <em class="ed-accent">{ctxStack}</em>.
            </p>
            <a href={`/stacks/${encodeURIComponent(ctxStack)}`} class="dm-btn dm-btn-secondary dm-btn-sm ctn-stack-link">
              ↑ Open {ctxStack}
            </a>
          </section>
        {/if}

        <section class="dm-card ctn-rail-card">
          <h3 class="ctn-h3">Endpoints</h3>
          {#if portRows.length === 0}
            <p class="ctn-empty-line">No exposed ports.</p>
          {:else}
            <ul class="ctn-endpoints">
              {#each portRows as p (p.port)}
                {#if p.bindings.length > 0}
                  {#each p.bindings as b (b.HostIp + b.HostPort)}
                    <li>
                      <span class="ctn-endpoint-port">:{b.HostPort} → {p.port}</span>
                      <span class="ctn-endpoint-mode-host">Host</span>
                      <ExternalLink size={11} strokeWidth={1.5} class="ctn-endpoint-icon" />
                    </li>
                  {/each}
                {:else}
                  <li>
                    <span class="ctn-endpoint-port">{p.port}</span>
                    <span class="ctn-endpoint-mode">Internal</span>
                  </li>
                {/if}
              {/each}
            </ul>
          {/if}
        </section>
      </aside>
    </div>
  {:else if tab === 'logs'}
    <div class="ctn-tab-pane">
      <div class="ctn-logs-bar">
        <div class="ctn-log-filters">
          {#each [
            ['all', 'All', logs.length],
            ['error', 'Error', logCounts.error],
            ['warn', 'Warn', logCounts.warn],
            ['info', 'Info', logCounts.info],
            ['fatal', 'Fatal', logCounts.fatal],
          ] as [id, label, n] (id)}
            <button
              type="button"
              class="ctn-log-filter"
              class:active={logFilter === id}
              onclick={() => (logFilter = id as any)}
            >{label}<span class="ctn-log-filter-count">{n}</span></button>
          {/each}
        </div>

        <div class="ctn-log-actions">
          <div class="ctn-log-grep">
            <Search size={12} strokeWidth={1.5} class="ctn-log-grep-icon" />
            <input
              type="text"
              placeholder="grep…"
              bind:value={logQuery}
              class="ctn-log-grep-input"
            />
          </div>
          <button
            type="button"
            class="ctn-log-follow"
            class:on={wsConnected}
            onclick={() => { wsConnected ? disconnectLogs() : connectLogs(); }}
          >
            <span class="ctn-log-follow-dot"></span>
            {wsConnected ? 'following' : 'paused'}
          </button>
          <button
            type="button"
            class="dm-btn dm-btn-ghost dm-btn-xs"
            onclick={() => (logs = [])}
          >
            <Trash size={11} strokeWidth={1.5} /> Clear
          </button>
        </div>
      </div>

      <div class="ctn-term ctn-logs-viewer" bind:this={logContainer}>
        {#each filteredLogLines as e, i (i)}
          <div class="ctn-log-row" data-lvl={e.lvl}>
            <span class="ctn-log-ts">{e.ts}</span>
            <span class="ctn-log-lvl ctn-log-lvl--{e.lvl || 'plain'}">{e.lvl || '·'}</span>
            <span class="ctn-log-msg">{@html colorizeLog(e.line)}</span>
          </div>
        {/each}
        {#if filteredLogLines.length === 0 && logs.length > 0}
          <div class="ctn-logs-empty"><em>no lines match this filter.</em></div>
        {:else if logs.length === 0 && wsConnected}
          <div class="ctn-logs-empty"><em>waiting for first line…</em></div>
        {:else if logs.length === 0}
          <div class="ctn-logs-empty"><em>not streaming — click "paused" to reconnect.</em></div>
        {/if}
        {#if wsConnected && logs.length > 0}
          <div class="ctn-log-row" style="opacity: 0.55">
            <span class="ctn-log-ts"></span>
            <span class="ctn-log-lvl ctn-log-lvl--info">···</span>
            <span class="ctn-log-msg">waiting for next line<span class="ed-cursor"></span></span>
          </div>
        {/if}
      </div>

      <div class="ctn-log-foot">
        <span>
          {filteredLogLines.length} of {logs.length} lines · stream
          <em class="ed-accent">{containerName(info) || id.slice(0, 12)}</em>
        </span>
        <span>Tail · since boot</span>
      </div>
    </div>
  {:else if tab === 'exec' && canExec}
    <div class="ctn-tab-pane">
      <div class="ctn-term-bar">
        <div class="ctn-term-shell-pills">
          <span class="ctn-term-shell-label">Shell:</span>
          {#each ['sh', 'bash'] as const as s (s)}
            <button
              type="button"
              class="ctn-shell-pill"
              class:active={execShell === s}
              disabled={execConnected}
              onclick={() => (execShell = s)}
            >{s}</button>
          {/each}
        </div>
        <div class="ctn-term-actions">
          {#if !execConnected}
            <button
              type="button"
              class="dm-btn dm-btn-primary dm-btn-sm"
              onclick={connectExec}
            >
              <TerminalIcon size={12} strokeWidth={1.5} /> Attach to /bin/{execShell}
            </button>
          {:else}
            <span class="dm-pill dm-pill-success ctn-meta-pill">
              <span class="dm-pill-dot"></span>attached
            </span>
            <button
              type="button"
              class="dm-btn dm-btn-ghost dm-btn-sm"
              onclick={disconnectExec}
            >Detach</button>
          {/if}
        </div>
      </div>
      <div bind:this={execContainer} class="ctn-term-shell"></div>
      <div class="ctn-term-foot">
        {#if execConnected}
          connected to <em class="ed-accent">/bin/{execShell}</em> · try
          <code>ls /etc</code>, <code>ps</code>, <code>env</code>
        {:else}
          not attached · Attach uses <em class="ed-accent">docker exec</em> against the running container
        {/if}
      </div>
    </div>
  {:else if tab === 'updates' && canUpgrade && !isRemote}
    <div class="ctn-tab-pane ctn-updates-grid">
      <div class="ctn-updates-main">
        {#if previewLoading && !updatePreview}
          <div class="dm-card ctn-updates-card">
            <Skeleton width="40%" height="1.25rem" />
            <Skeleton width="70%" height="0.85rem" />
          </div>
        {:else if updatePreview}
          {#if updateAvailable}
            <!-- Update available banner — accent border, version diff,
                 release meta sentence, primary action. -->
            <div class="dm-card ctn-updates-banner">
              <div class="ctn-updates-banner-head">
                <div class="ctn-updates-banner-text">
                  <div class="ctn-updates-banner-eyebrow">
                    <h3 class="ctn-h3">Update available</h3>
                  </div>
                  <div class="ctn-updates-vdiff">
                    <span class="ctn-updates-vdiff-old">{currentTagShort || imageTag}</span>
                    <ArrowRight size={14} strokeWidth={1.5} class="ctn-updates-vdiff-arrow" />
                    <span class="ctn-updates-vdiff-new">{newTag}</span>
                    {#if versionBump}
                      <span
                        class="dm-pill ctn-meta-pill"
                        class:dm-pill-success={versionBump === 'patch'}
                        class:dm-pill-neutral={versionBump === 'minor'}
                        class:dm-pill-warning={versionBump === 'major'}
                      >{versionBump}</span>
                    {/if}
                  </div>
                  <p class="ctn-updates-banner-meta">
                    {#if updatePreview.remote_last_updated}
                      Released <span class="ctn-updates-rel">{fmtRelTime(updatePreview.remote_last_updated)}</span>{#if updatePreview.remote_size} · <span class="ctn-updates-rel">{fmtBytes(updatePreview.remote_size, '—')}</span>{/if}.
                    {/if}
                    {#if updatePreview.latest_release?.name && updatePreview.latest_release.name !== updatePreview.latest_release.tag}
                      {updatePreview.latest_release.name}.
                    {/if}
                  </p>
                </div>
                <div class="ctn-updates-banner-actions">
                  <button
                    type="button"
                    class="dm-btn dm-btn-primary dm-btn-sm"
                    onclick={doUpdate}
                    disabled={updateBusy}
                  >
                    <Download size={12} strokeWidth={1.5} />
                    {updateBusy ? 'Pulling…' : 'Pull & restart'}
                  </button>
                  {#if updatePreview.latest_release?.url}
                    <a
                      href={updatePreview.latest_release.url}
                      target="_blank"
                      rel="noopener"
                      class="dm-btn dm-btn-secondary dm-btn-sm"
                    >
                      <ExternalLink size={11} strokeWidth={1.5} /> Release
                    </a>
                  {/if}
                </div>
              </div>
              {#if updatePreview.warnings && updatePreview.warnings.length > 0}
                <div class="ctn-updates-warnings">
                  {updatePreview.warnings.join(' · ')}
                </div>
              {/if}
            </div>
          {:else}
            <!-- Up to date — no version diff, just a calm reassurance card. -->
            <div class="dm-card ctn-updates-card">
              <div class="ctn-updates-head">
                <div class="ctn-updates-image">
                  <span class="ctn-updates-icon-wrap">
                    <Package size={18} strokeWidth={1.5} />
                  </span>
                  <div>
                    <h3 class="ctn-h3">Up to date</h3>
                    <div class="ctn-updates-image-ref">{updatePreview.image}</div>
                    <div class="ctn-updates-image-meta">
                      <div>local: {shortDigest(updatePreview.current_digest)} · built {fmtRelTime(updatePreview.current_created)}</div>
                      {#if updatePreview.remote_last_updated}
                        <div>remote: pushed {fmtRelTime(updatePreview.remote_last_updated)} · {fmtBytes(updatePreview.remote_size, '—')}</div>
                      {/if}
                    </div>
                  </div>
                </div>
                <button
                  type="button"
                  class="dm-btn dm-btn-secondary dm-btn-sm"
                  onclick={doUpdate}
                  disabled={updateBusy}
                >
                  <Download size={12} strokeWidth={1.5} />
                  {updateBusy ? 'Pulling…' : 'Re-pull image'}
                </button>
              </div>
            </div>
          {/if}

          {#if updatePreview.latest_release?.body}
            <!-- Release bodies are often prose, so they are shown verbatim. -->
            <section class="ctn-block">
              <div class="ctn-updates-release-head">
                <div class="ctn-updates-release-title">
                  <h3 class="ctn-h3">
                    Release notes{#if updatePreview.latest_release.tag} · {updatePreview.latest_release.tag}{/if}
                  </h3>
                  {#if updatePreview.latest_release.name && updatePreview.latest_release.name !== updatePreview.latest_release.tag}
                    <h3>{updatePreview.latest_release.name}</h3>
                  {/if}
                </div>
                {#if updatePreview.latest_release.url}
                  <a
                    href={updatePreview.latest_release.url}
                    target="_blank"
                    rel="noopener"
                    class="ctn-inline-link"
                  >
                    Open on GitHub <ExternalLink size={11} strokeWidth={1.5} />
                  </a>
                {/if}
              </div>
              <pre class="ctn-updates-release-body">{updatePreview.latest_release.body}</pre>
            </section>
          {/if}

          {#if updatePreview.local_size || updatePreview.remote_size || updatePreview.remote_last_updated || updatePreview.current_digest}
            <section class="ctn-block">
              <h3 class="ctn-h3">
                {#if updateAvailable && currentTagShort && newTag}
                  Image diff · {updatePreview.image.split(':')[0]}:{currentTagShort} → {newTag}
                {:else}
                  Image · {updatePreview.image}
                {/if}
              </h3>
              <div class="ctn-updates-imgdiff">
                {#if sizeDelta !== null}
                  <div class="ed-metric ctn-updates-metric">
                    <span class="ed-metric-label">Size delta</span>
                    <div
                      class="ed-metric-value"
                      class:ctn-updates-delta-down={sizeDelta < 0}
                      class:ctn-updates-delta-up={sizeDelta > 0}
                    >{fmtSizeDelta(sizeDelta)}</div>
                    <span class="ed-metric-meta">from {fmtBytes(updatePreview.local_size, '—')}</span>
                  </div>
                {:else if updatePreview.remote_size}
                  <div class="ed-metric ctn-updates-metric">
                    <span class="ed-metric-label">Remote size</span>
                    <div class="ed-metric-value">{fmtBytes(updatePreview.remote_size, '—')}</div>
                    <span class="ed-metric-meta">On registry</span>
                  </div>
                {/if}
                {#if updatePreview.remote_last_updated}
                  <div class="ed-metric ctn-updates-metric">
                    <span class="ed-metric-label">Pushed</span>
                    <div class="ed-metric-value">{fmtRelTime(updatePreview.remote_last_updated)}</div>
                    <span class="ed-metric-meta">{newTag || updatePreview.latest_release?.tag || 'latest tag'}</span>
                  </div>
                {/if}
                {#if updatePreview.current_digest}
                  <div class="ed-metric ctn-updates-metric">
                    <span class="ed-metric-label">Local digest</span>
                    <div class="ed-metric-value mono">{shortDigest(updatePreview.current_digest)}</div>
                    <span class="ed-metric-meta">built {fmtRelTime(updatePreview.current_created)}</span>
                  </div>
                {/if}
              </div>
              {#if updatePreview.docker_hub_url || updatePreview.github_url}
                <div class="ctn-updates-links">
                  {#if updatePreview.docker_hub_url}
                    <a href={updatePreview.docker_hub_url} target="_blank" rel="noopener">
                      Docker Hub <ExternalLink size={10} strokeWidth={1.5} />
                    </a>
                  {/if}
                  {#if updatePreview.github_url}
                    <a href={updatePreview.github_url} target="_blank" rel="noopener">
                      GitHub <ExternalLink size={10} strokeWidth={1.5} />
                    </a>
                  {/if}
                </div>
              {/if}
            </section>
          {/if}
        {/if}
      </div>

      <aside class="ctn-updates-rail">
        <div class="dm-card-flat ctn-updates-rail-card">
          <h3 class="ctn-h3">Version history</h3>
          <ul class="ctn-updates-versions">
            <li>
              <span class="ctn-updates-version-tag current">{currentTagShort || imageTag || '—'}</span>
              <span class="ctn-updates-version-meta">
                current{#if info?.State?.StartedAt} · {fmtRelTime(info.State.StartedAt)}{/if}
              </span>
            </li>
            {#each updateHistory.slice(0, 6) as e (e.id)}
              {@const tag = (e.image_ref.includes(':') ? e.image_ref.split(':').pop() : e.image_ref) || ''}
              <li>
                <span class="ctn-updates-version-tag">{trimV(tag) || shortDigest(e.old_digest)}</span>
                <span class="ctn-updates-version-meta">
                  {fmtRelTime(e.applied_at)}{#if e.rolled_back_at} · rolled back{/if}
                </span>
              </li>
            {/each}
          </ul>
          {#if updateHistory.length === 0}
            <p class="ctn-updates-rail-empty">No previous versions yet.</p>
          {/if}
        </div>

        {#if updateHistory.length > 0}
          <div class="dm-card-flat ctn-updates-rail-card">
            <h3 class="ctn-h3">Rollback timeline</h3>
            <ol class="ctn-updates-history">
              <span class="ctn-updates-history-rule" aria-hidden="true"></span>
              {#each updateHistory as e, i (e.id)}
                <li class="ctn-updates-history-row">
                  <span class="ctn-updates-history-dot" class:current={i === 0} aria-hidden="true"></span>
                  <div class="ctn-updates-history-body">
                    <div class="ctn-updates-history-head">
                      <span class="ctn-updates-history-ref">{e.image_ref}</span>
                      {#if e.rolled_back_at}
                        <span class="dm-pill dm-pill-warning ctn-meta-pill"><span class="dm-pill-dot"></span>rolled back</span>
                      {:else}
                        <button
                          type="button"
                          class="dm-btn dm-btn-ghost dm-btn-xs"
                          onclick={() => doRollback(e.id)}
                          disabled={updateBusy}
                          title="Roll back to this image"
                        >
                          <Undo2 size={11} strokeWidth={1.5} /> Rollback
                        </button>
                      {/if}
                    </div>
                    <span class="ctn-updates-history-meta">
                      {shortDigest(e.old_digest)} → {shortDigest(e.new_digest)} · {fmtRelTime(e.applied_at)}
                    </span>
                  </div>
                </li>
              {/each}
            </ol>
          </div>
        {/if}
      </aside>
    </div>
  {:else if tab === 'inspect'}
    <div class="ctn-tab-pane">
      <div class="ctn-inspect-bar">
        <h3 class="ctn-h3">docker inspect · <em class="ed-accent">{containerName(info) || id.slice(0, 12)}</em></h3>
        <div class="ctn-inspect-actions">
          {#each ['pretty', 'raw'] as v (v)}
            <button
              type="button"
              class="ctn-inspect-toggle"
              class:active={inspectView === v}
              onclick={() => (inspectView = v as any)}
            >{v}</button>
          {/each}
          <button
            type="button"
            class="dm-btn dm-btn-ghost dm-btn-xs"
            onclick={async () => {
              void copyWithToast(JSON.stringify(info, null, 2), 'Copied JSON');
            }}
          >
            <FileText size={11} strokeWidth={1.5} /> Copy
          </button>
        </div>
      </div>
      {#if inspectView === 'raw'}
        <pre class="ctn-term ctn-inspect-pre">{JSON.stringify(info, null, 2)}</pre>
      {:else}
        <pre class="ctn-term ctn-inspect-pre json-view">{@html highlightJSON(info)}</pre>
      {/if}
    </div>
  {:else if tab === 'network' && info}
    {@const netEntries = Object.entries(info.NetworkSettings?.Networks ?? {}) as Array<[string, any]>}
    {@const portsMap2 = (info.NetworkSettings?.Ports ?? {}) as Record<string, Array<{HostIp: string; HostPort: string}> | null>}
    {@const portRows2 = Object.entries(portsMap2).map(([k, v]) => ({ port: k, bindings: (v ?? []) as Array<{HostIp: string; HostPort: string}> }))}
    <div class="ctn-network-pane">
      <!-- LEFT: Connected networks -->
      <div>
        <h3 class="ctn-h3">Connected networks</h3>
        {#if netEntries.length === 0}
          <div class="ctn-network-empty">
            <p>Not attached to any user network. Default bridge or host networking.</p>
          </div>
        {:else}
          <div class="ctn-network-list">
            {#each netEntries as [netName, cfg] (netName)}
              <div class="ed-row" data-status="running" style="grid-template-columns: 6px 1fr auto">
                <span class="stripe"></span>
                <div class="ctn-network-id">
                  <div class="ctn-network-name">{netName}</div>
                  <div class="ctn-network-meta">
                    {cfg?.NetworkID ? (cfg.NetworkID as string).slice(0, 12) : 'bridge'}
                    {#if cfg?.IPPrefixLen}
                      <span class="ctn-meta-sep">·</span>{cfg.IPAddress}/{cfg.IPPrefixLen}
                    {/if}
                  </div>
                </div>
                <div class="ctn-network-addr">
                  {#if cfg?.IPAddress}<div>{cfg.IPAddress}</div>{/if}
                  {#if cfg?.MacAddress}<div class="ctn-network-mac">{cfg.MacAddress}</div>{/if}
                </div>
              </div>
            {/each}
          </div>
        {/if}
        <p class="ctn-network-note">
          Container is on {netEntries.length === 0 ? 'no user' : netEntries.length === 1 ? 'a single' : `${netEntries.length}`} network{netEntries.length === 1 ? '' : 's'}. Attach more via the Networks page or via <code>compose.yaml</code>.
        </p>
      </div>

      <!-- RIGHT: Port bindings -->
      <div>
        <h3 class="ctn-h3">Port bindings · {portRows2.length}</h3>
        {#if portRows2.length === 0}
          <div class="ctn-network-empty">
            <p>No ports declared in the image or compose.</p>
          </div>
        {:else}
          <div class="ctn-network-list">
            {#each portRows2 as p (p.port)}
              {#if p.bindings.length > 0}
                {#each p.bindings as b, j (b.HostIp + b.HostPort + j)}
                  <div class="ctn-port-row">
                    <span class="ctn-port-name">{p.port}</span>
                    <span class="dm-pill dm-pill-success ctn-meta-pill">{b.HostIp || '0.0.0.0'}:{b.HostPort}</span>
                    <span class="ctn-port-note">host-mapped</span>
                  </div>
                {/each}
              {:else}
                <div class="ctn-port-row">
                  <span class="ctn-port-name">{p.port}</span>
                  <span class="dm-pill dm-pill-neutral ctn-meta-pill">Internal</span>
                  <span class="ctn-port-note">No host binding</span>
                </div>
              {/if}
            {/each}
          </div>
        {/if}
        <p class="ctn-network-note">
          Internal ports are reachable from other containers on the same network only.
        </p>
      </div>

      <!-- BOTTOM: Throughput row, full-width -->
      {#if stats}
        <div class="ctn-network-throughput">
          <h3 class="ctn-h3">Throughput · last 60s</h3>
          <div class="ctn-throughput-grid">
            <EdMetric
              label="rx total"
              value={fmtBytes(stats.net_rx)}
              meta="since start"
            />
            <EdMetric
              label="tx total"
              value={fmtBytes(stats.net_tx)}
              meta="since start"
            />
            <EdMetric
              label="rx rate"
              value={statsHistory.length > 1
                ? `${fmtBytes(Math.max(0, stats.net_rx - statsHistory[0].net_rx) / Math.max(1, statsHistory.length))}/s`
                : '—'}
              meta="avg over buffer"
            />
            <EdMetric
              label="tx rate"
              value={statsHistory.length > 1
                ? `${fmtBytes(Math.max(0, stats.net_tx - statsHistory[0].net_tx) / Math.max(1, statsHistory.length))}/s`
                : '—'}
              meta="avg over buffer"
            />
          </div>
        </div>
      {/if}
    </div>
  {:else if tab === 'files' && info && canFiles && isRemote}
    <EmptyState title="Files" description="Browsing files is available for containers on the server host; agent hosts follow in a later release." />
  {:else if tab === 'files' && info && canFiles}
    <div class="ctn-files-pane">
      <div class="ctn-files-bar">
        <nav class="ctn-files-crumbs" aria-label="Path">
          {#each filesCrumbs as crumb, i (crumb.path)}
            {#if i > 0}
              <ChevronRight size={12} strokeWidth={1.5} class="ctn-files-crumb-sep" />
            {/if}
            <button
              type="button"
              class="ctn-files-crumb"
              class:active={crumb.path === filesPath}
              onclick={() => loadFiles(crumb.path)}
            >{crumb.label}</button>
          {/each}
        </nav>
        <div class="ctn-files-actions">
          <button
            type="button"
            class="ctn-files-action"
            onclick={pickAndUpload}
            disabled={!canExec || isRemote}
            title={isRemote ? 'Container files browse on remote hosts not yet implemented' : (!canExec ? 'Upload requires containers.exec' : 'Upload a file into this directory')}
          >
            <Upload size={13} strokeWidth={1.6} />
            <span>Upload</span>
          </button>
        </div>
      </div>

      {#if filesError}
        <div class="ctn-files-error">
          <p>{filesError}</p>
          <button type="button" class="ctn-files-action" onclick={() => loadFiles(filesPath)}>Retry</button>
        </div>
      {/if}

      <div class="ctn-files-table">
        <div class="ctn-files-head">
          <span>Name</span>
          <span class="ctn-files-cell-right">Size</span>
          <span>Mode</span>
          <span>Modified</span>
          <span></span>
        </div>
        {#if filesLoading && !filesEntries.length}
          <div class="ctn-files-pending">
            <p>Loading {filesPath}…</p>
          </div>
        {:else if !filesEntries.length}
          <div class="ctn-files-pending">
            <FilesIcon size={18} strokeWidth={1.5} class="ctn-files-icon" />
            <p>Empty directory.</p>
          </div>
        {:else}
          {#if filesPath !== '/'}
            <div
              role="button"
              tabindex="0"
              class="ctn-files-row"
              onclick={() => loadFiles(parentPath(filesPath))}
              onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); loadFiles(parentPath(filesPath)); } }}
            >
              <span class="ctn-files-name">
                <Folder size={14} strokeWidth={1.6} class="ctn-files-row-icon" />
                <span>..</span>
              </span>
              <span class="ctn-files-cell-right">—</span>
              <span class="ctn-files-mode">—</span>
              <span class="ctn-files-modified">Parent</span>
              <span></span>
            </div>
          {/if}
          {#each filesSorted as entry (entry.name)}
            <div
              role="button"
              tabindex="0"
              class="ctn-files-row"
              class:active={filesPreview?.path === joinPath(filesPath, entry.name)}
              onclick={() => openEntry(entry)}
              onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); openEntry(entry); } }}
            >
              <span class="ctn-files-name">
                {#if entry.type === 'dir'}
                  <Folder size={14} strokeWidth={1.6} class="ctn-files-row-icon" />
                {:else if entry.type === 'symlink'}
                  <Link2 size={14} strokeWidth={1.6} class="ctn-files-row-icon" />
                {:else}
                  <FileIcon size={14} strokeWidth={1.6} class="ctn-files-row-icon" />
                {/if}
                <span class="ctn-files-name-text">{entry.name}</span>
                {#if entry.type === 'symlink' && entry.link_dest}
                  <span class="ctn-files-link-dest">→ {entry.link_dest}</span>
                {/if}
              </span>
              <span class="ctn-files-cell-right">{entry.type === 'dir' ? '—' : fmtBytes(entry.size)}</span>
              <span class="ctn-files-mode">{entry.mode}</span>
              <span class="ctn-files-modified">{formatRelativeTime(entry.mod_time)}</span>
              <span class="ctn-files-row-actions">
                {#if entry.type === 'file'}
                  <button
                    type="button"
                    class="ctn-files-row-action"
                    onclick={(e) => { e.stopPropagation(); downloadEntry(entry); }}
                    title="Download"
                  >
                    <Download size={13} strokeWidth={1.6} />
                  </button>
                {/if}
              </span>
            </div>
          {/each}
        {/if}
      </div>

      {#if filesPreview}
        <div class="ctn-files-preview">
          <div class="ctn-files-preview-bar">
            <div class="ctn-files-preview-meta">
              <code class="ctn-files-preview-path">{filesPreview.path}</code>
              <span class="ctn-files-preview-size">
                {fmtBytes(filesPreview.size)}
                {#if filesPreview.truncated}
                  · truncated at 1 MiB
                {/if}
                {#if filesPreview.binary}
                  · binary
                {/if}
              </span>
            </div>
            <div class="ctn-files-preview-actions">
              {#if filesEditing}
                <button
                  type="button"
                  class="ctn-files-action"
                  onclick={() => { filesEditing = false; filesEditValue = filesPreview?.content ?? ''; }}
                  disabled={filesSaving}
                >
                  <XIcon size={13} strokeWidth={1.6} />
                  <span>Cancel</span>
                </button>
                <button
                  type="button"
                  class="ctn-files-action ctn-files-action-primary"
                  onclick={saveEdit}
                  disabled={filesSaving}
                >
                  <Save size={13} strokeWidth={1.6} />
                  <span>{filesSaving ? 'Saving…' : 'Save'}</span>
                </button>
              {:else}
                <button
                  type="button"
                  class="ctn-files-action"
                  onclick={downloadCurrentPreview}
                >
                  <Download size={13} strokeWidth={1.6} />
                  <span>Download</span>
                </button>
                {#if !filesPreview.binary && !filesPreview.truncated}
                  <button
                    type="button"
                    class="ctn-files-action"
                    onclick={() => { filesEditValue = filesPreview?.content ?? ''; filesEditing = true; }}
                    disabled={!canExec || isRemote}
                    title={isRemote ? 'Editing on remote hosts not yet implemented' : (!canExec ? 'Edit requires containers.exec' : 'Edit this file')}
                  >
                    <Pencil size={13} strokeWidth={1.6} />
                    <span>Edit</span>
                  </button>
                {/if}
              {/if}
              <button
                type="button"
                class="ctn-files-action"
                onclick={() => { filesPreview = null; filesEditing = false; }}
              >
                <XIcon size={13} strokeWidth={1.6} />
              </button>
            </div>
          </div>
          {#if filesPreviewLoading}
            <div class="ctn-files-preview-body ctn-files-preview-loading">Loading…</div>
          {:else if filesPreview.binary}
            <div class="ctn-files-preview-body ctn-files-preview-binary">
              Binary file — preview disabled. Use Download to fetch the raw bytes.
            </div>
          {:else if filesEditing}
            <textarea
              class="ctn-files-preview-edit"
              bind:value={filesEditValue}
              spellcheck="false"
            ></textarea>
          {:else}
            <pre class="ctn-files-preview-body"><code>{filesPreview.content}</code></pre>
          {/if}
        </div>
      {/if}
    </div>
  {/if}
</section>

<style>
  .ctn-frame { gap: 18px; }
  .ctn-ph h1 { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 40ch; }
  .ctn-ph-sub { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; min-width: 0; flex: 0 1 auto; }
  .ctn-h3 { margin: 0; font-size: var(--fs-sm); font-weight: 600; color: var(--subtle); }
  .ctn-h3 :global(em) { color: var(--fg); }
  .ctn-skeleton { display: flex; flex-direction: column; gap: 10px; }

  .ctn-meta-sep { color: var(--border-strong); }

  .ctn-meta-pill { font-size: 11.5px; }

  .ctn-remote {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    padding: 8px 14px;
    border: 1px solid color-mix(in srgb, var(--color-brand-500) 30%, var(--border));
    background: color-mix(in srgb, var(--color-brand-500) 5%, transparent);
    border-radius: 5px;
    color: var(--fg-muted);
    font-size: 12.5px;
    line-height: 1.55;
  }
  .ctn-remote-bullet { color: var(--color-brand-400); font-weight: 700; }

  .ctn-overview {
    display: grid;
    grid-template-columns: minmax(0, 1fr) 320px;
    gap: 32px;
    margin-top: 14px;
  }
  @media (max-width: 1100px) {
    .ctn-overview { grid-template-columns: 1fr; gap: 28px; }
  }
  .ctn-overview-main { display: flex; flex-direction: column; gap: 36px; min-width: 0; }
  .ctn-overview-rail { display: flex; flex-direction: column; gap: 18px; min-width: 0; }
  .ctn-block { display: flex; flex-direction: column; gap: 12px; min-width: 0; }
  .ctn-rail-card { display: flex; flex-direction: column; gap: 10px; padding: 0 0 14px; border: 0; border-bottom: 1px solid var(--border); border-radius: 0; background: transparent; }
  .ctn-empty-line {
    margin: 0;
    color: var(--fg-subtle);
    font-size: 12.5px;
    line-height: 1.55;
  }
  .ctn-empty-line code { font-family: var(--font-mono); font-size: 11.5px; color: var(--fg-muted); }

  /* ─────────── Identity KV ─────────── */
  .ctn-kv {
    margin: 0;
    display: grid;
    grid-template-columns: 90px 1fr;
    column-gap: 14px;
    row-gap: 8px;
    align-items: baseline;
    font-family: var(--font-mono);
    font-size: 12.5px;
  }
  .ctn-kv dt {
    margin: 0;
    color: var(--fg-subtle);
    font-size: 11.5px;
    letter-spacing: 0.06em;
    text-transform: lowercase;
  }
  .ctn-kv dd {
    margin: 0;
    color: var(--fg);
    word-break: break-all;
  }
  .ctn-kv-env {
    display: flex;
    flex-wrap: wrap;
    gap: 4px 10px;
  }
  .ctn-kv-env-pair { color: var(--fg-muted); }
  .ctn-kv-env-pair .secret { color: var(--fg-subtle); font-style: normal; }

  /* ─────────── Health ──────────
     Bordered container wraps the row(s) — even a single row gets the
     border so the section reads as a discrete card-without-fill. */
  .ctn-health-box {
    border: 1px solid var(--border);
    border-radius: 6px;
    overflow: hidden;
  }
  .ctn-health-namecell { display: flex; flex-direction: column; gap: 2px; }
  .ctn-health-name {
    font-size: 12px;
    color: var(--fg);
    font-weight: 500;
  }
  .ctn-health-detail {
    font-size: 11.5px;
    color: var(--fg-subtle);
    letter-spacing: 0.04em;
  }
  .ctn-health-status { display: flex; align-items: baseline; gap: 10px; font-size: 12.5px; color: var(--fg-muted); }
  .ctn-health-pill { font-size: 11.5px; }
  .ctn-health-streak { font-size: 11.5px; }
  .ctn-health-when {
    font-size: 11.5px;
    color: var(--fg-subtle);
  }
  .ctn-health-output {
    margin: 0;
    padding: 10px 14px;
    border-top: 1px solid var(--border);
    background: var(--bg-elevated);
    color: var(--fg-muted);
    font-family: var(--font-mono);
    font-size: 11px;
    line-height: 1.5;
    white-space: pre-wrap;
    max-height: 140px;
    overflow: auto;
  }

  /* Identity env show-more button */
  .ctn-kv-env-key { color: var(--fg); }
  .ctn-kv-env-toggle {
    background: transparent;
    border: 0;
    padding: 0;
    color: var(--accent-fg);
    font-family: var(--font-mono);
    font-size: 11px;
    letter-spacing: 0.04em;
    cursor: pointer;
    margin-left: 4px;
  }
  .ctn-kv-env-toggle:hover { color: var(--fg); }

  /* ─────────── Restart history ─────────── */
  .ctn-restart-list {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .ctn-restart-note {
    margin: 8px 0 0;
    font-size: 11.5px;
    color: var(--fg-subtle);
    line-height: 1.55;
    letter-spacing: 0.02em;
  }

  /* ─────────── Right-rail blocks ─────────── */
  .ctn-mounts {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .ctn-mounts li {
    display: flex;
    align-items: baseline;
    flex-wrap: wrap;
    gap: 6px;
    font-family: var(--font-mono);
    font-size: 11.5px;
    line-height: 1.5;
  }
  .ctn-mount-name { color: var(--accent-fg); word-break: break-all; }
  .ctn-mount-arrow { color: var(--fg-subtle); }
  .ctn-mount-dest { color: var(--fg-muted); word-break: break-all; }
  .ctn-mount-ro { font-size: 11.5px; padding: 0 4px; }

  .ctn-networks {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .ctn-networks li {
    font-family: var(--font-mono);
    font-size: 11.5px;
    line-height: 1.5;
  }
  .ctn-network-name { color: var(--fg); }
  .ctn-network-sep { color: var(--border-strong); margin: 0 6px; }
  .ctn-network-ip { color: var(--fg-subtle); }

  .ctn-stack-blurb {
    margin: 0;
    font-size: 12.5px;
    color: var(--fg-muted);
    line-height: 1.55;
  }
  .ctn-stack-link { width: 100%; text-decoration: none; }

  .ctn-endpoints {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
  }
  .ctn-endpoints li {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    padding: 8px 0;
    border-bottom: 1px solid var(--border-subtle);
    font-family: var(--font-mono);
    font-size: 11.5px;
  }
  .ctn-endpoints li:last-child { border-bottom: 0; }
  .ctn-endpoint-port { color: var(--fg); }
  .ctn-endpoint-mode-host {
    font-size: 11.5px;
    padding: 1px 5px;
    border-radius: 3px;
    border: 1px solid color-mix(in srgb, var(--color-success-500) 40%, var(--border));
    color: var(--color-success-400);
  }
  .ctn-endpoint-mode {
    font-size: 11.5px;
    color: var(--fg-subtle);
    text-transform: lowercase;
    letter-spacing: 0.04em;
  }
  :global(.ctn-endpoint-icon) { color: var(--fg-subtle); flex-shrink: 0; }

  /* ─────────── Network tab — 2-column with bottom throughput row ─────────── */
  .ctn-network-pane {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 24px;
    margin-top: 14px;
  }
  @media (max-width: 1100px) {
    .ctn-network-pane { grid-template-columns: 1fr; }
  }
  .ctn-network-list {
    margin-top: 12px;
    border: 1px solid var(--border);
    border-radius: 6px;
    overflow: hidden;
  }
  .ctn-network-id { display: flex; flex-direction: column; gap: 4px; min-width: 0; }
  .ctn-network-name {
    font-family: var(--font-mono);
    font-size: 13px;
    color: var(--fg);
  }
  .ctn-network-meta {
    font-size: 11.5px;
    color: var(--fg-subtle);
    letter-spacing: 0.04em;
  }
  .ctn-network-addr {
    text-align: right;
    font-family: var(--font-mono);
    font-size: 12px;
    color: var(--fg);
  }
  .ctn-network-mac {
    color: var(--fg-subtle);
    font-size: 11.5px;
    margin-top: 2px;
  }
  .ctn-network-note {
    margin: 8px 0 0;
    font-size: 11.5px;
    color: var(--fg-subtle);
    line-height: 1.55;
  }
  .ctn-network-note code {
    color: var(--fg-muted);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 3px;
    padding: 0 4px;
  }
  .ctn-port-row {
    display: grid;
    grid-template-columns: 1fr auto auto;
    gap: 12px;
    padding: 12px 14px;
    align-items: center;
    border-bottom: 1px solid var(--border);
  }
  .ctn-port-row:last-child { border-bottom: 0; }
  .ctn-port-name { font-family: var(--font-mono); font-size: 12.5px; color: var(--fg); }
  .ctn-port-note {
    font-size: 11.5px;
    color: var(--fg-subtle);
    letter-spacing: 0.04em;
  }
  .ctn-network-throughput {
    grid-column: 1 / -1;
    margin-top: 8px;
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .ctn-throughput-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
    gap: 12px;
  }

  .ctn-inline-link {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--accent-fg);
    letter-spacing: 0.04em;
    text-decoration: none;
  }
  .ctn-inline-link:hover { color: var(--fg); }

  .ctn-network-empty {
    padding: 32px 28px;
    border: 1px dashed var(--border-strong);
    border-radius: 6px;
    color: var(--fg-muted);
    font-size: 13px;
    line-height: 1.6;
    text-align: center;
  }
  .ctn-network-empty p { margin: 0; }

  .ctn-files-pane {
    display: flex;
    flex-direction: column;
    gap: 14px;
    margin-top: 14px;
  }
  .ctn-files-bar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 16px;
    flex-wrap: wrap;
  }
  .ctn-files-crumbs {
    display: flex;
    align-items: center;
    gap: 4px;
    flex-wrap: wrap;
    font-family: var(--font-mono);
    font-size: 12px;
  }
  .ctn-files-crumb {
    background: transparent;
    border: 0;
    padding: 4px 6px;
    border-radius: 4px;
    cursor: pointer;
    color: var(--fg-muted);
    font: inherit;
  }
  .ctn-files-crumb:hover { color: var(--fg); background: var(--surface); }
  .ctn-files-crumb.active { color: var(--fg); }
  :global(.ctn-files-crumb-sep) { color: var(--fg-subtle); }

  .ctn-files-actions { display: flex; gap: 6px; }
  .ctn-files-action {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 5px 10px;
    background: transparent;
    border: 1px solid var(--border);
    border-radius: 4px;
    color: var(--fg);
    font-size: 12px;
    cursor: pointer;
  }
  .ctn-files-action:hover:not(:disabled) { background: var(--surface); }
  .ctn-files-action:disabled { opacity: 0.5; cursor: not-allowed; }
  .ctn-files-action-primary {
    background: var(--fg);
    color: var(--bg);
    border-color: var(--fg);
  }

  .ctn-files-error {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 10px 14px;
    border: 1px solid var(--border);
    border-left: 3px solid #c0392b;
    border-radius: 4px;
    background: var(--surface);
  }
  .ctn-files-error p { margin: 0; font-size: 12.5px; color: var(--fg); }

  .ctn-files-table {
    border: 1px solid var(--border);
    border-radius: 6px;
    overflow: hidden;
  }
  .ctn-files-head,
  .ctn-files-row {
    display: grid;
    grid-template-columns: minmax(0, 1fr) 100px 110px 140px 60px;
    align-items: center;
    padding: 8px 14px;
    gap: 12px;
  }
  .ctn-files-head {
    background: var(--surface);
    border-bottom: 1px solid var(--border);
    font-size: 11.5px;
    color: var(--fg-subtle);
  }
  .ctn-files-row {
    width: 100%;
    text-align: left;
    background: transparent;
    border: 0;
    border-bottom: 1px solid var(--border-subtle);
    cursor: pointer;
    font: inherit;
    color: var(--fg);
  }
  .ctn-files-row:last-child { border-bottom: 0; }
  .ctn-files-row:hover { background: var(--surface-hover, var(--surface)); }
  .ctn-files-row.active { background: var(--surface); }
  .ctn-files-name {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
    overflow: hidden;
  }
  :global(.ctn-files-row-icon) { color: var(--fg-muted); flex-shrink: 0; }
  .ctn-files-name-text {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    font-size: 13px;
  }
  .ctn-files-link-dest {
    color: var(--fg-subtle);
    font-size: 11.5px;
    margin-left: 4px;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .ctn-files-cell-right {
    text-align: right;
    font-size: 11.5px;
    color: var(--fg-muted);
  }
  .ctn-files-mode {
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: var(--fg-muted);
  }
  .ctn-files-modified {
    font-size: 11.5px;
    color: var(--fg-muted);
  }
  .ctn-files-row-actions {
    display: inline-flex;
    justify-content: flex-end;
    gap: 4px;
  }
  .ctn-files-row-action {
    background: transparent;
    border: 0;
    padding: 4px;
    border-radius: 3px;
    cursor: pointer;
    color: var(--fg-muted);
  }
  .ctn-files-row-action:hover { background: var(--surface-hover, var(--surface)); color: var(--fg); }

  .ctn-files-pending {
    padding: 40px 28px;
    text-align: center;
    color: var(--fg-muted);
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 10px;
  }
  .ctn-files-pending p {
    margin: 0;
    max-width: 60ch;
    font-size: 13px;
    line-height: 1.6;
  }

  /* ─────────── File preview pane ─────────── */
  .ctn-files-preview {
    display: flex;
    flex-direction: column;
    border: 1px solid var(--border);
    border-radius: 6px;
    overflow: hidden;
  }
  .ctn-files-preview-bar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 10px 14px;
    background: var(--surface);
    border-bottom: 1px solid var(--border);
    flex-wrap: wrap;
  }
  .ctn-files-preview-meta {
    display: flex;
    align-items: baseline;
    gap: 10px;
    min-width: 0;
    flex-wrap: wrap;
  }
  .ctn-files-preview-path {
    font-family: var(--font-mono);
    font-size: 12.5px;
    color: var(--fg);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    max-width: 60ch;
  }
  .ctn-files-preview-size {
    font-size: 11.5px;
    color: var(--fg-subtle);
  }
  .ctn-files-preview-actions { display: inline-flex; gap: 6px; }
  .ctn-files-preview-body {
    margin: 0;
    padding: 14px;
    font-family: var(--font-mono);
    font-size: 12px;
    line-height: 1.5;
    color: var(--fg);
    background: var(--bg-elevated);
    max-height: 60vh;
    overflow: auto;
    white-space: pre-wrap;
    word-break: break-word;
  }
  .ctn-files-preview-loading,
  .ctn-files-preview-binary {
    color: var(--fg-muted);
    font-style: italic;
    text-align: center;
    padding: 32px 16px;
  }
  .ctn-files-preview-edit {
    width: 100%;
    min-height: 320px;
    max-height: 60vh;
    padding: 14px;
    font-family: var(--font-mono);
    font-size: 12px;
    line-height: 1.5;
    color: var(--fg);
    background: var(--bg-elevated);
    border: 0;
    resize: vertical;
    outline: none;
  }
  .ctn-files-preview-edit:focus { background: var(--surface); }

  :global(.ctn-files-icon) { color: var(--fg-subtle); margin-bottom: 4px; }

  /* ─────────── Generic per-tab pane ─────────── */
  .ctn-tab-pane {
    display: flex;
    flex-direction: column;
    gap: 14px;
    margin-top: 14px;
  }

  /* ─────────── Generic terminal-style block — used by Logs viewer,
                Terminal pane, Inspect viewer. ─────────── */
  .ctn-term {
    background: #0d1117;
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 12px 14px;
    font-family: var(--font-mono);
    font-size: 12.5px;
    line-height: 1.65;
    color: #e6edf3;
    overflow: auto;
  }

  /* ─────────── Logs ─────────── */
  .ctn-logs-bar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 16px;
    flex-wrap: wrap;
  }
  .ctn-log-filters { display: inline-flex; gap: 4px; flex-wrap: wrap; }
  .ctn-log-filter {
    background: transparent;
    border: 1px solid var(--border);
    color: var(--fg-muted);
    font-size: 11px;
    padding: 4px 9px;
    border-radius: 4px;
    cursor: pointer;
    display: inline-flex;
    align-items: baseline;
    gap: 6px;
    line-height: 1.3;
  }
  .ctn-log-filter:hover { color: var(--fg); background: var(--surface-hover); }
  .ctn-log-filter.active {
    background: var(--bg-elevated);
    border-color: var(--border-strong);
    color: var(--fg);
  }
  .ctn-log-filter:disabled { opacity: 0.4; cursor: not-allowed; }
  .ctn-log-filter-count { color: var(--fg-subtle); font-size: 11.5px; }

  .ctn-log-actions { display: inline-flex; align-items: center; gap: 8px; flex-wrap: wrap; }
  .ctn-log-grep {
    position: relative;
    display: inline-flex;
    align-items: center;
  }
  :global(.ctn-log-grep-icon) {
    position: absolute;
    left: 8px;
    color: var(--fg-subtle);
    pointer-events: none;
  }
  .ctn-log-grep-input {
    background: transparent;
    border: 1px solid var(--border);
    border-radius: 4px;
    color: var(--fg);
    font-family: var(--font-mono);
    font-size: 11.5px;
    padding: 4px 8px 4px 26px;
    width: 180px;
  }
  .ctn-log-grep-input:focus { outline: none; border-color: var(--color-brand-500); }
  .ctn-log-follow {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    background: transparent;
    border: 1px solid var(--border);
    border-radius: 4px;
    color: var(--fg-muted);
    font-size: 11px;
    padding: 4px 9px;
    cursor: pointer;
  }
  .ctn-log-follow.on {
    color: var(--accent-fg);
    border-color: color-mix(in srgb, var(--color-brand-500) 40%, var(--border));
    background: var(--accent-bg);
  }
  .ctn-log-follow-dot {
    width: 6px;
    height: 6px;
    border-radius: 999px;
    background: var(--fg-subtle);
  }
  .ctn-log-follow.on .ctn-log-follow-dot {
    background: var(--accent);
    box-shadow: 0 0 0 3px color-mix(in srgb, var(--color-brand-500) 25%, transparent);
  }

  .ctn-logs-viewer {
    max-height: 60vh;
    min-height: 320px;
  }
  .ctn-logs-empty {
    padding: 22px;
    text-align: center;
    color: rgba(230, 237, 243, 0.55);
    font-size: 12.5px;
  }
  .ctn-log-row {
    display: grid;
    grid-template-columns: 96px 56px 1fr;
    gap: 12px;
    padding: 1px 0;
    align-items: baseline;
  }
  .ctn-log-ts {
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: rgba(230, 237, 243, 0.45);
    letter-spacing: 0.02em;
    white-space: nowrap;
    text-align: right;
  }
  .ctn-log-lvl {
    font-size: 11.5px;
    color: rgba(230, 237, 243, 0.55);
    text-align: right;
  }
  .ctn-log-lvl--fatal { color: var(--color-danger-400); font-weight: 600; }
  .ctn-log-lvl--error { color: var(--color-danger-400); }
  .ctn-log-lvl--warn  { color: var(--color-warning-400); }
  .ctn-log-lvl--info  { color: var(--accent-fg); }
  .ctn-log-lvl--debug { color: var(--fg-subtle); }
  .ctn-log-msg {
    color: #e6edf3;
    word-break: break-word;
    white-space: pre-wrap;
  }
  .ctn-log-foot {
    display: flex;
    justify-content: space-between;
    font-size: 11px;
    color: var(--fg-subtle);
  }

  /* ─────────── Terminal ─────────── */
  .ctn-term-bar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 16px;
    flex-wrap: wrap;
  }
  .ctn-term-shell-pills { display: inline-flex; gap: 6px; align-items: center; }
  .ctn-term-shell-label {
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: var(--fg-subtle);
    margin-right: 4px;
  }
  .ctn-shell-pill {
    background: transparent;
    border: 1px solid var(--border);
    border-radius: 4px;
    color: var(--fg-muted);
    font-size: 11px;
    padding: 4px 10px;
    cursor: pointer;
  }
  .ctn-shell-pill:hover:not(:disabled) { color: var(--fg); background: var(--surface-hover); }
  .ctn-shell-pill.active {
    background: var(--bg-elevated);
    border-color: var(--border-strong);
    color: var(--fg);
  }
  .ctn-shell-pill:disabled { opacity: 0.45; cursor: not-allowed; }
  .ctn-term-actions { display: inline-flex; gap: 8px; align-items: center; }
  .ctn-term-shell {
    height: 60vh;
    min-height: 320px;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: #0d1117;
    padding: 12px;
  }
  .ctn-term-foot {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fg-subtle);
  }
  .ctn-term-foot code { color: var(--fg-muted); }

  /* ─────────── Inspect ─────────── */
  .ctn-inspect-bar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 16px;
    flex-wrap: wrap;
  }
  .ctn-inspect-actions { display: inline-flex; gap: 6px; align-items: center; }
  .ctn-inspect-toggle {
    background: transparent;
    border: 1px solid var(--border);
    color: var(--fg-muted);
    font-size: 11px;
    padding: 4px 10px;
    border-radius: 4px;
    cursor: pointer;
  }
  .ctn-inspect-toggle.active {
    background: var(--bg-elevated);
    border-color: var(--border-strong);
    color: var(--fg);
  }
  .ctn-inspect-pre {
    margin: 0;
    height: 60vh;
    min-height: 320px;
    padding: 14px 18px;
    font-size: 12px;
    line-height: 1.6;
  }

  /* Updates pane — 2-col layout with right rail for version history. */
  .ctn-updates-grid {
    display: grid;
    grid-template-columns: minmax(0, 1fr) 280px;
    gap: 28px;
    align-items: start;
  }
  @media (max-width: 980px) {
    .ctn-updates-grid { grid-template-columns: 1fr; }
  }
  .ctn-updates-main { display: flex; flex-direction: column; gap: 22px; min-width: 0; }
  .ctn-updates-rail { display: flex; flex-direction: column; gap: 16px; }
  .ctn-updates-rail-card { padding: 14px 16px; display: flex; flex-direction: column; gap: 10px; }
  .ctn-updates-rail-empty {
    margin: 0;
    font-size: 11px;
    color: var(--fg-subtle);
  }
  .ctn-updates-versions {
    margin: 0;
    padding: 0;
    list-style: none;
    font-family: var(--font-mono);
    font-size: 11.5px;
    line-height: 1.85;
  }
  .ctn-updates-versions li {
    display: flex;
    justify-content: space-between;
    gap: 12px;
  }
  .ctn-updates-version-tag {
    color: var(--fg-muted);
    word-break: break-all;
  }
  .ctn-updates-version-tag.current { color: var(--accent-fg); font-weight: 500; }
  .ctn-updates-version-meta { color: var(--fg-subtle); }

  /* Update-available banner — accent border only, no fill, so the card
     sits as a quiet outline against the page background. */
  .ctn-updates-banner {
    padding: 18px 20px;
    display: flex;
    flex-direction: column;
    gap: 12px;
    border-color: color-mix(in srgb, var(--color-brand-500) 60%, var(--border));
    background: transparent;
  }
  .ctn-updates-banner-head {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 16px;
    flex-wrap: wrap;
  }
  .ctn-updates-banner-text { min-width: 0; display: flex; flex-direction: column; gap: 4px; }
  .ctn-updates-banner-eyebrow { display: block; }
  .ctn-updates-banner-actions { display: inline-flex; gap: 8px; flex-shrink: 0; align-items: center; }
  .ctn-updates-rel {
    font-style: normal;
    color: var(--fg);
    letter-spacing: 0.01em;
  }
  .ctn-updates-vdiff {
    display: inline-flex;
    align-items: baseline;
    gap: 10px;
    margin-top: 6px;
    font-family: var(--font-mono);
  }
  .ctn-updates-vdiff-old {
    font-size: 14px;
    color: var(--fg-subtle);
    text-decoration: line-through;
  }
  .ctn-updates-vdiff-arrow { color: var(--fg-subtle); align-self: center; }
  .ctn-updates-vdiff-new {
    font-size: 20px;
    color: var(--accent-fg);
    font-weight: 600;
    letter-spacing: 0.01em;
  }
  .ctn-updates-banner-meta {
    margin: 8px 0 0;
    font-size: 12px;
    color: var(--fg-muted);
    max-width: 56ch;
    line-height: 1.55;
  }
  .ctn-updates-card { padding: 18px 20px; display: flex; flex-direction: column; gap: 14px; }

  /* Image diff metrics — 3-col EdMetric grid below the changelog. */
  .ctn-updates-imgdiff {
    margin-top: 12px;
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
    gap: 12px;
  }
  .ctn-updates-metric { padding: 12px 14px; }
  .ctn-updates-metric .ed-metric-value.mono {
    font-family: var(--font-mono);
    font-size: 14px;
  }
  .ctn-updates-delta-down { color: var(--color-success-400); }
  .ctn-updates-delta-up { color: var(--color-warning-400); }
  .ctn-updates-head {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 16px;
    flex-wrap: wrap;
  }
  .ctn-updates-image {
    display: flex;
    align-items: flex-start;
    gap: 14px;
    min-width: 0;
  }
  .ctn-updates-icon-wrap {
    width: 36px;
    height: 36px;
    border-radius: 5px;
    border: 1px solid color-mix(in srgb, var(--color-brand-500) 35%, var(--border));
    background: color-mix(in srgb, var(--color-brand-500) 10%, transparent);
    color: var(--color-brand-400);
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
  }
  .ctn-updates-image-ref {
    font-family: var(--font-mono);
    font-size: 13.5px;
    color: var(--fg);
    font-weight: 500;
  }
  .ctn-updates-image-meta {
    margin-top: 6px;
    font-size: 11px;
    color: var(--fg-subtle);
    line-height: 1.6;
  }
  .ctn-updates-links {
    margin-top: 8px;
    display: flex;
    gap: 14px;
    font-size: 11px;
  }
  .ctn-updates-links a {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    color: var(--accent-fg);
    text-decoration: none;
    letter-spacing: 0.02em;
  }
  .ctn-updates-links a:hover { color: var(--fg); }
  .ctn-updates-warnings {
    font-size: 11.5px;
    color: var(--color-warning-400);
    letter-spacing: 0.04em;
  }
  .ctn-updates-release-head {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 16px;
    flex-wrap: wrap;
  }
  .ctn-updates-release-title h3 {
    margin: 4px 0 0;
    font-size: 14px;
    font-weight: 600;
    color: var(--fg);
    display: inline-flex;
    align-items: center;
    gap: 8px;
  }
  .ctn-updates-release-body {
    margin: 0;
    padding: 12px 14px;
    border: 1px solid var(--border);
    border-radius: 5px;
    background: var(--bg-elevated);
    font-size: 11.5px;
    color: var(--fg-muted);
    white-space: pre-wrap;
    word-break: break-word;
    max-height: 40vh;
    overflow: auto;
    line-height: 1.55;
  }

  /* Update-history timeline — same shape as the stack-detail history. */
  .ctn-updates-history {
    list-style: none;
    margin: 0;
    padding: 8px 0 0;
    position: relative;
  }
  .ctn-updates-history-rule {
    position: absolute;
    left: 7px;
    top: 14px;
    bottom: 14px;
    width: 1px;
    background: var(--border);
  }
  .ctn-updates-history-row {
    position: relative;
    display: grid;
    grid-template-columns: 22px 1fr;
    gap: 12px;
    padding: 10px 0 14px;
  }
  .ctn-updates-history-dot {
    width: 11px;
    height: 11px;
    border-radius: 999px;
    background: var(--bg-elevated);
    border: 2px solid var(--border-strong);
    margin-top: 6px;
    margin-left: 2px;
    z-index: 1;
  }
  .ctn-updates-history-dot.current { border-color: var(--color-success-500); }
  .ctn-updates-history-body { display: flex; flex-direction: column; gap: 4px; min-width: 0; }
  .ctn-updates-history-head {
    display: flex;
    align-items: center;
    gap: 12px;
    flex-wrap: wrap;
  }
  .ctn-updates-history-ref {
    font-family: var(--font-mono);
    font-size: 12.5px;
    color: var(--fg);
    font-weight: 500;
    word-break: break-all;
  }
  .ctn-updates-history-meta {
    font-size: 11px;
    color: var(--fg-subtle);
    letter-spacing: 0.02em;
  }

  /* JSON syntax highlighting */
  :global(.json-view .json-key) { color: #7dd3fc; }
  :global(.json-view .json-string) { color: #86efac; }
  :global(.json-view .json-number) { color: #fdba74; }
  :global(.json-view .json-bool) { color: #c4b5fd; }
  :global(.json-view .json-null) { color: #94a3b8; font-style: normal; }

  .ctn-log-msg :global(.log-ts) { color: var(--subtle); }
  .ctn-log-msg :global(.log-error) { color: var(--bad); font-weight: 600; }
  .ctn-log-msg :global(.log-warn) { color: var(--warn); }
  .ctn-log-msg :global(.log-info) { color: var(--accent); }
  .ctn-log-msg :global(.log-debug) { color: var(--subtle); }
  .ctn-log-msg :global(.log-ok) { color: var(--ok); }
</style>
