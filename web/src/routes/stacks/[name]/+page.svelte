<script lang="ts">
  import { page } from '$app/state';
  import { fmtAgo, fmtBytes, fmtMem } from '$lib/format';
  import { goto } from '$app/navigation';
  import { untrack } from 'svelte';
  import { api, ApiError, runRestorable, type ScaleCheck, type PreflightResult, type Migration, type DeployHistoryEntry, type StackDependencies, type StackEnvironments, type StackCleanupPlan } from '$lib/api';
  import { Button, Badge, Skeleton, Modal } from '$lib/components/ui';
  import StatusChip, { type Tone } from '$lib/components/ui/StatusChip.svelte';
  import Tabs, { type TabItem } from '$lib/components/ui/Tabs.svelte';
  import Menu, { type MenuItem } from '$lib/components/ui/Menu.svelte';
  import OpProgress from '$lib/components/operations/OpProgress.svelte';
  import { operations, isActive, opRef } from '$lib/stores/operations.svelte';
  import { ArrowRight, ExternalLink } from 'lucide-svelte';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { stackOps } from '$lib/stores/stackOps.svelte';
  import { allowed } from '$lib/rbac.svelte';
  import { hosts } from '$lib/stores/host.svelte';
  import { pageContext } from '$lib/stores/pageContext.svelte';
  import { EventStream } from '$lib/events';
  import { Play, Square, Save, Trash2, AlertTriangle, RefreshCw, Maximize2, ArrowRightLeft, CheckCircle2, XCircle, Loader2, GitBranch, Link as LinkIcon, Unlink, History, RotateCcw, User, Plus, X, Layers, Repeat, Pencil, Eye, EyeOff } from 'lucide-svelte';
  import type { StackGitSource, StackGitSourceInput } from '$lib/api';

  import { isAllHosts } from '$lib/stores/host.svelte';

  // The stack detail page always operates on a specific host, never "all".
  // If the global picker is on "all", we resolve to the deployment's host
  // (from the list response) or fall back to "local".
  let deploymentHostId = $state<string>('local');
  const stackHost = $derived(isAllHosts(hosts.id) ? deploymentHostId : hosts.id);
  const isRemote = $derived(stackHost !== 'local');

  const name = $derived(page.params.name);

  // Scope-aware: stack-update / .deploy also require role-scope to cover
  // this stack's name (and host if known). Unscoped roles short-circuit
  // to "matches everything" in the scopeOK() helper.
  const canWrite = $derived(allowed('stacks.update', { stack: name, host: stackHost }));
  const canDeploy = $derived(allowed('stacks.deploy', { stack: name, host: stackHost }));

  $effect(() => {
    pageContext.set(name);
    return () => pageContext.clear();
  });

  let compose = $state('');
  let env = $state('');
  let services = $state<Array<{ service: string; container_id: string; state: string; status: string; image: string }>>([]);
  let loading = $state(true);
  // status === 'needs_recovery' when compose.yaml is missing/empty but
  // there's still a deployment row or running containers — UI renders
  // the recovery panel instead of the regular editor in that case.
  let stackStatus = $state<'ok' | 'needs_recovery'>('ok');
  // busy = this page kicked off an op; globalBusy = any tab (incl. this
  // one after a re-mount) knows an op is running for this stack on this
  // host. Disable destructive buttons on either.
  let busy = $state(false);
  const globalBusy = $derived(stackOps.isBusy(stackHost, name));
  const anyBusy = $derived(busy || globalBusy);

  // Live deploy progress — populated by polling /stacks/{name}/deploy/progress
  // while a deploy is in flight. Replaces the static "deploying…" pill
  // with phase + current service + elapsed seconds.
  type DeployProgress = {
    stack: string;
    phase: 'starting' | 'pulling' | 'creating' | 'starting_container' | 'healthcheck' | 'done' | 'failed';
    service?: string;
    image?: string;
    step_index: number;
    step_total: number;
    started_at: string;
    updated_at: string;
    error?: string;
  };
  let deployProgress = $state<DeployProgress | null>(null);
  let deployElapsed = $state(0);
  // Tick a clock every second so the elapsed label updates without
  // re-polling the backend. Server's progress poll runs slower.
  $effect(() => {
    if (!deployProgress) return;
    if (deployProgress.phase === 'done' || deployProgress.phase === 'failed') return;
    const id = setInterval(() => {
      deployElapsed = Math.floor((Date.now() - new Date(deployProgress!.started_at).getTime()) / 1000);
    }, 1000);
    return () => clearInterval(id);
  });
  // Poll the progress endpoint every 1.5 s while ANY deploy could be
  // running (anyBusy from local trigger OR active state seen). Drops
  // polling when phase is terminal so we don't hammer the backend.
  $effect(() => {
    const active = anyBusy || (deployProgress && deployProgress.phase !== 'done' && deployProgress.phase !== 'failed');
    if (!active) return;
    let cancelled = false;
    async function tick() {
      try {
        const p = await api.stacks.deployProgress(name);
        if (cancelled) return;
        deployProgress = p as DeployProgress | null;
      } catch { /* poll-tolerant */ }
    }
    tick();
    const id = setInterval(tick, 1500);
    return () => { cancelled = true; clearInterval(id); };
  });

  // Single short string the subtitle renders next to "X / N running"
  // when a deploy is in progress. Returns "" outside of an active deploy.
  function deployStatusLabel(p: DeployProgress | null, elapsed: number): string {
    if (!p) return '';
    const elapsedStr = elapsed >= 60
      ? `${Math.floor(elapsed / 60)}:${String(elapsed % 60).padStart(2, '0')}`
      : `${elapsed}s`;
    const stepStr = p.step_total > 0 && p.step_index > 0 ? `${p.step_index}/${p.step_total}` : '';
    switch (p.phase) {
      case 'starting':         return `deploying… ${elapsedStr}`;
      case 'pulling':          return `pulling ${p.service ?? ''} ${stepStr ? `(${stepStr})` : ''} · ${elapsedStr}`;
      case 'creating':         return `creating ${p.service ?? ''} ${stepStr ? `(${stepStr})` : ''} · ${elapsedStr}`;
      case 'starting_container': return `starting ${p.service ?? ''} ${stepStr ? `(${stepStr})` : ''} · ${elapsedStr}`;
      case 'healthcheck':      return `healthcheck ${p.service ?? ''} ${stepStr ? `(${stepStr})` : ''} · ${elapsedStr}`;
      case 'done':             return `deploy ok · ${elapsedStr}`;
      case 'failed':           return `deploy failed · ${elapsedStr}`;
    }
    return '';
  }

  let externalChange = $state<{ file: string; type: string } | null>(null);
  let modifiedOutside = $state<string[]>([]);
  let mountsStackDir = $state(false);

  // A deploy of stack files changed outside Dockmesh needs an explicit OK.
  async function confirmExternalChanges(err: unknown): Promise<boolean> {
    if (!(err instanceof ApiError) || err.status !== 409 || err.body?.code !== 'modified_outside') return false;
    const files: string = (err.body.files as string[] | undefined)?.join(', ') || 'compose.yaml or .env';
    return confirm.ask({
      title: 'Stack files changed outside Dockmesh',
      message: `${files} of "${err.body.stack ?? name}" changed since Dockmesh last wrote them.`,
      body: 'Something other than Dockmesh changed these files, for example a container that mounts the stack directory. Review them in the editor before you deploy them as they are.',
      confirmLabel: 'Deploy anyway', danger: true,
    });
  }
  let dirty = $state(false);

  type TabKey = 'overview' | 'compose' | 'environment' | 'logs' | 'history' | 'settings';
  let activeTab = $state<TabKey>('overview');

  // Live logs: one WebSocket per running service container, merged into one
  // stream. The sockets are closed when the tab is left so none leak.
  type LogLine = { service: string; line: string; ts: number };
  let logLines = $state<LogLine[]>([]);
  let logSockets = $state<Map<string, WebSocket>>(new Map());
  let logTailing = $state(true);
  let logServiceFilter = $state<Set<string>>(new Set());
  let logEl = $state<HTMLDivElement | null>(null);
  const LOG_BUFFER_MAX = 1500;
  // Bumped when all streams close, so a ticket that resolves afterwards
  // (tab left, page destroyed) does not open an orphaned socket.
  let logGen = 0;

  async function openLogStreamForService(svc: { service: string; container_id: string }) {
    if (!svc.container_id) return;
    if (logSockets.has(svc.service)) return;
    const gen = logGen;
    try {
      const { ticket } = await api.ws.ticket('containers.logs');
      if (gen !== logGen || logSockets.has(svc.service)) return;
      const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
      const hostQs = isRemote ? `&host=${encodeURIComponent(stackHost)}` : '';
      const ws = new WebSocket(
        `${proto}//${location.host}/api/v1/ws/logs/${svc.container_id}?ticket=${ticket}&tail=80${hostQs}`
      );
      ws.onmessage = (ev) => {
        const raw = String(ev.data ?? '').replace(/\r$/, '');
        if (!raw) return;
        // Strip Docker's RFC3339 prefix; the UI renders its own time column.
        const tsMatch = raw.match(/^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z)\s/);
        let line = raw;
        let ts = Date.now();
        if (tsMatch) {
          const parsed = new Date(tsMatch[1]).getTime();
          if (!Number.isNaN(parsed)) ts = parsed;
          line = raw.slice(tsMatch[0].length);
        }
        logLines = [...logLines.slice(-(LOG_BUFFER_MAX - 1)), { service: svc.service, line, ts }];
        if (logTailing) requestAnimationFrame(() => {
          if (logEl) logEl.scrollTop = logEl.scrollHeight;
        });
      };
      ws.onclose = () => {
        if (logSockets.get(svc.service) !== ws) return;
        logSockets.delete(svc.service);
        logSockets = new Map(logSockets);
      };
      ws.onerror = () => { /* close handler will clean up */ };
      logSockets.set(svc.service, ws);
      logSockets = new Map(logSockets);
    } catch {
      /* ignore — single-service failure shouldn't break the merged view */
    }
  }

  function closeAllLogStreams() {
    logGen++;
    for (const ws of logSockets.values()) {
      try { ws.close(); } catch { /* ignore */ }
    }
    logSockets.clear();
    logSockets = new Map();
  }

  async function startLogStreaming() {
    logLines = [];
    closeAllLogStreams();
    if (!logTailing) return;
    const running = services.filter((s) => s.state === 'running' && s.container_id);
    await Promise.all(running.map((s) => openLogStreamForService(s)));
  }

  function toggleLogTailing() {
    logTailing = !logTailing;
    if (logTailing) startLogStreaming();
    else closeAllLogStreams();
  }

  function toggleLogService(name: string) {
    if (logServiceFilter.has(name)) logServiceFilter.delete(name);
    else logServiceFilter.add(name);
    logServiceFilter = new Set(logServiceFilter);
  }

  // Per-service colour for the log-svc badge — stable hash → palette index.
  const LOG_PALETTE = [
    'var(--accent-fg)',
    'var(--color-success-400)',
    'var(--color-warning-400)',
    'var(--color-danger-400)',
    'var(--color-brand-300)',
    'var(--color-brand-400)',
  ];
  function svcColor(name: string): string {
    let h = 0;
    for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) >>> 0;
    return LOG_PALETTE[h % LOG_PALETTE.length];
  }

  function logLevelKind(line: string): '' | 'warn' | 'err' | 'ok' {
    const l = line.toLowerCase();
    if (/(\berror\b|\bfatal\b|panic|level=error)/.test(l)) return 'err';
    if (/(\bwarn\b|level=warn)/.test(l)) return 'warn';
    if (/(\binfo\b.*started|listening|ready)/.test(l)) return 'ok';
    return '';
  }

  const visibleLogLines = $derived.by(() => {
    if (logServiceFilter.size === 0) return logLines;
    return logLines.filter((l) => logServiceFilter.has(l.service));
  });

  // Depends on activeTab only: without untrack every services poll would
  // reopen the sockets.
  $effect(() => {
    const tab = activeTab;
    if (tab === 'logs') untrack(() => { startLogStreaming(); });
    else untrack(() => { closeAllLogStreams(); });
    return () => {
      untrack(() => { closeAllLogStreams(); });
    };
  });

  // ── Compose-derived metadata for the Overview right rail.
  // Lightweight regex parsing — good enough for the dashboard widgets;
  // the canonical parse still happens server-side at deploy time.
  function parseEndpoints(yaml: string): Array<{ service: string; port: string; mode: 'host' | 'internal' }> {
    if (!yaml) return [];
    const out: Array<{ service: string; port: string; mode: 'host' | 'internal' }> = [];
    // services: header → service blocks → ports: list. Track current
    // service via 2-space indent on the service name.
    const lines = yaml.split('\n');
    let inServices = false;
    let currentService = '';
    let inPortsList = false;
    let portsIndent = -1;
    for (const raw of lines) {
      const line = raw.replace(/\r$/, '');
      // Top-level keys reset all blocks.
      if (/^\S/.test(line)) {
        inServices = /^services\s*:/.test(line);
        currentService = '';
        inPortsList = false;
        portsIndent = -1;
        continue;
      }
      if (!inServices) continue;
      const svcMatch = line.match(/^ {2}([\w.-]+)\s*:\s*$/);
      if (svcMatch) {
        currentService = svcMatch[1];
        inPortsList = false;
        portsIndent = -1;
        continue;
      }
      if (!currentService) continue;
      const portsMatch = line.match(/^( {4,})ports\s*:\s*(.*)$/);
      if (portsMatch) {
        portsIndent = portsMatch[1].length;
        inPortsList = true;
        // Inline list form `ports: ["8080:80"]`
        const inline = portsMatch[2].trim();
        if (inline.startsWith('[')) {
          const inner = inline.replace(/^\[|\]$/g, '');
          for (const item of inner.split(',')) {
            const p = item.trim().replace(/^["']|["']$/g, '');
            if (p) out.push(parsePortSpec(currentService, p));
          }
          inPortsList = false;
        }
        continue;
      }
      if (inPortsList) {
        const itemIndent = line.match(/^( *)/)?.[1].length ?? 0;
        if (itemIndent <= portsIndent) {
          inPortsList = false;
          continue;
        }
        const item = line.trim();
        if (item.startsWith('-')) {
          const p = item.slice(1).trim().replace(/^["']|["']$/g, '');
          if (p) out.push(parsePortSpec(currentService, p));
        }
      }
    }
    return out;
  }

  function parsePortSpec(service: string, spec: string): { service: string; port: string; mode: 'host' | 'internal' } {
    // Forms we accept (best-effort):
    //   "8080:80" / "127.0.0.1:8080:80" / "8080:80/tcp" / "80" (internal-only)
    const m = spec.match(/^(?:[\d.]+:)?(\d+):(\d+)(?:\/(\w+))?$/);
    if (m) {
      const proto = m[3] ? `/${m[3]}` : '';
      return { service, port: `:${m[1]} → ${m[2]}${proto}`, mode: 'host' };
    }
    return { service, port: spec, mode: 'internal' };
  }

  function parseTopLevelList(yaml: string, key: 'volumes' | 'networks'): string[] {
    if (!yaml) return [];
    const out: string[] = [];
    const lines = yaml.split('\n');
    let inBlock = false;
    for (const raw of lines) {
      const line = raw.replace(/\r$/, '');
      if (/^\S/.test(line)) {
        inBlock = new RegExp(`^${key}\\s*:`).test(line);
        continue;
      }
      if (!inBlock) continue;
      const m = line.match(/^ {2}([\w.-]+)\s*:\s*(.*)$/);
      if (m) out.push(m[1]);
    }
    return out;
  }

  const composeEndpoints = $derived(parseEndpoints(compose));
  const composeVolumes = $derived(parseTopLevelList(compose, 'volumes'));
  const composeNetworks = $derived(parseTopLevelList(compose, 'networks'));

  // Compose-tab edit/redeploy guard. The yaml viewer is read-only by
  // default — operators can't accidentally typo a service while passing
  // through. "Edit" toggles to a textarea, "Save" persists the diff
  // and (for "Save & redeploy") kicks off a redeploy in one click.
  let composeEditing = $state(false);
  function startComposeEdit() { composeEditing = true; }
  async function saveCompose() {
    await save();
    composeEditing = false;
  }
  async function saveAndRedeploy() {
    await save();
    composeEditing = false;
    if (canDeploy) await deploy();
  }

  // Colour cues for the read-only compose view; the server parses the YAML.
  function escapeHtml(s: string): string {
    return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
  }
  function highlightYamlLine(raw: string): string {
    // Order matters: wrap strings BEFORE keys + numbers, so the key/number
    // regex can't match content inside `class="y-str"` spans we already
    // inserted. Lines that start with a quoted token still escape the
    // key regex because the leading char is `<` (not a word char) after
    // string-wrapping.
    let l = escapeHtml(raw);
    l = l.replace(/(#.*)$/, '<span class="y-com">$1</span>');
    l = l.replace(/('[^']*'|"[^"]*")/g, '<span class="y-str">$1</span>');
    l = l.replace(/^(\s*)([\w.-]+)(\s*:)/, '$1<span class="y-key">$2</span>$3');
    l = l.replace(/(:\s+)(\d+(?:\.\d+)?)(\s|$)/g, '$1<span class="y-num">$2</span>$3');
    return l;
  }
  const composeLines = $derived(compose.split('\n'));

  // ── Live per-container stats for the Resources rollup on Overview.
  // Open one stats WS per running service, aggregate cpu / memory /
  // network in real time. Sockets are torn down when the operator
  // leaves Overview.
  type StatsSample = {
    cpu_percent: number;
    mem_used: number;
    mem_limit: number;
    mem_percent: number;
    net_rx: number;
    net_tx: number;
  };
  let svcStats = $state<Map<string, StatsSample>>(new Map());
  let statsSockets = $state<Map<string, WebSocket>>(new Map());
  // Track previous net counters to derive a rate (B/s).
  type NetPrev = { rx: number; tx: number; ts: number; rate?: { rx: number; tx: number } };
  const statsPrev: Map<string, NetPrev> = new Map();
  let netRate = $state<{ rx: number; tx: number }>({ rx: 0, tx: 0 });
  let statsGen = 0;

  async function openStatsForService(svc: { service: string; container_id: string; state: string }) {
    if (statsSockets.has(svc.service)) return;
    if (!svc.container_id || svc.state !== 'running') return;
    const gen = statsGen;
    try {
      const { ticket } = await api.ws.ticket('containers.view');
      if (gen !== statsGen || statsSockets.has(svc.service)) return;
      const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
      const hostQs = isRemote ? `&host=${encodeURIComponent(stackHost)}` : '';
      const ws = new WebSocket(
        `${proto}//${location.host}/api/v1/ws/stats/${svc.container_id}?ticket=${ticket}${hostQs}`
      );
      ws.onmessage = (ev) => {
        try {
          const sample: StatsSample = JSON.parse(String(ev.data));
          svcStats.set(svc.service, sample);
          svcStats = new Map(svcStats);
          // Net rate — diff cumulative counters between consecutive
          // samples per service, average across services for total.
          const prev = statsPrev.get(svc.service);
          const now = Date.now();
          if (prev) {
            const dt = (now - prev.ts) / 1000;
            if (dt > 0.1) {
              const dRx = Math.max(0, sample.net_rx - prev.rx);
              const dTx = Math.max(0, sample.net_tx - prev.tx);
              // Replace the per-service contribution to the total.
              netRate = {
                rx: Math.max(0, netRate.rx + (dRx / dt - (prev.rate?.rx ?? 0))),
                tx: Math.max(0, netRate.tx + (dTx / dt - (prev.rate?.tx ?? 0))),
              };
              statsPrev.set(svc.service, {
                rx: sample.net_rx,
                tx: sample.net_tx,
                ts: now,
                rate: { rx: dRx / dt, tx: dTx / dt },
              });
            }
          } else {
            statsPrev.set(svc.service, { rx: sample.net_rx, tx: sample.net_tx, ts: now });
          }
        } catch { /* ignore malformed sample */ }
      };
      ws.onclose = () => {
        if (statsSockets.get(svc.service) !== ws) return;
        statsSockets.delete(svc.service);
        statsSockets = new Map(statsSockets);
      };
      statsSockets.set(svc.service, ws);
      statsSockets = new Map(statsSockets);
    } catch { /* single-service failure shouldn't break the rollup */ }
  }
  function closeAllStatsStreams() {
    statsGen++;
    for (const ws of statsSockets.values()) {
      try { ws.close(); } catch { /* ignore */ }
    }
    statsSockets.clear();
    statsSockets = new Map();
    svcStats.clear();
    svcStats = new Map();
    statsPrev.clear();
    netRate = { rx: 0, tx: 0 };
  }
  async function startStatsStreaming() {
    closeAllStatsStreams();
    const running = services.filter((s) => s.state === 'running' && s.container_id);
    await Promise.all(running.map((s) => openStatsForService(s)));
  }
  // Stable signature of running containers — re-run when the set
  // changes (deploy / stop / scale), but NOT every 5s services poll.
  const runningContainerSig = $derived(
    services
      .filter((s) => s.state === 'running' && s.container_id)
      .map((s) => s.container_id)
      .sort()
      .join('|')
  );

  $effect(() => {
    const tab = activeTab;
    const sig = runningContainerSig; // depend on the set of containers
    if (tab === 'overview' && sig) {
      untrack(() => { startStatsStreaming(); });
    } else if (tab !== 'overview') {
      untrack(() => { closeAllStatsStreams(); });
    }
    return () => {
      untrack(() => { closeAllStatsStreams(); });
    };
  });

  // Aggregated Overview-rail metrics derived from the live samples.
  const statsRollup = $derived.by(() => {
    let cpu = 0, mem = 0, memLim = 0, count = 0;
    for (const s of svcStats.values()) {
      cpu += s.cpu_percent;
      mem += s.mem_used;
      memLim += s.mem_limit;
      count += 1;
    }
    return {
      cpu,
      cpuLabel: count > 0 ? `${cpu.toFixed(1)}%` : '—',
      cpuMeta: count > 0 ? `${(cpu / 100).toFixed(2)} cores · ${count} container${count === 1 ? '' : 's'}` : 'no live samples',
      mem,
      memLim,
      memPct: memLim > 0 ? (mem / memLim) * 100 : 0,
      memLabel: count > 0 ? fmtMem(mem) : '—',
      memMeta: count > 0 && memLim > 0 ? `of ${fmtMem(memLim)} limit` : count > 0 ? 'no limit set' : 'no live samples',
      net: netRate,
      netLabel: count > 0 ? `${fmtBytes(netRate.rx)}/s ↓` : '—',
      netMeta: count > 0 ? `${fmtBytes(netRate.tx)}/s ↑` : 'no live samples',
      count,
    };
  });

  // ── Environment-tab table state. Parses .env into rows the operator
  // can edit individually, with auto-detected secret masking and per-key
  // scope (which compose service references the variable).
  type EnvRow = { key: string; value: string };
  let envRows = $state<EnvRow[]>([]);
  let envLastSerialized = $state('');
  let envEditingIdx = $state<number | null>(null);
  let envShowSecrets = $state(false);

  function parseEnvFile(text: string): EnvRow[] {
    const out: EnvRow[] = [];
    for (const raw of text.split('\n')) {
      const line = raw.replace(/\r$/, '').trim();
      if (!line || line.startsWith('#')) continue;
      const eq = line.indexOf('=');
      if (eq <= 0) continue;
      const k = line.slice(0, eq).trim();
      let v = line.slice(eq + 1).trim();
      if ((v.startsWith('"') && v.endsWith('"')) || (v.startsWith("'") && v.endsWith("'"))) {
        v = v.slice(1, -1);
      }
      out.push({ key: k, value: v });
    }
    return out;
  }

  function serializeEnvRows(rows: EnvRow[]): string {
    return rows.map((r) => `${r.key}=${r.value}`).join('\n') + (rows.length > 0 ? '\n' : '');
  }

  function isSecretKey(key: string): boolean {
    return /password|secret|token|api[_-]?key|credential|access[_-]?key/i.test(key);
  }

  // Map .env keys to the services that actually reference them via
  // ${KEY} or $KEY in compose.yaml. Walks the compose service blocks
  // and tracks which ones touch each key.
  function envScopeFor(yaml: string, key: string): string[] {
    if (!yaml || !key) return [];
    const out = new Set<string>();
    const lines = yaml.split('\n');
    let inServices = false;
    let currentService = '';
    for (const raw of lines) {
      const line = raw.replace(/\r$/, '');
      if (/^\S/.test(line)) {
        inServices = /^services\s*:/.test(line);
        currentService = '';
        continue;
      }
      if (!inServices) continue;
      const svcMatch = line.match(/^ {2}([\w.-]+)\s*:\s*$/);
      if (svcMatch) {
        currentService = svcMatch[1];
        continue;
      }
      if (!currentService) continue;
      // Match ${KEY} (with or without :- default), and bare $KEY when
      // not preceded by another `$` (compose escape for literal `$`).
      const re = new RegExp(`\\$\\{${key}(?:[:?-][^}]*)?\\}|(?<!\\$)\\$${key}\\b`);
      if (re.test(line)) out.add(currentService);
    }
    return [...out];
  }

  // Re-parse env into rows whenever it changes externally (server load,
  // textarea swap on tab switch). Skip if our own serializeEnvRows
  // produced this string — otherwise we'd thrash on each row edit.
  $effect(() => {
    if (env === envLastSerialized) return;
    envRows = parseEnvFile(env);
    envLastSerialized = env;
  });

  function commitEnvRows() {
    const s = serializeEnvRows(envRows);
    env = s;
    envLastSerialized = s;
    dirty = true;
  }

  function envAddRow() {
    envRows = [...envRows, { key: 'NEW_KEY', value: '' }];
    envEditingIdx = envRows.length - 1;
    commitEnvRows();
  }
  function envDeleteRow(i: number) {
    envRows = envRows.filter((_, x) => x !== i);
    if (envEditingIdx === i) envEditingIdx = null;
    commitEnvRows();
  }
  function envSaveEdit() { envEditingIdx = null; commitEnvRows(); }

  // Deploy history
  let historyEntries = $state<DeployHistoryEntry[]>([]);
  let historyLoading = $state(false);
  let historyLoadedOnce = $state(false);

  // Modal state: view-yaml and rollback-confirm both operate on a
  // selected entry we fetch-with-YAML on demand (list rows omit YAML).
  let yamlEntry = $state<DeployHistoryEntry | null>(null);
  let showYaml = $state(false);
  // "diff" compares against the previous deploy, fetched on demand.
  let yamlMode = $state<'raw' | 'diff'>('raw');
  let yamlPrevEntry = $state<DeployHistoryEntry | null>(null);
  let yamlDiffLoading = $state(false);
  let rollbackEntry = $state<DeployHistoryEntry | null>(null);
  let showRollbackConfirm = $state(false);
  let rollbackBusy = $state(false);

  // Deploy history is served under stacks.deploy.
  async function loadHistory() {
    if (!canDeploy) return;
    historyLoading = true;
    try {
      const raw = await api.stacks.listDeployments(name);
      historyEntries = raw ?? [];
      historyLoadedOnce = true;
    } catch (err) {
      toast.error('Load history failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      historyLoading = false;
    }
  }

  async function openYaml(id: number) {
    try {
      yamlEntry = await api.stacks.getDeployment(name, id);
      yamlMode = 'raw';
      yamlPrevEntry = null;
      showYaml = true;
    } catch (err) {
      toast.error('Load snapshot failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  // Toggle to diff-mode: fetch the previous deploy's full compose (we
  // already have its DB id from the list) and compare line-by-line.
  // No-op if there is no previous entry (i.e. the very first deploy).
  async function showYamlDiff() {
    if (!yamlEntry) return;
    const prevListEntry = historyEntries.find((e) => e.version === yamlEntry!.version - 1);
    if (!prevListEntry) {
      toast.info('No previous deploy to diff against');
      return;
    }
    yamlDiffLoading = true;
    try {
      yamlPrevEntry = await api.stacks.getDeployment(name, prevListEntry.id);
      yamlMode = 'diff';
    } catch (err) {
      toast.error('Load previous failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      yamlDiffLoading = false;
    }
  }

  // LCS-based unified diff between two multi-line strings. Compose
  // files are short enough (typically <300 lines) that the O(m·n)
  // table is cheap. Returns context lines plus +/- markers.
  type DiffLine = { kind: ' ' | '-' | '+'; text: string };
  function diffLines(oldText: string, newText: string): DiffLine[] {
    const o = oldText.split('\n');
    const n = newText.split('\n');
    const M = o.length, N = n.length;
    const lcs: number[][] = Array.from({ length: M + 1 }, () => new Array(N + 1).fill(0));
    for (let i = 0; i < M; i++) {
      for (let j = 0; j < N; j++) {
        if (o[i] === n[j]) lcs[i + 1][j + 1] = lcs[i][j] + 1;
        else lcs[i + 1][j + 1] = Math.max(lcs[i + 1][j], lcs[i][j + 1]);
      }
    }
    const out: DiffLine[] = [];
    let i = M, j = N;
    while (i > 0 || j > 0) {
      if (i > 0 && j > 0 && o[i - 1] === n[j - 1]) {
        out.push({ kind: ' ', text: o[i - 1] }); i--; j--;
      } else if (j > 0 && (i === 0 || lcs[i][j - 1] >= lcs[i - 1][j])) {
        out.push({ kind: '+', text: n[j - 1] }); j--;
      } else {
        out.push({ kind: '-', text: o[i - 1] }); i--;
      }
    }
    return out.reverse();
  }

  async function openRollbackConfirm(id: number) {
    try {
      rollbackEntry = await api.stacks.getDeployment(name, id);
      showRollbackConfirm = true;
    } catch (err) {
      toast.error('Load snapshot failed', err instanceof ApiError ? err.message : undefined);
    }
  }

  async function doRollback() {
    if (!rollbackEntry) return;
    rollbackBusy = true;
    try {
      const entryId = rollbackEntry.id;
      let res;
      try {
        res = await api.stacks.rollback(name, entryId, stackHost);
      } catch (err) {
        if (!(await confirmExternalChanges(err))) throw err;
        res = await api.stacks.rollback(name, entryId, stackHost, true);
      }
      toast.success(
        `Rolled back to #${res.rolled_back_to}`,
        `${res.result.services.length} service(s) redeployed`
      );
      showRollbackConfirm = false;
      rollbackEntry = null;
      // Reload everything that moved.
      await load();
      await loadHistory();
    } catch (err) {
      toast.error('Rollback failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      rollbackBusy = false;
    }
  }

  // Older history rows show their services collapsed, so a long history stays scannable.
  let expandedRows = $state<Set<number>>(new Set());
  function expandRow(id: number) {
    const next = new Set(expandedRows);
    next.add(id);
    expandedRows = next;
  }
  function collapseRow(id: number) {
    const next = new Set(expandedRows);
    next.delete(id);
    expandedRows = next;
  }

  // formatDuration renders milliseconds as a compact human label.
  // Skips fractional seconds for >10 s so the column stays narrow.
  function formatDuration(ms: number): string {
    if (ms < 1000) return `${ms}ms`;
    if (ms < 10_000) return `${(ms / 1000).toFixed(1)}s`;
    if (ms < 60_000) return `${Math.round(ms / 1000)}s`;
    const m = Math.floor(ms / 60_000);
    const s = Math.round((ms % 60_000) / 1000);
    return s === 0 ? `${m}m` : `${m}m${s}s`;
  }

  // buildGitCommitURL turns the stored repo_url + a commit SHA into a
  // browsable URL. Handles github.com / gitlab.com / gitea-style by
  // mapping `.git` suffix and `<host>/<owner>/<repo>` shape. Returns
  // empty string when we can't reliably construct one (custom hosts).
  function buildGitCommitURL(repoURL: string, sha: string): string {
    if (!repoURL || !sha) return '';
    let url = repoURL.replace(/\.git$/, '');
    if (url.startsWith('git@')) {
      // ssh format git@github.com:owner/repo → https://github.com/owner/repo
      url = url.replace(/^git@([^:]+):/, 'https://$1/');
    }
    try {
      const u = new URL(url);
      // GitHub + Gitea + Forgejo all use /commit/<sha>; GitLab uses /-/commit/<sha>
      if (u.hostname.includes('gitlab')) return `${url}/-/commit/${sha}`;
      return `${url}/commit/${sha}`;
    } catch {
      return '';
    }
  }

  // deployTriggerBadge classifies the deploy's trigger source into a
  // stable badge. Every entry gets one — even plain manual deploys —
  // so the timeline column-aligns instead of jumping around based on
  // whether the row happens to have a note set.
  function deployTriggerBadge(note?: string): { label: string; variant: 'info' | 'warn' | 'neutral' } {
    const n = (note ?? '').trim();
    if (n === '') return { label: 'manual', variant: 'neutral' };
    if (n.startsWith('rollback')) return { label: n, variant: 'warn' };
    if (n === 'git auto-deploy') return { label: 'git auto-deploy', variant: 'info' };
    if (n.startsWith('env:')) return { label: 'env override', variant: 'neutral' };
    // Custom note — show truncated, full text in tooltip.
    return { label: n.length > 24 ? n.slice(0, 24) + '…' : n, variant: 'neutral' };
  }

  // splitImageRef breaks an image reference into displayable parts so
  // the deploy-history can show registry/repo/tag separately and keep
  // long SHA digests from blowing out the row width.
  //   ghcr.io/foo/bar:latest        → host=ghcr.io, repo=foo/bar, tag=latest
  //   postgres:16-alpine            → host="",      repo=postgres, tag=16-alpine
  //   localhost:5000/svc:v1         → host=localhost:5000, repo=svc, tag=v1
  // SHA-style tags longer than 12 chars are shown shortened with a "…".
  function splitImageRef(ref: string): { host: string; repo: string; tag: string } {
    let host = '';
    let rest = ref;
    const firstSlash = ref.indexOf('/');
    if (firstSlash > 0) {
      const head = ref.slice(0, firstSlash);
      if (head.includes('.') || head.includes(':') || head === 'localhost') {
        host = head;
        rest = ref.slice(firstSlash + 1);
      }
    }
    let repo = rest;
    let tag = 'latest';
    const colon = rest.lastIndexOf(':');
    if (colon > 0) {
      repo = rest.slice(0, colon);
      tag = rest.slice(colon + 1);
    }
    if (tag.length > 12 && /^[0-9a-f]+$/i.test(tag)) {
      tag = tag.slice(0, 12) + '…';
    }
    return { host, repo, tag };
  }

  // Lazy-load history the first time the tab is opened, and refresh after
  // every deploy so a fresh row appears without a manual reload.
  $effect(() => {
    if ((activeTab === 'history' || activeTab === 'overview') && !historyLoadedOnce) {
      loadHistory();
    }
  });

  // Dependencies (P.12.7)
  let deps = $state<StackDependencies | null>(null);
  let showDepsEditor = $state(false);
  let depsEditList = $state<string[]>([]);
  let depsNewEntry = $state('');
  let depsBusy = $state(false);
  // All stack names, for the picker dropdown in the editor.
  let allStackNames = $state<string[]>([]);

  // Dependencies and overlays are served under stacks.deploy.
  async function loadDeps() {
    if (!canDeploy) return;
    try {
      deps = await api.stacks.getDependencies(name);
    } catch {
      deps = null;
    }
  }

  async function openDepsEditor() {
    depsEditList = deps?.depends_on ? [...deps.depends_on] : [];
    depsNewEntry = '';
    try {
      const list = await api.stacks.list();
      allStackNames = list.map((s) => s.name).filter((n) => n !== name);
    } catch {
      allStackNames = [];
    }
    showDepsEditor = true;
  }

  function depAdd(entry: string) {
    const v = entry.trim();
    if (!v || v === name || depsEditList.includes(v)) return;
    depsEditList = [...depsEditList, v];
    depsNewEntry = '';
  }

  function depRemove(entry: string) {
    depsEditList = depsEditList.filter((d) => d !== entry);
  }

  // Environments (P.12.8)
  let envs = $state<StackEnvironments | null>(null);
  let envBusy = $state(false);

  async function loadEnvs() {
    if (!canDeploy) return;
    try {
      envs = await api.stacks.getEnvironments(name);
    } catch {
      envs = null;
    }
  }

  async function setActiveEnv(active: string) {
    envBusy = true;
    try {
      await api.stacks.setActiveEnvironment(name, active);
      await loadEnvs();
      toast.success(active ? `Active environment: ${active}` : 'Environment cleared');
    } catch (err) {
      toast.error('Save failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      envBusy = false;
    }
  }

  async function saveDeps() {
    depsBusy = true;
    try {
      await api.stacks.setDependencies(name, depsEditList);
      await loadDeps();
      showDepsEditor = false;
      toast.success('Dependencies updated');
    } catch (err) {
      if (err instanceof ApiError && err.status === 422) {
        toast.error('Cycle detected', 'That would create a dependency loop — pick a different edge.');
      } else {
        toast.error('Save failed', err instanceof ApiError ? err.message : undefined);
      }
    } finally {
      depsBusy = false;
    }
  }

  // Scaling state (P.8)
  let replicaCounts = $state<Map<string, number>>(new Map());
  let showScale = $state(false);
  let scaleTarget = $state('');
  let scaleValue = $state(1);
  let scaleCheck = $state<ScaleCheck | null>(null);
  let scaleBusy = $state(false);
  let scaleForce = $state(false);

  // Rolling update state (P.12.5b)
  let showRolling = $state(false);
  let rollingTarget = $state('');
  let rollingOrder = $state<'stop-first' | 'start-first'>('stop-first');
  let rollingParallel = $state(1);
  let rollingFailure = $state<'pause' | 'continue' | 'rollback'>('pause');
  let rollingBusy = $state(false);
  let rollingErr = $state<string | null>(null);
  function openRolling(service: string) {
    rollingTarget = service;
    rollingOrder = 'stop-first';
    rollingParallel = 1;
    rollingFailure = 'pause';
    rollingErr = null;
    showRolling = true;
  }
  async function doRolling() {
    rollingBusy = true;
    rollingErr = null;
    try {
      const res = await api.stacks.rollingUpdate(name, rollingTarget, {
        order: rollingOrder,
        parallelism: rollingParallel,
        failure_action: rollingFailure
      }, stackHost);
      toast.success('Rolling update complete', `${rollingTarget}: ${res.updated}/${res.total_replicas} updated${res.rolled_back ? ' (rolled back)' : ''}`);
      showRolling = false;
      await loadReplicaCounts();
      await refreshStatus();
    } catch (err) {
      rollingErr = err instanceof ApiError ? err.message : 'rolling update failed';
    } finally {
      rollingBusy = false;
    }
  }

  async function loadReplicaCounts() {
    if (!allowed('stacks.deploy')) return;
    try {
      const list = await api.stacks.listScale(name, stackHost);
      const m = new Map<string, number>();
      for (const e of list) m.set(e.service, e.replicas);
      replicaCounts = m;
    } catch { /* ignore — not critical */ }
  }

  async function openScale(service: string) {
    scaleTarget = service;
    scaleValue = replicaCounts.get(service) ?? 1;
    scaleCheck = null;
    scaleForce = false;
    showScale = true;
    try {
      scaleCheck = await api.stacks.getScale(name, service, stackHost);
      if (scaleCheck) scaleValue = scaleCheck.current_replicas || 1;
    } catch { /* fail open */ }
  }

  async function doScale() {
    scaleBusy = true;
    try {
      const res = await api.stacks.scale(name, scaleTarget, scaleValue, scaleForce, stackHost);
      toast.success('Scaled', `${scaleTarget}: ${res.previous} → ${res.current}`);
      showScale = false;
      await loadReplicaCounts();
      await refreshStatus();
    } catch (err: any) {
      // Check for stateful warning (409 with force_needed).
      if (err?.status === 409) {
        try {
          const body = await err.json?.() ?? err;
          if (body?.force_needed) {
            toast.warning(body.message ?? 'Stateful service warning — enable force to proceed');
            return;
          }
        } catch {}
      }
      toast.error('Scale failed', err instanceof ApiError ? err.message : String(err));
    } finally {
      scaleBusy = false;
    }
  }

  // Migration state (P.9)
  let showMigrate = $state(false);
  let migrateTarget = $state('');
  let migratePreflight = $state<PreflightResult | null>(null);
  let migratePreflightLoading = $state(false);
  let migrateBusy = $state(false);
  let activeMigration = $state<Migration | null>(null);

  async function openMigrate() {
    migrateTarget = '';
    migratePreflight = null;
    showMigrate = true;
  }

  async function runPreflight() {
    if (!migrateTarget) return;
    migratePreflightLoading = true;
    migratePreflight = null;
    try {
      migratePreflight = await api.migrations.preflight(name, migrateTarget);
    } catch (err) {
      toast.error('Preflight failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      migratePreflightLoading = false;
    }
  }

  async function startMigration() {
    migrateBusy = true;
    try {
      const m = await api.migrations.initiate(name, migrateTarget);
      activeMigration = m;
      showMigrate = false;
      toast.success('Migration started', `${name} → ${migrateTarget}`);
      pollMigration(m.id);
    } catch (err) {
      toast.error('Migration failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      migrateBusy = false;
    }
  }

  let migrationPoll: ReturnType<typeof setInterval> | null = null;
  function stopMigrationPoll() {
    if (migrationPoll) clearInterval(migrationPoll);
    migrationPoll = null;
  }
  $effect(() => {
    name;
    return stopMigrationPoll;
  });

  function pollMigration(id: string) {
    stopMigrationPoll();
    const stack = name;
    const iv = setInterval(async () => {
      try {
        const m = await api.migrations.get(stack, id);
        if (migrationPoll !== iv) return;
        activeMigration = m;
        if (['completed', 'failed', 'rolled_back'].includes(m.status)) {
          stopMigrationPoll();
          if (m.status === 'completed') {
            toast.success('Migration completed');
            await load();
          } else {
            toast.error('Migration ' + m.status, m.error_message);
          }
        }
      } catch {
        if (migrationPoll === iv) stopMigrationPoll();
      }
    }, 3000);
    migrationPoll = iv;
  }

  const stream = new EventStream({
    onMessage: (msg) => {
      if (msg.source === 'stacks' && msg.name === name) {
        if (msg.type === 'modified') {
          externalChange = { file: msg.file ?? 'compose.yaml', type: msg.type };
        } else if (msg.type === 'removed' && !msg.file) {
          externalChange = { file: '', type: 'removed' };
        }
      }
      if (msg.source === 'docker' && msg.type === 'container') {
        // Container lifecycle events in our stack → reload status.
        refreshStatus();
      }
    }
  });

  // P.11.11 — git source state
  let gitSource = $state<StackGitSource | null>(null);
  let gitLoading = $state(false);
  let gitBusy = $state(false);
  let showGitDialog = $state(false);
  let gitForm = $state<StackGitSourceInput>({
    repo_url: '', branch: 'main', path_in_repo: '.', auth_kind: 'none',
    auto_deploy: false, poll_interval_sec: 300
  });

  async function loadGitSource() {
    if (!allowed('stacks.update')) {
      gitSource = null;
      return;
    }
    gitLoading = true;
    try {
      gitSource = await api.stacks.getGitSource(name);
    } catch (err) {
      if (err instanceof ApiError && err.status === 404) {
        gitSource = null;
      } else {
        gitSource = null;
      }
    } finally {
      gitLoading = false;
    }
  }

  function openGitDialog() {
    if (gitSource) {
      gitForm = {
        repo_url: gitSource.repo_url,
        branch: gitSource.branch,
        path_in_repo: gitSource.path_in_repo,
        auth_kind: gitSource.auth_kind,
        username: gitSource.username ?? '',
        auto_deploy: gitSource.auto_deploy,
        poll_interval_sec: gitSource.poll_interval_sec
      };
    } else {
      gitForm = { repo_url: '', branch: 'main', path_in_repo: '.', auth_kind: 'none', auto_deploy: false, poll_interval_sec: 300 };
    }
    showGitDialog = true;
  }

  function generateWebhookSecret() {
    const bytes = crypto.getRandomValues(new Uint8Array(24));
    gitForm.webhook_secret = Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
  }

  async function saveGitSource(e: Event) {
    e.preventDefault();
    if (!gitForm.repo_url.trim()) return;
    gitBusy = true;
    try {
      const res = await api.stacks.configureGitSource(name, gitForm);
      if (res.sync_error) {
        toast.error('Saved, but first sync failed', res.sync_error);
      } else {
        toast.success('Git source saved', res.sync?.changed ? `synced ${res.sync.new_sha.slice(0, 7)}` : 'up to date');
      }
      showGitDialog = false;
      await loadGitSource();
      await load();
    } catch (err) {
      toast.error('Failed to save', err instanceof ApiError ? err.message : undefined);
    } finally {
      gitBusy = false;
    }
  }

  async function syncNow() {
    if (!gitSource) return;
    gitBusy = true;
    try {
      const res = await api.stacks.syncGitSource(name);
      toast.success(res.changed ? `Synced ${res.new_sha.slice(0, 7)}` : 'Already up to date');
      if (res.deployed) toast.success('Auto-deploy triggered');
      await loadGitSource();
      await load();
    } catch (err) {
      toast.error('Sync failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      gitBusy = false;
    }
  }

  async function disconnectGit() {
    if (!(await confirm.ask({ title: 'Disconnect git source', message: 'Disconnect git source?', body: 'The compose.yaml stays in place on disk. Future pushes to the repo will no longer sync automatically.', confirmLabel: 'Disconnect' }))) return;
    gitBusy = true;
    try {
      await api.stacks.deleteGitSource(name);
      toast.success('Git source disconnected');
      gitSource = null;
    } catch (err) {
      toast.error('Failed to disconnect', err instanceof ApiError ? err.message : undefined);
    } finally {
      gitBusy = false;
    }
  }

  async function load() {
    loading = true;
    externalChange = null;
    dirty = false;
    loadGitSource();
    loadDeps();
    loadEnvs();
    try {
      const detail = await api.stacks.get(name);
      compose = detail.compose;
      env = detail.env ?? '';
      modifiedOutside = detail.modified_outside ?? [];
      mountsStackDir = !!detail.mounts_stack_dir;
      stackStatus = detail.status === 'needs_recovery' ? 'needs_recovery' : 'ok';
      if (stackStatus === 'needs_recovery') {
        latestBackupRun = undefined;
        loadLatestBackupRun();
      } else {
        latestBackupRun = null;
      }
      // Resolve deployment host for all-mode routing.
      try {
        const stackList = await api.stacks.list();
        const entry = stackList.find(s => s.name === name);
        if (entry?.deployment?.host_id) {
          deploymentHostId = entry.deployment.host_id;
        }
      } catch { /* ignore — fallback to local */ }
      try {
        services = await api.stacks.status(name, stackHost);
      } catch {
        services = [];
      }
      await loadReplicaCounts();
    } catch (err) {
      toast.error('Load failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      loading = false;
    }
  }

  async function refreshStatus() {
    try {
      services = await api.stacks.status(name, stackHost);
    } catch { /* ignore */ }
  }

  // Re-load whenever the resolved host changes.
  {
    let prev: string | null = null;
    $effect(() => {
      const cur = stackHost;
      if (prev === null) { prev = cur; return; }
      if (cur !== prev) { prev = cur; refreshStatus(); }
    });
  }

  async function save() {
    if (anyBusy) return;
    busy = true;
    try {
      await stackOps.run(stackHost, name, () => api.stacks.update(name, compose, env || undefined));
      dirty = false;
      toast.success('Saved');
    } catch (err) {
      toast.error('Save failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      busy = false;
    }
  }

  async function deploy() {
    if (anyBusy) return;
    busy = true;
    try {
      const res = await stackOps.run(stackHost, name, async () => {
        try {
          return await api.stacks.deploy(name, stackHost);
        } catch (err) {
          if (!(await confirmExternalChanges(err))) throw err;
          return api.stacks.deploy(name, stackHost, undefined, true);
        }
      });
      modifiedOutside = [];
      toast.success('Deployed', `${res.services.length} service(s) on ${hosts.selected?.name ?? 'local'}`);
      await refreshStatus();
      // If the user has opened History at least once, freshen it so
      // this deploy's new row appears without a manual reload.
      if (historyLoadedOnce) loadHistory();
    } catch (err) {
      toast.error('Deploy failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      busy = false;
    }
  }

  async function stop() {
    if (anyBusy) return;
    busy = true;
    try {
      await stackOps.run(stackHost, name, () => api.stacks.stop(name, stackHost));
      services = [];
      toast.info('Stopped');
    } catch (err) {
      toast.error('Stop failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      busy = false;
    }
  }

  // Recovery flow — only relevant when stackStatus === 'needs_recovery'.
  let recoverBusy = $state(false);
  let recoverWarnings = $state<string[]>([]);
  // Latest successful stack-typed backup run for this stack, looked up
  // when the recovery panel opens. null = none found, undefined = not
  // yet checked (loading).
  let latestBackupRun = $state<{ id: number; job_name: string; finished_at?: string; size_bytes: number } | null | undefined>(undefined);
  let restoreBusy = $state(false);

  async function loadLatestBackupRun() {
    try {
      const runs = await api.backups.listRuns(200);
      const match = runs.find(r =>
        runRestorable(r) &&
        r.sources?.some(s => s.type === 'stack' && s.name === name)
      );
      latestBackupRun = match
        ? { id: match.id, job_name: match.job_name, finished_at: match.finished_at, size_bytes: match.size_bytes }
        : null;
    } catch {
      latestBackupRun = null;
    }
  }
  async function recoverFromContainers() {
    if (recoverBusy || anyBusy) return;
    recoverBusy = true;
    try {
      const res = await api.stacks.recover(name);
      compose = res.stack.compose;
      env = res.stack.env ?? '';
      stackStatus = 'ok';
      recoverWarnings = res.warnings ?? [];
      const cc = res.recovered_from?.container_count ?? 0;
      toast.success('Compose reconstructed', `From ${cc} container${cc === 1 ? '' : 's'}. Review the YAML before redeploying.`);
    } catch (err) {
      toast.error('Recovery failed', err instanceof ApiError ? err.message : String(err));
    } finally {
      recoverBusy = false;
    }
  }
  async function restoreFromBackup() {
    if (restoreBusy || anyBusy || !latestBackupRun) return;
    if (!(await confirm.ask({
      title: `Restore ${name} from backup`,
      message: `Restore the compose file and all named volumes from the backup taken on ${latestBackupRun.finished_at ?? 'unknown'}.`,
      body: 'Existing volume data will be replaced with the snapshot. Bind mounts to host paths are not touched. Containers are not started — review and click Deploy after the restore.',
      confirmLabel: 'Restore',
      danger: true
    }))) return;
    restoreBusy = true;
    try {
      const res = await api.backups.restoreStack(latestBackupRun.id, { target_stack: name, mode: 'replace' });
      // Pull the freshly restored compose back into the editor.
      const detail = await api.stacks.get(name);
      compose = detail.compose;
      env = detail.env ?? '';
      stackStatus = detail.status === 'needs_recovery' ? 'needs_recovery' : 'ok';
      recoverWarnings = res.warnings ?? [];
      toast.success('Restored from backup', `${res.files_restored.length} file(s), ${res.volumes_restored.length} volume(s). Review and click Deploy.`);
    } catch (err) {
      toast.error('Restore failed', err instanceof ApiError ? err.message : String(err));
    } finally {
      restoreBusy = false;
    }
  }
  async function discardGhost() {
    if (!(await confirm.ask({
      title: `Discard stack ${name}`,
      message: 'Drops the dockmesh record for this stack.',
      body: 'Running containers are NOT touched — they keep running until you stop them via docker / Containers page. The stack just disappears from this list.',
      confirmLabel: 'Discard',
      danger: true
    }))) return;
    try {
      await api.stacks.discard(name);
      toast.success('Discarded', name);
      goto('/stacks');
    } catch (err) {
      toast.error('Discard failed', err instanceof ApiError ? err.message : String(err));
    }
  }

  // Delete flow. Four orthogonal decisions, each with a different
  // safety profile:
  //   - stop containers      ✅ default on  (safe, just docker stop+rm)
  //   - remove networks      ✅ default on  (project-scoped, no data)
  //   - remove volumes       ❌ default off (data loss, unrecoverable)
  //   - remove images        ❌ default off (re-pull cost; can be slow)
  // Preview endpoint is called on open so the user sees exactly what
  // would be touched (external volumes + shared images already filtered
  // out server-side). Remote hosts currently return 501 on the preview
  // — we catch that and disable the network/volume/image checkboxes.
  let showDelete = $state(false);
  let delBusy = $state(false);
  let delStop = $state(true);
  let delNetworks = $state(true);
  let delVolumes = $state(false);
  let delImages = $state(false);
  let delPlan = $state<StackCleanupPlan | null>(null);
  let delPlanError = $state<string | null>(null);
  let delPlanLoading = $state(false);
  async function openDelete() {
    if (anyBusy) return;
    delStop = services.length > 0;
    delNetworks = true;
    delVolumes = false;
    delImages = false;
    delPlan = null;
    delPlanError = null;
    showDelete = true;
    delPlanLoading = true;
    try {
      delPlan = await api.stacks.cleanupPreview(name);
    } catch (err) {
      delPlanError = err instanceof ApiError ? err.message : String(err);
    } finally {
      delPlanLoading = false;
    }
  }
  async function confirmDelete() {
    if (delBusy) return;
    delBusy = true;
    try {
      const res = await stackOps.run(stackHost, name, () =>
        api.stacks.delete(name, {
          stop: delStop,
          networks: delNetworks,
          volumes: delVolumes,
          images: delImages
        })
      );
      const parts: string[] = [];
      if (delStop && services.length > 0) parts.push(`${services.length} container${services.length === 1 ? '' : 's'}`);
      if (res?.cleanup) {
        if (res.cleanup.networks?.length) parts.push(`${res.cleanup.networks.length} network${res.cleanup.networks.length === 1 ? '' : 's'}`);
        if (res.cleanup.volumes?.length) parts.push(`${res.cleanup.volumes.length} volume${res.cleanup.volumes.length === 1 ? '' : 's'}`);
        if (res.cleanup.images?.length) parts.push(`${res.cleanup.images.length} image${res.cleanup.images.length === 1 ? '' : 's'}`);
      }
      toast.success('Deleted', parts.length > 0 ? `Removed: ${parts.join(', ')}` : name);
      if (res?.cleanup_error) {
        toast.error('Cleanup partial', res.cleanup_error);
      }
      showDelete = false;
      goto('/stacks');
    } catch (err) {
      toast.error('Delete failed', err instanceof ApiError ? err.message : undefined);
    } finally {
      delBusy = false;
    }
  }

  $effect(() => {
    if (name) {
      load();
      stream.start();
    }
    return stream.stop;
  });
  // ── Page header, tabs and the header menu.
  const stackState = $derived.by<{ tone: Tone; label: string }>(() => {
    if (services.length === 0) return { tone: 'off', label: 'not running' };
    const running = services.filter((s) => s.state === 'running');
    if (running.length === 0) return { tone: 'off', label: 'stopped' };
    const unhealthy = running.some((s) => (s.status ?? '').toLowerCase().includes('unhealthy'));
    if (running.length < services.length || unhealthy) return { tone: 'warn', label: 'degraded' };
    return { tone: 'ok', label: 'running' };
  });
  const lastDeploy = $derived(historyEntries[0]);

  const tabItems = $derived<TabItem[]>([
    { key: 'overview', label: 'Overview', count: services.length || null },
    { key: 'compose', label: 'Compose', dot: dirty },
    { key: 'environment', label: 'Environment', count: envRows.length || null },
    { key: 'logs', label: 'Logs' },
    ...(canDeploy ? [{ key: 'history', label: 'History', count: historyEntries.length || null }] : []),
    { key: 'settings', label: 'Settings' }
  ]);

  const headerMenu = $derived.by<MenuItem[]>(() => {
    const out: MenuItem[] = [];
    if (canWrite) {
      out.push({ label: gitSource ? 'Edit git source' : 'Connect a git repository', icon: GitBranch, onclick: openGitDialog });
      if (gitSource) out.push({ label: 'Sync from git now', icon: RefreshCw, onclick: syncNow, disabled: gitBusy });
      out.push({ label: 'Edit dependencies', icon: Layers, onclick: openDepsEditor });
    }
    out.push({ label: 'Reload from disk', icon: RefreshCw, onclick: load });
    out.push({ separator: true });
    out.push({ label: 'Delete stack…', icon: Trash2, danger: true, disabled: anyBusy, onclick: openDelete });
    return out;
  });

  function serviceMenu(s: { service: string; container_id: string; image: string }): MenuItem[] {
    const out: MenuItem[] = [{ label: 'Open container', href: `/containers/${s.container_id}?from=stack` }];
    if (canDeploy) {
      out.push({ label: 'Rolling update…', icon: Repeat, onclick: () => openRolling(s.service) });
      out.push({ label: 'Scale…', icon: Maximize2, onclick: () => openScale(s.service) });
    }
    if (canUpdateImages) {
      out.push({
        label: hasImageUpdate(s.image) ? 'Update image…' : 'Pull and update image…',
        icon: RefreshCw,
        disabled: !!svcOp(s.service) && isActive(svcOp(s.service)!),
        onclick: () => updateImage(s)
      });
    }
    return out;
  }

  // ── Image updates of the stack's services. The updater only runs on the
  // central server's daemon, so remote stacks don't offer it.
  let imageUpdates = $state<Record<string, { update_available: boolean }>>({});
  async function loadImageUpdates() {
    if (!allowed('images.view')) return;
    try {
      imageUpdates = (await api.images.updates()) ?? {};
    } catch {
      /* no badge without the cache */
    }
  }
  $effect(() => {
    void name;
    untrack(() => loadImageUpdates());
  });
  function hasImageUpdate(image: string): boolean {
    return !!imageUpdates[image]?.update_available;
  }
  const canUpdateImages = $derived(
    !isRemote && canDeploy && allowed('containers.update', { stack: name, host: stackHost })
  );
  // Operation per service: the container id changes with the update, the
  // service name does not.
  let svcOps = $state<Record<string, string>>({});
  function svcOp(service: string) {
    return operations.get(svcOps[service]);
  }
  async function updateImage(s: { service: string; container_id: string; image: string }) {
    const ok = await confirm.ask({
      title: `Update ${s.service}`,
      message: `Pulls ${s.image} and, when it changed, recreates every replica of ${s.service} from compose.yaml.`,
      body: 'The current image is kept for a rollback and volumes are carried over. Compose changes saved since the last deploy are applied too.',
      confirmLabel: 'Update'
    });
    if (!ok) return;
    const since = Date.now();
    const req = api.containers.doUpdate(s.container_id);
    const op = await operations.find(opRef.container(s.container_id), 'image.update', since);
    if (op) {
      svcOps = { ...svcOps, [s.service]: op.id };
      void operations.open(op.id);
    }
    try {
      const res = await req;
      if (!op) {
        if (res.updated) toast.success(`${s.service} updated`);
        else toast.info(`${s.service} is already up to date`);
      }
    } catch (e) {
      if (!op) toast.error(`Update of ${s.service} failed`, (e as Error).message);
    }
    await refreshStatus();
    void loadImageUpdates();
    if (historyLoadedOnce) loadHistory();
  }
</script>

<section class="dm-page stk">
  <header class="dm-ph">
    <h1 class="stk-name">{name}</h1>
    <StatusChip tone={stackState.tone} label={stackState.label} />
    <span class="dm-sub stk-sub">
      {#if services.length > 0}{services.filter((s) => s.state === 'running').length} / {services.length} running · {/if}{isRemote && hosts.selected ? hosts.selected.name : 'local'}{#if lastDeploy} · deployed {fmtAgo(lastDeploy.deployed_at)}{#if lastDeploy.deployed_by_name} by {lastDeploy.deployed_by_name}{/if}{/if}
      {#if anyBusy || deployProgress}
        ·
        <span
          class:stk-deploy-failed={deployProgress?.phase === 'failed'}
          class:stk-deploy-done={deployProgress?.phase === 'done'}
        >{deployStatusLabel(deployProgress, deployElapsed) || 'deploying…'}</span>
      {/if}
    </span>
    <span class="dm-sp"></span>
    {#if stackStatus !== 'needs_recovery' && canDeploy}
      <button type="button" class="dm-btn dm-btn-primary" onclick={deploy} disabled={anyBusy}>
        <Play size={13} strokeWidth={1.8} /> Deploy
      </button>
      {#if hosts.available.length > 1}
        <button type="button" class="dm-btn" onclick={openMigrate} disabled={anyBusy}>
          <ArrowRightLeft size={13} strokeWidth={1.8} /> Migrate
        </button>
      {/if}
      <button type="button" class="dm-btn" onclick={stop} disabled={anyBusy}>
        <Square size={12} strokeWidth={1.8} /> Stop
      </button>
    {/if}
    <Menu label="More stack actions" items={headerMenu} width={240} />
  </header>

  {#if stackStatus === 'needs_recovery'}
    <div class="stk-notice warn" role="alert">
      <div class="stk-notice-h">
        <AlertTriangle size={15} />
        <b>Compose file is missing or empty</b>
      </div>
      <p>
        This stack still has a deployment record and possibly running containers, but
        <code>/stacks/{name}/compose.yaml</code> is gone or empty. The editor stays hidden until the stack is
        recovered or discarded, so an empty file can't overwrite the running workload.
      </p>
      <div class="dm-tbl stk-recover">
        <div class="dm-tr" style="grid-template-columns: minmax(0, 1fr) 150px">
          <span class="dm-nm"><span class="dm-t">Recover from running containers</span><small>Writes a best-effort compose.yaml from the containers labelled com.docker.compose.project={name}. Review it before redeploying.</small></span>
          <span><Button variant="primary" size="sm" onclick={recoverFromContainers} loading={recoverBusy} disabled={recoverBusy}><RotateCcw size={13} /> Recover</Button></span>
        </div>
        <div class="dm-tr" style="grid-template-columns: minmax(0, 1fr) 150px">
          <span class="dm-nm">
            <span class="dm-t">Restore from last backup</span>
            <small>
              {#if latestBackupRun === undefined}
                Looking for a stack backup of {name}…
              {:else if latestBackupRun === null}
                No successful stack backup of {name} found.
              {:else}
                {latestBackupRun.job_name} · {latestBackupRun.finished_at ? new Date(latestBackupRun.finished_at).toLocaleString() : 'unknown'} · {fmtBytes(latestBackupRun.size_bytes)}. Replaces compose and named volumes; bind mounts stay.
              {/if}
            </small>
          </span>
          <span><Button variant="secondary" size="sm" onclick={restoreFromBackup} loading={restoreBusy} disabled={restoreBusy || !latestBackupRun}><History size={13} /> Restore</Button></span>
        </div>
        <div class="dm-tr" style="grid-template-columns: minmax(0, 1fr) 150px">
          <span class="dm-nm"><span class="dm-t">Remove stack record</span><small>Forgets the stack in Dockmesh. Running containers are not touched.</small></span>
          <span><Button variant="danger" size="sm" onclick={discardGhost}><Trash2 size={13} /> Discard record</Button></span>
        </div>
      </div>
    </div>
  {/if}

  {#if recoverWarnings.length > 0}
    <div class="stk-notice warn">
      <div class="stk-notice-h"><AlertTriangle size={15} /><b>Review the recovered compose before redeploying</b></div>
      <ul>{#each recoverWarnings as w}<li>{w}</li>{/each}</ul>
    </div>
  {/if}

  {#if externalChange}
    <div class="stk-notice warn stk-notice-row">
      <AlertTriangle size={15} />
      <div class="stk-notice-t">
        <b>{externalChange.file || 'Stack directory'} was {externalChange.type} outside Dockmesh</b>
        <span class:stk-bad={dirty}>{dirty ? 'You have unsaved edits; reloading discards them.' : 'Reload to pick up the change.'}</span>
      </div>
      <Button size="sm" variant="secondary" onclick={load}><RefreshCw size={13} /> Reload</Button>
      <Button size="sm" variant="ghost" onclick={() => (externalChange = null)}>Ignore</Button>
    </div>
  {/if}

  {#if modifiedOutside.length > 0}
    <div class="stk-notice warn stk-notice-row">
      <AlertTriangle size={15} />
      <div class="stk-notice-t">
        <b>Modified outside Dockmesh: {modifiedOutside.join(', ')}</b>
        <span>Review these files before deploying; the next deploy asks for confirmation.</span>
      </div>
    </div>
  {/if}

  {#if mountsStackDir}
    <div class="stk-notice warn stk-notice-row">
      <AlertTriangle size={15} />
      <div class="stk-notice-t">
        <b>A service mounts the stack directory</b>
        <span>Its containers can change compose.yaml and .env. Mount a subdirectory (<code>./data:/app/data</code>) or add <code>:ro</code>.</span>
      </div>
    </div>
  {/if}

  {#if activeMigration && !activeMigration.completed_at}
    <div class="stk-notice stk-notice-row">
      <Loader2 size={15} class="animate-spin" />
      <div class="stk-notice-t">
        <b>Migrating to {activeMigration.target_host_id}</b>
        <span>
          Phase: {activeMigration.phase ?? activeMigration.status}
          {#if activeMigration.progress?.current_volume}
            · volume {activeMigration.progress.volume_index}/{activeMigration.progress.volumes_total}: {activeMigration.progress.current_volume}
          {/if}
        </span>
      </div>
      <StatusChip tone="acc" label={activeMigration.status} />
    </div>
  {/if}

  {#if gitSource && (gitSource.last_sync_error || !gitSource.has_webhook_secret || ((gitSource.last_env_drift?.new_from_repo?.length ?? 0) + (gitSource.last_env_drift?.new_from_compose?.length ?? 0) > 0))}
    <div class="stk-notice warn stk-notice-row">
      <GitBranch size={15} />
      <div class="stk-notice-t">
        {#if gitSource.last_sync_error}
          <b>Last git sync failed</b><span title={gitSource.last_sync_error}>{gitSource.last_sync_error}</span>
        {:else if (gitSource.last_env_drift?.new_from_repo?.length ?? 0) + (gitSource.last_env_drift?.new_from_compose?.length ?? 0) > 0}
          <b>Env drift from the last sync</b>
          <span>
            {#if gitSource.last_env_drift?.new_from_repo?.length}new from repo: <code>{gitSource.last_env_drift.new_from_repo.join(', ')}</code>{/if}
            {#if gitSource.last_env_drift?.new_from_compose?.length} needed by compose: <code>{gitSource.last_env_drift.new_from_compose.join(', ')}</code>{/if}
            · fill in the values on the Environment tab.
          </span>
        {:else}
          <b>Git webhook disabled</b>
          <span>Pushes only reach this stack through polling or Sync now. Set a webhook secret in the git source settings.</span>
        {/if}
      </div>
      <Button size="sm" variant="ghost" onclick={() => (activeTab = 'settings')}>Git source</Button>
    </div>
  {/if}

  {#if stackStatus !== 'needs_recovery'}
    <Tabs label="Stack sections" tabs={tabItems} active={activeTab} onchange={(k) => (activeTab = k as TabKey)} />

    <!-- Overview -->
    {#if activeTab === 'overview'}
      <div class="stk-grid">
        <div class="stk-col">
          {#if loading}
            <div class="stk-skel">
              <Skeleton width="30%" height="1rem" />
              <Skeleton width="100%" height="3rem" />
            </div>
          {:else if services.length > 0}
            {@const cols = '14px minmax(140px, 1.3fr) minmax(140px, 1.5fr) 104px 64px 84px 34px'}
            <div class="dm-tbl">
              <div class="dm-tr dm-th" style="grid-template-columns: {cols}">
                <span></span><span>Service</span><span>Image</span><span>State</span><span class="r">CPU</span><span class="r">Memory</span><span></span>
              </div>
              {#each services as s (s.service + s.container_id)}
                {@const count = replicaCounts.get(s.service) ?? 0}
                {@const st = svcStats.get(s.service)}
                {@const unhealthy = (s.status ?? '').toLowerCase().includes('unhealthy')}
                {@const op = svcOp(s.service)}
                <div
                  class="dm-tr dm-click"
                  style="grid-template-columns: {cols}"
                  role="link"
                  tabindex="0"
                  onclick={() => goto(`/containers/${s.container_id}?from=stack`)}
                  onkeydown={(e) => e.key === 'Enter' && goto(`/containers/${s.container_id}?from=stack`)}
                >
                  <span class="dm-dot {s.state !== 'running' ? '' : unhealthy ? 'warn' : 'ok'}"></span>
                  <span class="dm-nm">
                    <a href={`/containers/${s.container_id}?from=stack`} onclick={(e) => e.stopPropagation()}>{s.service}{#if count > 1}<span class="stk-rep"> ×{count}</span>{/if}</a>
                    <small title={s.status}>{s.status || s.state}</small>
                  </span>
                  <span class="dm-num dm-dim stk-img" title={s.image}>
                    {s.image.split('/').pop()}
                    {#if hasImageUpdate(s.image)}<StatusChip tone="acc" label="update" title="A newer image is available" />{/if}
                  </span>
                  <span>
                    {#if op && (isActive(op) || Date.now() - Date.parse(op.finished_at ?? op.created_at) < 60_000)}
                      <OpProgress {op} compact />
                    {:else}
                      <StatusChip tone={s.state !== 'running' ? 'off' : unhealthy ? 'warn' : 'ok'} label={unhealthy ? 'unhealthy' : s.state} />
                    {/if}
                  </span>
                  <span class="dm-num r" class:warn={(st?.cpu_percent ?? 0) >= 80}>{st ? `${st.cpu_percent.toFixed(1)}%` : '—'}</span>
                  <span class="dm-num r" class:warn={(st?.mem_percent ?? 0) >= 85}>{st ? fmtMem(st.mem_used) : '—'}</span>
                  <span class="dm-tr-menu" role="presentation" onclick={(e) => e.stopPropagation()} onkeydown={(e) => e.stopPropagation()}>
                    <Menu label={`Actions for ${s.service}`} items={serviceMenu(s)} width={220} />
                  </span>
                </div>
              {/each}
            </div>
          {:else}
            <div class="dm-tbl"><div class="dm-empty">No containers run for this stack. Deploy it to start them.</div></div>
          {/if}

          {#if canDeploy}
          <section>
            <div class="stk-sec-h">
              <h2 class="dm-h2">Recent deploys</h2>
              <span class="dm-sp"></span>
              <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={() => (activeTab = 'history')}>History <ArrowRight size={12} /></button>
            </div>
            {#if historyEntries.length === 0}
              <p class="stk-line">{historyLoading ? 'Loading…' : 'No deploys recorded yet.'}</p>
            {:else}
              <div class="dm-tbl">
                {#each historyEntries.slice(0, 3) as entry, i (entry.id)}
                  {@const badge = deployTriggerBadge(entry.note)}
                  <div class="dm-tr" style="grid-template-columns: 44px minmax(0, 1fr) 110px 100px">
                    <span class="dm-num dm-dim">#{entry.version}</span>
                    <span class="dm-trunc" title={entry.note || badge.label}>{entry.note || badge.label}<span class="dm-dim">{entry.deployed_by_name ? ` · ${entry.deployed_by_name}` : ''}</span></span>
                    <span class="dm-num dm-dim" title={new Date(entry.deployed_at).toLocaleString()}>{fmtAgo(entry.deployed_at)}</span>
                    <span>
                      {#if entry.success === false}<StatusChip tone="bad" label="failed" title={entry.error_message} />
                      {:else if i === 0}<StatusChip tone="ok" label="current" />
                      {:else}<StatusChip tone="off" label="success" />{/if}
                    </span>
                  </div>
                {/each}
              </div>
            {/if}
          </section>
          {/if}
        </div>

        <aside class="stk-col">
          <section>
            <h2 class="dm-h2">Resources · rolled up</h2>
            <dl class="dm-kv">
              <dt>CPU</dt><dd class="dm-num">{statsRollup.cpuLabel} <span class="dm-dim">{statsRollup.cpuMeta}</span></dd>
              <dt>Memory</dt><dd class="dm-num">{statsRollup.memLabel} <span class="dm-dim">{statsRollup.memMeta}</span></dd>
              <dt>Network</dt><dd class="dm-num">{statsRollup.netLabel} <span class="dm-dim">{statsRollup.netMeta}</span></dd>
              <dt>Host</dt><dd>{#if isRemote}<a href={`/hosts/${stackHost}`}>{hosts.selected?.name ?? stackHost}</a>{:else}<a href="/hosts/local">local</a>{/if}</dd>
              <dt>Source</dt>
              <dd>
                {#if gitSource}
                  <span class="stk-src"><GitBranch size={12} /> <span class="dm-trunc" title={gitSource.repo_url}>{gitSource.repo_url}</span></span>
                  <span class="dm-dim">{gitSource.branch}{gitSource.auto_deploy ? ' · auto-deploy on push' : ''}</span>
                {:else}
                  compose editor
                {/if}
              </dd>
              <dt>Endpoints</dt>
              <dd class="dm-num">
                {#if composeEndpoints.length === 0}
                  <span class="dm-dim">no ports declared</span>
                {:else}
                  {#each composeEndpoints as ep, i (ep.service + i)}
                    <div>{ep.service} :{ep.port}{#if ep.mode === 'host'} <ExternalLink size={10} />{:else} <span class="dm-dim">internal</span>{/if}</div>
                  {/each}
                {/if}
              </dd>
            </dl>
          </section>

          <section>
            <h2 class="dm-h2">Networks · volumes</h2>
            {#if composeNetworks.length === 0 && composeVolumes.length === 0}
              <p class="stk-line">No top-level networks or volumes declared.</p>
            {:else}
              <div class="stk-tags">
                {#each composeNetworks as n (n)}<span class="stk-tag" title="network">{n}</span>{/each}
                {#each composeVolumes as v (v)}<span class="stk-tag" title="volume">{v}</span>{/each}
              </div>
            {/if}
          </section>

          {#if deps && (deps.depends_on.length > 0 || deps.dependents.length > 0)}
            <section>
              <h2 class="dm-h2">Dependencies</h2>
              <dl class="dm-kv">
                {#if deps.depends_on.length > 0}
                  <dt>Needs</dt><dd>{#each deps.depends_on as d, i (d)}{i > 0 ? ', ' : ''}<a href={`/stacks/${encodeURIComponent(d)}`}>{d}</a>{/each}</dd>
                {/if}
                {#if deps.dependents.length > 0}
                  <dt>Needed by</dt><dd>{#each deps.dependents as d, i (d)}{i > 0 ? ', ' : ''}<a href={`/stacks/${encodeURIComponent(d)}`}>{d}</a>{/each}</dd>
                {/if}
              </dl>
            </section>
          {/if}

          {#if !gitLoading && !gitSource && canWrite}
            <button type="button" class="stk-connect" onclick={openGitDialog}>
              <GitBranch size={16} />
              <span><b>Connect a git repository</b><small>Sync compose.yaml and .env from a repo, deploy on push or on demand.</small></span>
              <span class="dm-btn dm-btn-sm">Connect</span>
            </button>
          {/if}
        </aside>
      </div>
    {/if}

    <!-- Compose: read-only until Edit, so a stray keystroke can't change a deployed file. -->
    {#if activeTab === 'compose'}
      <div class="stk-toolbar">
        <span class="dm-mono stk-file">compose.yaml{#if dirty}<span class="stk-unsaved"> · unsaved</span>{/if}</span>
        <span class="dm-sp"></span>
        {#if !composeEditing}
          {#if canWrite}<button type="button" class="dm-btn" onclick={startComposeEdit}><Pencil size={13} /> Edit</button>{/if}
        {:else}
          <button type="button" class="dm-btn dm-btn-ghost" onclick={() => { composeEditing = false; }} disabled={anyBusy}>Cancel</button>
          <button type="button" class="dm-btn" onclick={saveCompose} disabled={anyBusy || !dirty}><Save size={13} /> Save</button>
          {#if canDeploy}
            <button type="button" class="dm-btn dm-btn-primary" onclick={saveAndRedeploy} disabled={anyBusy || !dirty}><Play size={13} /> Save &amp; deploy</button>
          {/if}
        {/if}
      </div>
      {#if !composeEditing}
        <pre class="stk-yaml-view">{#each composeLines as line, i (i)}<div class="stk-yaml-line">{@html highlightYamlLine(line) || '&nbsp;'}</div>{/each}</pre>
      {:else}
        <textarea class="dm-editor stk-yaml" bind:value={compose} oninput={() => (dirty = true)} spellcheck="false" aria-label="compose.yaml"></textarea>
      {/if}
    {/if}

    <!-- Environment -->
    {#if activeTab === 'environment'}
      <div class="stk-toolbar">
        <span class="dm-sub">{envRows.length} variable{envRows.length === 1 ? '' : 's'} in .env{#if dirty}<span class="stk-unsaved"> · unsaved</span>{/if}</span>
        <span class="dm-sp"></span>
        <button type="button" class="dm-btn dm-btn-ghost" onclick={() => (envShowSecrets = !envShowSecrets)}>
          {#if envShowSecrets}<EyeOff size={13} /> Hide secrets{:else}<Eye size={13} /> Show secrets{/if}
        </button>
        {#if canWrite}<button type="button" class="dm-btn" onclick={envAddRow}><Plus size={13} /> Add variable</button>{/if}
        {#if dirty}<button type="button" class="dm-btn dm-btn-primary" onclick={save} disabled={anyBusy}><Save size={13} /> Save</button>{/if}
      </div>

      {#if envRows.length === 0}
        <div class="dm-tbl"><div class="dm-empty">No .env variables yet. Values referenced as <code>${'{KEY}'}</code> in compose are resolved at deploy time.</div></div>
      {:else}
        {@const ecols = 'minmax(160px, 0.9fr) minmax(200px, 1.6fr) minmax(120px, 0.8fr) 64px'}
        <div class="dm-tbl">
          <div class="dm-tr dm-th" style="grid-template-columns: {ecols}"><span>Key</span><span>Value</span><span>Used by</span><span></span></div>
          {#each envRows as row, i (i)}
            {@const isSec = isSecretKey(row.key)}
            {@const scopes = envScopeFor(compose, row.key)}
            {@const editing = envEditingIdx === i}
            <div class="dm-tr" style="grid-template-columns: {ecols}">
              <span class="dm-mono">
                {#if editing}
                  <input class="dm-input stk-cell-input" aria-label="Key" bind:value={envRows[i].key} oninput={commitEnvRows} onkeydown={(e) => e.key === 'Enter' && envSaveEdit()} />
                {:else}{row.key}{/if}
              </span>
              <span class="dm-mono stk-val">
                {#if editing}
                  <input class="dm-input stk-cell-input" aria-label="Value" bind:value={envRows[i].value} oninput={commitEnvRows} onkeydown={(e) => e.key === 'Enter' && envSaveEdit()} />
                {:else if isSec && !envShowSecrets}
                  ••••••••••
                {:else if row.value}
                  <span title={row.value}>{row.value}</span>
                {:else}
                  <span class="dm-dim">(empty)</span>
                {/if}
                {#if isSec && !editing}<span class="stk-tag">secret</span>{/if}
              </span>
              <span class="dm-trunc" title={scopes.join(', ')}>{#if scopes.length === 0}<span class="dm-dim">unused</span>{:else}{scopes.join(', ')}{/if}</span>
              <span class="dm-tr-menu stk-row-btns">
                {#if canWrite}
                  {#if editing}
                    <button type="button" class="stk-ibtn" onclick={envSaveEdit} title="Done" aria-label="Done editing"><CheckCircle2 size={13} /></button>
                  {:else}
                    <button type="button" class="stk-ibtn" onclick={() => (envEditingIdx = i)} title="Edit {row.key}" aria-label="Edit {row.key}"><Pencil size={12} /></button>
                  {/if}
                  <button type="button" class="stk-ibtn danger" onclick={() => envDeleteRow(i)} title="Delete {row.key}" aria-label="Delete {row.key}"><Trash2 size={12} /></button>
                {/if}
              </span>
            </div>
          {/each}
        </div>
      {/if}

      <section>
        <h2 class="dm-h2">Overlays</h2>
        {#if !envs || envs.available.length === 0}
          <p class="stk-line">No overlays. A <code>compose.&lt;env&gt;.yaml</code> next to compose.yaml shows up here.</p>
        {:else}
          <p class="stk-line">
            {#if envs.active}Deploys merge <code>compose.{envs.active}.yaml</code> on top of the base.{:else}Deploys use the base compose.yaml as it is.{/if}
          </p>
          {#if canDeploy}
            <div class="stk-tags">
              <button type="button" class="stk-tag stk-pick" class:on={envs.active === ''} onclick={() => setActiveEnv('')} disabled={envBusy}>base</button>
              {#each envs.available as e (e)}
                <button type="button" class="stk-tag stk-pick" class:on={envs.active === e} onclick={() => setActiveEnv(e)} disabled={envBusy}>{e}</button>
              {/each}
            </div>
          {/if}
        {/if}
      </section>
    {/if}

    <!-- Logs (live) -->
    {#if activeTab === 'logs'}
      <div class="stk-toolbar">
        <div class="stk-tags">
          {#each services as s (s.service)}
            {@const on = logServiceFilter.size === 0 || logServiceFilter.has(s.service)}
            <button type="button" class="stk-tag stk-pick" class:on onclick={() => toggleLogService(s.service)} style:--svc-color={svcColor(s.service)}>
              <span class="stk-logdot" class:live={logSockets.has(s.service)}></span>{s.service}
            </button>
          {/each}
          {#if logServiceFilter.size > 0}
            <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={() => { logServiceFilter = new Set(); }}>Show all</button>
          {/if}
        </div>
        <span class="dm-sp"></span>
        <button type="button" class="dm-btn dm-btn-sm" onclick={toggleLogTailing}>
          {#if logTailing}<Square size={11} /> Pause{:else}<Play size={11} /> Follow{/if}
        </button>
        <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={() => { logLines = []; }}>Clear</button>
      </div>
      <div class="stk-logs dm-mono" bind:this={logEl}>
        {#if visibleLogLines.length === 0}
          <div class="dm-dim">{logSockets.size === 0 ? 'Not streaming. Press Follow to start.' : 'Waiting for the first line…'}</div>
        {:else}
          {#each visibleLogLines as l, i (i)}
            {@const kind = logLevelKind(l.line)}
            <div class="stk-ln">
              <span class="dm-dim" title={new Date(l.ts).toISOString()}>{new Date(l.ts).toLocaleTimeString()}</span>
              <span class="stk-ln-svc" style:color={svcColor(l.service)}>{l.service}</span>
              <span class:warn={kind === 'warn'} class:bad={kind === 'err'} class:ok={kind === 'ok'}>{l.line}</span>
            </div>
          {/each}
        {/if}
      </div>
      <p class="stk-line">Last {visibleLogLines.length} lines from {logSockets.size} container{logSockets.size === 1 ? '' : 's'} · keeps up to {LOG_BUFFER_MAX}.</p>
    {/if}

    <!-- History -->
    {#if activeTab === 'history'}
      <div class="stk-toolbar">
        <span class="dm-sub">{historyEntries.length} deploy{historyEntries.length === 1 ? '' : 's'}</span>
        <span class="dm-sp"></span>
        <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={loadHistory} disabled={historyLoading}>
          <RefreshCw size={12} class={historyLoading ? 'animate-spin' : ''} /> Refresh
        </button>
      </div>
      {#if historyLoading && historyEntries.length === 0}
        <div class="stk-skel"><Skeleton width="100%" height="2.5rem" /><Skeleton width="100%" height="2.5rem" /></div>
      {:else if historyEntries.length === 0}
        <div class="dm-tbl"><div class="dm-empty">No deploy history yet. The next successful deploy lands here and you can roll back to it.</div></div>
      {:else}
        {@const hcols = '52px minmax(180px, 1.6fr) 110px 110px 70px 96px 150px'}
        <div class="dm-tbl">
          <div class="dm-tr dm-th" style="grid-template-columns: {hcols}">
            <span>#</span><span>Change</span><span>By</span><span>When</span><span class="r">Took</span><span>Result</span><span></span>
          </div>
          {#each historyEntries as entry, i (entry.id)}
            {@const badge = deployTriggerBadge(entry.note)}
            {@const prev = historyEntries[i + 1]?.services ?? []}
            {@const changed = (entry.services ?? []).filter((svc) => {
              const p = prev.find((x) => x.service === svc.service)?.image;
              return p !== undefined && p !== svc.image;
            })}
            {@const open = expandedRows.has(entry.id)}
            <div class="dm-tr" style="grid-template-columns: {hcols}">
              <span class="dm-num dm-dim">#{entry.version}</span>
              <span class="dm-trunc" title={entry.note || badge.label}>
                {badge.label}{#if entry.note && entry.note !== badge.label}<span class="dm-dim"> · {entry.note}</span>{/if}
                {#if entry.git_commit_sha}
                  {@const url = buildGitCommitURL(gitSource?.repo_url ?? '', entry.git_commit_sha)}
                  {#if url}<a class="dm-mono stk-sha" href={url} target="_blank" rel="noopener">{entry.git_commit_sha.slice(0, 7)}</a>{:else}<code class="stk-sha">{entry.git_commit_sha.slice(0, 7)}</code>{/if}
                {/if}
              </span>
              <span class="dm-trunc">{entry.deployed_by_name ?? '—'}</span>
              <span class="dm-num dm-dim" title={new Date(entry.deployed_at).toLocaleString()}>{fmtAgo(entry.deployed_at)}</span>
              <span class="dm-num dm-dim r">{entry.duration_ms ? formatDuration(entry.duration_ms) : '—'}</span>
              <span>
                {#if entry.success === false}<StatusChip tone="bad" label="failed" title={entry.error_message} />
                {:else if i === 0}<StatusChip tone="ok" label="current" />
                {:else}<StatusChip tone="off" label="success" />{/if}
              </span>
              <span class="dm-tr-menu stk-row-btns">
                {#if entry.services && entry.services.length > 0}
                  <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" aria-expanded={open} onclick={() => (open ? collapseRow(entry.id) : expandRow(entry.id))} title="Images per service">
                    {entry.services.length}{changed.length ? ` · ${changed.length}Δ` : ''}
                  </button>
                {/if}
                <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={() => openYaml(entry.id)}>Compose</button>
                {#if canDeploy && i !== 0}
                  <button type="button" class="dm-btn dm-btn-sm" onclick={() => openRollbackConfirm(entry.id)}>Roll back</button>
                {/if}
              </span>
            </div>
            {#if open && entry.services}
              <div class="stk-hsvc">
                {#each entry.services as svc (svc.service)}
                  {@const parts = splitImageRef(svc.image)}
                  {@const isChanged = changed.some((c) => c.service === svc.service)}
                  <div class:stk-changed={isChanged}>
                    <span>{svc.service}</span>
                    <code title={svc.image}>{parts.host ? parts.host + '/' : ''}{parts.repo}:<b>{parts.tag}</b></code>
                    {#if isChanged}<span class="stk-delta" title="Changed vs #{historyEntries[i + 1]?.version}">changed</span>{/if}
                  </div>
                {/each}
              </div>
            {/if}
          {/each}
        </div>
      {/if}
    {/if}

    <!-- Settings -->
    {#if activeTab === 'settings'}
      <div class="stk-settings">
        <div class="dm-form-sec">
          <div>
            <h4>Git source</h4>
            <p>Sync compose.yaml and .env from a repository, deploy on push or on demand.</p>
          </div>
          <div>
            {#if gitLoading}
              <span class="dm-dim">Loading…</span>
            {:else if gitSource}
              <dl class="dm-kv stk-git">
                <dt>Repository</dt><dd class="dm-mono">{gitSource.repo_url}</dd>
                <dt>Branch</dt><dd class="dm-mono">{gitSource.branch}{gitSource.path_in_repo && gitSource.path_in_repo !== '.' ? ` · ${gitSource.path_in_repo}` : ''}</dd>
                <dt>Deploy</dt><dd>{gitSource.auto_deploy ? 'automatically after each sync' : 'manually'} · webhook {gitSource.has_webhook_secret ? 'on' : 'off (no secret set)'}</dd>
                <dt>Last sync</dt>
                <dd>
                  {#if gitSource.last_sync_sha}<code>{gitSource.last_sync_sha.slice(0, 7)}</code>{#if gitSource.last_sync_at} · {new Date(gitSource.last_sync_at).toLocaleString()}{/if}{:else}never{/if}
                  {#if gitSource.last_sync_error}<div class="stk-bad">{gitSource.last_sync_error}</div>{/if}
                </dd>
              </dl>
              {#if canWrite}
                <div class="stk-btns">
                  <button type="button" class="dm-btn" onclick={syncNow} disabled={gitBusy}><RefreshCw size={12} class={gitBusy ? 'animate-spin' : ''} /> Sync now</button>
                  <button type="button" class="dm-btn" onclick={openGitDialog} disabled={gitBusy}><Pencil size={12} /> Edit</button>
                  <button type="button" class="dm-btn dm-btn-ghost" onclick={disconnectGit} disabled={gitBusy}><Unlink size={12} /> Disconnect</button>
                </div>
              {/if}
            {:else if canWrite}
              <button type="button" class="dm-btn" onclick={openGitDialog}><LinkIcon size={12} /> Connect a repository</button>
            {:else}
              <span class="dm-dim">Not connected.</span>
            {/if}
          </div>
        </div>

        <div class="dm-form-sec">
          <div>
            <h4>Dependencies</h4>
            <p>The stacks this one needs and the stacks that need it.</p>
          </div>
          <div>
            {#if !deps || (deps.depends_on.length === 0 && deps.dependents.length === 0)}
              <span class="dm-dim">No prerequisites or dependents.</span>
            {:else}
              <dl class="dm-kv">
                {#if deps.depends_on.length > 0}<dt>Needs</dt><dd>{#each deps.depends_on as d, i (d)}{i > 0 ? ', ' : ''}<a href={`/stacks/${encodeURIComponent(d)}`}>{d}</a>{/each}</dd>{/if}
                {#if deps.dependents.length > 0}<dt>Needed by</dt><dd>{#each deps.dependents as d, i (d)}{i > 0 ? ', ' : ''}<a href={`/stacks/${encodeURIComponent(d)}`}>{d}</a>{/each}</dd>{/if}
              </dl>
            {/if}
            {#if canDeploy}<button type="button" class="dm-btn" onclick={openDepsEditor}>Edit dependencies</button>{/if}
          </div>
        </div>

        <div class="dm-form-sec">
          <div>
            <h4 class="stk-bad">Delete this stack</h4>
            <p>
              Stops {services.length} service{services.length === 1 ? '' : 's'}, removes the containers, deletes
              <code>stacks/{name}/</code> and its deploy history. Networks, volumes and images are only removed if you choose so next.
            </p>
          </div>
          <div>
            <button type="button" class="dm-btn dm-btn-danger" onclick={openDelete} disabled={anyBusy}><Trash2 size={13} /> Delete stack…</button>
          </div>
        </div>
      </div>
    {/if}
  {/if}
</section>

<!-- View YAML snapshot modal (P.12.6) -->
<Modal bind:open={showYaml} title={yamlEntry ? `Deploy #${yamlEntry.version} — ${new Date(yamlEntry.deployed_at).toLocaleString()}` : 'Deploy snapshot'} maxWidth="max-w-3xl">
  {#if yamlEntry}
    <div class="space-y-3">
      <div class="text-xs text-[var(--fg-muted)] flex items-center gap-3 flex-wrap">
        {#if yamlEntry.deployed_by_name}
          <span class="inline-flex items-center gap-1">
            <User class="w-3 h-3" />
            {yamlEntry.deployed_by_name}
          </span>
        {/if}
        {#if yamlEntry.note}
          <Badge variant="info">{yamlEntry.note}</Badge>
        {/if}
      </div>
      {#if yamlEntry.services && yamlEntry.services.length > 0}
        <div class="text-xs space-y-0.5 border border-[var(--border)] rounded-md p-3 bg-[var(--surface)]">
          <div class="font-medium text-[var(--fg-muted)] uppercase tracking-wider text-[10px] mb-1">Resolved images</div>
          {#each yamlEntry.services as svc}
            <div class="font-mono flex gap-2">
              <span class="text-[var(--fg-muted)]">{svc.service}</span>
              <span>{svc.image}</span>
            </div>
          {/each}
        </div>
      {/if}

      <!-- View-mode toggle: raw vs diff vs previous -->
      <div class="flex items-center gap-2">
        <div class="flex gap-1 p-0.5 rounded bg-[var(--bg-muted,rgba(0,0,0,0.04))]">
          <button
            type="button"
            class="px-2 py-1 text-xs rounded transition"
            class:bg-[var(--bg)]={yamlMode === 'raw'}
            class:font-medium={yamlMode === 'raw'}
            onclick={() => (yamlMode = 'raw')}
          >Raw compose</button>
          <button
            type="button"
            class="px-2 py-1 text-xs rounded transition"
            class:bg-[var(--bg)]={yamlMode === 'diff'}
            class:font-medium={yamlMode === 'diff'}
            onclick={showYamlDiff}
            disabled={yamlDiffLoading}
          >
            Diff vs #{yamlEntry.version - 1}
          </button>
        </div>
        {#if yamlMode === 'diff' && yamlPrevEntry}
          <span class="text-xs text-[var(--fg-muted)]">
            comparing against {new Date(yamlPrevEntry.deployed_at).toLocaleString()}
          </span>
        {/if}
      </div>

      {#if yamlMode === 'raw'}
        <pre class="border border-[var(--border)] rounded-md p-3 bg-[var(--surface)] text-xs font-mono overflow-auto max-h-96 whitespace-pre-wrap">{yamlEntry.compose_yaml}</pre>
      {:else if yamlPrevEntry}
        {@const lines = diffLines(yamlPrevEntry.compose_yaml ?? '', yamlEntry.compose_yaml ?? '')}
        <div class="border border-[var(--border)] rounded-md bg-[var(--surface)] text-xs font-mono overflow-auto max-h-96">
          {#each lines as ln}
            <div
              class="px-3 py-0 whitespace-pre-wrap"
              class:diff-context={ln.kind === ' '}
              class:diff-add={ln.kind === '+'}
              class:diff-del={ln.kind === '-'}
            ><span class="diff-marker">{ln.kind}</span> {ln.text}</div>
          {/each}
        </div>
      {/if}
    </div>
  {/if}
  {#snippet footer()}
    <Button variant="secondary" onclick={() => (showYaml = false)}>Close</Button>
  {/snippet}
</Modal>

<!-- Dependencies editor modal (P.12.7) -->
<Modal bind:open={showDepsEditor} title="Dependencies for {name}" maxWidth="max-w-lg">
  <div class="space-y-4 text-sm">
    <div class="text-xs text-[var(--fg-muted)]">
      Stacks listed here will be deployed first (if they aren't already running) whenever you deploy
      <span class="font-mono">{name}</span>. Deep chains deploy bottom-up. Cycles are rejected.
    </div>
    <div class="space-y-2">
      {#if depsEditList.length === 0}
        <div class="text-xs text-[var(--fg-muted)]">No prerequisites yet.</div>
      {:else}
        <div class="flex flex-wrap gap-1.5">
          {#each depsEditList as d}
            <div class="inline-flex items-center gap-1 px-2 py-0.5 rounded border border-[var(--border)] font-mono text-xs">
              <span>{d}</span>
              <button
                class="text-[var(--fg-muted)] hover:text-[var(--color-danger-400)]"
                onclick={() => depRemove(d)}
                title="Remove"
                aria-label="Remove {d}"
              >
                <X class="w-3 h-3" />
              </button>
            </div>
          {/each}
        </div>
      {/if}
    </div>
    <div class="space-y-1">
      <label class="text-xs font-medium text-[var(--fg-muted)]" for="dep-picker">Add a dependency</label>
      <div class="flex gap-2">
        <input
          id="dep-picker"
          class="dm-input flex-1"
          list="dep-stack-options"
          bind:value={depsNewEntry}
          placeholder="Pick a stack…"
          onkeydown={(e) => { if (e.key === 'Enter') { e.preventDefault(); depAdd(depsNewEntry); } }}
        />
        <datalist id="dep-stack-options">
          {#each allStackNames as s}
            <option value={s}></option>
          {/each}
        </datalist>
        <Button variant="secondary" onclick={() => depAdd(depsNewEntry)} disabled={!depsNewEntry.trim()}>
          <Plus class="w-3.5 h-3.5" />
          Add
        </Button>
      </div>
      <div class="text-[11px] text-[var(--fg-muted)]">
        Unknown stack names are accepted — declaring an edge for a stack you haven't created yet is fine.
      </div>
    </div>
  </div>
  {#snippet footer()}
    <Button variant="secondary" onclick={() => (showDepsEditor = false)} disabled={depsBusy}>Cancel</Button>
    <Button variant="primary" onclick={saveDeps} loading={depsBusy} disabled={depsBusy}>Save</Button>
  {/snippet}
</Modal>

<!-- Rollback confirm modal (P.12.6) -->
<Modal bind:open={showRollbackConfirm} title="Roll back to deploy #{rollbackEntry?.version}" maxWidth="max-w-lg">
  {#if rollbackEntry}
    <div class="space-y-4 text-sm">
      <div class="flex items-start gap-3 p-3 rounded-md border border-[color-mix(in_srgb,var(--color-warning-500)_40%,transparent)] bg-[color-mix(in_srgb,var(--color-warning-500)_8%,transparent)]">
        <AlertTriangle class="w-4 h-4 text-[var(--color-warning-400)] shrink-0 mt-0.5" />
        <div class="space-y-1">
          <div>
            This will overwrite <span class="font-mono">compose.yaml</span> with the snapshot from
            <span class="font-medium">{new Date(rollbackEntry.deployed_at).toLocaleString()}</span>
            and redeploy the stack.
          </div>
          <div class="text-xs text-[var(--fg-muted)]">
            Your current <span class="font-mono">.env</span> is kept as-is — secrets added or changed
            since this deploy will still use their current values. If you need to roll env back too,
            restore it manually after rollback.
          </div>
        </div>
      </div>
      {#if rollbackEntry.services && rollbackEntry.services.length > 0}
        <div class="text-xs space-y-0.5 border border-[var(--border)] rounded-md p-3 bg-[var(--surface)]">
          <div class="font-medium text-[var(--fg-muted)] uppercase tracking-wider text-[10px] mb-1">Images that will be redeployed</div>
          {#each rollbackEntry.services as svc}
            <div class="font-mono flex gap-2">
              <span class="text-[var(--fg-muted)]">{svc.service}</span>
              <span>{svc.image}</span>
            </div>
          {/each}
        </div>
      {/if}
    </div>
  {/if}
  {#snippet footer()}
    <Button variant="secondary" onclick={() => (showRollbackConfirm = false)} disabled={rollbackBusy}>Cancel</Button>
    <Button variant="primary" onclick={doRollback} loading={rollbackBusy} disabled={rollbackBusy}>
      <RotateCcw class="w-4 h-4" />
      Roll back
    </Button>
  {/snippet}
</Modal>

<!-- Scale modal -->
<Modal bind:open={showScale} title="Scale {scaleTarget}" maxWidth="max-w-sm">
  <div class="space-y-4">
    <div>
      <label for="scale-slider" class="block text-xs font-medium text-[var(--fg-muted)] mb-2">
        Replicas: <span class="text-[var(--fg)] font-bold text-lg">{scaleValue}</span>
      </label>
      <input
        id="scale-slider"
        type="range"
        min="0"
        max="10"
        step="1"
        bind:value={scaleValue}
        class="w-full accent-[var(--color-brand-500)]"
      />
      <div class="flex justify-between text-[10px] text-[var(--fg-subtle)] mt-1">
        <span>0</span><span>5</span><span>10</span>
      </div>
    </div>

    {#if scaleCheck?.has_container_name}
      <div class="p-3 rounded-lg bg-[color-mix(in_srgb,var(--color-danger-500)_10%,transparent)] border border-[color-mix(in_srgb,var(--color-danger-500)_30%,transparent)] text-xs text-[var(--color-danger-400)] flex items-start gap-2">
        <AlertTriangle class="w-4 h-4 shrink-0 mt-0.5" />
        <div>This service has <code class="font-mono">container_name</code> set. Remove it in the compose file to allow scaling beyond 1.</div>
      </div>
    {/if}

    {#if scaleCheck?.has_hard_port}
      <div class="p-3 rounded-lg bg-[color-mix(in_srgb,var(--color-danger-500)_10%,transparent)] border border-[color-mix(in_srgb,var(--color-danger-500)_30%,transparent)] text-xs text-[var(--color-danger-400)] flex items-start gap-2">
        <AlertTriangle class="w-4 h-4 shrink-0 mt-0.5" />
        <div>Hard-coded host port <code class="font-mono">{scaleCheck.hard_port_detail}</code>. Use a port range or remove the binding to scale beyond 1.</div>
      </div>
    {/if}

    {#if scaleCheck?.is_stateful && scaleValue > 1}
      <div class="p-3 rounded-lg bg-[color-mix(in_srgb,var(--color-warning-500)_10%,transparent)] border border-[color-mix(in_srgb,var(--color-warning-500)_30%,transparent)] text-xs text-[var(--color-warning-400)]">
        <div class="flex items-start gap-2">
          <AlertTriangle class="w-4 h-4 shrink-0 mt-0.5" />
          <div>
            This service looks like a database (<strong>{scaleCheck.stateful_image}</strong>) with mounted volumes.
            Scaling may cause data corruption.
          </div>
        </div>
        <label class="flex items-center gap-2 mt-2 cursor-pointer">
          <input type="checkbox" bind:checked={scaleForce} class="rounded" />
          <span>I understand the risk — proceed anyway</span>
        </label>
      </div>
    {/if}
  </div>

  {#snippet footer()}
    <Button variant="secondary" onclick={() => (showScale = false)}>Cancel</Button>
    <Button
      variant="primary"
      loading={scaleBusy}
      disabled={scaleBusy || (scaleValue > 1 && (scaleCheck?.has_container_name || scaleCheck?.has_hard_port)) || (scaleCheck?.is_stateful && scaleValue > 1 && !scaleForce)}
      onclick={doScale}
    >
      Scale to {scaleValue}
    </Button>
  {/snippet}
</Modal>

<!-- Rolling update modal (P.12.5b) -->
<Modal bind:open={showRolling} title="Rolling update: {rollingTarget}" maxWidth="max-w-md">
  <div class="space-y-3">
    <p class="text-xs text-[var(--fg-muted)]">
      Replaces every replica of <span class="font-mono">{rollingTarget}</span> one batch at a time. The container count stays the same; use Scale to change replica count.
    </p>
    <div>
      <label for="rolling-order" class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">Order</label>
      <select id="rolling-order" class="dm-input" bind:value={rollingOrder}>
        <option value="stop-first">stop-first (stop old, then start new)</option>
        <option value="start-first">start-first (start new, then stop old)</option>
      </select>
      <p class="text-xs text-[var(--fg-muted)] mt-1">
        start-first needs no hard host-port or container_name — it'll briefly run 2× the replicas.
      </p>
    </div>
    <div>
      <label for="rolling-parallel" class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">Parallelism</label>
      <input id="rolling-parallel" type="number" min="1" max="10" class="dm-input" bind:value={rollingParallel} />
      <p class="text-xs text-[var(--fg-muted)] mt-1">How many replicas to replace at once. 1 = safest.</p>
    </div>
    <div>
      <label for="rolling-failure" class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">On failure</label>
      <select id="rolling-failure" class="dm-input" bind:value={rollingFailure}>
        <option value="pause">pause — stop at first failed replica</option>
        <option value="continue">continue — replace remaining anyway</option>
        <option value="rollback">rollback — restart old replicas and abort</option>
      </select>
    </div>
    {#if rollingErr}
      <div class="p-3 text-xs rounded border border-[var(--color-danger-400)] text-[var(--color-danger-500)]">
        <AlertTriangle class="w-4 h-4 inline mr-1" />
        {rollingErr}
      </div>
    {/if}
  </div>
  {#snippet footer()}
    <Button variant="secondary" onclick={() => (showRolling = false)}>Cancel</Button>
    <Button variant="primary" loading={rollingBusy} disabled={rollingBusy} onclick={doRolling}>
      <Repeat class="w-3.5 h-3.5" /> Roll
    </Button>
  {/snippet}
</Modal>

<!-- Migrate modal -->
<Modal bind:open={showMigrate} title="Migrate {name}" maxWidth="max-w-lg">
  <div class="space-y-4">
    <div>
      <label for="migrate-target" class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">Target host</label>
      <select
        id="migrate-target"
        class="dm-input text-sm"
        bind:value={migrateTarget}
        onchange={() => { migratePreflight = null; if (migrateTarget) runPreflight(); }}
      >
        <option value="">Select a host…</option>
        {#each hosts.available.filter(h => h.id !== stackHost && h.id !== 'all') as h}
          <option value={h.id} disabled={h.status !== 'online'}>{h.name} {h.status !== 'online' ? `(${h.status})` : ''}</option>
        {/each}
      </select>
    </div>

    {#if migratePreflightLoading}
      <div class="flex items-center gap-2 text-sm text-[var(--fg-muted)]">
        <Loader2 class="w-4 h-4 animate-spin" /> Running pre-flight checks…
      </div>
    {/if}

    {#if migratePreflight}
      <div class="space-y-1.5">
        <div class="text-xs font-medium text-[var(--fg-muted)] uppercase tracking-wider">Pre-flight checks</div>
        {#each migratePreflight.checks as check}
          <div class="flex items-center gap-2 text-xs">
            {#if check.passed}
              <CheckCircle2 class="w-3.5 h-3.5 text-[var(--color-success-400)] shrink-0" />
            {:else}
              <XCircle class="w-3.5 h-3.5 text-[var(--color-danger-400)] shrink-0" />
            {/if}
            <span class="font-medium">{check.name.replace(/_/g, ' ')}</span>
            {#if check.detail}
              <span class="text-[var(--fg-muted)] {check.passed ? 'truncate' : 'break-words'}">{check.detail}</span>
            {/if}
          </div>
        {/each}
      </div>

      {#if !migratePreflight.passed}
        <div class="p-3 rounded-lg bg-[color-mix(in_srgb,var(--color-danger-500)_10%,transparent)] border border-[color-mix(in_srgb,var(--color-danger-500)_30%,transparent)] text-xs text-[var(--color-danger-400)]">
          Pre-flight checks failed. Fix the issues above before migrating.
        </div>
      {/if}
    {/if}

    <p class="text-xs text-[var(--fg-muted)]">
      The stack will be <strong>stopped</strong> on the current host during transfer (Safe Mode).
      Downtime depends on volume size.
    </p>
  </div>

  {#snippet footer()}
    <Button variant="secondary" onclick={() => (showMigrate = false)}>Cancel</Button>
    <Button
      variant="primary"
      loading={migrateBusy}
      disabled={migrateBusy || !migrateTarget || migratePreflightLoading || (migratePreflight && !migratePreflight.passed)}
      onclick={startMigration}
    >
      <ArrowRightLeft class="w-4 h-4" />
      Start migration
    </Button>
  {/snippet}
</Modal>

<!-- Git source configure dialog (P.11.11) -->
<Modal bind:open={showGitDialog} title={gitSource ? 'Edit git source' : 'Connect a git repository'} maxWidth="max-w-lg">
  <form onsubmit={saveGitSource} id="git-form" class="space-y-4">
    <div>
      <label class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5" for="git-repo">Repository URL</label>
      <input id="git-repo" class="dm-input" placeholder="https://github.com/acme/stack.git" bind:value={gitForm.repo_url} />
    </div>
    <div class="grid grid-cols-2 gap-3">
      <div>
        <label class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5" for="git-branch">Branch</label>
        <input id="git-branch" class="dm-input" bind:value={gitForm.branch as any} />
      </div>
      <div>
        <label class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5" for="git-path">Path in repo</label>
        <input id="git-path" class="dm-input" placeholder="." bind:value={gitForm.path_in_repo as any} />
      </div>
    </div>
    <div>
      <label class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5" for="git-auth">Authentication</label>
      <select id="git-auth" class="dm-input" bind:value={gitForm.auth_kind as any}>
        <option value="none">None (public repo)</option>
        <option value="http">HTTPS username + password / token</option>
        <option value="ssh">SSH private key</option>
      </select>
    </div>
    {#if gitForm.auth_kind === 'http'}
      <div>
        <label class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5" for="git-user">Username</label>
        <input id="git-user" class="dm-input" placeholder="your-github-username" bind:value={gitForm.username as any} />
      </div>
      <div>
        <label class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5" for="git-pass">
          Personal access token
          {#if gitSource?.has_password}<span class="font-normal normal-case">— leave blank to keep existing</span>{/if}
        </label>
        <input id="git-pass" type="password" class="dm-input" placeholder="ghp_… or github_pat_…" bind:value={gitForm.password as any} />
      </div>
      {#if gitForm.repo_url && /github\.com/.test(gitForm.repo_url)}
        <div class="text-xs text-[var(--fg-muted)] leading-relaxed">
          <strong class="text-[var(--fg)]">GitHub requires a PAT</strong>, not your account password.
          <a href="https://github.com/settings/personal-access-tokens" target="_blank" rel="noopener" class="underline">
            Create one
          </a>
          with <code class="font-mono">Contents: Read-only</code> permission on this repo.
        </div>
      {/if}
    {:else if gitForm.auth_kind === 'ssh'}
      <div>
        <label class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5" for="git-sshuser">SSH user</label>
        <input id="git-sshuser" class="dm-input" placeholder="git" bind:value={gitForm.username as any} />
      </div>
      <div>
        <label class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5" for="git-sshkey">
          Private key (PEM)
          {#if gitSource?.has_ssh_key}<span class="font-normal normal-case">— leave blank to keep existing</span>{/if}
        </label>
        <textarea id="git-sshkey" class="dm-input font-mono text-xs" rows="5" bind:value={gitForm.ssh_key as any}></textarea>
      </div>
    {/if}
    <div class="grid grid-cols-2 gap-3">
      <div>
        <label class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5" for="git-poll">Poll interval (sec)</label>
        <input id="git-poll" type="number" min="60" class="dm-input" bind:value={gitForm.poll_interval_sec as any} />
        <p class="text-xs text-[var(--fg-muted)] mt-1">60+ sec, or 0 for manual / webhook-only.</p>
      </div>
      <div>
        <label class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5" for="git-webhook">
          Webhook secret (HMAC)
          {#if gitSource?.has_webhook_secret}<span class="font-normal normal-case">— leave blank to keep existing</span>{/if}
        </label>
        <div class="flex gap-2">
          <input id="git-webhook" type="text" autocomplete="off" class="dm-input font-mono text-xs" bind:value={gitForm.webhook_secret as any} />
          <button type="button" class="dm-btn dm-btn-secondary dm-btn-sm" onclick={generateWebhookSecret}>Generate</button>
        </div>
        <p class="text-xs text-[var(--fg-muted)] mt-1">Required for webhooks. Copy it into your git provider; it is not shown again.</p>
      </div>
    </div>
    <label class="flex items-center gap-2 text-sm">
      <input type="checkbox" bind:checked={gitForm.auto_deploy as any} />
      Auto-deploy on new commits
    </label>
    {#if gitSource}
      <div class="text-xs text-[var(--fg-muted)] bg-[var(--bg-muted)] rounded p-2 border border-[var(--border)]">
        <p class="font-medium text-[var(--fg)] mb-1">Webhook URL</p>
        <code class="text-[11px] font-mono break-all">POST {location.origin}/api/v1/stacks/{name}/git/webhook</code>
        {#if !gitSource.has_webhook_secret && !gitForm.webhook_secret}
          <p class="mt-1 text-[var(--color-warning-400)]">Disabled until a webhook secret is set.</p>
        {/if}
      </div>
    {/if}
  </form>
  {#snippet footer()}
    <Button variant="ghost" onclick={() => (showGitDialog = false)}>Cancel</Button>
    <Button variant="primary" onclick={saveGitSource} disabled={gitBusy || !gitForm.repo_url}>
      <LinkIcon class="w-3.5 h-3.5" />
      {gitBusy ? 'Saving…' : gitSource ? 'Save' : 'Connect & sync'}
    </Button>
  {/snippet}
</Modal>

<!-- Delete stack. Lets the user cherry-pick which docker resources get
     cleaned up alongside the compose.yaml. Volume + image removal are
     opt-in only because they cause data loss / re-pull cost. -->
<Modal bind:open={showDelete} title="Delete stack {name}" maxWidth="max-w-lg">
  <div class="space-y-3 text-sm">
    <p>This removes <span class="font-mono">compose.yaml</span> from disk. Pick what else should be cleaned up:</p>

    {#if delPlanError}
      <div class="p-2.5 rounded-md border border-[color-mix(in_srgb,var(--color-warning-500)_40%,transparent)] bg-[color-mix(in_srgb,var(--color-warning-500)_8%,transparent)] text-xs">
        <div class="font-medium text-[var(--color-warning-400)]">Resource cleanup unavailable for this host</div>
        <div class="text-[var(--fg-muted)] mt-1">{delPlanError}</div>
        <div class="text-[var(--fg-muted)] mt-1">Only the compose.yaml will be removed. You can stop / clean up manually with <span class="font-mono">docker</span> on the host.</div>
      </div>
    {/if}

    <!-- Containers -->
    <label class="flex items-start gap-2 p-2.5 rounded-md border border-[var(--border)] bg-[var(--surface)] cursor-pointer">
      <input type="checkbox" bind:checked={delStop} class="accent-[var(--color-brand-500)] mt-0.5" />
      <span class="flex-1">
        <span class="font-medium">Stop and remove containers</span>
        <span class="block text-xs text-[var(--fg-muted)] mt-0.5">
          {#if services.length > 0}
            {services.length} container{services.length === 1 ? '' : 's'} currently running. If unchecked, they keep running after the stack is deleted (and can be re-adopted later).
          {:else}
            No containers are currently running for this stack.
          {/if}
        </span>
      </span>
    </label>

    <!-- Networks -->
    <label class="flex items-start gap-2 p-2.5 rounded-md border border-[var(--border)] bg-[var(--surface)] cursor-pointer {delPlanError ? 'opacity-50 pointer-events-none' : ''}">
      <input type="checkbox" bind:checked={delNetworks} disabled={!!delPlanError} class="accent-[var(--color-brand-500)] mt-0.5" />
      <span class="flex-1">
        <span class="font-medium">Remove project networks</span>
        <span class="block text-xs text-[var(--fg-muted)] mt-0.5">
          {#if delPlanLoading}
            Loading…
          {:else if delPlan && delPlan.networks.length > 0}
            Removes {delPlan.networks.length} network{delPlan.networks.length === 1 ? '' : 's'}: <span class="font-mono">{delPlan.networks.join(', ')}</span>
          {:else}
            No project networks to remove.
          {/if}
        </span>
      </span>
    </label>

    <!-- Volumes (opt-in, danger) -->
    <label class="flex items-start gap-2 p-2.5 rounded-md border border-[color-mix(in_srgb,var(--color-danger-500)_30%,transparent)] bg-[color-mix(in_srgb,var(--color-danger-500)_5%,transparent)] cursor-pointer {delPlanError ? 'opacity-50 pointer-events-none' : ''}">
      <input type="checkbox" bind:checked={delVolumes} disabled={!!delPlanError} class="accent-[var(--color-danger-500)] mt-0.5" />
      <span class="flex-1">
        <span class="font-medium flex items-center gap-1.5">
          <AlertTriangle class="w-3.5 h-3.5 text-[var(--color-danger-400)]" />
          Remove volumes
          <span class="text-[10px] px-1.5 py-0.5 rounded bg-[color-mix(in_srgb,var(--color-danger-500)_15%,transparent)] text-[var(--color-danger-400)] font-normal">unrecoverable</span>
        </span>
        <span class="block text-xs text-[var(--fg-muted)] mt-0.5">
          Deletes data inside these volumes <span class="font-medium">permanently</span>. External volumes are never touched.
          {#if delPlanLoading}
            Loading…
          {:else if delPlan && delPlan.volumes.length > 0}
            <span class="block mt-1">Removes {delPlan.volumes.length} volume{delPlan.volumes.length === 1 ? '' : 's'}: <span class="font-mono">{delPlan.volumes.join(', ')}</span></span>
          {:else if delPlan}
            <span class="block mt-1">No project-scoped volumes to remove.</span>
          {/if}
        </span>
      </span>
    </label>
    {#if delVolumes && delPlan && delPlan.volumes.length > 0}
      <div class="px-2.5 py-2 rounded-md bg-[color-mix(in_srgb,var(--color-danger-500)_12%,transparent)] border border-[color-mix(in_srgb,var(--color-danger-500)_40%,transparent)] text-xs text-[var(--color-danger-400)] flex items-start gap-2">
        <AlertTriangle class="w-4 h-4 shrink-0 mt-0.5" />
        <span>
          You are about to <span class="font-medium">permanently delete</span> the data in {delPlan.volumes.length} volume{delPlan.volumes.length === 1 ? '' : 's'}. This cannot be undone — no snapshot, no trash, no recovery. Take a backup first if the data matters.
        </span>
      </div>
    {/if}

    <!-- Images (opt-in, lighter warning) -->
    <label class="flex items-start gap-2 p-2.5 rounded-md border border-[var(--border)] bg-[var(--surface)] cursor-pointer {delPlanError ? 'opacity-50 pointer-events-none' : ''}">
      <input type="checkbox" bind:checked={delImages} disabled={!!delPlanError} class="accent-[var(--color-brand-500)] mt-0.5" />
      <span class="flex-1">
        <span class="font-medium">Remove images</span>
        <span class="block text-xs text-[var(--fg-muted)] mt-0.5">
          {#if delPlanLoading}
            Loading…
          {:else if delPlan && delPlan.images.length > 0}
            Removes {delPlan.images.length} image{delPlan.images.length === 1 ? '' : 's'}: <span class="font-mono break-all">{delPlan.images.join(', ')}</span>
          {:else if delPlan}
            No images to remove (or all are shared with other projects).
          {/if}
          {#if delPlan && delPlan.skipped_in_use && delPlan.skipped_in_use.length > 0}
            <span class="block mt-1">Skipped (still used elsewhere): {delPlan.skipped_in_use.length} image{delPlan.skipped_in_use.length === 1 ? '' : 's'}</span>
          {/if}
          <span class="block mt-1">Next deploy re-pulls them — can be slow on metered connections.</span>
        </span>
      </span>
    </label>
  </div>
  {#snippet footer()}
    <Button variant="secondary" onclick={() => (showDelete = false)} disabled={delBusy}>Cancel</Button>
    <Button variant="danger" onclick={confirmDelete} loading={delBusy} disabled={delBusy}>
      <Trash2 class="w-4 h-4" />
      Delete
    </Button>
  {/snippet}
</Modal>

<style>
  .stk { gap: 18px; }
  .stk-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; max-width: 40ch; }
  .stk-sub { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; min-width: 0; flex: 0 1 auto; }
  .stk-deploy-done { color: var(--ok); }
  .stk-deploy-failed { color: var(--bad); }
  .stk-bad { color: var(--bad); }

  /* Notices are the only boxes on the page: they must stand out. */
  .stk-notice {
    border: 1px solid var(--border);
    border-radius: var(--r);
    padding: 10px 12px;
    background: var(--band);
    font-size: var(--fs-sm);
  }
  .stk-notice.warn {
    border-color: color-mix(in srgb, var(--warn) 45%, transparent);
    background: color-mix(in srgb, var(--warn) 7%, transparent);
  }
  .stk-notice.warn > :global(svg), .stk-notice-h :global(svg) { color: var(--warn); flex-shrink: 0; }
  .stk-notice-h { display: flex; align-items: center; gap: 8px; }
  .stk-notice p { margin: 6px 0 10px; color: var(--muted); }
  .stk-notice ul { margin: 6px 0 0; padding-left: 20px; color: var(--muted); }
  .stk-notice code, .stk-line code { font-family: var(--font-mono); font-size: 12px; }
  .stk-notice-row { display: flex; align-items: center; gap: 10px; }
  .stk-notice-t { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 2px; }
  .stk-notice-t > span { color: var(--muted); overflow: hidden; text-overflow: ellipsis; }
  .stk-recover { border-bottom: 0; }
  .stk-recover .dm-tr { height: auto; min-height: 56px; padding: 6px 0; }
  .stk-recover .dm-nm small { white-space: normal; }

  .stk-grid {
    display: grid;
    grid-template-columns: minmax(0, 1.7fr) minmax(280px, 1fr);
    gap: 32px;
    align-items: start;
  }
  @media (max-width: 1100px) { .stk-grid { grid-template-columns: minmax(0, 1fr); } }
  .stk-col { display: flex; flex-direction: column; gap: 24px; min-width: 0; }
  .stk-skel { display: flex; flex-direction: column; gap: 10px; }
  .stk-sec-h { display: flex; align-items: center; gap: 8px; }
  .stk-sec-h .dm-h2 { margin: 0; }
  .stk-line { margin: 0; color: var(--subtle); font-size: var(--fs-sm); }
  .r { text-align: right; }
  .stk-rep { color: var(--subtle); font-weight: 400; }
  .stk-img { display: flex; align-items: center; gap: 6px; }
  .stk-src { display: inline-flex; align-items: center; gap: 6px; max-width: 100%; }

  .stk-tags { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; }
  .stk-tag {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 2px 8px;
    border-radius: 999px;
    background: var(--field);
    color: var(--muted);
    font-size: var(--fs-xs);
    font-family: var(--font-mono);
    border: 0;
    white-space: nowrap;
  }
  .stk-pick { cursor: pointer; font-family: inherit; font-size: var(--fs-sm); padding: 3px 10px; }
  .stk-pick:hover { color: var(--fg); }
  .stk-pick.on { background: var(--accent-soft); color: var(--fg); }
  .stk-pick:disabled { opacity: 0.5; cursor: default; }
  .stk-logdot { width: 6px; height: 6px; background: var(--svc-color, var(--subtle)); opacity: 0.45; }
  .stk-logdot.live { opacity: 1; }

  .stk-connect {
    all: unset;
    box-sizing: border-box;
    cursor: pointer;
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 10px 0;
    border-top: 1px solid var(--border);
    border-bottom: 1px solid var(--border);
  }
  .stk-connect > span:nth-child(2) { flex: 1; min-width: 0; display: flex; flex-direction: column; }
  .stk-connect small { color: var(--subtle); font-size: var(--fs-xs); }
  .stk-connect:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }

  .stk-toolbar { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; min-width: 0; }
  .stk-file { font-size: var(--fs-sm); color: var(--muted); }
  .stk-unsaved { color: var(--warn); }

  .stk-yaml-view, .stk-yaml {
    margin: 0;
    min-height: 420px;
    font-family: var(--font-mono);
    font-size: 12.5px;
    line-height: 1.65;
  }
  .stk-yaml-view {
    background: var(--band);
    border: 1px solid var(--border);
    border-radius: var(--r);
    padding: 12px 14px;
    overflow: auto;
    max-height: 70vh;
  }
  .stk-yaml { width: 100%; min-height: 60vh; resize: vertical; }
  .stk-yaml-line { min-height: 1.65em; white-space: pre; }
  .stk-yaml-view :global(.y-key) { color: var(--accent); }
  .stk-yaml-view :global(.y-str) { color: var(--warn); }
  .stk-yaml-view :global(.y-num) { color: var(--ok); }
  .stk-yaml-view :global(.y-com) { color: var(--subtle); }

  .stk-val { display: flex; align-items: center; gap: 8px; }
  .stk-val > span:first-child { overflow: hidden; text-overflow: ellipsis; }
  .stk-cell-input { width: 100%; min-height: 28px; font-family: var(--font-mono); font-size: 12px; }
  .stk-row-btns { display: flex; justify-content: flex-end; gap: 4px; align-items: center; }
  .stk-ibtn {
    width: 26px;
    height: 26px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    background: none;
    border: 1px solid transparent;
    border-radius: var(--r-sm);
    color: var(--muted);
    cursor: pointer;
  }
  .stk-ibtn:hover { border-color: var(--border); color: var(--fg); }
  .stk-ibtn.danger:hover { color: var(--bad); }

  .stk-logs {
    height: 60vh;
    overflow: auto;
    background: var(--band);
    border: 1px solid var(--border);
    border-radius: var(--r);
    padding: 8px 12px;
    font-size: 12px;
    line-height: 1.55;
  }
  .stk-ln { display: grid; grid-template-columns: 76px 110px minmax(0, 1fr); gap: 10px; }
  .stk-ln > span:last-child { white-space: pre-wrap; overflow-wrap: anywhere; }
  .stk-ln-svc { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .stk-ln .warn { color: var(--warn); }
  .stk-ln .bad { color: var(--bad); }
  .stk-ln .ok { color: var(--ok); }

  .stk-sha { margin-left: 8px; font-size: 11.5px; color: var(--accent); }
  .stk-hsvc {
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding: 6px 6px 10px 64px;
    border-bottom: 1px solid var(--border);
    font-size: var(--fs-sm);
  }
  .stk-hsvc > div { display: grid; grid-template-columns: 160px minmax(0, 1fr) auto; gap: 12px; }
  .stk-hsvc code { font-family: var(--font-mono); font-size: 12px; color: var(--muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .stk-hsvc .stk-changed code { color: var(--fg); }
  .stk-delta { color: var(--warn); font-size: var(--fs-xs); }

  .stk-settings { display: flex; flex-direction: column; }
  .stk-git { margin-bottom: 8px; }
  .stk-btns { display: flex; gap: 8px; flex-wrap: wrap; }

  /* Compose diff in the deploy snapshot dialog */
  .diff-context { color: var(--muted); }
  .diff-add { background: color-mix(in srgb, var(--ok) 12%, transparent); color: var(--fg); }
  .diff-del { background: color-mix(in srgb, var(--bad) 12%, transparent); color: var(--fg); }
  .diff-marker { display: inline-block; width: 12px; color: var(--subtle); user-select: none; }
  .diff-add .diff-marker { color: var(--ok); }
  .diff-del .diff-marker { color: var(--bad); }
</style>
