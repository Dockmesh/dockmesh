<script lang="ts">
  import { page } from '$app/stores';
  import { goto } from '$app/navigation';
  import { api, ApiError } from '$lib/api';
  import { tick } from 'svelte';
  import { Terminal as XTerm } from '@xterm/xterm';
  import { FitAddon } from '@xterm/addon-fit';
  import '@xterm/xterm/css/xterm.css';
  import { Card, Badge, Button, Skeleton } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { allowed } from '$lib/rbac';
  import { hosts } from '$lib/stores/host.svelte';
  import type { UpdatePreview, UpdateHistoryEntry, MetricsSample } from '$lib/api';
  import {
    ChevronLeft,
    Play,
    Square,
    RotateCw,
    Trash2,
    FileText,
    Terminal as TerminalIcon,
    Activity,
    Code2,
    Trash,
    Link as LinkIcon,
    Download,
    Undo2,
    ExternalLink,
    Package,
    Info,
    Network,
    HardDrive,
    Tag
  } from 'lucide-svelte';

  const id = $derived($page.params.id);
  const canControl = $derived(allowed('container.control'));
  const canExec = $derived(allowed('container.exec'));
  // Resolve the host: prefer the URL ?host=… (set when the user navigated
  // here from a remote-host listing), otherwise the global selection.
  const targetHost = $derived($page.url.searchParams.get('host') || hosts.id);
  const isRemote = $derived(targetHost !== 'local');

  let info = $state<any>(null);
  let loading = $state(true);
  let tab = $state<'overview' | 'logs' | 'exec' | 'updates' | 'inspect'>('overview');

  // For remote hosts in 3.1.2.4: Logs / Stats / Terminal / Inspect work.
  // Updates is still local-only (image pulls live on the central server's
  // docker daemon — that one comes in 3.1.3 with stack deploy).
  $effect(() => {
    if (isRemote && tab === 'updates') {
      tab = 'inspect';
    }
  });

  // Updates state
  let updatePreview = $state<UpdatePreview | null>(null);
  let updateHistory = $state<UpdateHistoryEntry[]>([]);
  let previewLoading = $state(false);
  let updateBusy = $state(false);

  // Logs
  let logs = $state<string[]>([]);
  let wsConnected = $state(false);
  let autoScroll = $state(true);
  let logContainer: HTMLDivElement | null = $state(null);
  let ws: WebSocket | null = null;

  // Stats
  interface StatsSample {
    cpu_percent: number;
    mem_used: number;
    mem_limit: number;
    mem_percent: number;
    net_rx: number;
    net_tx: number;
    blk_read: number;
    blk_write: number;
    pids_current: number;
  }
  let stats = $state<StatsSample | null>(null);
  let statsHistory = $state<StatsSample[]>([]);
  let statsConnected = $state(false);
  let statsWs: WebSocket | null = null;

  // History metrics (server-side collected)
  type HistoryRange = '1h' | '6h' | '24h' | '7d' | '30d';
  let historyRange = $state<HistoryRange>('1h');
  let historySamples = $state<MetricsSample[]>([]);
  let historyLoading = $state(false);

  const RANGE_SECONDS: Record<HistoryRange, number> = {
    '1h': 3600,
    '6h': 6 * 3600,
    '24h': 24 * 3600,
    '7d': 7 * 86400,
    '30d': 30 * 86400
  };

  function rangeResolution(r: HistoryRange): 'raw' | '1m' | '1h' {
    if (r === '1h' || r === '6h') return 'raw';
    if (r === '24h' || r === '7d') return '1m';
    return '1h';
  }

  async function loadHistory() {
    historyLoading = true;
    try {
      const to = Math.floor(Date.now() / 1000);
      const from = to - RANGE_SECONDS[historyRange];
      historySamples = await api.containers.metrics(id, from, to, rangeResolution(historyRange));
    } catch { /* ignore — empty history is fine */
      historySamples = [];
    } finally {
      historyLoading = false;
    }
  }

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
    if (!confirm('Pull the latest image and recreate this container? The old image will be kept as a rollback snapshot.')) return;
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
    if (!confirm('Roll back this container to the previous image version?')) return;
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

  function fmtMB(bytes?: number): string {
    if (!bytes) return '—';
    return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
  }

  function shortDigest(d?: string): string {
    if (!d) return '—';
    const m = d.match(/sha256:([a-f0-9]{12})/);
    return m ? m[0] : d.slice(0, 19);
  }

  // ---------- Logs ----------
  async function connectLogs() {
    disconnectLogs();
    logs = [];
    try {
      const { ticket } = await api.ws.ticket();
      const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
      const hostQs = isRemote ? `&host=${encodeURIComponent(targetHost)}` : '';
      ws = new WebSocket(`${proto}//${location.host}/api/v1/ws/logs/${id}?ticket=${ticket}&tail=200${hostQs}`);
      ws.onopen = () => { wsConnected = true; };
      ws.onmessage = async (ev) => {
        logs = [...logs, ev.data as string];
        if (logs.length > 5000) logs = logs.slice(-5000);
        if (autoScroll) {
          await tick();
          if (logContainer) logContainer.scrollTop = logContainer.scrollHeight;
        }
      };
      ws.onclose = () => { wsConnected = false; };
      ws.onerror = () => { wsConnected = false; };
    } catch (err) {
      toast.error('Logs connect failed', err instanceof ApiError ? err.message : undefined);
    }
  }
  function disconnectLogs() {
    if (ws) { ws.close(); ws = null; }
    wsConnected = false;
  }

  // ---------- Stats ----------
  async function connectStats() {
    disconnectStats();
    statsHistory = [];
    try {
      const { ticket } = await api.ws.ticket();
      const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
      const hostQs = isRemote ? `&host=${encodeURIComponent(targetHost)}` : '';
      statsWs = new WebSocket(`${proto}//${location.host}/api/v1/ws/stats/${id}?ticket=${ticket}${hostQs}`);
      statsWs.onopen = () => { statsConnected = true; };
      statsWs.onmessage = (ev) => {
        try {
          const s = JSON.parse(ev.data);
          if (s.error) return;
          stats = s;
          statsHistory = [...statsHistory, s].slice(-60);
        } catch { /* ignore */ }
      };
      statsWs.onclose = () => { statsConnected = false; };
    } catch { /* ignore */ }
  }
  function disconnectStats() {
    if (statsWs) { statsWs.close(); statsWs = null; }
    statsConnected = false;
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

    try {
      const { ticket } = await api.ws.ticket();
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
      toast.error('Exec failed', err instanceof ApiError ? err.message : undefined);
    }
  }
  function disconnectExec() {
    resizeObserver?.disconnect();
    resizeObserver = null;
    if (execWs) { execWs.close(); execWs = null; }
    if (term) { term.dispose(); term = null; }
    fitAddon = null;
    execConnected = false;
  }

  // ---------- Actions ----------
  async function action(op: 'start' | 'stop' | 'restart') {
    try {
      if (op === 'start') await api.containers.start(id, targetHost);
      else if (op === 'stop') await api.containers.stop(id, targetHost);
      else await api.containers.restart(id, targetHost);
      toast.success(op);
      await loadInfo();
      if (tab === 'logs') connectLogs();
    } catch (err) {
      toast.error(`${op} failed`, err instanceof ApiError ? err.message : undefined);
    }
  }

  async function remove() {
    if (!confirm('Remove this container?')) return;
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
      loadHistory();
    } else if (tab === 'logs') {
      disconnectExec();
      disconnectStats();
      connectLogs();
    } else if (tab === 'exec') {
      disconnectLogs();
      disconnectStats();
      tick().then(connectExec);
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

  // Helpers
  function containerName(inf: any): string {
    return (inf?.Name ?? '').replace(/^\//, '');
  }

  function portList(inf: any): string {
    if (!inf?.NetworkSettings?.Ports) return '—';
    const out: string[] = [];
    for (const [priv, bindings] of Object.entries(inf.NetworkSettings.Ports)) {
      if (Array.isArray(bindings) && bindings.length > 0) {
        for (const b of bindings as any[]) out.push(`${b.HostPort}→${priv}`);
      }
    }
    return out.join(', ') || '—';
  }

  function formatBytes(n: number): string {
    if (n < 1024) return `${n} B`;
    if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
    if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
    return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`;
  }

  function sparkPath(values: number[], max: number, w: number, h: number): string {
    if (values.length === 0) return '';
    const m = max || Math.max(1, ...values);
    const stepX = w / Math.max(1, values.length - 1);
    return values
      .map((v, i) => `${i === 0 ? 'M' : 'L'}${(i * stepX).toFixed(1)},${(h - (v / m) * h).toFixed(1)}`)
      .join(' ');
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

  function sparkArea(values: number[], max: number, w: number, h: number): string {
    if (values.length === 0) return '';
    const m = max || Math.max(1, ...values);
    const stepX = w / Math.max(1, values.length - 1);
    const pts = values
      .map((v, i) => `${(i * stepX).toFixed(1)},${(h - (v / m) * h).toFixed(1)}`)
      .join(' L');
    return `M0,${h} L${pts} L${w},${h} Z`;
  }
</script>

<section class="space-y-5">
  <a href="/containers" class="inline-flex items-center gap-1 text-sm text-[var(--fg-muted)] hover:text-[var(--fg)]">
    <ChevronLeft class="w-4 h-4" />
    Containers
  </a>

  {#if loading}
    <Skeleton width="40%" height="2rem" />
    <Skeleton width="70%" height="1rem" />
  {:else if info}
    <div class="flex items-center justify-between flex-wrap gap-3">
      <div class="flex items-center gap-3 min-w-0">
        <h2 class="text-2xl font-semibold tracking-tight font-mono truncate">
          {containerName(info) || id.slice(0, 12)}
        </h2>
        <Badge variant={info.State?.Running ? 'success' : 'default'} dot>
          {info.State?.Status ?? 'unknown'}
        </Badge>
      </div>
      {#if canControl}
        <div class="flex gap-2 flex-wrap">
          {#if info.State?.Running}
            <Button variant="secondary" onclick={() => action('restart')}>
              <RotateCw class="w-4 h-4" /> Restart
            </Button>
            <Button variant="secondary" onclick={() => action('stop')}>
              <Square class="w-4 h-4" /> Stop
            </Button>
          {:else}
            <Button variant="primary" onclick={() => action('start')}>
              <Play class="w-4 h-4" /> Start
            </Button>
          {/if}
          <Button variant="danger" onclick={remove}>
            <Trash2 class="w-4 h-4" /> Remove
          </Button>
        </div>
      {/if}
    </div>

    <!-- Info cards -->
    <div class="grid grid-cols-1 md:grid-cols-3 gap-4">
      <Card class="p-4">
        <div class="text-xs text-[var(--fg-muted)] uppercase tracking-wider font-medium">Image</div>
        <div class="font-mono text-sm mt-1 truncate">{info.Config?.Image}</div>
      </Card>
      <Card class="p-4">
        <div class="text-xs text-[var(--fg-muted)] uppercase tracking-wider font-medium">Ports</div>
        <div class="font-mono text-sm mt-1 truncate">{portList(info)}</div>
      </Card>
      <Card class="p-4">
        <div class="text-xs text-[var(--fg-muted)] uppercase tracking-wider font-medium">Created</div>
        <div class="font-mono text-xs mt-1">{info.Created?.slice(0, 19).replace('T', ' ')}</div>
      </Card>
    </div>
  {/if}

  <!-- Tabs -->
  <div class="border-b border-[var(--border)] flex gap-1">
    {#snippet tabBtn(id: typeof tab, label: string, Icon: any, connected: boolean)}
      <button
        class="px-4 py-2.5 text-sm border-b-2 transition-colors flex items-center gap-2
               {tab === id
          ? 'border-[var(--color-brand-500)] text-[var(--fg)]'
          : 'border-transparent text-[var(--fg-muted)] hover:text-[var(--fg)]'}"
        onclick={() => (tab = id)}
      >
        <Icon class="w-3.5 h-3.5" />
        {label}
        {#if connected && tab === id}
          <span class="w-1.5 h-1.5 rounded-full bg-[var(--color-success-500)]"></span>
        {/if}
      </button>
    {/snippet}
    {@render tabBtn('overview', 'Overview', Info, statsConnected)}
    {@render tabBtn('logs', 'Logs', FileText, wsConnected)}
    {#if canExec}
      {@render tabBtn('exec', 'Terminal', TerminalIcon, execConnected)}
    {/if}
    {#if canControl && !isRemote}
      {@render tabBtn('updates', 'Updates', Download, false)}
    {/if}
    {@render tabBtn('inspect', 'Inspect', Code2, false)}
  </div>

  {#if isRemote}
    <div class="dm-card p-3 text-xs text-[var(--fg-muted)] flex items-center gap-2">
      <span class="text-[var(--color-brand-400)]">⚡</span>
      Remote host — Logs, Stats, Terminal and Inspect are streamed via the agent.
      Image updates are still local-only (coming with stack deploy in 3.1.3).
    </div>
  {/if}

  <!-- Tab panels -->
  {#if tab === 'overview' && info}
    <!-- Live Stats (merged from Stats tab) -->
    {#if stats}
      <div class="grid grid-cols-2 lg:grid-cols-4 gap-3">
        <Card class="p-4">
          <div class="flex justify-between items-baseline mb-2">
            <div class="text-[10px] text-[var(--fg-muted)] uppercase tracking-wider font-medium">CPU</div>
            <div class="text-xl font-semibold font-mono tabular-nums">
              {stats.cpu_percent.toFixed(1)}<span class="text-xs text-[var(--fg-muted)]">%</span>
            </div>
          </div>
          <svg viewBox="0 0 200 40" class="w-full h-10">
            <defs><linearGradient id="cpu-g" x1="0" y1="0" x2="0" y2="1"><stop offset="0%" stop-color="#06b6d4" stop-opacity="0.3" /><stop offset="100%" stop-color="#06b6d4" stop-opacity="0" /></linearGradient></defs>
            <path d={sparkArea(statsHistory.map(s => s.cpu_percent), 100, 200, 40)} fill="url(#cpu-g)" />
            <path d={sparkPath(statsHistory.map(s => s.cpu_percent), 100, 200, 40)} fill="none" stroke="#06b6d4" stroke-width="1.5" stroke-linejoin="round" />
          </svg>
        </Card>
        <Card class="p-4">
          <div class="flex justify-between items-baseline mb-2">
            <div class="text-[10px] text-[var(--fg-muted)] uppercase tracking-wider font-medium">Memory</div>
            <div class="text-xl font-semibold font-mono tabular-nums">
              {stats.mem_percent.toFixed(1)}<span class="text-xs text-[var(--fg-muted)]">%</span>
            </div>
          </div>
          <div class="text-[10px] text-[var(--fg-subtle)] font-mono -mt-1 mb-1">{formatBytes(stats.mem_used)} / {formatBytes(stats.mem_limit)}</div>
          <svg viewBox="0 0 200 40" class="w-full h-10">
            <defs><linearGradient id="mem-g" x1="0" y1="0" x2="0" y2="1"><stop offset="0%" stop-color="#22c55e" stop-opacity="0.3" /><stop offset="100%" stop-color="#22c55e" stop-opacity="0" /></linearGradient></defs>
            <path d={sparkArea(statsHistory.map(s => s.mem_percent), 100, 200, 40)} fill="url(#mem-g)" />
            <path d={sparkPath(statsHistory.map(s => s.mem_percent), 100, 200, 40)} fill="none" stroke="#22c55e" stroke-width="1.5" stroke-linejoin="round" />
          </svg>
        </Card>
        <Card class="p-4">
          <div class="text-[10px] text-[var(--fg-muted)] uppercase tracking-wider font-medium mb-2">Network</div>
          <div class="grid grid-cols-2 gap-2">
            <div><div class="text-[10px] text-[var(--fg-subtle)]">↓ rx</div><div class="text-sm font-mono tabular-nums">{formatBytes(stats.net_rx)}</div></div>
            <div><div class="text-[10px] text-[var(--fg-subtle)]">↑ tx</div><div class="text-sm font-mono tabular-nums">{formatBytes(stats.net_tx)}</div></div>
          </div>
        </Card>
        <Card class="p-4">
          <div class="text-[10px] text-[var(--fg-muted)] uppercase tracking-wider font-medium mb-2">Block I/O · PIDs</div>
          <div class="grid grid-cols-3 gap-2">
            <div><div class="text-[10px] text-[var(--fg-subtle)]">read</div><div class="text-xs font-mono tabular-nums">{formatBytes(stats.blk_read)}</div></div>
            <div><div class="text-[10px] text-[var(--fg-subtle)]">write</div><div class="text-xs font-mono tabular-nums">{formatBytes(stats.blk_write)}</div></div>
            <div><div class="text-[10px] text-[var(--fg-subtle)]">pids</div><div class="text-xs font-mono tabular-nums">{stats.pids_current}</div></div>
          </div>
        </Card>
      </div>
    {:else if statsConnected}
      <Card class="p-4 text-center text-xs text-[var(--fg-muted)]">waiting for first stats sample…</Card>
    {/if}

    <!-- History chart -->
    {#if historySamples.length > 0 || historyLoading}
      <Card class="p-4">
        <div class="flex items-center justify-between flex-wrap gap-2 mb-3">
          <h3 class="font-semibold text-xs uppercase tracking-wider text-[var(--fg-muted)]">History</h3>
          <div class="flex gap-1 text-[10px]">
            {#each ['1h', '6h', '24h', '7d', '30d'] as const as r}
              <button
                class="px-2 py-0.5 rounded font-mono transition-colors
                       {historyRange === r
                  ? 'bg-[var(--color-brand-500)] text-white'
                  : 'text-[var(--fg-muted)] hover:bg-[var(--surface-hover)]'}"
                onclick={() => { historyRange = r; loadHistory(); }}
              >{r}</button>
            {/each}
          </div>
        </div>
        {#if historyLoading && historySamples.length === 0}
          <div class="text-xs text-[var(--fg-muted)]">loading…</div>
        {:else if historySamples.length === 0}
          <div class="text-xs text-[var(--fg-muted)]">no samples yet</div>
        {:else}
          {@const cpuVals = historySamples.map(s => s.cpu_percent)}
          {@const memPct = historySamples.map(s => s.mem_limit > 0 ? (s.mem_used / s.mem_limit) * 100 : 0)}
          <div class="grid grid-cols-1 lg:grid-cols-2 gap-4">
            <div>
              <div class="flex justify-between text-[10px] text-[var(--fg-muted)] mb-1"><span>CPU %</span><span>{cpuVals.length} samples</span></div>
              <svg viewBox="0 0 400 60" class="w-full h-14">
                <defs><linearGradient id="hcpu" x1="0" y1="0" x2="0" y2="1"><stop offset="0%" stop-color="#06b6d4" stop-opacity="0.3" /><stop offset="100%" stop-color="#06b6d4" stop-opacity="0" /></linearGradient></defs>
                <path d={sparkArea(cpuVals, 100, 400, 60)} fill="url(#hcpu)" />
                <path d={sparkPath(cpuVals, 100, 400, 60)} fill="none" stroke="#06b6d4" stroke-width="1.5" stroke-linejoin="round" />
              </svg>
            </div>
            <div>
              <div class="flex justify-between text-[10px] text-[var(--fg-muted)] mb-1"><span>Memory %</span><span>peak {Math.max(...memPct).toFixed(1)}%</span></div>
              <svg viewBox="0 0 400 60" class="w-full h-14">
                <defs><linearGradient id="hmem" x1="0" y1="0" x2="0" y2="1"><stop offset="0%" stop-color="#22c55e" stop-opacity="0.3" /><stop offset="100%" stop-color="#22c55e" stop-opacity="0" /></linearGradient></defs>
                <path d={sparkArea(memPct, 100, 400, 60)} fill="url(#hmem)" />
                <path d={sparkPath(memPct, 100, 400, 60)} fill="none" stroke="#22c55e" stroke-width="1.5" stroke-linejoin="round" />
              </svg>
            </div>
          </div>
        {/if}
      </Card>
    {/if}

    <!-- Config sections -->
    <div class="grid grid-cols-1 lg:grid-cols-2 gap-4">
      <!-- Environment Variables -->
      <Card>
        <div class="px-5 py-3 border-b border-[var(--border)] text-xs font-medium text-[var(--fg-muted)] uppercase tracking-wider flex items-center gap-2">
          <Tag class="w-3.5 h-3.5" /> Environment ({(info.Config?.Env ?? []).length})
        </div>
        <div class="max-h-64 overflow-auto">
          {#if (info.Config?.Env ?? []).length === 0}
            <div class="px-5 py-4 text-xs text-[var(--fg-subtle)]">No environment variables</div>
          {:else}
            <div class="divide-y divide-[var(--border)]">
              {#each info.Config?.Env ?? [] as envLine}
                {@const idx = envLine.indexOf('=')}
                {@const key = idx >= 0 ? envLine.slice(0, idx) : envLine}
                {@const val = idx >= 0 ? envLine.slice(idx + 1) : ''}
                <div class="px-5 py-2 flex gap-2 text-xs hover:bg-[var(--surface-hover)]">
                  <span class="font-mono font-medium text-[var(--fg)] shrink-0">{key}</span>
                  <span class="font-mono text-[var(--fg-muted)] truncate" title={val}>{val || '(empty)'}</span>
                </div>
              {/each}
            </div>
          {/if}
        </div>
      </Card>

      <!-- Mounts / Volumes -->
      <Card>
        <div class="px-5 py-3 border-b border-[var(--border)] text-xs font-medium text-[var(--fg-muted)] uppercase tracking-wider flex items-center gap-2">
          <HardDrive class="w-3.5 h-3.5" /> Mounts ({(info.Mounts ?? []).length})
        </div>
        <div class="max-h-64 overflow-auto">
          {#if (info.Mounts ?? []).length === 0}
            <div class="px-5 py-4 text-xs text-[var(--fg-subtle)]">No mounts</div>
          {:else}
            <div class="divide-y divide-[var(--border)]">
              {#each info.Mounts ?? [] as m}
                <div class="px-5 py-2.5 text-xs hover:bg-[var(--surface-hover)]">
                  <div class="flex items-center gap-2">
                    <Badge variant={m.Type === 'volume' ? 'info' : 'default'}>{m.Type}</Badge>
                    <span class="font-mono text-[var(--fg)] truncate">{m.Name || m.Source}</span>
                    {#if m.RW === false}<Badge variant="warning">ro</Badge>{/if}
                  </div>
                  <div class="font-mono text-[var(--fg-muted)] mt-0.5 truncate" title={m.Destination}>→ {m.Destination}</div>
                </div>
              {/each}
            </div>
          {/if}
        </div>
      </Card>

      <!-- Networks -->
      <Card>
        <div class="px-5 py-3 border-b border-[var(--border)] text-xs font-medium text-[var(--fg-muted)] uppercase tracking-wider flex items-center gap-2">
          <Network class="w-3.5 h-3.5" /> Networks ({Object.keys(info.NetworkSettings?.Networks ?? {}).length})
        </div>
        <div class="max-h-64 overflow-auto">
          {#if Object.keys(info.NetworkSettings?.Networks ?? {}).length === 0}
            <div class="px-5 py-4 text-xs text-[var(--fg-subtle)]">No networks</div>
          {:else}
            <div class="divide-y divide-[var(--border)]">
              {#each Object.entries(info.NetworkSettings?.Networks ?? {}) as [netName, netCfg]}
                {@const cfg = netCfg as any}
                <div class="px-5 py-2.5 text-xs hover:bg-[var(--surface-hover)]">
                  <div class="font-mono font-medium text-[var(--fg)]">{netName}</div>
                  <div class="text-[var(--fg-muted)] mt-0.5 font-mono">
                    {#if cfg?.IPAddress}IP: {cfg.IPAddress}{/if}
                    {#if cfg?.Gateway} · GW: {cfg.Gateway}{/if}
                    {#if cfg?.MacAddress} · MAC: {cfg.MacAddress}{/if}
                  </div>
                </div>
              {/each}
            </div>
          {/if}
        </div>
      </Card>

      <!-- Labels + Config -->
      <Card>
        <div class="px-5 py-3 border-b border-[var(--border)] text-xs font-medium text-[var(--fg-muted)] uppercase tracking-wider flex items-center gap-2">
          <Info class="w-3.5 h-3.5" /> Configuration
        </div>
        <div class="divide-y divide-[var(--border)]">
          <div class="px-5 py-2.5 flex justify-between text-xs">
            <span class="text-[var(--fg-muted)]">Restart Policy</span>
            <span class="font-mono text-[var(--fg)]">{info.HostConfig?.RestartPolicy?.Name ?? '—'}</span>
          </div>
          <div class="px-5 py-2.5 flex justify-between text-xs">
            <span class="text-[var(--fg-muted)]">Working Dir</span>
            <span class="font-mono text-[var(--fg)]">{info.Config?.WorkingDir || '/'}</span>
          </div>
          <div class="px-5 py-2.5 flex justify-between text-xs">
            <span class="text-[var(--fg-muted)]">User</span>
            <span class="font-mono text-[var(--fg)]">{info.Config?.User || 'root'}</span>
          </div>
          <div class="px-5 py-2.5 flex justify-between text-xs">
            <span class="text-[var(--fg-muted)]">Entrypoint</span>
            <span class="font-mono text-[var(--fg)] truncate max-w-[200px]" title={(info.Config?.Entrypoint ?? []).join(' ')}>
              {(info.Config?.Entrypoint ?? []).join(' ') || '—'}
            </span>
          </div>
          <div class="px-5 py-2.5 flex justify-between text-xs">
            <span class="text-[var(--fg-muted)]">Command</span>
            <span class="font-mono text-[var(--fg)] truncate max-w-[200px]" title={(info.Config?.Cmd ?? []).join(' ')}>
              {(info.Config?.Cmd ?? []).join(' ') || '—'}
            </span>
          </div>
          <div class="px-5 py-2.5 flex justify-between text-xs">
            <span class="text-[var(--fg-muted)]">Privileged</span>
            <span class="font-mono text-[var(--fg)]">{info.HostConfig?.Privileged ? 'yes' : 'no'}</span>
          </div>
          <div class="px-5 py-2.5 flex justify-between text-xs">
            <span class="text-[var(--fg-muted)]">Network Mode</span>
            <span class="font-mono text-[var(--fg)]">{info.HostConfig?.NetworkMode ?? '—'}</span>
          </div>
        </div>

        <!-- Labels subsection -->
        {#if Object.keys(info.Config?.Labels ?? {}).length > 0}
          <div class="px-5 py-3 border-t border-[var(--border)] text-xs font-medium text-[var(--fg-muted)] uppercase tracking-wider">
            Labels ({Object.keys(info.Config?.Labels ?? {}).length})
          </div>
          <div class="max-h-48 overflow-auto divide-y divide-[var(--border)]">
            {#each Object.entries(info.Config?.Labels ?? {}).sort(([a], [b]) => a.localeCompare(b)) as [k, v]}
              <div class="px-5 py-1.5 flex gap-2 text-[10px] hover:bg-[var(--surface-hover)]">
                <span class="font-mono font-medium text-[var(--fg)] shrink-0">{k}</span>
                <span class="font-mono text-[var(--fg-muted)] truncate">{v}</span>
              </div>
            {/each}
          </div>
        {/if}
      </Card>
    </div>
  {:else if tab === 'logs'}
    <div class="flex items-center gap-3 text-sm">
      <label class="flex items-center gap-2 cursor-pointer">
        <input type="checkbox" bind:checked={autoScroll} class="accent-[var(--color-brand-500)]" />
        <span class="text-[var(--fg-muted)]">auto-scroll</span>
      </label>
      <Button size="xs" variant="ghost" onclick={() => (logs = [])}>
        <Trash class="w-3.5 h-3.5" /> Clear
      </Button>
      {#if wsConnected}
        <Button size="xs" variant="ghost" onclick={disconnectLogs}>
          <LinkIcon class="w-3.5 h-3.5" /> Disconnect
        </Button>
      {:else}
        <Button size="xs" variant="ghost" onclick={connectLogs}>
          <LinkIcon class="w-3.5 h-3.5" /> Reconnect
        </Button>
      {/if}
      <span class="text-xs text-[var(--fg-subtle)] ml-auto">{logs.length} lines</span>
    </div>
    <div
      bind:this={logContainer}
      class="h-[60vh] overflow-auto rounded-xl border border-[var(--border)] bg-black p-4 font-mono text-xs leading-relaxed"
      style="font-family: var(--font-mono);"
    >
      {#each logs as line, i (i)}
        <div class="whitespace-pre-wrap break-all log-line">{@html colorizeLog(line)}</div>
      {/each}
      {#if logs.length === 0 && wsConnected}
        <div class="text-[var(--fg-subtle)]">waiting for log output…</div>
      {:else if logs.length === 0}
        <div class="text-[var(--fg-subtle)]">disconnected</div>
      {/if}
    </div>
  {:else if tab === 'exec'}
    <div class="flex items-center gap-3 text-sm">
      <label class="flex items-center gap-2">
        <span class="text-[var(--fg-muted)]">Shell:</span>
        <select class="dm-input !py-1 !px-2 !w-auto text-sm" bind:value={execShell}>
          <option value="sh">/bin/sh</option>
          <option value="bash">/bin/bash</option>
        </select>
      </label>
      <Button size="xs" variant="ghost" onclick={connectExec}>Reconnect</Button>
      <span class="text-xs text-[var(--fg-subtle)] ml-auto">
        {execConnected ? 'connected' : 'disconnected'}
      </span>
    </div>
    <div bind:this={execContainer} class="h-[60vh] rounded-xl border border-[var(--border)] bg-black p-3"></div>
  {:else if tab === 'updates'}
    <div class="space-y-4">
      <!-- Preview card -->
      {#if previewLoading && !updatePreview}
        <Card class="p-5">
          <Skeleton width="40%" height="1.25rem" />
          <Skeleton class="mt-3" width="70%" height="0.85rem" />
        </Card>
      {:else if updatePreview}
        <Card class="p-5">
          <div class="flex items-start justify-between flex-wrap gap-4">
            <div class="flex items-start gap-3">
              <div class="w-10 h-10 rounded-lg bg-[color-mix(in_srgb,var(--color-brand-500)_15%,transparent)] text-[var(--color-brand-400)] flex items-center justify-center shrink-0">
                <Package class="w-5 h-5" />
              </div>
              <div>
                <div class="font-mono text-sm">{updatePreview.image}</div>
                <div class="text-xs text-[var(--fg-muted)] mt-1 space-y-0.5 font-mono">
                  <div>local: {shortDigest(updatePreview.current_digest)} · built {fmtRelTime(updatePreview.current_created)}</div>
                  {#if updatePreview.remote_last_updated}
                    <div>remote: pushed {fmtRelTime(updatePreview.remote_last_updated)} · {fmtMB(updatePreview.remote_size)}</div>
                  {/if}
                </div>
                <div class="flex gap-3 mt-2 text-xs">
                  {#if updatePreview.docker_hub_url}
                    <a href={updatePreview.docker_hub_url} target="_blank" rel="noopener" class="text-[var(--color-brand-400)] hover:underline inline-flex items-center gap-1">
                      Docker Hub <ExternalLink class="w-3 h-3" />
                    </a>
                  {/if}
                  {#if updatePreview.github_url}
                    <a href={updatePreview.github_url} target="_blank" rel="noopener" class="text-[var(--color-brand-400)] hover:underline inline-flex items-center gap-1">
                      GitHub <ExternalLink class="w-3 h-3" />
                    </a>
                  {/if}
                </div>
              </div>
            </div>
            <Button variant="primary" onclick={doUpdate} loading={updateBusy}>
              <Download class="w-4 h-4" /> Pull & update
            </Button>
          </div>

          {#if updatePreview.warnings && updatePreview.warnings.length > 0}
            <div class="text-xs text-[var(--fg-subtle)] mt-3">
              {updatePreview.warnings.join(' · ')}
            </div>
          {/if}
        </Card>

        {#if updatePreview.latest_release}
          <Card>
            <div class="px-5 py-3 border-b border-[var(--border)] flex items-center justify-between">
              <div class="flex items-center gap-2">
                <h3 class="font-semibold text-sm">
                  {updatePreview.latest_release.name || updatePreview.latest_release.tag}
                </h3>
                <Badge variant="info">{updatePreview.latest_release.tag}</Badge>
              </div>
              <a href={updatePreview.latest_release.url} target="_blank" rel="noopener"
                 class="text-xs text-[var(--color-brand-400)] hover:underline inline-flex items-center gap-1">
                Open release <ExternalLink class="w-3 h-3" />
              </a>
            </div>
            <div class="p-5 max-h-[40vh] overflow-auto">
              <pre class="font-mono text-xs whitespace-pre-wrap break-words text-[var(--fg-muted)]">{updatePreview.latest_release.body || '(no release notes)'}</pre>
            </div>
          </Card>
        {/if}
      {/if}

      <!-- History -->
      {#if updateHistory.length > 0}
        <Card>
          <div class="px-5 py-3 border-b border-[var(--border)] text-xs font-medium text-[var(--fg-muted)] uppercase tracking-wider">
            History
          </div>
          <div class="divide-y divide-[var(--border)]">
            {#each updateHistory as e}
              <div class="flex items-center gap-3 px-5 py-3">
                <div class="flex-1 min-w-0">
                  <div class="font-mono text-sm truncate">{e.image_ref}</div>
                  <div class="text-xs text-[var(--fg-muted)] font-mono truncate">
                    {shortDigest(e.old_digest)} → {shortDigest(e.new_digest)} · {fmtRelTime(e.applied_at)}
                  </div>
                </div>
                {#if e.rolled_back_at}
                  <Badge variant="warning">rolled back</Badge>
                {:else}
                  <Button size="xs" variant="ghost" onclick={() => doRollback(e.id)} disabled={updateBusy}>
                    <Undo2 class="w-3.5 h-3.5" /> Rollback
                  </Button>
                {/if}
              </div>
            {/each}
          </div>
        </Card>
      {/if}
    </div>
  {:else if tab === 'inspect'}
    <Card>
      <pre class="h-[60vh] overflow-auto p-5 font-mono text-xs leading-relaxed json-view">{@html highlightJSON(info)}</pre>
    </Card>
  {/if}
</section>

<style>
  /* JSON syntax highlighting */
  :global(.json-view .json-key) { color: #7dd3fc; }
  :global(.json-view .json-string) { color: #86efac; }
  :global(.json-view .json-number) { color: #fdba74; }
  :global(.json-view .json-bool) { color: #c4b5fd; }
  :global(.json-view .json-null) { color: #94a3b8; font-style: italic; }

  /* Log line colorization */
  :global(.log-line) { color: #d4d4d4; }
  :global(.log-line .log-ts) { color: #6b7280; }
  :global(.log-line .log-error) { color: #f87171; font-weight: 600; }
  :global(.log-line .log-warn) { color: #fbbf24; }
  :global(.log-line .log-info) { color: #60a5fa; }
  :global(.log-line .log-debug) { color: #6b7280; }
  :global(.log-line .log-ok) { color: #4ade80; }
</style>
