<script lang="ts">
  import { onMount, onDestroy, tick } from 'svelte';
  import { page } from '$app/stores';
  import { api, type Agent, type SystemMetrics, ApiError } from '$lib/api';
  import { Badge, Button, Skeleton, AnimatedNumber } from '$lib/components/ui';
  import { toast } from '$lib/stores/toast.svelte';
  import { allowed } from '$lib/rbac';
  import { Server, Cpu, MemoryStick, HardDrive, ArrowLeft, ShieldCheck, Clock, ArrowUpCircle } from 'lucide-svelte';

  let id = $derived($page.params.id);

  let agent = $state<Agent | null>(null);
  let metrics = $state<SystemMetrics | null>(null);
  let events = $state<{ ts: string; action: string; target?: string; username?: string }[]>([]);
  let loadError = $state<string | null>(null);
  let loading = $state(true);

  // Client-side rolling 60-sample window per metric. A new sample
  // lands every ~3 s so the sparklines cover ~3 minutes of history.
  // Backend already smooths CPU% over a 5 s window, so what we plot
  // here is already quiet — the sparklines read as "recent trend",
  // not jitter.
  const WINDOW = 60;
  let cpuHistory = $state<number[]>([]);
  let memHistory = $state<number[]>([]);

  let pollTimer: ReturnType<typeof setInterval> | null = null;

  async function loadAgent() {
    try {
      agent = await api.agents.get(id);
      loadError = null;
    } catch (err) {
      loadError = err instanceof ApiError ? err.message : String(err);
    } finally {
      loading = false;
    }
  }

  async function pollMetrics() {
    if (!agent) return;
    try {
      // Local host shorthand: pass 'local', otherwise the agent's id.
      const host = id === 'local' ? 'local' : id;
      const m = await api.system.metrics(host);
      if ('items' in (m as any)) return; // shouldn't happen for a single-host call
      metrics = m as SystemMetrics;
      cpuHistory = [...cpuHistory, metrics.cpu_percent].slice(-WINDOW);
      memHistory = [...memHistory, metrics.mem_percent].slice(-WINDOW);
    } catch {
      /* agent might be offline; metrics stays stale rather than flashing */
    }
  }

  async function loadEvents() {
    if (!agent) return;
    try {
      const hostName = agent.name;
      // Pull a generous window and filter client-side to anything
      // targeting this host by name OR id. Audit action targets look
      // like "mystack @ <host>", "<host>", or just stack/container
      // names, so a substring match is the least-lossy filter.
      const raw = await api.audit.list(200);
      events = raw
        .filter(e => {
          const t = (e.target || '') + ' ' + (e.details || '');
          return t.includes(hostName) || t.includes(id);
        })
        .slice(0, 20);
    } catch {
      /* ignore — event feed is best-effort */
    }
  }

  onMount(async () => {
    await loadAgent();
    if (agent) {
      await Promise.all([pollMetrics(), loadEvents()]);
      pollTimer = setInterval(async () => {
        await pollMetrics();
      }, 3000);
    }
  });

  onDestroy(() => {
    if (pollTimer) clearInterval(pollTimer);
  });

  // Sparkline generator — returns an SVG path for the given series.
  // Normalises to the 0..100 y-range of a percent metric, so both
  // cpu% and mem% plot at the same scale.
  function spark(series: number[], min = 0, max = 100): string {
    if (series.length < 2) return '';
    const w = 160;
    const h = 32;
    const step = w / (WINDOW - 1);
    return series
      .map((v, i) => {
        const x = (series.length === WINDOW ? i : i + (WINDOW - series.length)) * step;
        const y = h - ((v - min) / (max - min)) * h;
        return `${i === 0 ? 'M' : 'L'} ${x.toFixed(1)} ${y.toFixed(1)}`;
      })
      .join(' ');
  }

  function fmtBytes(n?: number): string {
    if (!n) return '—';
    const kb = 1024, mb = kb * 1024, gb = mb * 1024, tb = gb * 1024;
    if (n >= tb) return (n / tb).toFixed(1) + ' TB';
    if (n >= gb) return (n / gb).toFixed(1) + ' GB';
    if (n >= mb) return (n / mb).toFixed(1) + ' MB';
    if (n >= kb) return (n / kb).toFixed(0) + ' KB';
    return n + ' B';
  }

  function fmtUptime(sec?: number): string {
    if (!sec) return '—';
    const d = Math.floor(sec / 86400);
    const h = Math.floor((sec % 86400) / 3600);
    if (d > 0) return `${d}d ${h}h`;
    const m = Math.floor((sec % 3600) / 60);
    return `${h}h ${m}m`;
  }

  function fmtAgo(ts?: string): string {
    if (!ts) return '—';
    const diffMs = Date.now() - new Date(ts).getTime();
    if (diffMs < 10_000) return 'just now';
    const sec = Math.floor(diffMs / 1000);
    if (sec < 60) return `${sec}s ago`;
    const min = Math.floor(sec / 60);
    if (min < 60) return `${min}m ago`;
    const hr = Math.floor(min / 60);
    if (hr < 24) return `${hr}h ago`;
    return `${Math.floor(hr / 24)}d ago`;
  }

  function statusVariant(s?: string): 'success' | 'warning' | 'danger' | 'default' {
    if (s === 'online') return 'success';
    if (s === 'pending') return 'warning';
    if (s === 'revoked') return 'danger';
    return 'default';
  }

  function actionColor(action: string): string {
    if (action.includes('deploy') || action.includes('create')) return 'text-[var(--color-success-400)]';
    if (action.includes('delete') || action.includes('fail')) return 'text-[var(--color-danger-400)]';
    if (action.includes('update') || action.includes('restart')) return 'text-[var(--color-warning-400)]';
    return 'text-[var(--fg-muted)]';
  }
