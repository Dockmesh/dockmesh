<script lang="ts">
  import { page } from '$app/state';
  import { fmtAgo, fmtBytes, fmtMem } from '$lib/format';
  import { goto } from '$app/navigation';
  import { untrack } from 'svelte';
  import { api, ApiError, runRestorable, type ScaleCheck, type PreflightResult, type Migration, type DeployHistoryEntry, type StackDependencies, type StackEnvironments } from '$lib/api';
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
  import DeleteStackDialog from '../_DeleteStackDialog.svelte';
  import { m } from '$lib/paraglide/messages.js';
  import { containerStateLabel } from '$lib/labels';
  import { fmtDateTime, fmtTime } from '$lib/i18n';

  import { isAllHosts } from '$lib/stores/host.svelte';

  // The stack detail page always operates on a specific host, never "all".
  // If the global picker is on "all", we resolve to the deployment's host
  // (from the list response) or fall back to "local".
  let deploymentHostId = $state<string>('local');
  let everDeployed = $state(true);
  const stackHost = $derived(isAllHosts(hosts.id) ? deploymentHostId : hosts.id);
  const isRemote = $derived(stackHost !== 'local');

  const name = $derived(page.params.name);

  // Scope-aware: stack-update / .deploy also require role-scope to cover
  // this stack's name (and host if known). Unscoped roles short-circuit
  // to "matches everything" in the scopeOK() helper.
  const canWrite = $derived(allowed('stacks.update'));
  const canDeploy = $derived(allowed('stacks.deploy'));

  $effect(() => {
    pageContext.set(name);
    return () => pageContext.clear();
  });

  let compose = $state('');
  let env = $state('');
  let envHidden = $state(false);
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
    const sv = { service: p.service ?? '', step: stepStr ? `(${stepStr})` : '', elapsed: elapsedStr };
    switch (p.phase) {
      case 'starting':         return m.stacks_d_dp_deploying({ elapsed: elapsedStr });
      case 'pulling':          return m.stacks_d_dp_pulling(sv);
      case 'creating':         return m.stacks_d_dp_creating(sv);
      case 'starting_container': return m.stacks_d_dp_starting(sv);
      case 'healthcheck':      return m.stacks_d_dp_healthcheck(sv);
      case 'done':             return m.stacks_d_dp_done({ elapsed: elapsedStr });
      case 'failed':           return m.stacks_d_dp_failed({ elapsed: elapsedStr });
    }
    return '';
  }

  let externalChange = $state<{ file: string; type: string } | null>(null);
  let modifiedOutside = $state<string[]>([]);
  let mountsStackDir = $state(false);

  // A deploy of stack files changed outside Dockmesh needs an explicit OK.
  async function confirmExternalChanges(err: unknown): Promise<boolean> {
    if (!(err instanceof ApiError) || err.status !== 409 || err.body?.code !== 'modified_outside') return false;
    const files: string = (err.body.files as string[] | undefined)?.join(', ') || m.stacks_ext_files_fallback();
    return confirm.ask({
      title: m.stacks_ext_title(),
      message: m.stacks_ext_message({ files, name: err.body.stack ?? name }),
      body: m.stacks_d_ext_body(),
      confirmLabel: m.stacks_ext_confirm(), danger: true,
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
    const mt = spec.match(/^(?:[\d.]+:)?(\d+):(\d+)(?:\/(\w+))?$/);
    if (mt) {
      const proto = mt[3] ? `/${mt[3]}` : '';
      return { service, port: `:${mt[1]} → ${mt[2]}${proto}`, mode: 'host' };
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
      const mt = line.match(/^ {2}([\w.-]+)\s*:\s*(.*)$/);
      if (mt) out.push(mt[1]);
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
    cpu_percent_core?: number;
    mem_used: number;
    mem_limit: number;
    mem_unlimited?: boolean;
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
    let cpu = 0, cpuCores = 0, mem = 0, memLim = 0, count = 0;
    let unlimited = false;
    for (const s of svcStats.values()) {
      cpu += s.cpu_percent;
      cpuCores += (s.cpu_percent_core ?? 0) / 100;
      mem += s.mem_used;
      memLim += s.mem_limit;
      unlimited ||= !!s.mem_unlimited;
      count += 1;
    }
    // One container without a limit can use all host RAM, so the sum of
    // the others' limits says nothing.
    if (unlimited) memLim = 0;
    return {
      cpu,
      cpuLabel: count > 0 ? `${cpu.toFixed(1)}%` : '—',
      cpuMeta: count > 0 ? m.stacks_d_cpu_meta({ cores: cpuCores.toFixed(2), containers: m.stacks_n_containers({ count }) }) : m.stacks_d_no_samples(),
      mem,
      memLim,
      memPct: memLim > 0 ? (mem / memLim) * 100 : 0,
      memLabel: count > 0 ? fmtMem(mem) : '—',
      memMeta: count > 0 && memLim > 0 ? m.stacks_d_mem_of_limit({ limit: fmtMem(memLim) }) : count > 0 ? m.stacks_d_no_limit() : m.stacks_d_no_samples(),
      net: netRate,
      netLabel: count > 0 ? `${fmtBytes(netRate.rx)}/s ↓` : '—',
      netMeta: count > 0 ? `${fmtBytes(netRate.tx)}/s ↑` : m.stacks_d_no_samples(),
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
      toast.error(m.stacks_d_load_history_failed(), err instanceof ApiError ? err.message : undefined);
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
      toast.error(m.stacks_d_load_snapshot_failed(), err instanceof ApiError ? err.message : undefined);
    }
  }

  // Toggle to diff-mode: fetch the previous deploy's full compose (we
  // already have its DB id from the list) and compare line-by-line.
  // No-op if there is no previous entry (i.e. the very first deploy).
  async function showYamlDiff() {
    if (!yamlEntry) return;
    const prevListEntry = historyEntries.find((e) => e.version === yamlEntry!.version - 1);
    if (!prevListEntry) {
      toast.info(m.stacks_d_no_prev_deploy());
      return;
    }
    yamlDiffLoading = true;
    try {
      yamlPrevEntry = await api.stacks.getDeployment(name, prevListEntry.id);
      yamlMode = 'diff';
    } catch (err) {
      toast.error(m.stacks_d_load_prev_failed(), err instanceof ApiError ? err.message : undefined);
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
      toast.error(m.stacks_d_load_snapshot_failed(), err instanceof ApiError ? err.message : undefined);
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
        m.stacks_d_rolled_back({ version: res.rolled_back_to }),
        m.stacks_d_services_redeployed({ count: res.result.services.length })
      );
      showRollbackConfirm = false;
      rollbackEntry = null;
      // Reload everything that moved.
      await load();
      await loadHistory();
    } catch (err) {
      toast.error(m.stacks_d_rollback_failed(), err instanceof ApiError ? err.message : undefined);
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
    const min = Math.floor(ms / 60_000);
    const s = Math.round((ms % 60_000) / 1000);
    return s === 0 ? `${min}m` : `${min}m${s}s`;
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
  // `raw` is the note text the label stands for, so the history row only
  // repeats the note when the label doesn't already show it.
  function deployTriggerBadge(note?: string): { label: string; raw: string; variant: 'info' | 'warn' | 'neutral' } {
    const n = (note ?? '').trim();
    if (n === '') return { label: m.stacks_d_trigger_manual(), raw: '', variant: 'neutral' };
    if (n.startsWith('rollback')) return { label: n, raw: n, variant: 'warn' };
    if (n === 'git auto-deploy') return { label: m.stacks_d_trigger_git(), raw: n, variant: 'info' };
    if (n.startsWith('env:')) return { label: m.stacks_d_trigger_env(), raw: '', variant: 'neutral' };
    // Custom note — show truncated, full text in tooltip.
    const label = n.length > 24 ? n.slice(0, 24) + '…' : n;
    return { label, raw: label, variant: 'neutral' };
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
      toast.success(active ? m.stacks_d_active_env({ name: active }) : m.stacks_d_env_cleared());
    } catch (err) {
      toast.error(m.stacks_d_save_failed(), err instanceof ApiError ? err.message : undefined);
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
      toast.success(m.stacks_d_deps_updated());
    } catch (err) {
      if (err instanceof ApiError && err.status === 422) {
        toast.error(m.stacks_d_cycle(), m.stacks_d_cycle_body());
      } else {
        toast.error(m.stacks_d_save_failed(), err instanceof ApiError ? err.message : undefined);
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
      toast.success(m.stacks_d_rolling_done(), `${m.stacks_d_rolling_detail({ service: rollingTarget, updated: res.updated, total: res.total_replicas })}${res.rolled_back ? m.stacks_d_rolled_back_suffix() : ''}`);
      showRolling = false;
      await loadReplicaCounts();
      await refreshStatus();
    } catch (err) {
      rollingErr = err instanceof ApiError ? err.message : m.stacks_d_rolling_failed();
    } finally {
      rollingBusy = false;
    }
  }

  async function loadReplicaCounts() {
    if (!allowed('stacks.deploy')) return;
    try {
      const list = await api.stacks.listScale(name, stackHost);
      const counts = new Map<string, number>();
      for (const e of list) counts.set(e.service, e.replicas);
      replicaCounts = counts;
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
      toast.success(m.stacks_d_scaled(), `${scaleTarget}: ${res.previous} → ${res.current}`);
      showScale = false;
      await loadReplicaCounts();
      await refreshStatus();
    } catch (err: any) {
      // Check for stateful warning (409 with force_needed).
      if (err?.status === 409) {
        try {
          const body = await err.json?.() ?? err;
          if (body?.force_needed) {
            toast.warning(body.message ?? m.stacks_d_stateful_warning());
            return;
          }
        } catch {}
      }
      toast.error(m.stacks_d_scale_failed(), err instanceof ApiError ? err.message : String(err));
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
      toast.error(m.stacks_d_preflight_failed(), err instanceof ApiError ? err.message : undefined);
    } finally {
      migratePreflightLoading = false;
    }
  }

  async function startMigration() {
    migrateBusy = true;
    try {
      let mig;
      try {
        mig = await api.migrations.initiate(name, migrateTarget);
      } catch (err) {
        if (!(await confirmExternalChanges(err))) throw err;
        mig = await api.migrations.initiate(name, migrateTarget, true);
      }
      activeMigration = mig;
      showMigrate = false;
      toast.success(m.stacks_d_migration_started(), `${name} → ${migrateTarget}`);
      pollMigration(mig.id);
    } catch (err) {
      toast.error(m.stacks_d_migration_failed(), err instanceof ApiError ? err.message : undefined);
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
        const mig = await api.migrations.get(stack, id);
        if (migrationPoll !== iv) return;
        activeMigration = mig;
        if (['completed', 'failed', 'rolled_back'].includes(mig.status)) {
          stopMigrationPoll();
          if (mig.status === 'completed') {
            toast.success(m.stacks_d_migration_completed());
            await load();
          } else {
            toast.error(mig.status === 'failed' ? m.stacks_d_migration_failed() : m.stacks_d_migration_status({ status: mig.status }), mig.error_message);
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
        toast.error(m.stacks_d_git_saved_sync_failed(), res.sync_error);
      } else {
        toast.success(m.stacks_d_git_saved(), res.sync?.changed ? m.stacks_d_synced_sha({ sha: res.sync.new_sha.slice(0, 7) }) : m.stacks_d_up_to_date());
      }
      showGitDialog = false;
      await loadGitSource();
      await load();
    } catch (err) {
      toast.error(m.stacks_d_save_failed_to(), err instanceof ApiError ? err.message : undefined);
    } finally {
      gitBusy = false;
    }
  }

  async function syncNow() {
    if (!gitSource) return;
    gitBusy = true;
    try {
      const res = await api.stacks.syncGitSource(name);
      toast.success(res.changed ? m.stacks_d_synced_sha_cap({ sha: res.new_sha.slice(0, 7) }) : m.stacks_d_already_up_to_date());
      if (res.deployed) toast.success(m.stacks_d_auto_deploy_triggered());
      await loadGitSource();
      await load();
    } catch (err) {
      toast.error(m.stacks_d_sync_failed(), err instanceof ApiError ? err.message : undefined);
    } finally {
      gitBusy = false;
    }
  }

  async function disconnectGit() {
    if (!(await confirm.ask({ title: m.stacks_d_disconnect_title(), message: m.stacks_d_disconnect_message(), body: m.stacks_d_disconnect_body(), confirmLabel: m.stacks_d_disconnect() }))) return;
    gitBusy = true;
    try {
      await api.stacks.deleteGitSource(name);
      toast.success(m.stacks_d_git_disconnected());
      gitSource = null;
    } catch (err) {
      toast.error(m.stacks_d_disconnect_failed(), err instanceof ApiError ? err.message : undefined);
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
      envHidden = !!detail.env_hidden;
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
        const hostOfStack = entry?.deployment?.host_id || entry?.target_host;
        everDeployed = !!entry?.deployment;
        if (hostOfStack) {
          deploymentHostId = hostOfStack;
        }
      } catch { /* ignore — fallback to local */ }
      try {
        services = await api.stacks.status(name, stackHost);
      } catch {
        services = [];
      }
      await loadReplicaCounts();
    } catch (err) {
      toast.error(m.stacks_d_load_failed(), err instanceof ApiError ? err.message : undefined);
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
      toast.success(m.common_saved());
    } catch (err) {
      toast.error(m.stacks_d_save_failed(), err instanceof ApiError ? err.message : undefined);
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
      toast.success(m.stacks_deployed(), m.stacks_d_deployed_on({ count: res.services.length, host: hosts.selected?.name ?? m.common_local() }));
      await refreshStatus();
      // If the user has opened History at least once, freshen it so
      // this deploy's new row appears without a manual reload.
      if (historyLoadedOnce) loadHistory();
    } catch (err) {
      toast.error(m.stacks_deploy_failed(), err instanceof ApiError ? err.message : undefined);
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
      toast.info(m.stacks_stopped_toast());
    } catch (err) {
      toast.error(m.stacks_stop_failed(), err instanceof ApiError ? err.message : undefined);
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
      toast.success(m.stacks_d_reconstructed(), m.stacks_d_reconstructed_detail({ count: cc }));
    } catch (err) {
      toast.error(m.stacks_d_recovery_failed(), err instanceof ApiError ? err.message : String(err));
    } finally {
      recoverBusy = false;
    }
  }
  async function restoreFromBackup() {
    if (restoreBusy || anyBusy || !latestBackupRun) return;
    if (!(await confirm.ask({
      title: m.stacks_d_restore_title({ name }),
      message: m.stacks_d_restore_message({ date: latestBackupRun.finished_at ?? m.stacks_d_unknown() }),
      body: m.stacks_d_restore_body(),
      confirmLabel: m.stacks_d_restore(),
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
      toast.success(m.stacks_d_restored(), m.stacks_d_restored_detail({ files: res.files_restored.length, volumes: res.volumes_restored.length }));
    } catch (err) {
      toast.error(m.stacks_d_restore_failed(), err instanceof ApiError ? err.message : String(err));
    } finally {
      restoreBusy = false;
    }
  }
  async function discardGhost() {
    if (!(await confirm.ask({
      title: m.stacks_d_discard_title({ name }),
      message: m.stacks_d_discard_message(),
      body: m.stacks_d_discard_body(),
      confirmLabel: m.stacks_d_discard(),
      danger: true
    }))) return;
    try {
      await api.stacks.discard(name);
      toast.success(m.stacks_d_discarded(), name);
      goto('/stacks');
    } catch (err) {
      toast.error(m.stacks_d_discard_failed(), err instanceof ApiError ? err.message : String(err));
    }
  }

  let showDelete = $state(false);
  function openDelete() {
    if (!anyBusy) showDelete = true;
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
    if (services.length === 0) return { tone: 'off', label: everDeployed ? m.stacks_d_state_not_running() : m.stacks_d_state_not_deployed() };
    const running = services.filter((s) => s.state === 'running');
    if (running.length === 0) return { tone: 'off', label: m.stacks_d_state_stopped() };
    const unhealthy = running.some((s) => (s.status ?? '').toLowerCase().includes('unhealthy'));
    if (running.length < services.length || unhealthy) return { tone: 'warn', label: m.stacks_d_state_degraded() };
    return { tone: 'ok', label: m.stacks_d_state_running() };
  });
  const lastDeploy = $derived(historyEntries[0]);


  const tabItems = $derived<TabItem[]>([
    { key: 'overview', label: m.common_overview(), count: services.length || null },
    { key: 'compose', label: m.stacks_d_tab_compose(), dot: dirty },
    { key: 'environment', label: m.stacks_d_tab_environment(), count: envRows.length || null },
    { key: 'logs', label: m.common_logs() },
    ...(canDeploy ? [{ key: 'history', label: m.stacks_d_tab_history(), count: historyEntries.length || null }] : []),
    { key: 'settings', label: m.common_settings() }
  ]);

  const headerMenu = $derived.by<MenuItem[]>(() => {
    const out: MenuItem[] = [];
    if (canWrite) {
      out.push({ label: gitSource ? m.stacks_d_edit_git() : m.stacks_d_connect_git(), icon: GitBranch, onclick: openGitDialog });
      if (gitSource) out.push({ label: m.stacks_d_sync_git_now(), icon: RefreshCw, onclick: syncNow, disabled: gitBusy });
      out.push({ label: m.stacks_d_edit_deps(), icon: Layers, onclick: openDepsEditor });
    }
    out.push({ label: m.stacks_d_reload_disk(), icon: RefreshCw, onclick: load });
    if (allowed('stacks.delete')) {
      out.push({ separator: true });
      out.push({ label: m.stacks_d_delete_stack(), icon: Trash2, danger: true, disabled: anyBusy, onclick: openDelete });
    }
    return out;
  });

  function serviceMenu(s: { service: string; container_id: string; image: string }): MenuItem[] {
    const out: MenuItem[] = [{ label: m.topology_open_container(), href: `/containers/${s.container_id}?from=stack` }];
    if (canDeploy) {
      out.push({ label: m.stacks_d_rolling_ellipsis(), icon: Repeat, onclick: () => openRolling(s.service) });
      out.push({ label: m.stacks_d_scale_ellipsis(), icon: Maximize2, onclick: () => openScale(s.service) });
    }
    if (canUpdateImages) {
      out.push({
        label: hasImageUpdate(s.image) ? m.stacks_d_update_image() : m.stacks_d_pull_update_image(),
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
    !isRemote && canDeploy && allowed('containers.upgrade')
  );
  // Operation per service: the container id changes with the update, the
  // service name does not.
  let svcOps = $state<Record<string, string>>({});
  function svcOp(service: string) {
    return operations.get(svcOps[service]);
  }
  async function updateImage(s: { service: string; container_id: string; image: string }) {
    const ok = await confirm.ask({
      title: m.stacks_d_update_title({ service: s.service }),
      message: m.stacks_d_update_message({ image: s.image, service: s.service }),
      body: m.stacks_d_update_body(),
      confirmLabel: m.updates_update()
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
        if (res.updated) toast.success(m.stacks_d_service_updated({ service: s.service }));
        else toast.info(m.updates_already_current({ name: s.service }));
      }
    } catch (e) {
      if (!op) toast.error(m.updates_one_failed({ name: s.service }), (e as Error).message);
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
      {#if services.length > 0}{m.stacks_d_sub_running({ running: services.filter((s) => s.state === 'running').length, total: services.length })}{/if}{isRemote && hosts.selected ? hosts.selected.name : m.common_local()}{#if lastDeploy}{m.stacks_d_sub_deployed({ ago: fmtAgo(lastDeploy.deployed_at) })}{#if lastDeploy.deployed_by_name}{m.stacks_d_sub_by({ name: lastDeploy.deployed_by_name })}{/if}{/if}
      {#if anyBusy || deployProgress}
        ·
        <span
          class:stk-deploy-failed={deployProgress?.phase === 'failed'}
          class:stk-deploy-done={deployProgress?.phase === 'done'}
        >{deployStatusLabel(deployProgress, deployElapsed) || m.stacks_d_deploying()}</span>
      {/if}
    </span>
    <span class="dm-sp"></span>
    {#if stackStatus !== 'needs_recovery' && canDeploy}
      <button type="button" class="dm-btn dm-btn-primary" onclick={deploy} disabled={anyBusy}>
        <Play size={13} strokeWidth={1.8} /> {m.common_deploy()}
      </button>
      {#if hosts.available.length > 1}
        <button type="button" class="dm-btn" onclick={openMigrate} disabled={anyBusy}>
          <ArrowRightLeft size={13} strokeWidth={1.8} /> {m.stacks_d_migrate()}
        </button>
      {/if}
    {/if}
    {#if stackStatus !== 'needs_recovery' && allowed('stacks.operate') && services.some((s) => s.state === 'running')}
      <button type="button" class="dm-btn" onclick={stop} disabled={anyBusy}>
        <Square size={12} strokeWidth={1.8} /> {m.common_stop()}
      </button>
    {/if}
    <Menu label={m.stacks_d_more_actions()} items={headerMenu} width={240} />
  </header>

  {#if stackStatus === 'needs_recovery'}
    <div class="stk-notice warn" role="alert">
      <div class="stk-notice-h">
        <AlertTriangle size={15} />
        <b>{m.stacks_d_missing_title()}</b>
      </div>
      <p>
        {m.stacks_d_missing_p1()}
        <code>/stacks/{name}/compose.yaml</code> {m.stacks_d_missing_p2()}
      </p>
      <div class="dm-tbl stk-recover">
        <div class="dm-tr" style="grid-template-columns: minmax(0, 1fr) 150px">
          <span class="dm-nm"><span class="dm-t">{m.stacks_d_recover_title()}</span><small>{m.stacks_d_recover_desc({ name })}</small></span>
          <span><Button variant="primary" size="sm" onclick={recoverFromContainers} loading={recoverBusy} disabled={recoverBusy}><RotateCcw size={13} /> {m.stacks_d_recover()}</Button></span>
        </div>
        <div class="dm-tr" style="grid-template-columns: minmax(0, 1fr) 150px">
          <span class="dm-nm">
            <span class="dm-t">{m.stacks_d_restore_last()}</span>
            <small>
              {#if latestBackupRun === undefined}
                {m.stacks_d_looking_backup({ name })}
              {:else if latestBackupRun === null}
                {m.stacks_d_no_backup({ name })}
              {:else}
                {latestBackupRun.job_name} · {latestBackupRun.finished_at ? fmtDateTime(latestBackupRun.finished_at) : m.stacks_d_unknown()} · {fmtBytes(latestBackupRun.size_bytes)}. {m.stacks_d_restore_replaces()}
              {/if}
            </small>
          </span>
          <span><Button variant="secondary" size="sm" onclick={restoreFromBackup} loading={restoreBusy} disabled={restoreBusy || !latestBackupRun}><History size={13} /> {m.stacks_d_restore()}</Button></span>
        </div>
        <div class="dm-tr" style="grid-template-columns: minmax(0, 1fr) 150px">
          <span class="dm-nm"><span class="dm-t">{m.stacks_d_remove_record()}</span><small>{m.stacks_d_remove_record_desc()}</small></span>
          <span><Button variant="danger" size="sm" onclick={discardGhost}><Trash2 size={13} /> {m.stacks_d_discard_record()}</Button></span>
        </div>
      </div>
    </div>
  {/if}

  {#if recoverWarnings.length > 0}
    <div class="stk-notice warn">
      <div class="stk-notice-h"><AlertTriangle size={15} /><b>{m.stacks_d_review_recovered()}</b></div>
      <ul>{#each recoverWarnings as w}<li>{w}</li>{/each}</ul>
    </div>
  {/if}

  {#if externalChange}
    <div class="stk-notice warn stk-notice-row">
      <AlertTriangle size={15} />
      <div class="stk-notice-t">
        <b>{externalChange.type === 'removed' ? m.stacks_d_ext_removed({ file: externalChange.file || m.stacks_d_stack_directory() }) : m.stacks_d_ext_modified({ file: externalChange.file || m.stacks_d_stack_directory() })}</b>
        <span class:stk-bad={dirty}>{dirty ? m.stacks_d_unsaved_discard() : m.stacks_d_reload_pick_up()}</span>
      </div>
      <Button size="sm" variant="secondary" onclick={load}><RefreshCw size={13} /> {m.stacks_d_reload()}</Button>
      <Button size="sm" variant="ghost" onclick={() => (externalChange = null)}>{m.stacks_d_ignore()}</Button>
    </div>
  {/if}

  {#if modifiedOutside.length > 0}
    <div class="stk-notice warn stk-notice-row">
      <AlertTriangle size={15} />
      <div class="stk-notice-t">
        <b>{m.stacks_d_modified_outside({ files: modifiedOutside.join(', ') })}</b>
        <span>{m.stacks_d_modified_outside_hint()}</span>
      </div>
    </div>
  {/if}

  {#if mountsStackDir}
    <div class="stk-notice warn stk-notice-row">
      <AlertTriangle size={15} />
      <div class="stk-notice-t">
        <b>{m.stacks_d_mounts_title()}</b>
        <span>{m.stacks_d_mounts_a()}<code>./data:/app/data</code>{m.stacks_d_mounts_b()} <code>:ro</code>{m.stacks_d_mounts_c()}</span>
      </div>
    </div>
  {/if}

  {#if activeMigration && !activeMigration.completed_at}
    <div class="stk-notice stk-notice-row">
      <Loader2 size={15} class="animate-spin" />
      <div class="stk-notice-t">
        <b>{m.stacks_d_migrating_to({ host: activeMigration.target_host_id })}</b>
        <span>
          {m.stacks_d_phase({ phase: activeMigration.phase ?? activeMigration.status })}
          {#if activeMigration.progress?.current_volume}
            {m.stacks_d_migr_volume({ index: activeMigration.progress.volume_index, total: activeMigration.progress.volumes_total, name: activeMigration.progress.current_volume })}
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
          <b>{m.stacks_d_git_sync_failed()}</b><span title={gitSource.last_sync_error}>{gitSource.last_sync_error}</span>
        {:else if (gitSource.last_env_drift?.new_from_repo?.length ?? 0) + (gitSource.last_env_drift?.new_from_compose?.length ?? 0) > 0}
          <b>{m.stacks_d_env_drift_last()}</b>
          <span>
            {#if gitSource.last_env_drift?.new_from_repo?.length}{m.stacks_d_new_from_repo()} <code>{gitSource.last_env_drift.new_from_repo.join(', ')}</code>{/if}
            {#if gitSource.last_env_drift?.new_from_compose?.length} {m.stacks_d_needed_by_compose()} <code>{gitSource.last_env_drift.new_from_compose.join(', ')}</code>{/if}
            {m.stacks_d_fill_values()}
          </span>
        {:else}
          <b>{m.stacks_d_webhook_disabled()}</b>
          <span>{m.stacks_d_webhook_disabled_desc()}</span>
        {/if}
      </div>
      <Button size="sm" variant="ghost" onclick={() => (activeTab = 'settings')}>{m.stacks_d_git_source()}</Button>
    </div>
  {/if}

  {#if stackStatus !== 'needs_recovery'}
    <Tabs label={m.stacks_d_sections()} tabs={tabItems} active={activeTab} onchange={(k) => (activeTab = k as TabKey)} />

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
            {@const cols = 'minmax(140px, 1.3fr) minmax(140px, 1.5fr) 120px 64px 84px 34px'}
            <div class="dm-tbl">
              <div class="dm-tr dm-th" style="grid-template-columns: {cols}">
                <span>{m.stacks_d_col_service()}</span><span>{m.common_image()}</span><span>{m.common_state()}</span><span class="r">CPU</span><span class="r">{m.stacks_d_memory()}</span><span></span>
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
                  <span class="dm-nm">
                    <a href={`/containers/${s.container_id}?from=stack`} onclick={(e) => e.stopPropagation()}>{s.service}{#if count > 1}<span class="stk-rep"> ×{count}</span>{/if}</a>
                    <small title={s.status}>{s.status || s.state}</small>
                  </span>
                  <span class="dm-mono dm-dim stk-img" title={s.image}>
                    {s.image.split('/').pop()}
                    {#if hasImageUpdate(s.image)}<span class="stk-upd" title={m.stacks_d_newer_image()}>{m.stacks_d_update_tag()}</span>{/if}
                  </span>
                  <span>
                    {#if op && (isActive(op) || Date.now() - Date.parse(op.finished_at ?? op.created_at) < 60_000)}
                      <OpProgress {op} compact />
                    {:else}
                      <StatusChip tone={s.state !== 'running' ? 'off' : unhealthy ? 'warn' : 'ok'} label={unhealthy ? m.stacks_d_unhealthy() : containerStateLabel(s.state)} />
                    {/if}
                  </span>
                  <span class="dm-num r" class:warn={(st?.cpu_percent ?? 0) >= 80}>{st ? `${st.cpu_percent.toFixed(1)}%` : '—'}</span>
                  <span class="dm-num r" class:warn={(st?.mem_percent ?? 0) >= 85}>{st ? fmtMem(st.mem_used) : '—'}</span>
                  <span class="dm-tr-menu" role="presentation" onclick={(e) => e.stopPropagation()} onkeydown={(e) => e.stopPropagation()}>
                    <Menu label={m.stacks_actions_for({ name: s.service })} items={serviceMenu(s)} width={220} />
                  </span>
                </div>
              {/each}
            </div>
          {:else}
            <div class="dm-tbl"><div class="dm-empty">{m.stacks_d_no_containers()}</div></div>
          {/if}

          {#if canDeploy}
          <section>
            <div class="stk-sec-h">
              <h2 class="dm-h2">{m.stacks_d_recent_deploys()}</h2>
              <span class="dm-sp"></span>
              <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={() => (activeTab = 'history')}>{m.stacks_d_tab_history()} <ArrowRight size={12} /></button>
            </div>
            {#if historyEntries.length === 0}
              <p class="stk-line">{historyLoading ? m.common_loading() : m.stacks_d_no_deploys()}</p>
            {:else}
              <div class="dm-tbl">
                {#each historyEntries.slice(0, 3) as entry, i (entry.id)}
                  {@const badge = deployTriggerBadge(entry.note)}
                  <div class="dm-tr" style="grid-template-columns: 44px minmax(0, 1fr) 110px 100px">
                    <span class="dm-num dm-dim">#{entry.version}</span>
                    <span class="dm-trunc" title={entry.note || badge.label}>{entry.note || badge.label}<span class="dm-dim">{entry.deployed_by_name ? ` · ${entry.deployed_by_name}` : ''}</span></span>
                    <span class="dm-num dm-dim" title={fmtDateTime(entry.deployed_at)}>{fmtAgo(entry.deployed_at)}</span>
                    <span>
                      {#if entry.success === false}<StatusChip tone="bad" label={m.stacks_d_chip_failed()} title={entry.error_message} />
                      {:else if i === 0}<StatusChip tone="ok" label={m.stacks_d_chip_current()} />
                      {:else}<StatusChip tone="off" label={m.stacks_d_chip_success()} />{/if}
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
            <h2 class="dm-h2">{m.stacks_d_resources()}</h2>
            <dl class="dm-kv">
              <dt>CPU</dt><dd class="dm-num">{statsRollup.cpuLabel} <span class="dm-dim">{statsRollup.cpuMeta}</span></dd>
              <dt>{m.stacks_d_memory()}</dt><dd class="dm-num">{statsRollup.memLabel} <span class="dm-dim">{statsRollup.memMeta}</span></dd>
              <dt>{m.common_network()}</dt><dd class="dm-num">{statsRollup.netLabel} <span class="dm-dim">{statsRollup.netMeta}</span></dd>
              <dt>{m.common_host()}</dt><dd>{#if isRemote}<a href={`/hosts/${stackHost}`}>{hosts.selected?.name ?? stackHost}</a>{:else}<a href="/hosts/local">{m.common_local()}</a>{/if}</dd>
              <dt>{m.common_source()}</dt>
              <dd>
                {#if gitSource}
                  <span class="stk-src"><GitBranch size={12} /> <span class="dm-trunc" title={gitSource.repo_url}>{gitSource.repo_url}</span></span>
                  <span class="dm-dim">{gitSource.branch}{gitSource.auto_deploy ? m.stacks_d_auto_deploy_push() : ''}</span>
                {:else}
                  {m.stacks_d_compose_editor()}
                {/if}
              </dd>
              <dt>{m.stacks_d_endpoints()}</dt>
              <dd class="dm-num">
                {#if composeEndpoints.length === 0}
                  <span class="dm-dim">{m.stacks_d_no_ports()}</span>
                {:else}
                  {#each composeEndpoints as ep, i (ep.service + i)}
                    <div>{ep.service} :{ep.port}{#if ep.mode === 'host'} <ExternalLink size={10} />{:else} <span class="dm-dim">{m.topology_internal()}</span>{/if}</div>
                  {/each}
                {/if}
              </dd>
            </dl>
          </section>

          <section>
            <h2 class="dm-h2">{m.stacks_d_nets_vols()}</h2>
            {#if composeNetworks.length === 0 && composeVolumes.length === 0}
              <p class="stk-line">{m.stacks_d_no_nets_vols()}</p>
            {:else}
              <div class="stk-tags">
                {#each composeNetworks as n (n)}<span class="stk-tag" title={m.stacks_d_tag_network()}>{n}</span>{/each}
                {#each composeVolumes as v (v)}<span class="stk-tag" title={m.stacks_d_tag_volume()}>{v}</span>{/each}
              </div>
            {/if}
          </section>

          {#if deps && (deps.depends_on.length > 0 || deps.dependents.length > 0)}
            <section>
              <h2 class="dm-h2">{m.stacks_d_dependencies()}</h2>
              <dl class="dm-kv">
                {#if deps.depends_on.length > 0}
                  <dt>{m.stacks_d_needs()}</dt><dd>{#each deps.depends_on as d, i (d)}{i > 0 ? ', ' : ''}<a href={`/stacks/${encodeURIComponent(d)}`}>{d}</a>{/each}</dd>
                {/if}
                {#if deps.dependents.length > 0}
                  <dt>{m.stacks_d_needed_by()}</dt><dd>{#each deps.dependents as d, i (d)}{i > 0 ? ', ' : ''}<a href={`/stacks/${encodeURIComponent(d)}`}>{d}</a>{/each}</dd>
                {/if}
              </dl>
            </section>
          {/if}

          {#if !gitLoading && !gitSource && canWrite}
            <button type="button" class="stk-connect" onclick={openGitDialog}>
              <GitBranch size={16} />
              <span><b>{m.stacks_d_connect_git()}</b><small>{m.stacks_d_connect_desc()}</small></span>
              <span class="dm-btn dm-btn-sm">{m.stacks_d_connect()}</span>
            </button>
          {/if}
        </aside>
      </div>
    {/if}

    <!-- Compose: read-only until Edit, so a stray keystroke can't change a deployed file. -->
    {#if activeTab === 'compose'}
      <div class="stk-toolbar">
        <span class="dm-mono stk-file">compose.yaml{#if dirty}<span class="stk-unsaved">{m.stacks_d_unsaved()}</span>{/if}</span>
        <span class="dm-sp"></span>
        {#if !composeEditing}
          {#if canWrite}<button type="button" class="dm-btn" onclick={startComposeEdit}><Pencil size={13} /> {m.common_edit()}</button>{/if}
        {:else}
          <button type="button" class="dm-btn dm-btn-ghost" onclick={() => { composeEditing = false; }} disabled={anyBusy}>{m.common_cancel()}</button>
          <button type="button" class="dm-btn" onclick={saveCompose} disabled={anyBusy || !dirty}><Save size={13} /> {m.common_save()}</button>
          {#if canDeploy}
            <button type="button" class="dm-btn dm-btn-primary" onclick={saveAndRedeploy} disabled={anyBusy || !dirty}><Play size={13} /> {m.stacks_d_save_deploy()}</button>
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
    {#if activeTab === 'environment' && envHidden}
      <div class="dm-tbl"><div class="dm-empty">{m.stacks_d_env_hidden()}</div></div>
    {:else if activeTab === 'environment'}
      <div class="stk-toolbar">
        <span class="dm-sub">{m.stacks_d_n_variables({ count: envRows.length })}{#if dirty}<span class="stk-unsaved">{m.stacks_d_unsaved()}</span>{/if}</span>
        <span class="dm-sp"></span>
        <button type="button" class="dm-btn dm-btn-ghost" onclick={() => (envShowSecrets = !envShowSecrets)}>
          {#if envShowSecrets}<EyeOff size={13} /> {m.stacks_d_hide_secrets()}{:else}<Eye size={13} /> {m.stacks_d_show_secrets()}{/if}
        </button>
        {#if canWrite}<button type="button" class="dm-btn" onclick={envAddRow}><Plus size={13} /> {m.stacks_d_add_variable()}</button>{/if}
        {#if dirty}<button type="button" class="dm-btn dm-btn-primary" onclick={save} disabled={anyBusy}><Save size={13} /> {m.common_save()}</button>{/if}
      </div>

      {#if envRows.length === 0}
        <div class="dm-tbl"><div class="dm-empty">{m.stacks_d_no_env_before()} <code>${'{KEY}'}</code> {m.stacks_d_no_env_after()}</div></div>
      {:else}
        {@const ecols = 'minmax(160px, 0.9fr) minmax(200px, 1.6fr) minmax(120px, 0.8fr) 64px'}
        <div class="dm-tbl">
          <div class="dm-tr dm-th" style="grid-template-columns: {ecols}"><span>{m.common_key()}</span><span>{m.common_value()}</span><span>{m.stacks_d_used_by()}</span><span></span></div>
          {#each envRows as row, i (i)}
            {@const isSec = isSecretKey(row.key)}
            {@const scopes = envScopeFor(compose, row.key)}
            {@const editing = envEditingIdx === i}
            <div class="dm-tr" style="grid-template-columns: {ecols}">
              <span class="dm-mono">
                {#if editing}
                  <input class="dm-input stk-cell-input" aria-label={m.common_key()} bind:value={envRows[i].key} oninput={commitEnvRows} onkeydown={(e) => e.key === 'Enter' && envSaveEdit()} />
                {:else}{row.key}{/if}
              </span>
              <span class="dm-mono stk-val">
                {#if editing}
                  <input class="dm-input stk-cell-input" aria-label={m.common_value()} bind:value={envRows[i].value} oninput={commitEnvRows} onkeydown={(e) => e.key === 'Enter' && envSaveEdit()} />
                {:else if isSec && !envShowSecrets}
                  ••••••••••
                {:else if row.value}
                  <span title={row.value}>{row.value}</span>
                {:else}
                  <span class="dm-dim">{m.stacks_d_empty()}</span>
                {/if}
                {#if isSec && !editing}<span class="stk-tag">{m.stacks_d_secret()}</span>{/if}
              </span>
              <span class="dm-trunc" title={scopes.join(', ')}>{#if scopes.length === 0}<span class="dm-dim">{m.templates_unused()}</span>{:else}{scopes.join(', ')}{/if}</span>
              <span class="dm-tr-menu stk-row-btns">
                {#if canWrite}
                  {#if editing}
                    <button type="button" class="stk-ibtn" onclick={envSaveEdit} title={m.common_done()} aria-label={m.stacks_d_done_editing()}><CheckCircle2 size={13} /></button>
                  {:else}
                    <button type="button" class="stk-ibtn" onclick={() => (envEditingIdx = i)} title={m.stacks_d_edit_key({ key: row.key })} aria-label={m.stacks_d_edit_key({ key: row.key })}><Pencil size={12} /></button>
                  {/if}
                  <button type="button" class="stk-ibtn danger" onclick={() => envDeleteRow(i)} title={m.stacks_d_delete_key({ key: row.key })} aria-label={m.stacks_d_delete_key({ key: row.key })}><Trash2 size={12} /></button>
                {/if}
              </span>
            </div>
          {/each}
        </div>
      {/if}

      <section>
        <h2 class="dm-h2">{m.stacks_d_overlays()}</h2>
        {#if !envs || envs.available.length === 0}
          <p class="stk-line">{m.stacks_d_no_overlays_before()} <code>compose.&lt;env&gt;.yaml</code> {m.stacks_d_no_overlays_after()}</p>
        {:else}
          <p class="stk-line">
            {#if envs.active}{m.stacks_d_overlay_merge_before()} <code>compose.{envs.active}.yaml</code> {m.stacks_d_overlay_merge_after()}{:else}{m.stacks_d_overlay_base_only()}{/if}
          </p>
          {#if canDeploy}
            <div class="stk-tags">
              <button type="button" class="stk-tag stk-pick" class:on={envs.active === ''} onclick={() => setActiveEnv('')} disabled={envBusy}>{m.stacks_d_base()}</button>
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
            <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={() => { logServiceFilter = new Set(); }}>{m.stacks_d_show_all()}</button>
          {/if}
        </div>
        <span class="dm-sp"></span>
        <button type="button" class="dm-btn dm-btn-sm" onclick={toggleLogTailing}>
          {#if logTailing}<Square size={11} /> {m.stacks_d_pause()}{:else}<Play size={11} /> {m.stacks_d_follow()}{/if}
        </button>
        <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={() => { logLines = []; }}>{m.stacks_d_clear()}</button>
      </div>
      <div class="stk-logs dm-mono" bind:this={logEl}>
        {#if visibleLogLines.length === 0}
          <div class="dm-dim">{logSockets.size === 0 ? m.stacks_d_not_streaming() : m.stacks_d_waiting_line()}</div>
        {:else}
          {#each visibleLogLines as l, i (i)}
            {@const kind = logLevelKind(l.line)}
            <div class="stk-ln">
              <span class="dm-dim" title={new Date(l.ts).toISOString()}>{fmtTime(l.ts)}</span>
              <span class="stk-ln-svc" style:color={svcColor(l.service)}>{l.service}</span>
              <span class:warn={kind === 'warn'} class:bad={kind === 'err'} class:ok={kind === 'ok'}>{l.line}</span>
            </div>
          {/each}
        {/if}
      </div>
      <p class="stk-line">{m.stacks_d_log_footer({ lines: visibleLogLines.length, containers: m.stacks_n_containers({ count: logSockets.size }), max: LOG_BUFFER_MAX })}</p>
    {/if}

    <!-- History -->
    {#if activeTab === 'history'}
      <div class="stk-toolbar">
        <span class="dm-sub">{m.stacks_d_n_deploys({ count: historyEntries.length })}</span>
        <span class="dm-sp"></span>
        <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={loadHistory} disabled={historyLoading}>
          <RefreshCw size={12} class={historyLoading ? 'animate-spin' : ''} /> {m.common_refresh()}
        </button>
      </div>
      {#if historyLoading && historyEntries.length === 0}
        <div class="stk-skel"><Skeleton width="100%" height="2.5rem" /><Skeleton width="100%" height="2.5rem" /></div>
      {:else if historyEntries.length === 0}
        <div class="dm-tbl"><div class="dm-empty">{m.stacks_d_no_history()}</div></div>
      {:else}
        {@const hcols = '52px minmax(180px, 1.6fr) 110px 110px 70px 96px 150px'}
        <div class="dm-tbl">
          <div class="dm-tr dm-th" style="grid-template-columns: {hcols}">
            <span>#</span><span>{m.stacks_d_col_change()}</span><span>{m.stacks_d_col_by()}</span><span>{m.stacks_d_col_when()}</span><span class="r">{m.stacks_d_col_took()}</span><span>{m.stacks_d_col_result()}</span><span></span>
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
                {badge.label}{#if entry.note && entry.note !== badge.raw}<span class="dm-dim"> · {entry.note}</span>{/if}
                {#if entry.git_commit_sha}
                  {@const url = buildGitCommitURL(gitSource?.repo_url ?? '', entry.git_commit_sha)}
                  {#if url}<a class="dm-mono stk-sha" href={url} target="_blank" rel="noopener">{entry.git_commit_sha.slice(0, 7)}</a>{:else}<code class="stk-sha">{entry.git_commit_sha.slice(0, 7)}</code>{/if}
                {/if}
              </span>
              <span class="dm-trunc">{entry.deployed_by_name ?? '—'}</span>
              <span class="dm-num dm-dim" title={fmtDateTime(entry.deployed_at)}>{fmtAgo(entry.deployed_at)}</span>
              <span class="dm-num dm-dim r">{entry.duration_ms ? formatDuration(entry.duration_ms) : '—'}</span>
              <span>
                {#if entry.success === false}<StatusChip tone="bad" label={m.stacks_d_chip_failed()} title={entry.error_message} />
                {:else if i === 0}<StatusChip tone="ok" label={m.stacks_d_chip_current()} />
                {:else}<StatusChip tone="off" label={m.stacks_d_chip_success()} />{/if}
              </span>
              <span class="dm-tr-menu stk-row-btns">
                {#if entry.services && entry.services.length > 0}
                  <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" aria-expanded={open} onclick={() => (open ? collapseRow(entry.id) : expandRow(entry.id))} title={m.stacks_d_images_per_service()}>
                    {entry.services.length}{changed.length ? ` · ${changed.length}Δ` : ''}
                  </button>
                {/if}
                <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={() => openYaml(entry.id)}>{m.stacks_d_tab_compose()}</button>
                {#if canDeploy && i !== 0}
                  <button type="button" class="dm-btn dm-btn-sm" onclick={() => openRollbackConfirm(entry.id)}>{m.stacks_d_roll_back()}</button>
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
                    {#if isChanged}<span class="stk-delta" title={m.stacks_d_changed_vs({ version: historyEntries[i + 1]?.version ?? '' })}>{m.stacks_d_changed()}</span>{/if}
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
            <h4>{m.stacks_d_git_source()}</h4>
            <p>{m.stacks_d_git_source_desc()}</p>
          </div>
          <div>
            {#if gitLoading}
              <span class="dm-dim">{m.common_loading()}</span>
            {:else if gitSource}
              <dl class="dm-kv stk-git">
                <dt>{m.stacks_d_repository()}</dt><dd class="dm-mono">{gitSource.repo_url}</dd>
                <dt>{m.stacks_git_branch()}</dt><dd class="dm-mono">{gitSource.branch}{gitSource.path_in_repo && gitSource.path_in_repo !== '.' ? ` · ${gitSource.path_in_repo}` : ''}</dd>
                <dt>{m.common_deploy()}</dt><dd>{gitSource.auto_deploy ? m.stacks_d_auto_after_sync() : m.stacks_d_manually()} · {gitSource.has_webhook_secret ? m.stacks_d_webhook_on() : m.stacks_d_webhook_off()}</dd>
                <dt>{m.stacks_d_last_sync()}</dt>
                <dd>
                  {#if gitSource.last_sync_sha}<code>{gitSource.last_sync_sha.slice(0, 7)}</code>{#if gitSource.last_sync_at} · {fmtDateTime(gitSource.last_sync_at)}{/if}{:else}{m.common_never()}{/if}
                  {#if gitSource.last_sync_error}<div class="stk-bad">{gitSource.last_sync_error}</div>{/if}
                </dd>
              </dl>
              {#if canWrite}
                <div class="stk-btns">
                  <button type="button" class="dm-btn" onclick={syncNow} disabled={gitBusy}><RefreshCw size={12} class={gitBusy ? 'animate-spin' : ''} /> {m.stacks_d_sync_now()}</button>
                  <button type="button" class="dm-btn" onclick={openGitDialog} disabled={gitBusy}><Pencil size={12} /> {m.common_edit()}</button>
                  <button type="button" class="dm-btn dm-btn-ghost" onclick={disconnectGit} disabled={gitBusy}><Unlink size={12} /> {m.stacks_d_disconnect()}</button>
                </div>
              {/if}
            {:else if canWrite}
              <button type="button" class="dm-btn" onclick={openGitDialog}><LinkIcon size={12} /> {m.stacks_d_connect_repo()}</button>
            {:else}
              <span class="dm-dim">{m.stacks_d_not_connected()}</span>
            {/if}
          </div>
        </div>

        <div class="dm-form-sec">
          <div>
            <h4>{m.stacks_d_dependencies()}</h4>
            <p>{m.stacks_d_deps_desc()}</p>
          </div>
          <div>
            {#if !deps || (deps.depends_on.length === 0 && deps.dependents.length === 0)}
              <span class="dm-dim">{m.stacks_d_no_deps()}</span>
            {:else}
              <dl class="dm-kv">
                {#if deps.depends_on.length > 0}<dt>{m.stacks_d_needs()}</dt><dd>{#each deps.depends_on as d, i (d)}{i > 0 ? ', ' : ''}<a href={`/stacks/${encodeURIComponent(d)}`}>{d}</a>{/each}</dd>{/if}
                {#if deps.dependents.length > 0}<dt>{m.stacks_d_needed_by()}</dt><dd>{#each deps.dependents as d, i (d)}{i > 0 ? ', ' : ''}<a href={`/stacks/${encodeURIComponent(d)}`}>{d}</a>{/each}</dd>{/if}
              </dl>
            {/if}
            {#if canDeploy}<button type="button" class="dm-btn" onclick={openDepsEditor}>{m.stacks_d_edit_deps()}</button>{/if}
          </div>
        </div>

        <div class="dm-form-sec">
          <div>
            <h4 class="stk-bad">{m.stacks_d_delete_this()}</h4>
            <p>
              {m.stacks_d_delete_desc_before({ services: m.stacks_d_n_services({ count: services.length }) })}
              <code>stacks/{name}/</code> {m.stacks_d_delete_desc_after()}
            </p>
          </div>
          <div>
            <button type="button" class="dm-btn dm-btn-danger" onclick={openDelete} disabled={anyBusy}><Trash2 size={13} /> {m.stacks_d_delete_stack()}</button>
          </div>
        </div>
      </div>
    {/if}
  {/if}
</section>

<!-- View YAML snapshot modal -->
<Modal bind:open={showYaml} title={yamlEntry ? m.stacks_d_snapshot_title({ version: yamlEntry.version, date: fmtDateTime(yamlEntry.deployed_at) }) : m.stacks_d_snapshot()} maxWidth="max-w-3xl">
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
          <div class="font-medium text-[var(--fg-muted)] uppercase tracking-wider text-[10px] mb-1">{m.stacks_d_resolved_images()}</div>
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
          >{m.stacks_d_raw_compose()}</button>
          <button
            type="button"
            class="px-2 py-1 text-xs rounded transition"
            class:bg-[var(--bg)]={yamlMode === 'diff'}
            class:font-medium={yamlMode === 'diff'}
            onclick={showYamlDiff}
            disabled={yamlDiffLoading}
          >
            {m.stacks_d_diff_vs({ version: yamlEntry.version - 1 })}
          </button>
        </div>
        {#if yamlMode === 'diff' && yamlPrevEntry}
          <span class="text-xs text-[var(--fg-muted)]">
            {m.stacks_d_comparing({ date: fmtDateTime(yamlPrevEntry.deployed_at) })}
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
    <Button variant="secondary" onclick={() => (showYaml = false)}>{m.common_close()}</Button>
  {/snippet}
</Modal>

<!-- Dependencies editor modal -->
<Modal bind:open={showDepsEditor} title={m.stacks_d_deps_title({ name })} maxWidth="max-w-lg">
  <div class="space-y-4 text-sm">
    <div class="text-xs text-[var(--fg-muted)]">
      {m.stacks_d_deps_intro_before()}
      <span class="font-mono">{name}</span>{m.stacks_d_deps_intro_after()}
    </div>
    <div class="space-y-2">
      {#if depsEditList.length === 0}
        <div class="text-xs text-[var(--fg-muted)]">{m.stacks_d_no_prereq()}</div>
      {:else}
        <div class="flex flex-wrap gap-1.5">
          {#each depsEditList as d}
            <div class="inline-flex items-center gap-1 px-2 py-0.5 rounded border border-[var(--border)] font-mono text-xs">
              <span>{d}</span>
              <button
                class="text-[var(--fg-muted)] hover:text-[var(--color-danger-400)]"
                onclick={() => depRemove(d)}
                title={m.common_remove()}
                aria-label={m.templates_remove_named({ name: d })}
              >
                <X class="w-3 h-3" />
              </button>
            </div>
          {/each}
        </div>
      {/if}
    </div>
    <div class="space-y-1">
      <label class="text-xs font-medium text-[var(--fg-muted)]" for="dep-picker">{m.stacks_d_add_dep()}</label>
      <div class="flex gap-2">
        <input
          id="dep-picker"
          class="dm-input flex-1"
          list="dep-stack-options"
          bind:value={depsNewEntry}
          placeholder={m.stacks_d_pick_stack()}
          onkeydown={(e) => { if (e.key === 'Enter') { e.preventDefault(); depAdd(depsNewEntry); } }}
        />
        <datalist id="dep-stack-options">
          {#each allStackNames as s}
            <option value={s}></option>
          {/each}
        </datalist>
        <Button variant="secondary" onclick={() => depAdd(depsNewEntry)} disabled={!depsNewEntry.trim()}>
          <Plus class="w-3.5 h-3.5" />
          {m.common_add()}
        </Button>
      </div>
      <div class="text-[11px] text-[var(--fg-muted)]">
        {m.stacks_d_unknown_ok()}
      </div>
    </div>
  </div>
  {#snippet footer()}
    <Button variant="secondary" onclick={() => (showDepsEditor = false)} disabled={depsBusy}>{m.common_cancel()}</Button>
    <Button variant="primary" onclick={saveDeps} loading={depsBusy} disabled={depsBusy}>{m.common_save()}</Button>
  {/snippet}
</Modal>

<!-- Rollback confirm modal -->
<Modal bind:open={showRollbackConfirm} title={m.stacks_d_rollback_title({ version: rollbackEntry?.version ?? '' })} maxWidth="max-w-lg">
  {#if rollbackEntry}
    <div class="space-y-4 text-sm">
      <div class="flex items-start gap-3 p-3 rounded-md border border-[color-mix(in_srgb,var(--color-warning-500)_40%,transparent)] bg-[color-mix(in_srgb,var(--color-warning-500)_8%,transparent)]">
        <AlertTriangle class="w-4 h-4 text-[var(--color-warning-400)] shrink-0 mt-0.5" />
        <div class="space-y-1">
          <div>
            {m.stacks_d_rb_overwrite()} <span class="font-mono">compose.yaml</span> {m.stacks_d_rb_snapshot_from()}
            <span class="font-medium">{fmtDateTime(rollbackEntry.deployed_at)}</span>
            {m.stacks_d_rb_redeploy()}
          </div>
          <div class="text-xs text-[var(--fg-muted)]">
            {m.stacks_d_rb_env_before()} <span class="font-mono">.env</span> {m.stacks_d_rb_env_after()}
          </div>
          {#if rollbackEntry.environment}
            <div class="text-xs text-[var(--fg-muted)]">
              {m.stacks_d_rb_envused_before()} <span class="font-mono">{rollbackEntry.environment}</span>{m.stacks_d_rb_envused_mid()}
              <span class="font-mono">compose.{rollbackEntry.environment}.yaml</span> {m.stacks_d_rb_envused_after()}
            </div>
          {/if}
        </div>
      </div>
      {#if !rollbackEntry.compose_yaml}
        <div class="text-xs text-[var(--color-danger-400)]">
          {m.stacks_d_rb_no_snapshot()}
        </div>
      {/if}
      {#if rollbackEntry.services && rollbackEntry.services.length > 0}
        <div class="text-xs space-y-0.5 border border-[var(--border)] rounded-md p-3 bg-[var(--surface)]">
          <div class="font-medium text-[var(--fg-muted)] uppercase tracking-wider text-[10px] mb-1">{m.stacks_d_rb_images()}</div>
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
    <Button variant="secondary" onclick={() => (showRollbackConfirm = false)} disabled={rollbackBusy}>{m.common_cancel()}</Button>
    <Button variant="primary" onclick={doRollback} loading={rollbackBusy} disabled={rollbackBusy || !rollbackEntry?.compose_yaml}>
      <RotateCcw class="w-4 h-4" />
      {m.stacks_d_roll_back()}
    </Button>
  {/snippet}
</Modal>

<!-- Scale modal -->
<Modal bind:open={showScale} title={m.stacks_d_scale_title({ service: scaleTarget })} maxWidth="max-w-sm">
  <div class="space-y-4">
    <div>
      <label for="scale-slider" class="block text-xs font-medium text-[var(--fg-muted)] mb-2">
        {m.stacks_d_replicas()} <span class="text-[var(--fg)] font-bold text-lg">{scaleValue}</span>
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
        <div>{m.stacks_d_scale_cname_before()} <code class="font-mono">container_name</code> {m.stacks_d_scale_cname_after()}</div>
      </div>
    {/if}

    {#if scaleCheck?.has_hard_port}
      <div class="p-3 rounded-lg bg-[color-mix(in_srgb,var(--color-danger-500)_10%,transparent)] border border-[color-mix(in_srgb,var(--color-danger-500)_30%,transparent)] text-xs text-[var(--color-danger-400)] flex items-start gap-2">
        <AlertTriangle class="w-4 h-4 shrink-0 mt-0.5" />
        <div>{m.stacks_d_scale_port_before()} <code class="font-mono">{scaleCheck.hard_port_detail}</code>{m.stacks_d_scale_port_after()}</div>
      </div>
    {/if}

    {#if scaleCheck?.is_stateful && scaleValue > 1}
      <div class="p-3 rounded-lg bg-[color-mix(in_srgb,var(--color-warning-500)_10%,transparent)] border border-[color-mix(in_srgb,var(--color-warning-500)_30%,transparent)] text-xs text-[var(--color-warning-400)]">
        <div class="flex items-start gap-2">
          <AlertTriangle class="w-4 h-4 shrink-0 mt-0.5" />
          <div>
            {m.stacks_d_scale_db_before()}<strong>{scaleCheck.stateful_image}</strong>{m.stacks_d_scale_db_after()}
          </div>
        </div>
        <label class="flex items-center gap-2 mt-2 cursor-pointer">
          <input type="checkbox" bind:checked={scaleForce} class="rounded" />
          <span>{m.stacks_d_scale_risk()}</span>
        </label>
      </div>
    {/if}
  </div>

  {#snippet footer()}
    <Button variant="secondary" onclick={() => (showScale = false)}>{m.common_cancel()}</Button>
    <Button
      variant="primary"
      loading={scaleBusy}
      disabled={scaleBusy || (scaleValue > 1 && (scaleCheck?.has_container_name || scaleCheck?.has_hard_port)) || (scaleCheck?.is_stateful && scaleValue > 1 && !scaleForce)}
      onclick={doScale}
    >
      {m.stacks_d_scale_to({ count: scaleValue })}
    </Button>
  {/snippet}
</Modal>

<!-- Rolling update modal -->
<Modal bind:open={showRolling} title={m.stacks_d_rolling_title({ service: rollingTarget })} maxWidth="max-w-md">
  <div class="space-y-3">
    <p class="text-xs text-[var(--fg-muted)]">
      {m.stacks_d_rolling_intro_before()} <span class="font-mono">{rollingTarget}</span> {m.stacks_d_rolling_intro_after()}
    </p>
    <div>
      <label for="rolling-order" class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">{m.stacks_d_order()}</label>
      <select id="rolling-order" class="dm-input" bind:value={rollingOrder}>
        <option value="stop-first">{m.stacks_d_order_stop_first()}</option>
        <option value="start-first">{m.stacks_d_order_start_first()}</option>
      </select>
      <p class="text-xs text-[var(--fg-muted)] mt-1">
        {m.stacks_d_order_hint()}
      </p>
    </div>
    <div>
      <label for="rolling-parallel" class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">{m.stacks_d_parallelism()}</label>
      <input id="rolling-parallel" type="number" min="1" max="10" class="dm-input" bind:value={rollingParallel} />
      <p class="text-xs text-[var(--fg-muted)] mt-1">{m.stacks_d_parallelism_hint()}</p>
    </div>
    <div>
      <label for="rolling-failure" class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">{m.stacks_d_on_failure()}</label>
      <select id="rolling-failure" class="dm-input" bind:value={rollingFailure}>
        <option value="pause">{m.stacks_d_fail_pause()}</option>
        <option value="continue">{m.stacks_d_fail_continue()}</option>
        <option value="rollback">{m.stacks_d_fail_rollback()}</option>
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
    <Button variant="secondary" onclick={() => (showRolling = false)}>{m.common_cancel()}</Button>
    <Button variant="primary" loading={rollingBusy} disabled={rollingBusy} onclick={doRolling}>
      <Repeat class="w-3.5 h-3.5" /> {m.stacks_d_roll()}
    </Button>
  {/snippet}
</Modal>

<!-- Migrate modal -->
<Modal bind:open={showMigrate} title={m.stacks_d_migrate_title({ name })} maxWidth="max-w-lg">
  <div class="space-y-4">
    <div>
      <label for="migrate-target" class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5">{m.templates_dd_target_host()}</label>
      <select
        id="migrate-target"
        class="dm-input text-sm"
        bind:value={migrateTarget}
        onchange={() => { migratePreflight = null; if (migrateTarget) runPreflight(); }}
      >
        <option value="">{m.stacks_d_select_host()}</option>
        {#each hosts.available.filter(h => h.id !== stackHost && h.id !== 'all') as h}
          <option value={h.id} disabled={h.status !== 'online'}>{h.name} {h.status !== 'online' ? `(${h.status === 'offline' ? m.common_offline() : h.status})` : ''}</option>
        {/each}
      </select>
    </div>

    {#if migratePreflightLoading}
      <div class="flex items-center gap-2 text-sm text-[var(--fg-muted)]">
        <Loader2 class="w-4 h-4 animate-spin" /> {m.stacks_d_running_preflight()}
      </div>
    {/if}

    {#if migratePreflight}
      <div class="space-y-1.5">
        <div class="text-xs font-medium text-[var(--fg-muted)] uppercase tracking-wider">{m.stacks_d_preflight()}</div>
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
          {m.stacks_d_preflight_failed_hint()}
        </div>
      {/if}
    {/if}

    <p class="text-xs text-[var(--fg-muted)]">
      {m.stacks_d_migrate_note_before()} <strong>{m.stacks_d_migrate_stopped()}</strong> {m.stacks_d_migrate_note_after()}
    </p>
  </div>

  {#snippet footer()}
    <Button variant="secondary" onclick={() => (showMigrate = false)}>{m.common_cancel()}</Button>
    <Button
      variant="primary"
      loading={migrateBusy}
      disabled={migrateBusy || !migrateTarget || migratePreflightLoading || (migratePreflight && !migratePreflight.passed)}
      onclick={startMigration}
    >
      <ArrowRightLeft class="w-4 h-4" />
      {m.stacks_d_start_migration()}
    </Button>
  {/snippet}
</Modal>

<!-- Git source configure dialog -->
<Modal bind:open={showGitDialog} title={gitSource ? m.stacks_d_edit_git() : m.stacks_d_connect_git()} maxWidth="max-w-lg">
  <form onsubmit={saveGitSource} id="git-form" class="space-y-4">
    <div>
      <label class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5" for="git-repo">{m.stacks_git_repo_url()}</label>
      <input id="git-repo" class="dm-input" placeholder="https://github.com/acme/stack.git" bind:value={gitForm.repo_url} />
    </div>
    <div class="grid grid-cols-2 gap-3">
      <div>
        <label class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5" for="git-branch">{m.stacks_git_branch()}</label>
        <input id="git-branch" class="dm-input" bind:value={gitForm.branch as any} />
      </div>
      <div>
        <label class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5" for="git-path">{m.stacks_git_path()}</label>
        <input id="git-path" class="dm-input" placeholder="." bind:value={gitForm.path_in_repo as any} />
      </div>
    </div>
    <div>
      <label class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5" for="git-auth">{m.stacks_git_auth()}</label>
      <select id="git-auth" class="dm-input" bind:value={gitForm.auth_kind as any}>
        <option value="none">{m.stacks_git_auth_none()}</option>
        <option value="http">{m.stacks_d_auth_http()}</option>
        <option value="ssh">{m.stacks_d_auth_ssh()}</option>
      </select>
    </div>
    {#if gitForm.auth_kind === 'http'}
      <div>
        <label class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5" for="git-user">{m.stacks_git_username()}</label>
        <input id="git-user" class="dm-input" placeholder={m.stacks_git_username_placeholder()} bind:value={gitForm.username as any} />
      </div>
      <div>
        <label class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5" for="git-pass">
          {m.stacks_git_pat()}
          {#if gitSource?.has_password}<span class="font-normal normal-case">{m.stacks_d_keep_existing()}</span>{/if}
        </label>
        <input id="git-pass" type="password" class="dm-input" placeholder={m.stacks_git_pat_or()} bind:value={gitForm.password as any} />
      </div>
      {#if gitForm.repo_url && /github\.com/.test(gitForm.repo_url)}
        <div class="text-xs text-[var(--fg-muted)] leading-relaxed">
          <strong class="text-[var(--fg)]">{m.stacks_d_gh_pat()}</strong>{m.stacks_d_gh_not_password()}
          <a href="https://github.com/settings/personal-access-tokens" target="_blank" rel="noopener" class="underline">
            {m.stacks_d_gh_create()}
          </a>
          {m.stacks_d_gh_with()} <code class="font-mono">Contents: Read-only</code> {m.stacks_d_gh_permission()}
        </div>
      {/if}
    {:else if gitForm.auth_kind === 'ssh'}
      <div>
        <label class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5" for="git-sshuser">{m.stacks_git_ssh_user()}</label>
        <input id="git-sshuser" class="dm-input" placeholder="git" bind:value={gitForm.username as any} />
      </div>
      <div>
        <label class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5" for="git-sshkey">
          {m.stacks_d_private_key_pem()}
          {#if gitSource?.has_ssh_key}<span class="font-normal normal-case">{m.stacks_d_keep_existing()}</span>{/if}
        </label>
        <textarea id="git-sshkey" class="dm-input font-mono text-xs" rows="5" bind:value={gitForm.ssh_key as any}></textarea>
      </div>
    {/if}
    <div class="grid grid-cols-2 gap-3">
      <div>
        <label class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5" for="git-poll">{m.stacks_d_poll_sec()}</label>
        <input id="git-poll" type="number" min="0" class="dm-input" bind:value={gitForm.poll_interval_sec as any} />
        <p class="text-xs text-[var(--fg-muted)] mt-1">{m.stacks_d_poll_hint()}</p>
      </div>
      <div>
        <label class="block text-xs font-medium text-[var(--fg-muted)] mb-1.5" for="git-webhook">
          {m.stacks_d_webhook_hmac()}
          {#if gitSource?.has_webhook_secret}<span class="font-normal normal-case">{m.stacks_d_keep_existing()}</span>{/if}
        </label>
        <div class="flex gap-2">
          <input id="git-webhook" type="text" autocomplete="off" class="dm-input font-mono text-xs" bind:value={gitForm.webhook_secret as any} />
          <button type="button" class="dm-btn dm-btn-secondary dm-btn-sm" onclick={generateWebhookSecret}>{m.stacks_d_generate()}</button>
        </div>
        <p class="text-xs text-[var(--fg-muted)] mt-1">{m.stacks_d_webhook_hint()}</p>
      </div>
    </div>
    <label class="flex items-center gap-2 text-sm">
      <input type="checkbox" bind:checked={gitForm.auto_deploy as any} />
      {m.stacks_git_auto_deploy()}
    </label>
    {#if gitSource}
      <div class="text-xs text-[var(--fg-muted)] bg-[var(--bg-muted)] rounded p-2 border border-[var(--border)]">
        <p class="font-medium text-[var(--fg)] mb-1">{m.stacks_d_webhook_url()}</p>
        <code class="text-[11px] font-mono break-all">POST {location.origin}/api/v1/stacks/{name}/git/webhook</code>
        {#if !gitSource.has_webhook_secret && !gitForm.webhook_secret}
          <p class="mt-1 text-[var(--color-warning-400)]">{m.stacks_d_webhook_disabled_until()}</p>
        {/if}
      </div>
    {/if}
  </form>
  {#snippet footer()}
    <Button variant="ghost" onclick={() => (showGitDialog = false)}>{m.common_cancel()}</Button>
    <Button variant="primary" onclick={saveGitSource} disabled={gitBusy || !gitForm.repo_url}>
      <LinkIcon class="w-3.5 h-3.5" />
      {gitBusy ? m.common_saving() : gitSource ? m.common_save() : m.stacks_d_connect_sync()}
    </Button>
  {/snippet}
</Modal>

<DeleteStackDialog bind:open={showDelete} {name} host={stackHost} containers={services.length} ondeleted={() => goto('/stacks')} />

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
  .stk-upd { color: var(--accent); font-family: var(--font-sans); }
</style>
