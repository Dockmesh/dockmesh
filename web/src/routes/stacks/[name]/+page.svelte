<script lang="ts">
  import { page } from '$app/stores';
  import { goto } from '$app/navigation';
  import { untrack } from 'svelte';
  import { api, ApiError, type ScaleCheck, type PreflightResult, type Migration, type DeployHistoryEntry, type StackDependencies, type StackEnvironments, type StackCleanupPlan } from '$lib/api';
  import { Button, Card, Badge, Skeleton, Modal } from '$lib/components/ui';
  import { Eyebrow, StatusPill, EdRow, EdMetric } from '$lib/components/editorial';
  import { ArrowRight, ExternalLink } from 'lucide-svelte';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { stackOps } from '$lib/stores/stackOps.svelte';
  import { allowed } from '$lib/rbac.svelte';
  import { hosts } from '$lib/stores/host.svelte';
  import { pageContext } from '$lib/stores/pageContext.svelte';
  import { EventStream } from '$lib/events';
  import { ChevronLeft, Play, Square, Save, Trash2, AlertTriangle, RefreshCw, Server, Maximize2, ArrowRightLeft, CheckCircle2, XCircle, Loader2, GitBranch, Link as LinkIcon, Unlink, History, RotateCcw, FileText, User, Network, Plus, X, Layers, Repeat, Pencil, Eye, EyeOff } from 'lucide-svelte';
  import type { StackGitSource, StackGitSourceInput } from '$lib/api';

  import { isAllHosts } from '$lib/stores/host.svelte';

  // Scope-aware: stack-update / .deploy also require role-scope to cover
  // this stack's name (and host if known). Unscoped roles short-circuit
  // to "matches everything" in the scopeOK() helper.
  const canWrite = $derived(allowed('stacks.update', { stack: name, host: stackHost }));
  const canDeploy = $derived(allowed('stacks.deploy', { stack: name, host: stackHost }));

  // The stack detail page always operates on a specific host, never "all".
  // If the global picker is on "all", we resolve to the deployment's host
  // (from the list response) or fall back to "local".
  let deploymentHostId = $state<string>('local');
  const stackHost = $derived(isAllHosts(hosts.id) ? deploymentHostId : hosts.id);
  const isRemote = $derived(stackHost !== 'local');

  const name = $derived($page.params.name);

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
  let dirty = $state(false);

  // Tab state. P.12.6 introduced overview+history; the editorial detail
  // page splits the original "everything on overview" into 5 tabs that
  // map directly to the design mockup: Overview (services + side rail),
  // Compose (yaml + env + git source), Environment (overlays + deps),
  // History (deploy timeline), Settings (delete + future config).
  type TabKey = 'overview' | 'compose' | 'environment' | 'logs' | 'history' | 'settings';
  let activeTab = $state<TabKey>('overview');

  // ── Logs (live) — open one WebSocket per running service container,
  // merge their stdout/stderr into a single stream tagged with the
  // service name. The mockup renders one viewer with service-pills as
  // toggles; we mirror that exactly. Cleanup on tab leave is critical
  // so we don't leak sockets across navigation.
  type LogLine = { service: string; line: string; ts: number };
  let logLines = $state<LogLine[]>([]);
  let logSockets = $state<Map<string, WebSocket>>(new Map());
  let logTailing = $state(true);
  let logServiceFilter = $state<Set<string>>(new Set());
  let logEl = $state<HTMLDivElement | null>(null);
  const LOG_BUFFER_MAX = 1500;

  async function openLogStreamForService(svc: { service: string; container_id: string }) {
    if (!svc.container_id) return;
    if (logSockets.has(svc.service)) return;
    try {
      const { ticket } = await api.ws.ticket();
      const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
      const hostQs = isRemote ? `&host=${encodeURIComponent(stackHost)}` : '';
      const ws = new WebSocket(
        `${proto}//${location.host}/api/v1/ws/logs/${svc.container_id}?ticket=${ticket}&tail=80${hostQs}`
      );
      ws.onmessage = (ev) => {
        const raw = String(ev.data ?? '').replace(/\r$/, '');
        if (!raw) return;
        // Docker prefixes every line with an RFC3339-nano timestamp
        // when ContainerLogs is called with `Timestamps: true` (our
        // default — useful for ordering but redundant in the UI which
        // renders its own time column). Strip it from the displayed
        // line and reuse the parsed ts so the log timeline matches
        // what the container actually wrote (not when our WS received
        // it). App-level timestamps inside the message (e.g.
        // `1:M 27 May 2026 18:20:45.357 *…` from redis) we leave in
        // place — those are app-specific log conventions the operator
        // may want to see.
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
      ws.onclose = () => { logSockets.delete(svc.service); logSockets = new Map(logSockets); };
      ws.onerror = () => { /* close handler will clean up */ };
      logSockets.set(svc.service, ws);
      logSockets = new Map(logSockets);
    } catch {
      /* ignore — single-service failure shouldn't break the merged view */
    }
  }

  function closeAllLogStreams() {
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

  // Tab gate — open / close streams as the operator switches tabs.
  // Only depend on `activeTab`. The stream helpers read `services` and
  // mutate `logSockets` / `logLines`, but we wrap those calls in untrack
  // so the effect doesn't re-fire on every 5s services poll (which would
  // tear down + reopen the WS connections in a tight loop and trip
  // svelte's effect-update-depth limiter).
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

  // Tiny client-side YAML highlighter — same regex set as the wizard's
  // mockup. Server-side yaml parsing remains the source of truth at
  // deploy time; this is purely for read-only colour cues.
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

  async function openStatsForService(svc: { service: string; container_id: string; state: string }) {
    if (statsSockets.has(svc.service)) return;
    if (!svc.container_id || svc.state !== 'running') return;
    try {
      const { ticket } = await api.ws.ticket();
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
        statsSockets.delete(svc.service);
        statsSockets = new Map(statsSockets);
      };
      statsSockets.set(svc.service, ws);
      statsSockets = new Map(statsSockets);
    } catch { /* single-service failure shouldn't break the rollup */ }
  }
  function closeAllStatsStreams() {
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
      memLabel: count > 0 ? bytes(mem) : '—',
      memMeta: count > 0 && memLim > 0 ? `of ${bytes(memLim)} limit` : count > 0 ? 'no limit set' : 'no live samples',
      net: netRate,
      netLabel: count > 0 ? `${bytes(netRate.rx)}/s ↓` : '—',
      netMeta: count > 0 ? `${bytes(netRate.tx)}/s ↑` : 'no live samples',
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

  function bytes(n: number): string {
    if (!n || n < 1) return '0 B';
    const u = ['B', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.min(Math.floor(Math.log(n) / Math.log(1024)), u.length - 1);
    return `${(n / Math.pow(1024, i)).toFixed(i === 0 ? 0 : 1)} ${u[i]}`;
  }

  // Deploy history (P.12.6)
  let historyEntries = $state<DeployHistoryEntry[]>([]);
  let historyLoading = $state(false);
  let historyLoadedOnce = $state(false);

  // Modal state: view-yaml and rollback-confirm both operate on a
  // selected entry we fetch-with-YAML on demand (list rows omit YAML).
  let yamlEntry = $state<DeployHistoryEntry | null>(null);
  let showYaml = $state(false);
  // Diff-mode for the deploy-snapshot drawer. Raw shows the compose.yaml
  // verbatim; "diff" computes a unified line-diff against the immediately
  // previous deploy (one step lower in version). yamlPrevCompose holds
  // the lazily-fetched previous compose so the diff isn't recomputed
  // every render. null = not loaded yet.
  let yamlMode = $state<'raw' | 'diff'>('raw');
  let yamlPrevEntry = $state<DeployHistoryEntry | null>(null);
  let yamlDiffLoading = $state(false);
  let rollbackEntry = $state<DeployHistoryEntry | null>(null);
  let showRollbackConfirm = $state(false);
  let rollbackBusy = $state(false);

  async function loadHistory() {
    historyLoading = true;
    try {
      const raw = await api.stacks.listDeployments(name);
      // Defense-in-depth: backend returns [] for empty, but treat null
      // (older servers, proxy quirks) as empty too so the UI never
      // crashes on a .length access.
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
      const res = await api.stacks.rollback(name, rollbackEntry.id, stackHost);
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

  function relTime(iso: string): string {
    const d = new Date(iso).getTime();
    const diff = Date.now() - d;
    const m = Math.floor(diff / 60000);
    if (m < 1) return 'just now';
    if (m < 60) return `${m}m ago`;
    const h = Math.floor(m / 60);
    if (h < 24) return `${h}h ago`;
    const days = Math.floor(h / 24);
    if (days < 30) return `${days}d ago`;
    return new Date(iso).toLocaleDateString();
  }

  // deployNoteBadge classifies the free-text note on a history row into
  // one of a few canonical badge variants. Returns null when there is
  // genuinely nothing to show (the "current" pill plus the actor name
  // already say enough for plain manual deploys without an env override).
  // Below the top 10 history rows, the service list collapses to a
  // single one-liner that the user can expand on demand. Avoids the
  // wall-of-text effect when a stack has dozens of deploys captured.
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
    if (activeTab === 'history' && !historyLoadedOnce) {
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

  async function loadDeps() {
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

  function pollMigration(id: string) {
    const iv = setInterval(async () => {
      try {
        const m = await api.migrations.get(name, id);
        activeMigration = m;
        if (['completed', 'failed', 'rolled_back'].includes(m.status)) {
          clearInterval(iv);
          if (m.status === 'completed') {
            toast.success('Migration completed');
            await load();
          } else {
            toast.error('Migration ' + m.status, m.error_message);
          }
        }
      } catch {
        clearInterval(iv);
      }
    }, 3000);
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
      const res = await stackOps.run(stackHost, name, () => api.stacks.deploy(name, stackHost));
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
      // Find the newest successful stack-typed run for THIS stack.
      const match = runs.find(r =>
        r.status === 'success' &&
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
      const res = await api.backups.restoreStack(latestBackupRun.id, name);
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
</script>

<section class="stack-detail-frame">
  <!-- Editorial header: eyebrow + italic stack name + StatusPill + meta.
       No narrative subtitle — the surface info is enough. The breadcrumb
       in the topbar already shows "stacks / {name}" so we don't repeat
       a manual back-link here. -->
  <header class="stack-header">
    <div class="stack-header-text">
      <div class="stack-title-row">
        <h1 class="ed-title stack-title">{name}</h1>
        {#if services.length > 0}
          {@const allRunning = services.every((s) => s.state === 'running')}
          <StatusPill status={allRunning ? 'running' : 'degraded'} />
        {/if}
      </div>
      {#if services.length > 0}
        <p class="ed-subtitle stack-subtitle">
          {services.filter((s) => s.state === 'running').length} / {services.length} running{#if isRemote && hosts.selected} · {hosts.selected.name}{/if}{#if anyBusy || deployProgress}
            ·
            <span
              class:stack-deploy-status-failed={deployProgress?.phase === 'failed'}
              class:stack-deploy-status-done={deployProgress?.phase === 'done'}
            >
              {deployStatusLabel(deployProgress, deployElapsed) || 'deploying…'}
            </span>
          {/if}
        </p>
      {/if}
    </div>
    <div class="ed-actions">
      {#if stackStatus !== 'needs_recovery' && canDeploy}
        <button
          type="button"
          class="dm-btn dm-btn-primary dm-btn-sm"
          onclick={deploy}
          disabled={anyBusy}
        >
          <Play size={13} strokeWidth={1.5} />
          Deploy
        </button>
        {#if hosts.available.length > 1}
          <button
            type="button"
            class="dm-btn dm-btn-secondary dm-btn-sm"
            onclick={openMigrate}
            disabled={anyBusy}
          >
            <ArrowRightLeft size={13} strokeWidth={1.5} />
            Migrate
          </button>
        {/if}
        <button
          type="button"
          class="dm-btn dm-btn-secondary dm-btn-sm"
          onclick={stop}
          disabled={anyBusy}
        >
          <Square size={13} strokeWidth={1.5} />
          Stop
        </button>
      {/if}
    </div>
  </header>

  {#if stackStatus === 'needs_recovery'}
    <!-- Stack record exists but compose.yaml is missing or empty.
         Don't render the regular editor — it would let the operator
         "save" an empty file over the running deployment. Offer the
         three explicit recovery paths instead. -->
    <div class="dm-card p-5 border-[color-mix(in_srgb,var(--color-warning-500)_50%,transparent)] bg-[color-mix(in_srgb,var(--color-warning-500)_6%,transparent)]">
      <div class="flex items-start gap-3 mb-4">
        <AlertTriangle class="w-5 h-5 text-[var(--color-warning-400)] shrink-0 mt-0.5" />
        <div>
          <div class="font-semibold text-[var(--color-warning-400)]">Compose file is missing or empty</div>
          <div class="text-sm text-[var(--fg-muted)] mt-1">
            This stack still has a deployment record (and possibly running containers), but
            <span class="font-mono">/stacks/{name}/compose.yaml</span> is gone or empty on disk.
            Pick one of the recovery options below — the regular editor is hidden until the
            stack is recovered or discarded so we can't accidentally overwrite a running
            workload with an empty config.
          </div>
        </div>
      </div>
      <div class="grid gap-3 md:grid-cols-3">
        <div class="p-3 rounded-md border border-[var(--border)] bg-[var(--surface)] flex flex-col">
          <div class="font-medium text-sm mb-1">Recover from running containers</div>
          <div class="text-xs text-[var(--fg-muted)] flex-1 mb-3">
            Inspect the live containers labelled <span class="font-mono">com.docker.compose.project={name}</span>
            and write a best-effort <span class="font-mono">compose.yaml</span> back to disk. Review the
            result before redeploying.
          </div>
          <Button variant="primary" onclick={recoverFromContainers} loading={recoverBusy} disabled={recoverBusy}>
            <RotateCcw class="w-3.5 h-3.5" />
            Recover
          </Button>
        </div>
        <div class="p-3 rounded-md border border-[var(--border)] bg-[var(--surface)] flex flex-col">
          <div class="font-medium text-sm mb-1">Restore from last backup</div>
          <div class="text-xs text-[var(--fg-muted)] flex-1 mb-3">
            {#if latestBackupRun === undefined}
              Looking for a stack-typed backup of <span class="font-mono">{name}</span>…
            {:else if latestBackupRun === null}
              No successful backup of this stack found. Take a backup proactively (Backups → New job, source <span class="font-mono">stack:{name}</span>) so this option is available next time.
            {:else}
              Latest run: <span class="font-mono">{latestBackupRun.job_name}</span> on
              {latestBackupRun.finished_at ? new Date(latestBackupRun.finished_at).toLocaleString() : 'unknown'} ({Math.round(latestBackupRun.size_bytes / 1024 / 1024)} MB).
              Restores compose + named volumes; bind mounts are untouched. Existing volume data is replaced.
            {/if}
          </div>
          <Button variant="secondary" onclick={restoreFromBackup} loading={restoreBusy} disabled={restoreBusy || !latestBackupRun}>
            <History class="w-3.5 h-3.5" />
            Restore
          </Button>
        </div>
        <div class="p-3 rounded-md border border-[color-mix(in_srgb,var(--color-danger-500)_30%,transparent)] bg-[color-mix(in_srgb,var(--color-danger-500)_5%,transparent)] flex flex-col">
          <div class="font-medium text-sm mb-1">Remove stack record</div>
          <div class="text-xs text-[var(--fg-muted)] flex-1 mb-3">
            Forget the stack in dockmesh. Running containers are <span class="font-medium">not touched</span> — clean them up via the Containers page or <span class="font-mono">docker</span> directly afterwards.
          </div>
          <Button variant="danger" onclick={discardGhost}>
            <Trash2 class="w-3.5 h-3.5" />
            Discard record
          </Button>
        </div>
      </div>
    </div>
  {/if}

  {#if recoverWarnings.length > 0}
    <div class="dm-card p-4 border-[color-mix(in_srgb,var(--color-warning-500)_40%,transparent)]">
      <div class="font-medium text-[var(--color-warning-400)] mb-2 flex items-center gap-2">
        <AlertTriangle class="w-4 h-4" />
        Recovery warnings — review the compose before redeploying
      </div>
      <ul class="text-xs text-[var(--fg-muted)] space-y-1 list-disc pl-5">
        {#each recoverWarnings as w}<li>{w}</li>{/each}
      </ul>
    </div>
  {/if}

  {#if externalChange}
    <div class="dm-card p-4 border-[color-mix(in_srgb,var(--color-warning-500)_40%,transparent)] flex items-start gap-3">
      <AlertTriangle class="w-5 h-5 text-[var(--color-warning-400)] shrink-0 mt-0.5" />
      <div class="flex-1 text-sm">
        <div class="font-medium text-[var(--color-warning-400)]">
          {externalChange.file || 'Stack directory'} was {externalChange.type} outside Dockmesh
        </div>
        {#if dirty}
          <div class="text-xs text-[var(--color-danger-400)] mt-1">
            You have unsaved edits — reloading will discard them.
          </div>
        {:else}
          <div class="text-xs text-[var(--fg-muted)] mt-1">
            Reload to pick up the external change.
          </div>
        {/if}
      </div>
      <div class="flex gap-2 shrink-0">
        <Button size="sm" variant="secondary" onclick={load}>
          <RefreshCw class="w-3.5 h-3.5" />
          Reload
        </Button>
        <Button size="sm" variant="ghost" onclick={() => (externalChange = null)}>Ignore</Button>
      </div>
    </div>
  {/if}

  <!-- Git source (P.11.11) — Editorial treatment -->
  {#if !gitLoading}
    <section class="gs-section">
      <div class="gs-eyebrow">git source</div>
      {#if gitSource}
        <div class="gs-card gs-card-connected">
          <div class="gs-card-main">
            <div class="gs-card-icon">
              <GitBranch size={16} strokeWidth={1.5} />
            </div>
            <div class="gs-card-body">
              <div class="gs-card-title">
                <span class="gs-repo">{gitSource.repo_url}</span>
                <span class="dm-pill dm-pill-neutral gs-pill">
                  <span class="dm-pill-dot"></span>{gitSource.branch}
                </span>
                {#if gitSource.auto_deploy}
                  <span class="dm-pill dm-pill-success gs-pill">
                    <span class="dm-pill-dot"></span>auto-deploy
                  </span>
                {/if}
                {#if gitSource.has_webhook_secret}
                  <span class="dm-pill dm-pill-info gs-pill">
                    <span class="dm-pill-dot"></span>webhook
                  </span>
                {/if}
              </div>
              <div class="gs-card-sub">
                {#if gitSource.last_sync_sha}
                  <code class="gs-sha">{gitSource.last_sync_sha.slice(0, 7)}</code>
                  {#if gitSource.last_sync_at}
                    <span class="gs-meta">synced {new Date(gitSource.last_sync_at).toLocaleString()}</span>
                  {/if}
                {:else}
                  <span class="gs-meta">never synced</span>
                {/if}
                {#if gitSource.path_in_repo && gitSource.path_in_repo !== '.'}
                  <span class="gs-meta">· path: <code>{gitSource.path_in_repo}</code></span>
                {/if}
              </div>
              {#if gitSource.last_sync_error}
                <div class="gs-error">
                  <AlertTriangle size={12} strokeWidth={1.5} />
                  {gitSource.last_sync_error}
                </div>
              {/if}
            </div>
            {#if canWrite}
              <div class="gs-actions">
                <button
                  type="button"
                  class="dm-btn dm-btn-secondary dm-btn-sm"
                  onclick={syncNow}
                  disabled={gitBusy}
                >
                  <RefreshCw size={12} strokeWidth={1.5} class={gitBusy ? 'animate-spin' : ''} />
                  Sync now
                </button>
                <button
                  type="button"
                  class="dm-btn dm-btn-ghost dm-btn-sm"
                  onclick={openGitDialog}
                  disabled={gitBusy}
                >
                  <Pencil size={12} strokeWidth={1.5} />
                  Edit
                </button>
                <button
                  type="button"
                  class="dm-btn dm-btn-ghost dm-btn-sm gs-disconnect"
                  onclick={disconnectGit}
                  disabled={gitBusy}
                  aria-label="Disconnect"
                >
                  <Unlink size={12} strokeWidth={1.5} />
                </button>
              </div>
            {/if}
          </div>
          {#if gitSource.last_env_drift && ((gitSource.last_env_drift.new_from_repo?.length ?? 0) + (gitSource.last_env_drift.new_from_compose?.length ?? 0) > 0)}
            <div class="gs-drift">
              <div class="gs-drift-title">
                <AlertTriangle size={12} strokeWidth={1.5} />
                Env drift from last sync
              </div>
              {#if gitSource.last_env_drift.new_from_repo?.length}
                <div class="gs-drift-row">
                  <span class="gs-drift-label">new from repo:</span>
                  <code>{gitSource.last_env_drift.new_from_repo.join(', ')}</code>
                </div>
              {/if}
              {#if gitSource.last_env_drift.new_from_compose?.length}
                <div class="gs-drift-row">
                  <span class="gs-drift-label">needed by compose:</span>
                  <code>{gitSource.last_env_drift.new_from_compose.join(', ')}</code>
                </div>
              {/if}
              <div class="gs-drift-hint">
                Fill in the values on the <em>Environment</em> tab. Existing values are untouched.
              </div>
            </div>
          {/if}
        </div>
      {:else if canWrite}
        <button
          type="button"
          class="gs-card gs-card-empty"
          onclick={openGitDialog}
        >
          <div class="gs-card-icon gs-card-icon-empty">
            <GitBranch size={16} strokeWidth={1.5} />
          </div>
          <div class="gs-card-body">
            <div class="gs-empty-title">Connect a git repository</div>
            <div class="gs-empty-blurb">
              Sync <code>compose.yaml</code> and <code>.env</code> from a public or private repo. Auto-deploy on push, or pull on demand.
            </div>
          </div>
          <div class="gs-empty-cta">
            <span class="dm-btn dm-btn-primary dm-btn-sm">
              <LinkIcon size={12} strokeWidth={1.5} />
              Connect
            </span>
          </div>
        </button>
      {/if}
    </section>
  {/if}

  <!-- Active migration banner -->
  {#if activeMigration && !activeMigration.completed_at}
    <Card class="p-4 border-[var(--color-brand-500)]/30">
      <div class="flex items-center gap-3">
        <Loader2 class="w-5 h-5 text-[var(--color-brand-400)] animate-spin shrink-0" />
        <div class="flex-1 min-w-0">
          <div class="text-sm font-medium">
            Migrating to {activeMigration.target_host_id}
          </div>
          <div class="text-xs text-[var(--fg-muted)]">
            Phase: {activeMigration.phase ?? activeMigration.status}
            {#if activeMigration.progress?.current_volume}
              — Volume {activeMigration.progress.volume_index}/{activeMigration.progress.volumes_total}: {activeMigration.progress.current_volume}
            {/if}
          </div>
        </div>
        <Badge variant="info" dot>{activeMigration.status}</Badge>
      </div>
    </Card>
  {/if}

  {#if stackStatus !== 'needs_recovery'}
  <!-- Editorial tab strip — Overview / Compose / Environment / History /
       Settings. Hidden in recovery mode so the operator can't accidentally
       land on inactive tabs while compose.yaml is gone. Counts on the
       relevant tabs match the loaded entry counts. -->
  <div class="ed-tabs stack-tabs" role="tablist" aria-label="Stack sections">
    <button
      role="tab"
      type="button"
      aria-selected={activeTab === 'overview'}
      class="ed-tab"
      class:active={activeTab === 'overview'}
      onclick={() => (activeTab = 'overview')}
    >
      Overview
      {#if services.length > 0}<span class="count">{services.length}</span>{/if}
    </button>
    <button
      role="tab"
      type="button"
      aria-selected={activeTab === 'compose'}
      class="ed-tab"
      class:active={activeTab === 'compose'}
      onclick={() => (activeTab = 'compose')}
    >
      Compose
      {#if dirty}<span class="count" style="color: var(--color-warning-400); border-color: color-mix(in srgb, var(--color-warning-500) 40%, var(--border));">unsaved</span>{/if}
    </button>
    <button
      role="tab"
      type="button"
      aria-selected={activeTab === 'environment'}
      class="ed-tab"
      class:active={activeTab === 'environment'}
      onclick={() => (activeTab = 'environment')}
    >
      Environment
      {#if envs && envs.available.length > 0}<span class="count">{envs.available.length}</span>{/if}
    </button>
    <button
      role="tab"
      type="button"
      aria-selected={activeTab === 'logs'}
      class="ed-tab"
      class:active={activeTab === 'logs'}
      onclick={() => (activeTab = 'logs')}
    >
      Logs
      <span class="count" style:color={activeTab === 'logs' && logTailing ? 'var(--color-success-400)' : undefined}>
        {activeTab === 'logs' && logTailing ? 'live' : 'stream'}
      </span>
    </button>
    <button
      role="tab"
      type="button"
      aria-selected={activeTab === 'history'}
      class="ed-tab"
      class:active={activeTab === 'history'}
      onclick={() => (activeTab = 'history')}
    >
      History
      {#if historyEntries.length > 0}<span class="count">{historyEntries.length}</span>{/if}
    </button>
    <button
      role="tab"
      type="button"
      aria-selected={activeTab === 'settings'}
      class="ed-tab"
      class:active={activeTab === 'settings'}
      onclick={() => (activeTab = 'settings')}
    >
      Settings
    </button>
  </div>

  <!-- ─────────────────────────── Overview ─────────────────────────── -->
  {#if activeTab === 'overview'}
    <div class="stack-overview">
      <!-- LEFT: services list -->
      <div class="stack-overview-main">
        <section class="stack-block">
          <Eyebrow>Services · {services.length}</Eyebrow>
          {#if loading}
            <div class="stack-skeleton">
              <Skeleton width="30%" height="1rem" />
              <Skeleton width="100%" height="3rem" />
            </div>
          {:else if services.length > 0}
            <div class="stack-services">
              {#each services as s (s.service + s.container_id)}
                {@const count = replicaCounts.get(s.service) ?? 0}
                <EdRow
                  status={s.state === 'running' ? 'running' : 'stopped'}
                  href={`/containers/${s.container_id}?from=stack`}
                  columns="6px minmax(0, 1.4fr) minmax(0, 1.4fr) auto auto"
                >
                  <span class="stack-svc-name">
                    <span class="stack-svc-id">
                      {s.service}{#if count > 1}<span class="stack-svc-replicas">×{count}</span>{/if}
                    </span>
                    <span class="stack-svc-meta">{s.status}</span>
                  </span>
                  <span class="stack-svc-image" title={s.image}>{s.image}</span>
                  <StatusPill status={s.state === 'running' ? 'running' : 'stopped'} label={s.state} />
                  <span class="stack-svc-actions" onclick={(e) => e.stopPropagation()} role="presentation">
                    {#if canDeploy}
                      <button
                        type="button"
                        class="stack-svc-btn"
                        title="Rolling update {s.service}"
                        aria-label="Rolling update {s.service}"
                        onclick={(e) => { e.preventDefault(); openRolling(s.service); }}
                      >
                        <Repeat size={12} strokeWidth={1.5} />
                      </button>
                      <button
                        type="button"
                        class="stack-svc-btn"
                        title="Scale {s.service}"
                        aria-label="Scale {s.service}"
                        onclick={(e) => { e.preventDefault(); openScale(s.service); }}
                      >
                        <Maximize2 size={12} strokeWidth={1.5} />
                      </button>
                    {/if}
                  </span>
                </EdRow>
              {/each}
            </div>
          {:else}
            <div class="stack-empty">
              <p>No containers running for this stack.</p>
            </div>
          {/if}
        </section>

        <!-- Heads-up — surfaces the most-actionable per-deploy notes -->
        {#if services.some((s) => s.state !== 'running' || (s.status ?? '').toLowerCase().includes('unhealthy'))}
          <section class="stack-block">
            <Eyebrow>Heads-up</Eyebrow>
            <div class="stack-headsup">
              <AlertTriangle size={14} strokeWidth={1.5} class="stack-headsup-icon" />
              <p>
                {#each services.filter((s) => s.state !== 'running') as s, i (s.service)}
                  {#if i > 0}<span class="stack-headsup-sep"> · </span>{/if}
                  <em class="ed-accent">{s.service}</em> is <strong>{s.state}</strong>
                {/each}
                {#if services.some((s) => s.state === 'running' && (s.status ?? '').toLowerCase().includes('unhealthy'))}
                  <span class="stack-headsup-sep"> · </span>
                  one or more containers report <strong>unhealthy</strong> via Docker healthcheck
                {/if}
              </p>
            </div>
          </section>
        {/if}
      </div>

      <!-- RIGHT: rolled-up live resources + endpoints + networks/volumes.
           Stats arrive on the per-container WS stream; the metrics
           silently update every poll (~1s on the backend). -->
      <aside class="stack-overview-rail">
        <section class="stack-block">
          <Eyebrow>Resources · rolled up</Eyebrow>
          <EdMetric
            label="CPU · stack"
            value={statsRollup.cpuLabel}
            meta={statsRollup.cpuMeta}
          />
          <EdMetric
            label="Memory"
            value={statsRollup.memLabel}
            meta={statsRollup.memMeta}
          />
          <EdMetric
            label="Network · in"
            value={statsRollup.netLabel}
            meta={statsRollup.netMeta}
          />
        </section>

        <section class="stack-block">
          <Eyebrow>Endpoints</Eyebrow>
          {#if composeEndpoints.length === 0}
            <p class="stack-empty-line">No <code>ports:</code> declared in compose.</p>
          {:else}
            <ul class="stack-endpoints">
              {#each composeEndpoints as ep, i (ep.service + i)}
                <li>
                  <span class="stack-endpoint-svc">{ep.service}</span>
                  <span class="stack-endpoint-port">{ep.port}</span>
                  {#if ep.mode === 'host'}
                    <ExternalLink size={11} strokeWidth={1.5} class="stack-endpoint-icon" />
                  {:else}
                    <span class="stack-endpoint-tag">internal</span>
                  {/if}
                </li>
              {/each}
            </ul>
          {/if}
        </section>

        <section class="stack-block">
          <Eyebrow>Networks · volumes</Eyebrow>
          {#if composeNetworks.length === 0 && composeVolumes.length === 0}
            <p class="stack-empty-line">No top-level networks or volumes declared.</p>
          {:else}
            <p class="stack-netvol">
              {#each composeNetworks as n, i (n)}
                {#if i > 0}<br/>{/if}<span class="stack-netvol-prefix">net:</span>
                <span class="stack-netvol-name">{n}</span>
              {/each}
              {#if composeNetworks.length > 0 && composeVolumes.length > 0}<br/>{/if}
              {#each composeVolumes as v, i (v)}
                {#if i > 0}<br/>{/if}<span class="stack-netvol-prefix">vol:</span>
                <span class="stack-netvol-name">{v}</span>
              {/each}
            </p>
          {/if}
        </section>
      </aside>
    </div>
  {/if}

  <!-- ─────────────────────────── Compose ───────────────────────────
       Read-only by default — edit-and-redeploy is a deliberate two-step
       so an accidental keystroke can't change a deployed compose. The
       textarea only mounts when the operator clicks "Edit". -->
  {#if activeTab === 'compose'}
    <div class="stack-pane">
      <section class="stack-block">
        <div class="stack-block-head">
          <Eyebrow>
            compose.yaml
            {#if dirty}<span class="stack-dirty"> · unsaved</span>{/if}
          </Eyebrow>
          <div class="ed-actions">
            {#if !composeEditing}
              {#if canWrite}
                <button
                  type="button"
                  class="dm-btn dm-btn-secondary dm-btn-sm"
                  onclick={startComposeEdit}
                >
                  <Save size={13} strokeWidth={1.5} />
                  Edit
                </button>
              {/if}
            {:else}
              <button
                type="button"
                class="dm-btn dm-btn-ghost dm-btn-sm"
                onclick={() => { composeEditing = false; }}
                disabled={anyBusy}
              >Cancel</button>
              <button
                type="button"
                class="dm-btn dm-btn-secondary dm-btn-sm"
                onclick={saveCompose}
                disabled={anyBusy || !dirty}
              >
                <Save size={13} strokeWidth={1.5} />
                Save
              </button>
              {#if canDeploy}
                <button
                  type="button"
                  class="dm-btn dm-btn-primary dm-btn-sm"
                  onclick={saveAndRedeploy}
                  disabled={anyBusy || !dirty}
                >
                  <Play size={13} strokeWidth={1.5} />
                  Save &amp; deploy
                </button>
              {/if}
            {/if}
          </div>
        </div>
        {#if !composeEditing}
          <pre class="stack-yaml-view yaml">{#each composeLines as line, i (i)}<div class="stack-yaml-line">{@html highlightYamlLine(line) || '&nbsp;'}</div>{/each}</pre>
        {:else}
          <textarea
            class="stack-yaml"
            bind:value={compose}
            oninput={() => (dirty = true)}
            spellcheck="false"
          ></textarea>
        {/if}
      </section>
    </div>
  {/if}

  <!-- ─────────────────────────── Environment ─────────────────────────── -->
  {#if activeTab === 'environment'}
    <div class="stack-pane">
      <section class="stack-block">
        <div class="stack-block-head">
          <Eyebrow>
            Environment · {envRows.length} key{envRows.length === 1 ? '' : 's'}
            {#if dirty}<span class="stack-dirty"> · unsaved</span>{/if}
          </Eyebrow>
          <div class="ed-actions">
            <button
              type="button"
              class="dm-btn dm-btn-ghost dm-btn-sm"
              onclick={() => (envShowSecrets = !envShowSecrets)}
            >
              {#if envShowSecrets}
                <EyeOff size={13} strokeWidth={1.5} /> Hide secrets
              {:else}
                <Eye size={13} strokeWidth={1.5} /> Show secrets
              {/if}
            </button>
            {#if canWrite}
              <button
                type="button"
                class="dm-btn dm-btn-secondary dm-btn-sm"
                onclick={envAddRow}
              >
                <Plus size={13} strokeWidth={1.5} /> Add variable
              </button>
            {/if}
            {#if dirty}
              <button
                type="button"
                class="dm-btn dm-btn-primary dm-btn-sm"
                onclick={save}
                disabled={anyBusy}
              >
                <Save size={13} strokeWidth={1.5} /> Save
              </button>
            {/if}
          </div>
        </div>

        {#if envRows.length === 0}
          <div class="env-empty">
            <p>
              No <code>.env</code> variables yet. Click
              <em class="ed-accent">Add variable</em> above to define one — values
              referenced as <code>${'{KEY}'}</code> in compose are resolved at deploy time.
            </p>
          </div>
        {:else}
          <table class="ed-table env-table">
            <thead>
              <tr>
                <th>Key</th>
                <th>Value</th>
                <th>Scope</th>
                <th class="env-th-actions"></th>
              </tr>
            </thead>
            <tbody>
              {#each envRows as row, i (i)}
                {@const isSec = isSecretKey(row.key)}
                {@const scopes = envScopeFor(compose, row.key)}
                {@const editing = envEditingIdx === i}
                <tr>
                  <td class="env-cell-key">
                    {#if editing}
                      <input
                        class="ed-input ed-input-mono env-cell-input"
                        bind:value={envRows[i].key}
                        oninput={commitEnvRows}
                        onkeydown={(e) => e.key === 'Enter' && envSaveEdit()}
                      />
                    {:else}
                      <span class="font-mono">{row.key}</span>
                    {/if}
                  </td>
                  <td class="env-cell-value">
                    {#if editing}
                      <input
                        class="ed-input ed-input-mono env-cell-input"
                        bind:value={envRows[i].value}
                        oninput={commitEnvRows}
                        onkeydown={(e) => e.key === 'Enter' && envSaveEdit()}
                      />
                    {:else}
                      <span class="font-mono">
                        {#if isSec && !envShowSecrets}
                          ••••••••••••
                        {:else if row.value}
                          {row.value}
                        {:else}
                          <span class="env-value-empty">(empty)</span>
                        {/if}
                      </span>
                      {#if isSec}
                        <span class="dm-pill dm-pill-neutral env-secret-pill">secret</span>
                      {/if}
                    {/if}
                  </td>
                  <td class="env-cell-scope">
                    {#if scopes.length === 0}
                      <span class="env-scope-unused">unused</span>
                    {:else}
                      <span class="env-scope-list">
                        {#each scopes as svc, j (svc)}
                          {#if j > 0}<span class="env-scope-sep">,</span>{/if}<span class="env-scope-svc">{svc}</span>
                        {/each}
                      </span>
                    {/if}
                  </td>
                  <td class="env-cell-actions">
                    {#if canWrite}
                      {#if editing}
                        <button
                          type="button"
                          class="stack-svc-btn"
                          onclick={envSaveEdit}
                          title="Done"
                          aria-label="Done editing"
                        >
                          <CheckCircle2 size={13} strokeWidth={1.5} />
                        </button>
                      {:else}
                        <button
                          type="button"
                          class="stack-svc-btn"
                          onclick={() => (envEditingIdx = i)}
                          title="Edit {row.key}"
                          aria-label="Edit {row.key}"
                        >
                          <Pencil size={12} strokeWidth={1.5} />
                        </button>
                      {/if}
                      <button
                        type="button"
                        class="stack-svc-btn env-cell-delete"
                        onclick={() => envDeleteRow(i)}
                        title="Delete {row.key}"
                        aria-label="Delete {row.key}"
                      >
                        <Trash2 size={12} strokeWidth={1.5} />
                      </button>
                    {/if}
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        {/if}
      </section>

      <section class="stack-block">
        <Eyebrow>Overlays · {envs?.available.length ?? 0}</Eyebrow>
        {#if !envs || envs.available.length === 0}
          <p class="stack-empty-line">
            No overlays. Drop <code>compose.{`<env>`}.yaml</code> next to <code>compose.yaml</code> and
            an overlay shows up here.
          </p>
        {:else}
          <p class="stack-explainer">
            This stack has <em class="ed-accent">{envs.available.length}</em> overlay{envs.available.length > 1 ? 's' : ''}.
            {#if envs.active}
              Deploys merge <code>compose.{envs.active}.yaml</code> on top of the base.
            {:else}
              Deploys use the base <code>compose.yaml</code> as-is.
            {/if}
          </p>
          {#if canWrite}
            <div class="stack-env-pills">
              <button
                type="button"
                class="stack-env-pill"
                class:active={envs.active === ''}
                onclick={() => setActiveEnv('')}
                disabled={envBusy}
              >base</button>
              {#each envs.available as e (e)}
                <button
                  type="button"
                  class="stack-env-pill"
                  class:active={envs.active === e}
                  onclick={() => setActiveEnv(e)}
                  disabled={envBusy}
                >{e}</button>
              {/each}
            </div>
          {/if}
        {/if}
      </section>

      <section class="stack-block">
        <div class="stack-block-head">
          <Eyebrow>Dependencies</Eyebrow>
          {#if canWrite}
            <button class="stack-inline-link" onclick={openDepsEditor}>Edit</button>
          {/if}
        </div>
        {#if !deps || (deps.depends_on.length === 0 && deps.dependents.length === 0)}
          <p class="stack-empty-line">No prerequisites or dependents.</p>
        {:else}
          <div class="stack-deps">
            {#if deps.depends_on.length > 0}
              <div>
                <span class="stack-deps-label">needs</span>
                {#each deps.depends_on as d (d)}
                  <a href={`/stacks/${encodeURIComponent(d)}`} class="stack-dep-pill">{d}</a>
                {/each}
              </div>
            {/if}
            {#if deps.dependents.length > 0}
              <div>
                <span class="stack-deps-label">needed by</span>
                {#each deps.dependents as d (d)}
                  <a href={`/stacks/${encodeURIComponent(d)}`} class="stack-dep-pill">{d}</a>
                {/each}
              </div>
            {/if}
          </div>
        {/if}
      </section>
    </div>
  {/if}

  <!-- ─────────────────────────── Logs (live) ─────────────────────────── -->
  {#if activeTab === 'logs'}
    <div class="stack-pane">
      <div class="stack-logs-bar">
        <div class="stack-logs-services">
          {#each services as s (s.service)}
            {@const isActiveFilter = logServiceFilter.size === 0 || logServiceFilter.has(s.service)}
            {@const hasStream = logSockets.has(s.service)}
            <button
              type="button"
              class="stack-log-svc"
              class:active={isActiveFilter}
              onclick={() => toggleLogService(s.service)}
              style:--svc-color={svcColor(s.service)}
            >
              <span class="stack-log-svc-dot" class:live={hasStream}></span>
              {s.service}
            </button>
          {/each}
          {#if logServiceFilter.size > 0}
            <button
              type="button"
              class="stack-inline-link"
              onclick={() => { logServiceFilter = new Set(); }}
            >clear filter</button>
          {/if}
        </div>
        <div class="stack-logs-actions">
          <button
            type="button"
            class="dm-btn dm-btn-{logTailing ? 'primary' : 'secondary'} dm-btn-xs"
            onclick={toggleLogTailing}
          >
            {#if logTailing}
              <Square class="w-3 h-3" />
              tailing
            {:else}
              <Play class="w-3 h-3" />
              paused
            {/if}
          </button>
          <button
            type="button"
            class="dm-btn dm-btn-ghost dm-btn-xs"
            onclick={() => { logLines = []; }}
            title="Clear buffer"
          >clear</button>
        </div>
      </div>

      <div class="log-viewer stack-log-viewer" bind:this={logEl}>
        {#if visibleLogLines.length === 0}
          <div class="stack-log-empty">
            {#if logSockets.size === 0}
              <em>not streaming — toggle "tailing" to start.</em>
            {:else}
              <em>waiting for the first line…</em>
            {/if}
          </div>
        {:else}
          {#each visibleLogLines as l, i (i)}
            {@const kind = logLevelKind(l.line)}
            <div class="log-line">
              <span class="log-time" title={new Date(l.ts).toISOString()}>
                {new Date(l.ts).toLocaleTimeString()}
              </span>
              <span class="log-svc" style:color={svcColor(l.service)}>{l.service}</span>
              <span
                class="log-msg"
                class:log-msg--warn={kind === 'warn'}
                class:log-msg--err={kind === 'err'}
                class:log-msg--ok={kind === 'ok'}
              >{l.line}</span>
            </div>
          {/each}
        {/if}
      </div>

      <p class="stack-log-foot">
        showing last <em class="ed-accent">{visibleLogLines.length}</em> lines · merged from
        {logSockets.size} container{logSockets.size === 1 ? '' : 's'} · buffer cap {LOG_BUFFER_MAX}
      </p>
    </div>
  {/if}

  <!-- ─────────────────────────── History ─────────────────────────── -->
  {#if activeTab === 'history'}
    <div class="stack-pane">
      <section class="stack-block">
        <div class="stack-block-head">
          <Eyebrow>Deploys · {historyEntries.length}</Eyebrow>
          <button
            class="stack-inline-link"
            onclick={loadHistory}
            disabled={historyLoading}
            title="Refresh history"
          >
            <RefreshCw class="w-3 h-3 {historyLoading ? 'animate-spin' : ''}" />
            refresh
          </button>
        </div>

        {#if historyLoading && historyEntries.length === 0}
          <div class="stack-skeleton">
            <Skeleton width="40%" height="1rem" />
            <Skeleton width="100%" height="2.5rem" />
            <Skeleton width="100%" height="2.5rem" />
          </div>
        {:else if historyEntries.length === 0}
          <div class="stack-empty">
            <p>No deploy history yet. The next successful deploy lands here and you can roll back to it.</p>
          </div>
        {:else}
          <ol class="stack-history">
            <span class="stack-history-rule" aria-hidden="true"></span>
            {#each historyEntries as entry, i (entry.id)}
              <li class="stack-history-row">
                <span
                  class="stack-history-dot"
                  class:current={i === 0}
                  aria-hidden="true"
                ></span>
                <div class="stack-history-body">
                  <div class="stack-history-head">
                    <span class="stack-history-version">#{entry.version}</span>
                    {#if entry.success === false}
                      <span class="stack-history-status stack-history-status-fail" title={entry.error_message ?? 'Deploy failed'}>
                        <XCircle size={12} strokeWidth={1.5} />
                      </span>
                    {:else if entry.success === true}
                      <span class="stack-history-status stack-history-status-ok" title="Deploy succeeded">
                        <CheckCircle2 size={12} strokeWidth={1.5} />
                      </span>
                    {/if}
                    {#if i === 0 && entry.success !== false}
                      <span class="dm-pill dm-pill-success">
                        <span class="dm-pill-dot"></span>current
                      </span>
                    {/if}
                    {#if entry}
                      {@const badge = deployTriggerBadge(entry.note)}
                      <span class="dm-pill dm-pill-{badge.variant}" title={entry.note || 'manual deploy'}>
                        <span class="dm-pill-dot"></span>{badge.label}
                      </span>
                    {/if}
                    <span class="stack-history-time" title={new Date(entry.deployed_at).toLocaleString()}>
                      {relTime(entry.deployed_at)}
                    </span>
                    {#if entry.duration_ms != null && entry.duration_ms > 0}
                      <span class="stack-history-duration" title="Deploy took {(entry.duration_ms / 1000).toFixed(1)} s">
                        {formatDuration(entry.duration_ms)}
                      </span>
                    {/if}
                    {#if entry.deployed_by_name}
                      <span class="stack-history-actor">
                        <User class="w-3 h-3" />
                        {entry.deployed_by_name}
                      </span>
                    {/if}
                    {#if entry.git_commit_sha}
                      {@const sha = entry.git_commit_sha.slice(0, 7)}
                      {@const url = buildGitCommitURL(gitSource?.repo_url ?? '', entry.git_commit_sha)}
                      {#if url}
                        <a href={url} target="_blank" rel="noopener" class="stack-history-sha" title="View commit on remote">
                          {sha}
                        </a>
                      {:else}
                        <code class="stack-history-sha" title={entry.git_commit_sha}>{sha}</code>
                      {/if}
                    {/if}
                    <span class="stack-history-spacer"></span>
                    <button
                      type="button"
                      class="stack-svc-btn"
                      title="View compose.yaml"
                      aria-label="View compose.yaml"
                      onclick={() => openYaml(entry.id)}
                    >
                      <FileText size={12} strokeWidth={1.5} />
                    </button>
                    {#if canDeploy && i !== 0}
                      <Button
                        variant="secondary"
                        size="sm"
                        onclick={() => openRollbackConfirm(entry.id)}
                      >
                        <RotateCcw class="w-3 h-3" />
                        Roll back
                      </Button>
                    {/if}
                  </div>
                  {#if entry.services && entry.services.length > 0}
                    {@const prev = historyEntries[i + 1]?.services ?? []}
                    {@const isExpanded = i < 10 || expandedRows.has(entry.id)}
                    {@const changedCount = entry.services.filter((svc) => {
                      const prevImg = prev.find((p) => p.service === svc.service)?.image;
                      return prevImg !== undefined && prevImg !== svc.image;
                    }).length}
                    {#if isExpanded}
                      <ul class="stack-history-svc-list">
                        {#each entry.services as svc (svc.service)}
                          {@const parts = splitImageRef(svc.image)}
                          {@const prevImage = prev.find((p) => p.service === svc.service)?.image}
                          {@const changed = prevImage !== undefined && prevImage !== svc.image}
                          <li class="stack-history-svc-row" class:stack-history-svc-changed={changed}>
                            <span class="stack-history-svc-name">{svc.service}</span>
                            <code class="stack-history-svc-image" title={svc.image}>{parts.host ? parts.host + '/' : ''}{parts.repo}:<span class="stack-history-svc-tag">{parts.tag}</span></code>
                            {#if changed}
                              <span class="stack-history-svc-delta" title="Changed vs #{historyEntries[i + 1]?.version}">Δ</span>
                            {/if}
                          </li>
                        {/each}
                      </ul>
                      {#if i >= 10}
                        <button
                          type="button"
                          class="stack-history-collapse"
                          onclick={() => collapseRow(entry.id)}
                        >
                          collapse
                        </button>
                      {/if}
                    {:else}
                      <button
                        type="button"
                        class="stack-history-expand"
                        onclick={() => expandRow(entry.id)}
                        title="Show services"
                      >
                        {entry.services.length} service{entry.services.length === 1 ? '' : 's'}
                        {#if changedCount > 0}
                          · <span class="stack-history-expand-delta">{changedCount} changed</span>
                        {/if}
                        · click to expand
                      </button>
                    {/if}
                  {/if}
                </div>
              </li>
            {/each}
          </ol>
        {/if}
      </section>
    </div>
  {/if}

  <!-- ─────────────────────────── Settings ─────────────────────────── -->
  {#if activeTab === 'settings'}
    <div class="stack-pane stack-settings">
      <section class="stack-setting-row">
        <div>
          <h3>Auto-update images</h3>
          <p>Watchtower-style polling that pulls new image tags and redeploys the stack when an upstream version is published.</p>
        </div>
        <div class="stack-setting-control">
          <select class="ed-input ed-input-mono">
            <option>off</option>
            <option>patch only (~major.minor)</option>
            <option>minor (^major)</option>
            <option>always latest</option>
          </select>
        </div>
      </section>

      <section class="stack-setting-row">
        <div>
          <h3>Webhook on deploy</h3>
          <p>POST to a URL whenever a redeploy succeeds or fails — useful for chat notifications or downstream pipelines.</p>
        </div>
        <div class="stack-setting-control">
          <input class="ed-input ed-input-mono" placeholder="https://hooks.example.com/dockmesh" />
        </div>
      </section>

      <section class="stack-setting-row">
        <div>
          <h3>Restart policy</h3>
          <p>Default restart policy applied to services that don't specify one in compose.</p>
        </div>
        <div class="stack-setting-control">
          <select class="ed-input ed-input-mono">
            <option>no</option>
            <option>on-failure</option>
            <option selected>unless-stopped</option>
            <option>always</option>
          </select>
        </div>
      </section>

      <section class="stack-setting-row stack-setting-danger">
        <div>
          <h3>Delete this stack</h3>
          <p>
            Stops all <strong>{services.length}</strong> service{services.length === 1 ? '' : 's'},
            removes containers and named volumes, deletes <code>stacks/{name}/</code> on disk and
            forgets compose history. <strong>Cannot be undone.</strong>
          </p>
        </div>
        <div class="stack-setting-control">
          <button
            type="button"
            class="dm-btn dm-btn-danger dm-btn-sm"
            onclick={openDelete}
            disabled={anyBusy}
          >
            <Trash2 size={13} strokeWidth={1.5} />
            Delete {name}
          </button>
        </div>
      </section>
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
              <span class="text-[var(--fg-muted)] truncate">{check.detail}</span>
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
          {#if gitSource?.has_webhook_secret}<span class="font-normal normal-case">— set to keep</span>{/if}
        </label>
        <input id="git-webhook" type="password" class="dm-input" bind:value={gitForm.webhook_secret as any} />
      </div>
    </div>
    <label class="flex items-center gap-2 text-sm">
      <input type="checkbox" bind:checked={gitForm.auto_deploy as any} />
      Auto-deploy on new commits
    </label>
    {#if gitSource?.has_webhook_secret || (gitSource && !gitSource.has_webhook_secret)}
      <div class="text-xs text-[var(--fg-muted)] bg-[var(--bg-muted)] rounded p-2 border border-[var(--border)]">
        <p class="font-medium text-[var(--fg)] mb-1">Webhook URL</p>
        <code class="text-[11px] font-mono break-all">POST /api/v1/stacks/{name}/git/webhook</code>
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
  .stack-detail-frame {
    display: flex;
    flex-direction: column;
    gap: 22px;
    max-width: 1480px;
    padding-bottom: 48px;
  }

  /* Header — eyebrow, italic title, status pill + meta inline,
     action cluster on the right. Matches the mockup's stack-detail
     header pattern (no narrative subtitle). */
  .stack-header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 24px;
    flex-wrap: wrap;
  }
  .stack-header-text {
    min-width: 0;
    max-width: 80ch;
    flex: 1 1 40ch;
  }
  .stack-title-row {
    display: flex;
    align-items: baseline;
    gap: 14px;
    flex-wrap: wrap;
    margin-top: 10px;
  }
  .stack-title {
    font-size: 26px;
    line-height: 1.2;
    letter-spacing: -0.02em;
  }
  /* Override the global .ed-title em italic-serif treatment for the stack
     name. Stack identifiers are programmatic — they read better in a
     bold sans (consistent with container names) than in italic Newsreader. */
  .stack-title :global(em) {
    font-family: var(--font-sans);
    font-style: normal;
    font-weight: 700;
    color: var(--fg);
  }
  .stack-meta {
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: var(--fg-subtle);
    letter-spacing: 0.04em;
    display: inline-flex;
    align-items: baseline;
    gap: 8px;
  }
  .stack-meta-sep { color: var(--border-strong); }
  :global(.stack-meta-spin) {
    color: var(--accent-fg);
    animation: stack-spin 0.9s linear infinite;
    display: inline-block;
  }
  @keyframes stack-spin { to { transform: rotate(360deg); } }

  /* Tab strip. Inherit the global .ed-tabs / .ed-tab styles, just nudge
     the bottom margin so it sits cleanly above the content cards. */
  .stack-tabs { margin-top: 6px; }

  /* ─────────────────── Overview layout ─────────────────── */
  .stack-overview {
    display: grid;
    grid-template-columns: minmax(0, 1.55fr) minmax(280px, 1fr);
    gap: 32px;
    margin-top: 14px;
  }
  @media (max-width: 1100px) {
    .stack-overview { grid-template-columns: 1fr; gap: 24px; }
  }
  .stack-overview-main { display: flex; flex-direction: column; gap: 28px; min-width: 0; }
  .stack-overview-rail { display: flex; flex-direction: column; gap: 24px; min-width: 0; }

  .stack-block { display: flex; flex-direction: column; gap: 12px; min-width: 0; }
  .stack-block-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
  }
  .stack-skeleton { display: flex; flex-direction: column; gap: 8px; }
  .stack-empty {
    padding: 18px 0;
    color: var(--fg-muted);
    font-size: 13px;
    border-top: 1px solid var(--border);
    border-bottom: 1px solid var(--border-subtle);
  }
  .stack-empty p { margin: 0; }
  .stack-empty-line {
    margin: 0;
    color: var(--fg-subtle);
    font-size: 12.5px;
  }
  .stack-explainer {
    margin: 0;
    color: var(--fg-muted);
    font-size: 13px;
    line-height: 1.6;
  }
  .stack-explainer code { font-size: 11.5px; }

  /* Services list — transparent rows, hairline dividers */
  .stack-services {
    border-top: 1px solid var(--border);
  }
  .stack-svc-name { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
  .stack-svc-id {
    font-size: 13.5px;
    color: var(--fg);
    font-weight: 500;
    display: inline-flex;
    align-items: center;
    gap: 8px;
  }
  .stack-svc-replicas {
    font-family: var(--font-mono);
    font-size: 10px;
    padding: 1px 5px;
    border-radius: 3px;
    background: color-mix(in srgb, var(--color-brand-500) 15%, transparent);
    color: var(--color-brand-300);
  }
  .stack-svc-meta {
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
  }
  .stack-svc-image {
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: var(--fg-muted);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .stack-svc-actions {
    display: inline-flex;
    align-items: center;
    gap: 4px;
  }
  .stack-svc-btn {
    background: transparent;
    border: 0;
    color: var(--fg-subtle);
    width: 24px;
    height: 24px;
    border-radius: 4px;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    cursor: pointer;
    transition: color 0.12s, background 0.12s;
  }
  .stack-svc-btn:hover { color: var(--fg); background: var(--surface-hover); }

  /* Heads-up panel — single-line warning surface */
  .stack-headsup {
    display: grid;
    grid-template-columns: auto 1fr;
    gap: 12px;
    padding: 12px 14px;
    border: 1px dashed color-mix(in srgb, var(--color-warning-500) 50%, var(--border));
    border-radius: 5px;
    background: color-mix(in srgb, var(--color-warning-500) 5%, transparent);
  }
  :global(.stack-headsup-icon) { color: var(--color-warning-400); margin-top: 4px; flex-shrink: 0; }
  .stack-headsup p {
    margin: 0;
    font-size: 13px;
    color: var(--fg-muted);
    line-height: 1.6;
  }
  .stack-headsup-sep { color: var(--border-strong); }

  /* Right-rail blocks */
  /* Resources rollup — vertical stack of 3 EdMetrics so the Network
     line and the Memory line aren't squashed in a 3-column grid. */
  .stack-overview-rail .stack-block:first-child { gap: 8px; }
  .stack-overview-rail .stack-block:first-child :global(.ed-metric) { padding: 12px 14px; }

  /* Endpoints — compact list, no card chrome, hairline rows. Service
     left, port spec mid, link/internal hint right. Matches the mockup's
     <li> per-row pattern with subtle border-bottom dividers. */
  .stack-endpoints {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
  }
  .stack-endpoints li {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 12px;
    padding: 8px 0;
    border-bottom: 1px solid var(--border-subtle);
    font-family: var(--font-mono);
    font-size: 11.5px;
  }
  .stack-endpoints li:last-child { border-bottom: 0; }
  .stack-endpoint-svc { color: var(--fg); white-space: nowrap; flex-shrink: 0; }
  .stack-endpoint-port { color: var(--fg-muted); flex: 1; text-align: right; }
  .stack-endpoint-tag {
    font-size: 9.5px;
    color: var(--fg-subtle);
    letter-spacing: 0.04em;
    text-transform: lowercase;
  }
  :global(.stack-endpoint-icon) { color: var(--fg-subtle); flex-shrink: 0; }

  /* Networks · volumes — single mono paragraph, label-prefix muted +
     name in body color. Compact, no per-line border or card. */
  .stack-netvol {
    margin: 0;
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: var(--fg-muted);
    line-height: 1.7;
  }
  .stack-netvol-prefix { color: var(--fg-subtle); margin-right: 6px; }
  .stack-netvol-name { color: var(--accent-fg); }

  /* ─────────────────── Compose / Environment / Logs / History panes ─────────────────── */
  .stack-pane {
    display: flex;
    flex-direction: column;
    gap: 24px;
    margin-top: 14px;
  }
  .stack-yaml {
    width: 100%;
    min-height: 360px;
    padding: 14px 16px;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--bg);
    color: var(--fg);
    font-family: var(--font-mono);
    font-size: 12.5px;
    line-height: 1.65;
    resize: vertical;
    transition: border-color 0.15s;
  }
  .stack-yaml:focus { outline: none; border-color: var(--color-brand-500); }
  .stack-yaml-short { min-height: 100px; }
  .stack-dirty { color: var(--color-warning-400); font-weight: 500; }

  /* Read-only YAML viewer — same chrome as the textarea so the toggle
     is visually seamless. Rendered as a stack of <div> lines so we can
     run the highlighter per line. */
  .stack-yaml-view {
    margin: 0;
    width: 100%;
    min-height: 360px;
    padding: 14px 16px;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: var(--bg);
    font-family: var(--font-mono);
    font-size: 12.5px;
    line-height: 1.65;
    color: var(--fg);
    overflow: auto;
    white-space: pre;
  }
  .stack-yaml-line { min-height: 1.65em; }
  .stack-yaml-view :global(.y-key) { color: var(--accent-fg); }
  .stack-yaml-view :global(.y-str) { color: var(--color-warning-400); }
  .stack-yaml-view :global(.y-num) { color: var(--color-success-400); }
  .stack-yaml-view :global(.y-com) { color: var(--fg-subtle); font-style: normal; }
  .stack-inline-link {
    background: transparent;
    border: 0;
    cursor: pointer;
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--accent-fg);
    letter-spacing: 0.04em;
    display: inline-flex;
    align-items: center;
    gap: 4px;
  }
  .stack-inline-link:hover { color: var(--fg); }
  .stack-inline-link:disabled { opacity: 0.5; cursor: not-allowed; }

  /* Env-vars table */
  .env-table { width: 100%; }
  .env-table th, .env-table td { padding: 10px 12px; }
  .env-cell-key { width: 26%; }
  .env-cell-value { width: 38%; }
  .env-cell-scope { width: auto; }
  .env-cell-actions { width: 64px; text-align: right; }
  .env-th-actions { width: 64px; }
  .env-cell-input {
    border-bottom: 1px solid var(--border-strong);
    padding: 4px 0;
    font-size: 12.5px;
  }
  .env-secret-pill { margin-left: 8px; }
  .env-value-empty { color: var(--fg-subtle); font-style: normal; }
  .env-scope-list {
    display: inline-flex;
    flex-wrap: wrap;
    gap: 0 4px;
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: var(--fg-muted);
  }
  .env-scope-svc { color: var(--accent-fg); }
  .env-scope-sep { color: var(--border-strong); margin-right: 4px; }
  .env-scope-unused {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fg-subtle);
    font-style: normal;
  }
  .env-cell-actions { display: flex; gap: 2px; justify-content: flex-end; }
  .env-cell-delete { color: var(--fg-subtle); }
  .env-cell-delete:hover { color: var(--color-danger-400); }
  .env-empty {
    padding: 18px 0;
    color: var(--fg-muted);
    font-size: 13px;
    border-top: 1px solid var(--border);
    border-bottom: 1px solid var(--border-subtle);
  }
  .env-empty p { margin: 0; line-height: 1.6; }
  .env-empty code { font-family: var(--font-mono); font-size: 11.5px; color: var(--fg-muted); }

  .stack-env-pills { display: flex; flex-wrap: wrap; gap: 6px; }
  .stack-env-pill {
    padding: 4px 10px;
    border: 1px solid var(--border);
    border-radius: 5px;
    background: transparent;
    color: var(--fg-muted);
    font-family: var(--font-mono);
    font-size: 11.5px;
    cursor: pointer;
    transition: border-color 0.12s, background 0.12s, color 0.12s;
  }
  .stack-env-pill:hover { background: var(--surface-hover); color: var(--fg); }
  .stack-env-pill.active {
    border-color: var(--color-brand-500);
    background: color-mix(in srgb, var(--color-brand-500) 12%, transparent);
    color: var(--accent-fg);
  }
  .stack-env-pill:disabled { opacity: 0.5; cursor: not-allowed; }

  .stack-deps { display: flex; flex-direction: column; gap: 10px; font-size: 12.5px; }
  .stack-deps > div {
    display: inline-flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 6px;
  }
  .stack-deps-label {
    font-family: var(--font-mono);
    font-size: 10.5px;
    letter-spacing: 0.14em;
    text-transform: uppercase;
    color: var(--fg-subtle);
    margin-right: 4px;
  }
  .stack-dep-pill {
    display: inline-flex;
    align-items: center;
    padding: 2px 8px;
    border: 1px solid var(--border);
    border-radius: 3px;
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: var(--fg);
    text-decoration: none;
  }
  .stack-dep-pill:hover { background: var(--surface-hover); border-color: var(--border-strong); }

  /* ─────────────────── Logs viewer ─────────────────── */
  .stack-logs-bar {
    display: flex;
    align-items: center;
    gap: 12px;
    flex-wrap: wrap;
  }
  .stack-logs-services {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    align-items: center;
  }
  .stack-log-svc {
    --svc-color: var(--accent-fg);
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 4px 9px;
    border: 1px solid var(--border);
    border-radius: 4px;
    background: transparent;
    color: var(--fg-muted);
    font-family: var(--font-mono);
    font-size: 11px;
    cursor: pointer;
    transition: color 0.12s, border-color 0.12s, background 0.12s;
  }
  .stack-log-svc:hover { color: var(--fg); background: var(--surface-hover); }
  .stack-log-svc.active {
    color: var(--svc-color);
    border-color: color-mix(in srgb, var(--accent) 35%, var(--border));
  }
  .stack-log-svc-dot {
    width: 6px;
    height: 6px;
    border-radius: 999px;
    background: var(--border-strong);
  }
  .stack-log-svc-dot.live {
    background: var(--svc-color);
    box-shadow: 0 0 0 2px color-mix(in srgb, var(--svc-color) 30%, transparent);
  }
  .stack-logs-actions { margin-left: auto; display: inline-flex; gap: 8px; }
  .stack-log-viewer {
    max-height: 480px;
    min-height: 280px;
  }
  .stack-log-empty {
    padding: 22px;
    text-align: center;
    color: var(--fg-subtle);
    font-size: 12.5px;
  }
  .stack-log-foot {
    margin: 0;
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
    letter-spacing: 0.04em;
  }

  /* ─────────────────── History timeline ─────────────────── */
  .stack-history {
    list-style: none;
    margin: 0;
    padding: 8px 0 0;
    position: relative;
  }
  .stack-history-rule {
    position: absolute;
    left: 7px;
    top: 14px;
    bottom: 14px;
    width: 1px;
    background: var(--border);
  }
  .stack-history-row {
    position: relative;
    display: grid;
    grid-template-columns: 22px 1fr;
    gap: 12px;
    padding: 10px 0 18px;
  }
  .stack-history-dot {
    width: 11px;
    height: 11px;
    border-radius: 999px;
    background: var(--bg);
    border: 2px solid var(--border-strong);
    margin-top: 6px;
    margin-left: 2px;
    z-index: 1;
  }
  .stack-history-dot.current { border-color: var(--color-success-500); }
  .stack-history-body { display: flex; flex-direction: column; gap: 6px; min-width: 0; }
  .stack-history-head {
    display: flex;
    align-items: center;
    gap: 10px;
    flex-wrap: wrap;
  }
  .stack-history-version {
    font-family: var(--font-mono);
    font-size: 13px;
    color: var(--fg);
    font-weight: 500;
  }
  .stack-history-time {
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: var(--fg-subtle);
  }
  .stack-history-actor {
    display: inline-flex;
    align-items: center;
    gap: 4px;
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fg-muted);
  }
  .stack-history-spacer { flex: 1; }
  .stack-history-note {
    margin: 0;
    font-size: 13px;
    color: var(--fg);
    line-height: 1.55;
  }
  .stack-history-services {
    margin: 0;
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fg-subtle);
    letter-spacing: 0.02em;
  }
  .stack-history-images { color: var(--fg-muted); }

  /* Service list — one flex row per service, name fixed-width on the
     left, image truncates with ellipsis to keep rows aligned. No more
     CSS grid + display:contents (was wrapping on narrow widths). */
  .stack-history-svc-list {
    margin: 4px 0 0;
    padding: 0;
    list-style: none;
    display: flex;
    flex-direction: column;
    gap: 2px;
    font-family: var(--font-mono);
    font-size: 11px;
  }
  .stack-history-svc-row {
    display: flex;
    align-items: baseline;
    gap: 12px;
    min-width: 0;
  }
  .stack-history-svc-name {
    color: var(--fg);
    font-weight: 500;
    flex-shrink: 0;
    min-width: 80px;
  }
  .stack-history-svc-image {
    color: var(--fg-muted);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    min-width: 0;
    flex: 1;
    font-family: var(--font-mono);
    background: none;
    padding: 0;
  }
  .stack-history-svc-tag { color: var(--fg); }
  .stack-history-svc-changed .stack-history-svc-tag {
    color: var(--color-warning-400, #d97706);
    font-weight: 500;
  }
  .stack-history-svc-delta {
    color: var(--color-warning-400, #d97706);
    font-weight: 600;
    text-align: right;
    font-size: 11px;
    align-self: center;
  }

  /* Compact mode for history rows beyond the top 10 — single one-liner
     summary instead of the full service list. */
  .stack-history-expand,
  .stack-history-collapse {
    margin-top: 4px;
    padding: 4px 8px;
    background: transparent;
    border: 1px dashed var(--border);
    border-radius: 4px;
    color: var(--fg-muted);
    font-size: 11.5px;
    font-family: var(--font-mono);
    cursor: pointer;
    align-self: flex-start;
    transition: background 0.12s, border-color 0.12s, color 0.12s;
  }
  .stack-history-expand:hover,
  .stack-history-collapse:hover {
    background: color-mix(in srgb, var(--fg) 4%, transparent);
    border-color: var(--fg-muted);
    color: var(--fg);
  }
  .stack-history-expand-delta {
    color: var(--color-warning-400, #d97706);
    font-weight: 500;
  }

  /* Compose-yaml diff in the snapshot drawer */
  .diff-context { color: var(--fg-muted); }
  .diff-add {
    background: color-mix(in srgb, var(--color-success-400, #16a34a) 12%, transparent);
    color: var(--fg);
  }
  .diff-del {
    background: color-mix(in srgb, var(--color-danger-400, #dc2626) 12%, transparent);
    color: var(--fg);
  }
  .diff-marker {
    display: inline-block;
    width: 12px;
    color: var(--fg-subtle);
    user-select: none;
  }
  .diff-add .diff-marker { color: var(--color-success-400, #16a34a); }
  .diff-del .diff-marker { color: var(--color-danger-400, #dc2626); }

  /* Live deploy-progress status text colors */
  .stack-deploy-status-done   { color: var(--color-success-400, #16a34a); }
  .stack-deploy-status-failed { color: var(--color-danger-400, #dc2626); }

  /* History-MED status icons + meta */
  .stack-history-status {
    display: inline-flex;
    align-items: center;
  }
  .stack-history-status-ok   { color: var(--color-success-400, #16a34a); }
  .stack-history-status-fail { color: var(--color-danger-400, #dc2626); }
  .stack-history-duration {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fg-subtle);
    letter-spacing: 0.02em;
  }
  .stack-history-sha {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fg-muted);
    text-decoration: none;
    padding: 1px 4px;
    border-radius: 3px;
    background: color-mix(in srgb, var(--fg) 4%, transparent);
  }
  a.stack-history-sha:hover {
    color: var(--color-brand-400, #2563eb);
    text-decoration: underline;
  }

  /* ─────────────────── Settings ─────────────────── */
  .stack-settings { gap: 0; }
  .stack-setting-row {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(260px, 320px);
    gap: 36px;
    align-items: start;
    padding: 22px 0;
    border-top: 1px solid var(--border);
  }
  .stack-setting-row:first-child { border-top: 0; padding-top: 16px; }
  .stack-setting-row h3 {
    margin: 0;
    font-size: 14px;
    color: var(--fg);
    font-weight: 500;
  }
  .stack-setting-row p {
    margin: 6px 0 0;
    font-size: 13px;
    color: var(--fg-muted);
    line-height: 1.55;
    max-width: 60ch;
  }
  .stack-setting-row p code { font-size: 11.5px; }
  .stack-setting-control { display: flex; flex-direction: column; gap: 6px; min-width: 0; }
  .stack-setting-note {
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
    letter-spacing: 0.02em;
  }
  .stack-setting-danger { border-top-color: color-mix(in srgb, var(--color-danger-500) 30%, var(--border)); }

  /* ───────────── Git source widget (P.11.11 editorial) ───────────── */
  .gs-section {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .gs-eyebrow {
    font-size: 10.5px;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--fg-subtle);
    font-weight: 500;
  }
  .gs-card {
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: 6px;
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }
  .gs-card-main {
    display: flex;
    align-items: flex-start;
    gap: 12px;
    padding: 14px 16px;
  }
  .gs-card-icon {
    flex-shrink: 0;
    width: 32px;
    height: 32px;
    border-radius: 6px;
    background: color-mix(in srgb, var(--color-brand-500) 12%, transparent);
    color: var(--color-brand-400);
    display: flex;
    align-items: center;
    justify-content: center;
  }
  .gs-card-icon-empty {
    background: color-mix(in srgb, var(--fg-muted) 8%, transparent);
    color: var(--fg-muted);
  }
  .gs-card-body { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 4px; }
  .gs-card-title {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
  }
  .gs-repo {
    font-family: var(--font-mono);
    font-size: 13px;
    font-weight: 500;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    max-width: 100%;
  }
  .gs-pill {
    font-size: 10.5px;
  }
  .gs-card-sub {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
    font-size: 11.5px;
    color: var(--fg-muted);
  }
  .gs-sha { font-family: var(--font-mono); font-size: 11px; }
  .gs-meta { font-size: 11px; }
  .gs-error {
    margin-top: 4px;
    color: var(--color-danger-400);
    font-size: 11.5px;
    display: flex;
    align-items: center;
    gap: 4px;
  }
  .gs-actions {
    flex-shrink: 0;
    display: flex;
    align-items: center;
    gap: 6px;
  }
  .gs-disconnect { color: var(--color-danger-400); }
  .gs-drift {
    background: color-mix(in srgb, var(--color-warning-500) 8%, transparent);
    border-top: 1px solid color-mix(in srgb, var(--color-warning-500) 25%, var(--border));
    padding: 10px 16px;
    display: flex;
    flex-direction: column;
    gap: 4px;
    font-size: 11.5px;
  }
  .gs-drift-title {
    display: flex;
    align-items: center;
    gap: 6px;
    color: var(--color-warning-400);
    font-weight: 500;
  }
  .gs-drift-row { display: flex; gap: 6px; align-items: baseline; flex-wrap: wrap; }
  .gs-drift-label { color: var(--fg-muted); }
  .gs-drift-row code {
    font-family: var(--font-mono);
    font-size: 11px;
    background: color-mix(in srgb, var(--fg) 4%, transparent);
    padding: 1px 4px;
    border-radius: 3px;
  }
  .gs-drift-hint {
    color: var(--fg-muted);
    font-size: 11px;
    margin-top: 2px;
  }
  .gs-card-empty {
    text-align: left;
    cursor: pointer;
    width: 100%;
    background: var(--bg);
  }
  .gs-card-empty:hover {
    border-color: color-mix(in srgb, var(--color-brand-500) 40%, var(--border));
    background: color-mix(in srgb, var(--color-brand-500) 3%, var(--bg));
  }
  .gs-empty-title { font-size: 13px; font-weight: 500; }
  .gs-empty-blurb { font-size: 11.5px; color: var(--fg-muted); }
  .gs-empty-blurb code { font-family: var(--font-mono); font-size: 10.5px; }
  .gs-empty-cta { flex-shrink: 0; align-self: center; }
</style>
