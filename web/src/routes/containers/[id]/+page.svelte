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
    Link as LinkIcon
  } from 'lucide-svelte';

  const id = $derived($page.params.id);

  let info = $state<any>(null);
  let loading = $state(true);
  let tab = $state<'logs' | 'exec' | 'stats' | 'inspect'>('logs');

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
      info = await api.containers.inspect(id);
    } catch (err) {
      toast.error('Load failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      loading = false;
    }
  }

  // ---------- Logs ----------
  async function connectLogs() {
    disconnectLogs();
    logs = [];
    try {
      const { ticket } = await api.ws.ticket();
      const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
      ws = new WebSocket(`${proto}//${location.host}/api/v1/ws/logs/${id}?ticket=${ticket}&tail=200`);
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
      statsWs = new WebSocket(`${proto}//${location.host}/api/v1/ws/stats/${id}?ticket=${ticket}`);
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
      execWs = new WebSocket(`${proto}//${location.host}/api/v1/ws/exec/${id}?ticket=${ticket}&cmd=${encodeURIComponent(cmd)}`);
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
      if (op === 'start') await api.containers.start(id);
      else if (op === 'stop') await api.containers.stop(id);
      else await api.containers.restart(id);
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
      await api.containers.remove(id, true);
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
    if (tab === 'logs') {
      disconnectExec();
      disconnectStats();
      connectLogs();
    } else if (tab === 'exec') {
      disconnectLogs();
      disconnectStats();
      tick().then(connectExec);
    } else if (tab === 'stats') {
      disconnectLogs();
      disconnectExec();
      connectStats();
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
    {@render tabBtn('logs', 'Logs', FileText, wsConnected)}
    {@render tabBtn('exec', 'Terminal', TerminalIcon, execConnected)}
    {@render tabBtn('stats', 'Stats', Activity, statsConnected)}
    {@render tabBtn('inspect', 'Inspect', Code2, false)}
  </div>

  <!-- Tab panels -->
  {#if tab === 'logs'}
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
        <div class="whitespace-pre-wrap break-all text-[#a8c5a8]">{line}</div>
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
  {:else if tab === 'stats'}
    {#if !stats}
      <Card class="p-8 text-center text-sm text-[var(--fg-muted)]">waiting for first sample…</Card>
    {:else}
      <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
        <Card class="p-5">
          <div class="flex justify-between items-baseline mb-3">
            <div class="text-xs text-[var(--fg-muted)] uppercase tracking-wider font-medium">CPU</div>
            <div class="text-2xl font-semibold font-mono tabular-nums">
              {stats.cpu_percent.toFixed(1)}<span class="text-sm text-[var(--fg-muted)]">%</span>
            </div>
          </div>
          <svg viewBox="0 0 300 60" class="w-full h-16">
            <defs>
              <linearGradient id="cpu-grad" x1="0" y1="0" x2="0" y2="1">
                <stop offset="0%" stop-color="#06b6d4" stop-opacity="0.3" />
                <stop offset="100%" stop-color="#06b6d4" stop-opacity="0" />
              </linearGradient>
            </defs>
            <path d={sparkArea(statsHistory.map(s => s.cpu_percent), 100, 300, 60)} fill="url(#cpu-grad)" />
            <path d={sparkPath(statsHistory.map(s => s.cpu_percent), 100, 300, 60)} fill="none" stroke="#06b6d4" stroke-width="2" stroke-linejoin="round" />
          </svg>
        </Card>
        <Card class="p-5">
          <div class="flex justify-between items-baseline mb-3">
            <div class="text-xs text-[var(--fg-muted)] uppercase tracking-wider font-medium">Memory</div>
            <div class="text-2xl font-semibold font-mono tabular-nums">
              {stats.mem_percent.toFixed(1)}<span class="text-sm text-[var(--fg-muted)]">%</span>
            </div>
          </div>
          <div class="text-xs text-[var(--fg-muted)] mb-2 font-mono">{formatBytes(stats.mem_used)} / {formatBytes(stats.mem_limit)}</div>
          <svg viewBox="0 0 300 60" class="w-full h-16">
            <defs>
              <linearGradient id="mem-grad" x1="0" y1="0" x2="0" y2="1">
                <stop offset="0%" stop-color="#22c55e" stop-opacity="0.3" />
                <stop offset="100%" stop-color="#22c55e" stop-opacity="0" />
              </linearGradient>
            </defs>
            <path d={sparkArea(statsHistory.map(s => s.mem_percent), 100, 300, 60)} fill="url(#mem-grad)" />
            <path d={sparkPath(statsHistory.map(s => s.mem_percent), 100, 300, 60)} fill="none" stroke="#22c55e" stroke-width="2" stroke-linejoin="round" />
          </svg>
        </Card>
        <Card class="p-5">
          <div class="text-xs text-[var(--fg-muted)] uppercase tracking-wider font-medium mb-3">Network</div>
          <div class="grid grid-cols-2 gap-4">
            <div>
              <div class="text-xs text-[var(--fg-subtle)]">↓ received</div>
              <div class="text-lg font-mono tabular-nums">{formatBytes(stats.net_rx)}</div>
            </div>
            <div>
              <div class="text-xs text-[var(--fg-subtle)]">↑ sent</div>
              <div class="text-lg font-mono tabular-nums">{formatBytes(stats.net_tx)}</div>
            </div>
          </div>
        </Card>
        <Card class="p-5">
          <div class="text-xs text-[var(--fg-muted)] uppercase tracking-wider font-medium mb-3">Block I/O · PIDs</div>
          <div class="grid grid-cols-3 gap-4">
            <div>
              <div class="text-xs text-[var(--fg-subtle)]">read</div>
              <div class="text-sm font-mono tabular-nums">{formatBytes(stats.blk_read)}</div>
            </div>
            <div>
              <div class="text-xs text-[var(--fg-subtle)]">write</div>
              <div class="text-sm font-mono tabular-nums">{formatBytes(stats.blk_write)}</div>
            </div>
            <div>
              <div class="text-xs text-[var(--fg-subtle)]">pids</div>
              <div class="text-sm font-mono tabular-nums">{stats.pids_current}</div>
            </div>
          </div>
        </Card>
      </div>
      <div class="text-xs text-[var(--fg-subtle)]">{statsHistory.length} samples (rolling 60s)</div>
    {/if}
  {:else if tab === 'inspect'}
    <Card>
      <pre class="h-[60vh] overflow-auto p-5 font-mono text-xs leading-relaxed">{JSON.stringify(info, null, 2)}</pre>
    </Card>
  {/if}
</section>
