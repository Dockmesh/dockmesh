<script lang="ts">
  import { page } from '$app/stores';
  import { goto } from '$app/navigation';
  import { api, ApiError } from '$lib/api';
  import { tick } from 'svelte';
  import { Terminal } from '@xterm/xterm';
  import { FitAddon } from '@xterm/addon-fit';
  import '@xterm/xterm/css/xterm.css';

  const id = $derived($page.params.id);

  let info = $state<any>(null);
  let loading = $state(true);
  let error = $state('');
  let tab = $state<'logs' | 'exec' | 'stats' | 'inspect'>('logs');

  // Log state
  let logs = $state<string[]>([]);
  let wsConnected = $state(false);
  let autoScroll = $state(true);
  let logContainer: HTMLDivElement | null = $state(null);
  let ws: WebSocket | null = null;

  // Stats state
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

  // Exec state
  let execContainer: HTMLDivElement | null = $state(null);
  let execConnected = $state(false);
  let execShell = $state<'sh' | 'bash'>('sh');
  let term: Terminal | null = null;
  let fitAddon: FitAddon | null = null;
  let execWs: WebSocket | null = null;
  let resizeObserver: ResizeObserver | null = null;

  async function loadInfo() {
    loading = true;
    try {
      info = await api.containers.inspect(id);
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Load failed';
    } finally {
      loading = false;
    }
  }

  async function connectLogs() {
    if (ws) {
      ws.close();
      ws = null;
    }
    logs = [];
    try {
      const { ticket } = await api.ws.ticket();
      const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
      const url = `${proto}//${location.host}/api/v1/ws/logs/${id}?ticket=${ticket}&tail=200`;
      ws = new WebSocket(url);
      ws.onopen = () => {
        wsConnected = true;
      };
      ws.onmessage = async (ev) => {
        logs = [...logs, ev.data as string];
        // Cap at 5000 lines to keep the DOM sane.
        if (logs.length > 5000) logs = logs.slice(-5000);
        if (autoScroll) {
          await tick();
          if (logContainer) {
            logContainer.scrollTop = logContainer.scrollHeight;
          }
        }
      };
      ws.onclose = () => {
        wsConnected = false;
      };
      ws.onerror = () => {
        wsConnected = false;
        logs = [...logs, '[connection error]'];
      };
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'WS ticket failed';
    }
  }

  function disconnect() {
    if (ws) {
      ws.close();
      ws = null;
    }
    wsConnected = false;
  }

  async function connectStats() {
    disconnectStats();
    try {
      const { ticket } = await api.ws.ticket();
      const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
      statsWs = new WebSocket(`${proto}//${location.host}/api/v1/ws/stats/${id}?ticket=${ticket}`);
      statsWs.onopen = () => { statsConnected = true; };
      statsWs.onmessage = (ev) => {
        try {
          const s = JSON.parse(ev.data) as StatsSample;
          if ((s as any).error) return;
          stats = s;
          statsHistory = [...statsHistory, s].slice(-60);
        } catch { /* ignore */ }
      };
      statsWs.onclose = () => { statsConnected = false; };
      statsWs.onerror = () => { statsConnected = false; };
    } catch {
      statsConnected = false;
    }
  }

  function disconnectStats() {
    if (statsWs) {
      statsWs.close();
      statsWs = null;
    }
    statsConnected = false;
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
      .map((v, i) => {
        const x = (i * stepX).toFixed(1);
        const y = (h - (v / m) * h).toFixed(1);
        return `${i === 0 ? 'M' : 'L'}${x},${y}`;
      })
      .join(' ');
  }

  async function connectExec() {
    disconnectExec();
    if (!execContainer) return;

    term = new Terminal({
      fontFamily: 'JetBrains Mono, Menlo, Consolas, monospace',
      fontSize: 13,
      cursorBlink: true,
      theme: {
        background: '#000000',
        foreground: '#e7ecf5'
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
      const url = `${proto}//${location.host}/api/v1/ws/exec/${id}?ticket=${ticket}&cmd=${encodeURIComponent(cmd)}`;
      execWs = new WebSocket(url);
      execWs.binaryType = 'arraybuffer';

      execWs.onopen = () => {
        execConnected = true;
        // Send initial resize
        const { cols, rows } = term!;
        execWs!.send(JSON.stringify({ type: 'resize', cols, rows }));
      };
      execWs.onmessage = (ev) => {
        if (typeof ev.data === 'string') {
          // Error JSON from server
          term!.write(`\r\n\x1b[31m${ev.data}\x1b[0m\r\n`);
        } else {
          term!.write(new Uint8Array(ev.data));
        }
      };
      execWs.onclose = () => {
        execConnected = false;
        term?.write('\r\n\x1b[33m[session closed]\x1b[0m\r\n');
      };
      execWs.onerror = () => {
        execConnected = false;
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
      error = err instanceof ApiError ? err.message : 'exec connect failed';
    }
  }

  function disconnectExec() {
    resizeObserver?.disconnect();
    resizeObserver = null;
    if (execWs) {
      execWs.close();
      execWs = null;
    }
    if (term) {
      term.dispose();
      term = null;
    }
    fitAddon = null;
    execConnected = false;
  }

  async function action(op: 'start' | 'stop' | 'restart') {
    try {
      if (op === 'start') await api.containers.start(id);
      else if (op === 'stop') await api.containers.stop(id);
      else if (op === 'restart') await api.containers.restart(id);
      await loadInfo();
      // Reconnect logs since the container may have been recreated.
      if (tab === 'logs') connectLogs();
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Action failed';
    }
  }

  async function remove() {
    if (!confirm('Remove this container?')) return;
    try {
      await api.containers.remove(id, true);
      goto('/containers');
    } catch (err) {
      error = err instanceof ApiError ? err.message : 'Remove failed';
    }
  }

  $effect(() => {
    if (id) {
      loadInfo();
    }
  });

  // Tab effect: open/close WS + terminal when switching tabs.
  $effect(() => {
    if (tab === 'logs') {
      disconnectExec();
      disconnectStats();
      connectLogs();
    } else if (tab === 'exec') {
      disconnect();
      disconnectStats();
      tick().then(connectExec);
    } else if (tab === 'stats') {
      disconnect();
      disconnectExec();
      statsHistory = [];
      connectStats();
    } else {
      disconnect();
      disconnectExec();
      disconnectStats();
    }
  });

  $effect(() => () => {
    disconnect();
    disconnectExec();
    disconnectStats();
  });

  function containerName(inf: any): string {
    if (!inf) return '';
    return (inf.Name ?? '').replace(/^\//, '');
  }

  function portList(inf: any): string {
    if (!inf?.NetworkSettings?.Ports) return '';
    const out: string[] = [];
    for (const [priv, bindings] of Object.entries(inf.NetworkSettings.Ports)) {
      if (Array.isArray(bindings) && bindings.length > 0) {
        for (const b of bindings as any[]) {
          out.push(`${b.HostPort}→${priv}`);
        }
      }
    }
    return out.join(', ');
  }
</script>

<section class="space-y-4">
  <div class="flex items-center gap-3">
    <a href="/containers" class="text-[var(--muted)] hover:text-[var(--fg)]">← Containers</a>
    <h2 class="text-xl font-semibold font-mono">{containerName(info) || id.slice(0, 12)}</h2>
    {#if info}
      <span class="text-xs px-2 py-0.5 rounded {info.State?.Running ? 'bg-green-500/20 text-green-500' : 'bg-[var(--bg)] text-[var(--muted)]'}">
        {info.State?.Status ?? '?'}
      </span>
    {/if}
  </div>

  {#if error}
    <div class="p-3 rounded border border-red-500/30 bg-red-500/10 text-red-500 text-sm">{error}</div>
  {/if}

  {#if info}
    <div class="flex flex-wrap gap-2">
      {#if info.State?.Running}
        <button class="px-3 py-1 text-sm border border-[var(--border)] rounded" onclick={() => action('restart')}>Restart</button>
        <button class="px-3 py-1 text-sm border border-[var(--border)] rounded" onclick={() => action('stop')}>Stop</button>
      {:else}
        <button class="px-3 py-1 text-sm border border-[var(--border)] rounded" onclick={() => action('start')}>Start</button>
      {/if}
      <button class="px-3 py-1 text-sm border border-red-500/50 text-red-500 rounded ml-auto" onclick={remove}>Remove</button>
    </div>

    <div class="grid grid-cols-1 md:grid-cols-3 gap-3 text-sm">
      <div class="p-3 rounded border border-[var(--border)] bg-[var(--panel)]">
        <div class="text-xs text-[var(--muted)]">Image</div>
        <div class="font-mono truncate">{info.Config?.Image}</div>
      </div>
      <div class="p-3 rounded border border-[var(--border)] bg-[var(--panel)]">
        <div class="text-xs text-[var(--muted)]">Ports</div>
        <div class="font-mono">{portList(info) || '—'}</div>
      </div>
      <div class="p-3 rounded border border-[var(--border)] bg-[var(--panel)]">
        <div class="text-xs text-[var(--muted)]">Created</div>
        <div class="font-mono text-xs">{info.Created?.slice(0, 19).replace('T', ' ')}</div>
      </div>
    </div>
  {/if}

  <div class="border-b border-[var(--border)] flex gap-0">
    <button
      class="px-4 py-2 text-sm border-b-2 {tab === 'logs' ? 'border-brand-500 text-[var(--fg)]' : 'border-transparent text-[var(--muted)]'}"
      onclick={() => (tab = 'logs')}
    >
      Logs {#if wsConnected && tab === 'logs'}<span class="w-1.5 h-1.5 inline-block rounded-full bg-green-500 ml-1"></span>{/if}
    </button>
    <button
      class="px-4 py-2 text-sm border-b-2 {tab === 'exec' ? 'border-brand-500 text-[var(--fg)]' : 'border-transparent text-[var(--muted)]'}"
      onclick={() => (tab = 'exec')}
    >
      Terminal {#if execConnected && tab === 'exec'}<span class="w-1.5 h-1.5 inline-block rounded-full bg-green-500 ml-1"></span>{/if}
    </button>
    <button
      class="px-4 py-2 text-sm border-b-2 {tab === 'stats' ? 'border-brand-500 text-[var(--fg)]' : 'border-transparent text-[var(--muted)]'}"
      onclick={() => (tab = 'stats')}
    >
      Stats {#if statsConnected && tab === 'stats'}<span class="w-1.5 h-1.5 inline-block rounded-full bg-green-500 ml-1"></span>{/if}
    </button>
    <button
      class="px-4 py-2 text-sm border-b-2 {tab === 'inspect' ? 'border-brand-500 text-[var(--fg)]' : 'border-transparent text-[var(--muted)]'}"
      onclick={() => (tab = 'inspect')}
    >
      Inspect
    </button>
  </div>

  {#if tab === 'logs'}
    <div class="flex items-center gap-3 text-sm">
      <label class="flex items-center gap-1">
        <input type="checkbox" bind:checked={autoScroll} /> auto-scroll
      </label>
      <button class="px-2 py-0.5 border border-[var(--border)] rounded" onclick={() => (logs = [])}>Clear</button>
      {#if wsConnected}
        <button class="px-2 py-0.5 border border-[var(--border)] rounded" onclick={disconnect}>Disconnect</button>
      {:else}
        <button class="px-2 py-0.5 border border-[var(--border)] rounded" onclick={connectLogs}>Reconnect</button>
      {/if}
      <span class="text-[var(--muted)] text-xs ml-auto">{logs.length} lines</span>
    </div>
    <div
      bind:this={logContainer}
      class="h-[60vh] overflow-auto p-3 rounded border border-[var(--border)] bg-black text-green-200 font-mono text-xs leading-relaxed"
    >
      {#each logs as line, i (i)}
        <div class="whitespace-pre-wrap break-all">{line}</div>
      {/each}
      {#if logs.length === 0 && wsConnected}
        <div class="text-[var(--muted)]">waiting for log output…</div>
      {:else if logs.length === 0}
        <div class="text-[var(--muted)]">disconnected</div>
      {/if}
    </div>
  {:else if tab === 'exec'}
    <div class="flex items-center gap-3 text-sm">
      <label class="flex items-center gap-1">
        Shell:
        <select class="px-2 py-0.5 rounded border border-[var(--border)] bg-[var(--bg)] ml-1" bind:value={execShell}>
          <option value="sh">/bin/sh</option>
          <option value="bash">/bin/bash</option>
        </select>
      </label>
      <button class="px-2 py-0.5 border border-[var(--border)] rounded" onclick={connectExec}>Reconnect</button>
      {#if execConnected}
        <span class="text-xs text-[var(--muted)] ml-auto">connected</span>
      {:else}
        <span class="text-xs text-[var(--muted)] ml-auto">disconnected</span>
      {/if}
    </div>
    <div
      bind:this={execContainer}
      class="h-[60vh] rounded border border-[var(--border)] bg-black p-2"
    ></div>
  {:else if tab === 'stats'}
    {#if !stats}
      <p class="text-[var(--muted)]">waiting for first sample…</p>
    {:else}
      <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
        <div class="p-4 rounded border border-[var(--border)] bg-[var(--panel)]">
          <div class="flex justify-between items-baseline mb-2">
            <div class="text-sm text-[var(--muted)]">CPU</div>
            <div class="text-2xl font-bold font-mono">{stats.cpu_percent.toFixed(1)}<span class="text-sm text-[var(--muted)]">%</span></div>
          </div>
          <svg viewBox="0 0 300 60" class="w-full h-14">
            <path d={sparkPath(statsHistory.map(s => s.cpu_percent), 100, 300, 60)} fill="none" stroke="#2563eb" stroke-width="2" />
          </svg>
        </div>
        <div class="p-4 rounded border border-[var(--border)] bg-[var(--panel)]">
          <div class="flex justify-between items-baseline mb-2">
            <div class="text-sm text-[var(--muted)]">Memory</div>
            <div class="text-2xl font-bold font-mono">{stats.mem_percent.toFixed(1)}<span class="text-sm text-[var(--muted)]">%</span></div>
          </div>
          <div class="text-xs text-[var(--muted)] mb-1">{formatBytes(stats.mem_used)} / {formatBytes(stats.mem_limit)}</div>
          <svg viewBox="0 0 300 60" class="w-full h-14">
            <path d={sparkPath(statsHistory.map(s => s.mem_percent), 100, 300, 60)} fill="none" stroke="#16a34a" stroke-width="2" />
          </svg>
        </div>
        <div class="p-4 rounded border border-[var(--border)] bg-[var(--panel)]">
          <div class="text-sm text-[var(--muted)] mb-2">Network</div>
          <div class="flex gap-6 text-sm font-mono">
            <div><span class="text-[var(--muted)]">↓ rx:</span> {formatBytes(stats.net_rx)}</div>
            <div><span class="text-[var(--muted)]">↑ tx:</span> {formatBytes(stats.net_tx)}</div>
          </div>
        </div>
        <div class="p-4 rounded border border-[var(--border)] bg-[var(--panel)]">
          <div class="text-sm text-[var(--muted)] mb-2">Block I/O + PIDs</div>
          <div class="flex gap-4 text-sm font-mono flex-wrap">
            <div><span class="text-[var(--muted)]">read:</span> {formatBytes(stats.blk_read)}</div>
            <div><span class="text-[var(--muted)]">write:</span> {formatBytes(stats.blk_write)}</div>
            <div><span class="text-[var(--muted)]">pids:</span> {stats.pids_current}</div>
          </div>
        </div>
      </div>
      <div class="text-xs text-[var(--muted)]">{statsHistory.length} samples (rolling 60s)</div>
    {/if}
  {:else if tab === 'inspect'}
    <pre class="h-[60vh] overflow-auto p-3 rounded border border-[var(--border)] bg-[var(--panel)] font-mono text-xs">{JSON.stringify(info, null, 2)}</pre>
  {/if}
</section>