</script>

<section class="space-y-4">
  <div class="flex items-center gap-2 text-xs text-[var(--fg-muted)]">
    <a href="/agents" class="hover:text-[var(--fg)] flex items-center gap-1"><ArrowLeft class="w-3 h-3" /> Agents</a>
  </div>

  {#if loading}
    <div class="dm-card p-6"><Skeleton width="40%" height="1.5rem" /></div>
  {:else if loadError}
    <div class="dm-card p-6 text-sm text-[var(--color-danger-400)]">Failed to load: {loadError}</div>
  {:else if agent}
    <!-- Header -->
    <div class="dm-card p-5">
      <div class="flex items-start gap-4 flex-wrap">
        <div class="w-12 h-12 rounded-lg bg-[color-mix(in_srgb,var(--color-brand-500)_15%,transparent)] text-[var(--color-brand-400)] flex items-center justify-center shrink-0">
          <Server class="w-6 h-6" />
        </div>
        <div class="flex-1 min-w-0">
          <div class="flex items-center gap-2 flex-wrap">
            <h1 class="text-xl font-semibold">{agent.name}</h1>
            <Badge variant={statusVariant(agent.status)} dot>{agent.status}</Badge>
          </div>
          <div class="mt-1 text-xs text-[var(--fg-muted)] font-mono truncate">
            {#if agent.hostname}{agent.hostname}{:else}—{/if}
            {#if agent.os}· {agent.os}/{agent.arch}{/if}
            {#if agent.docker_version}· docker {agent.docker_version}{/if}
            {#if agent.version}· agent {agent.version}{/if}
          </div>
        </div>
        {#if agent.status === 'online' && allowed('user.manage')}
          <Button variant="secondary" size="sm" onclick={async () => {
            try {
              await api.agents.upgrade(agent!.id);
              toast.success('Upgrade dispatched', agent!.name);
            } catch (err) {
              toast.error('Upgrade failed', err instanceof ApiError ? err.message : undefined);
            }
          }}>
            <ArrowUpCircle class="w-3.5 h-3.5" /> Trigger upgrade
          </Button>
        {/if}
      </div>
    </div>

    <!-- Live metrics with sparklines -->
    <div class="grid grid-cols-1 sm:grid-cols-3 gap-3">
      <!-- CPU -->
      <div class="dm-card p-4">
        <div class="flex items-center justify-between text-[11px] text-[var(--fg-muted)] uppercase tracking-wider font-medium">
          <div class="flex items-center gap-1.5"><Cpu class="w-3.5 h-3.5" /><span>CPU</span></div>
          {#if metrics}<span class="normal-case text-[var(--fg-subtle)]">{metrics.cpu_cores} cores</span>{/if}
        </div>
        {#if metrics}
          <div class="mt-1.5 text-xl font-semibold font-mono tabular-nums leading-tight text-[var(--color-brand-300)]">
            <AnimatedNumber value={metrics.cpu_percent} format={(n) => n.toFixed(0) + '%'} />
          </div>
          <svg viewBox="0 0 160 32" class="w-full h-8 mt-2" preserveAspectRatio="none">
            <path d={spark(cpuHistory)} stroke="var(--color-brand-400)" stroke-width="1.2" fill="none" stroke-linejoin="round" />
          </svg>
          <div class="mt-1 text-[11px] text-[var(--fg-subtle)] tabular-nums">
            <AnimatedNumber value={metrics.cpu_used_cores} format={(n) => n.toFixed(2)} /> / {metrics.cpu_cores.toFixed(2)} cores
          </div>
        {:else}
          <Skeleton class="mt-2" width="4rem" height="1.5rem" />
        {/if}
      </div>

      <!-- Memory -->
      <div class="dm-card p-4">
        <div class="flex items-center justify-between text-[11px] text-[var(--fg-muted)] uppercase tracking-wider font-medium">
          <div class="flex items-center gap-1.5"><MemoryStick class="w-3.5 h-3.5" /><span>Memory</span></div>
          {#if metrics && metrics.mem_total > 0}<span class="normal-case text-[var(--fg-subtle)]">{fmtBytes(metrics.mem_total)}</span>{/if}
        </div>
        {#if metrics && metrics.mem_total > 0}
          <div class="mt-1.5 text-xl font-semibold font-mono tabular-nums leading-tight text-[var(--color-brand-300)]">
            <AnimatedNumber value={metrics.mem_percent} format={(n) => n.toFixed(0) + '%'} />
          </div>
          <svg viewBox="0 0 160 32" class="w-full h-8 mt-2" preserveAspectRatio="none">
            <path d={spark(memHistory)} stroke="#14b8a6" stroke-width="1.2" fill="none" stroke-linejoin="round" />
          </svg>
          <div class="mt-1 text-[11px] text-[var(--fg-subtle)] tabular-nums">
            <AnimatedNumber value={metrics.mem_used} format={fmtBytes} /> / {fmtBytes(metrics.mem_total)}
          </div>
        {:else}
          <Skeleton class="mt-2" width="4rem" height="1.5rem" />
        {/if}
      </div>

      <!-- Disk -->
      <div class="dm-card p-4">
        <div class="flex items-center justify-between text-[11px] text-[var(--fg-muted)] uppercase tracking-wider font-medium">
          <div class="flex items-center gap-1.5"><HardDrive class="w-3.5 h-3.5" /><span>Disk</span></div>
          {#if metrics && metrics.disk_path}<span class="normal-case text-[var(--fg-subtle)] font-mono truncate ml-2" title={metrics.disk_path}>{metrics.disk_path}</span>{/if}
        </div>
        {#if metrics && metrics.disk_total > 0}
          <div class="mt-1.5 text-xl font-semibold font-mono tabular-nums leading-tight text-[var(--color-brand-300)]">
            <AnimatedNumber value={metrics.disk_percent} format={(n) => n.toFixed(0) + '%'} />
          </div>
          <div class="mt-3 h-1.5 rounded-full overflow-hidden bg-[var(--surface-hover)]">
            <div class="h-full rounded-full transition-all duration-500 bg-[var(--color-brand-500)]" style:width="{metrics.disk_percent}%"></div>
          </div>
          <div class="mt-2 text-[11px] text-[var(--fg-subtle)] tabular-nums">
            {fmtBytes(metrics.disk_used)} / {fmtBytes(metrics.disk_total)}
          </div>
        {:else}
          <Skeleton class="mt-2" width="4rem" height="1.5rem" />
        {/if}
      </div>
    </div>

    <!-- Event feed -->
    <div class="dm-card">
      <div class="px-4 py-2.5 border-b border-[var(--border)] flex items-center gap-2">
        <Clock class="w-4 h-4 text-[var(--fg-muted)]" />
        <h3 class="text-sm font-semibold">Recent activity on this host</h3>
        <span class="ml-auto text-[11px] text-[var(--fg-subtle)]">{events.length} entries</span>
      </div>
      {#if events.length === 0}
        <p class="px-4 py-6 text-xs text-[var(--fg-muted)] text-center">No recent activity recorded against this host.</p>
      {:else}
        <div class="divide-y divide-[var(--border)]">
          {#each events as ev (ev.ts + ev.action)}
            <div class="flex items-center gap-3 px-4 py-2 hover:bg-[var(--surface-hover)]">
              <span class="font-mono text-[10px] text-[var(--fg-subtle)] shrink-0 w-28">{fmtAgo(ev.ts)}</span>
              <span class="font-mono text-[11px] shrink-0 w-32 truncate {actionColor(ev.action)}">{ev.action}</span>
              {#if ev.target}
                <span class="text-xs text-[var(--fg-muted)] truncate flex-1 font-mono">{ev.target}</span>
              {/if}
              {#if ev.username}
                <span class="text-[10px] text-[var(--fg-subtle)] shrink-0">by {ev.username}</span>
              {/if}
            </div>
          {/each}
        </div>
      {/if}
    </div>

    <!-- Footer strip — matches the marketing deep-dive mock: at-a-glance
         trust signals (mTLS, versions, last-seen). No bandwidth numbers;
         the agent protocol doesn't track those yet. -->
    <div class="flex flex-wrap items-center gap-x-4 gap-y-1 px-4 py-2.5 rounded-lg border border-[var(--border)] bg-[var(--surface)] text-[10px] font-mono text-[var(--fg-muted)]">
      <span class="flex items-center gap-1.5 text-[var(--color-success-400)]">
        <ShieldCheck class="w-3 h-3" />
        mTLS OK
      </span>
      {#if metrics && metrics.uptime_seconds}
        <span>up {fmtUptime(metrics.uptime_seconds)}</span>
      {/if}
      {#if agent.docker_version}<span>docker {agent.docker_version}</span>{/if}
      {#if agent.version}<span>agent {agent.version}</span>{/if}
      <span class="ml-auto">last seen {fmtAgo(agent.last_seen_at)}</span>
    </div>
  {/if}
</section>
