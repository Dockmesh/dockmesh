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
  let tab = $state<'logs' | 'exec' | 'inspect'>('logs');

  // Log state
  let logs = $state<string[]>([]);
  let wsConnected = $state(false);
  let autoScroll = $state(true);
  let logContainer: HTMLDivElement | null = $state(null);
  let ws: WebSocket | null = null;

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
      connectLogs();
    } else if (tab === 'exec') {
      disconnect();
      // Wait for the DOM node to exist before attaching xterm.
      tick().then(connectExec);
    } else {
      disconnect();
      disconnectExec();
    }
  });

  $effect(() => () => {
    disconnect();
    disconnectExec();
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
  {:else if tab === 'inspect'}
    <pre class="h-[60vh] overflow-auto p-3 rounded border border-[var(--border)] bg-[var(--panel)] font-mono text-xs">{JSON.stringify(info, null, 2)}</pre>
  {/if}
</section>
