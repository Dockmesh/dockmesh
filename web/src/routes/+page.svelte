<script lang="ts">
  import { untrack } from 'svelte';
  import { api, isFanOut, type SystemMetrics, type ContainerSummary } from '$lib/api';
  import { allowed } from '$lib/rbac';
  import { hosts } from '$lib/stores/host.svelte';
  import { autoRefresh } from '$lib/autorefresh';
  import { Skeleton, Badge, AnimatedNumber } from '$lib/components/ui';
  import {
    Box,
    Cpu,
    MemoryStick,
    HardDrive,
    Activity,
    RefreshCw,
    Server,
    CheckCircle2,
    AlertTriangle,
    Rocket,
    Download,
    Archive,
    ShieldCheck,
    Layers
  } from 'lucide-svelte';

  // Per-host metrics row used in all-mode. Matches the backend
  // systemMetricsRow shape (flattened Metrics + host metadata).
  type PerHostMetrics = SystemMetrics & { host_id: string; host_name: string };

  type StackCard = {
    name: string;
    state: 'running' | 'stopped' | 'unhealthy' | 'partial';
    services: Array<{ name: string; state: string }>;
    // Hosts where this stack's containers currently live. In all-mode
    // this can be multiple — today we still deploy a stack to one host
    // at a time but the per-host-label grouping detects anything that
    // drifted to run on multiple hosts.
    hosts: Array<{ id: string; name: string }>;
  };

  let health = $state<{ status: string; version: string; docker: boolean } | null>(null);
  // Single-host mode: one Metrics object. All-mode: array of per-host rows.
  let sysMetrics = $state<SystemMetrics | null>(null);
  let perHostMetrics = $state<PerHostMetrics[]>([]);
  let containerStats = $state({ total: 0, running: 0, stopped: 0, unhealthy: 0 });
  let stackCards = $state<StackCard[]>([]);
  let recentAudit = $state<any[]>([]);
  let agentCount = $state({ online: 0, total: 0 });
  let loading = $state(true);
  let error = $state('');
  let stackFilter = $state<'all' | 'running' | 'stopped' | 'unhealthy'>('all');

  const isAll = $derived(hosts.isAll);
  const isRemote = $derived(hosts.id !== 'local' && hosts.id !== 'all');

  async function load() {
    // Only flip the skeleton on the FIRST load. Read guarded by
    // untrack() so a caller $effect doesn't subscribe to these state
    // vars — writes inside load() would otherwise re-run the effect
    // and cause a runaway fetch loop.
    const isFirstLoad = untrack(
      () => !sysMetrics && perHostMetrics.length === 0 && stackCards.length === 0
    );
    if (isFirstLoad) loading = true;
    error = '';
    try {
      // Dashboard fetches the COMPACT container summary (~1 KB) instead
      // of the full container list (~15 KB). The summary already gives
      // us per-state counts + per-stack rollup, which is everything this
      // page needs. A 10-second auto-refresh loop that used to push 90
      // KB/min now pushes ~6 KB/min and server-side CPU drops from 26 ms
      // to <5 ms per call.
      const [h, sysRaw, summary, stacksList, audit, hostList] = await Promise.all([
        api.health(),
        api.system.metrics(hosts.id).catch(() => null),
        api.containers.summary(hosts.id).catch((): ContainerSummary => ({ total: 0, running: 0, stopped: 0, unhealthy: 0, by_stack: {} })),
        api.stacks.list().catch(() => []),
        allowed('audit.read') ? api.audit.list(8).catch(() => []) : Promise.resolve([]),
        api.hosts.list().catch(() => [])
      ]);
      health = h;

      // System metrics: in all-mode extract per-host rows for the
      // mini-table; in single-host mode keep the one snapshot as-is.
      if (sysRaw && isFanOut(sysRaw)) {
        perHostMetrics = sysRaw.items as PerHostMetrics[];
        sysMetrics = null;
      } else {
        perHostMetrics = [];
        sysMetrics = sysRaw as SystemMetrics | null;
      }

      // Container counts come pre-aggregated from /containers/summary.
      containerStats.total = summary.total;
      containerStats.running = summary.running;
      containerStats.stopped = summary.stopped;
      containerStats.unhealthy = summary.unhealthy;

      // Build stack cards from the summary's per-stack rollup. The
      // summary tells us which compose projects have containers and
      // their aggregate state; we merge that onto the stacks-from-disk
      // list so stacks without any running containers still render as
      // "stopped" cards.
      const hostName = new Map<string, string>(hostList.map((h: any) => [h.id, h.name] as [string, string]));
      stackCards = stacksList.map((s: any) => {
        const rollup = summary.by_stack[s.name];
        if (!rollup) {
          return { name: s.name, state: 'stopped' as const, services: [], hosts: [] };
        }
        let state: StackCard['state'];
        if (rollup.unhealthy > 0) state = 'unhealthy';
        else if (rollup.running === rollup.total) state = 'running';
        else if (rollup.running === 0) state = 'stopped';
        else state = 'partial';
        return {
          name: s.name,
          state,
          services: rollup.services.map((name) => ({ name, state: 'running' })),
          hosts: rollup.hosts.map((id) => ({ id, name: hostName.get(id) ?? (id === 'local' ? 'Local' : id) }))
        };
      });

      recentAudit = audit;
      agentCount.total = hostList.length;
      agentCount.online = hostList.filter((x: any) => x.status === 'online').length;
    } catch (err: any) {
      error = err.message ?? 'Failed to load';
    } finally {
      loading = false;
    }
  }

  // Re-load when the host picker changes.
  let prevHost = hosts.id;
  $effect(() => {
    const cur = hosts.id;
    if (cur !== prevHost) {
      prevHost = cur;
      load();
    }
  });

  $effect(() => {
    load();
  });

  // Auto-refresh every 10s so the dashboard reflects container state
  // changes, backup completions, new audit entries without requiring
  // the user to hit Refresh. The returned cleanup stops the interval
  // on unmount.
  $effect(() => autoRefresh(load, 10_000));

  function actionColor(action: string): 'success' | 'warning' | 'danger' | 'info' | 'default' {
    if (action.includes('delete') || action.includes('remove') || action.includes('failed'))
      return 'danger';
    if (action.includes('create') || action.includes('deploy') || action.includes('start'))
      return 'success';
    if (action.includes('update') || action.includes('refresh')) return 'info';
    return 'default';
  }

  // Map a raw audit event to a human-readable sentence. Keeping the
  // mapping frontend-side lets us i18n later without a backend schema
  // change. Every case falls through to a generic "action — target"
  // format so unknown actions still render.
  function formatActivity(e: any): string {
    const tgt = e.target ?? '';
    const short = tgt.length > 12 && /^[0-9a-f]/.test(tgt) ? tgt.slice(0, 12) : tgt;
    switch (e.action) {
      case 'auth.login':
        return 'signed in';
      case 'auth.logout':
        return 'signed out';
      case 'auth.login_failed':
        return `failed sign-in attempt${tgt ? ' for ' + tgt : ''}`;
      case 'auth.sso_login':
        return 'signed in via SSO';
      case 'stack.create':
        return `created stack ${tgt}`;
      case 'stack.update':
        return `updated stack ${tgt}`;
      case 'stack.delete':
        return `deleted stack ${tgt}`;
      case 'stack.deploy':
        return `deployed stack ${tgt}`;
      case 'stack.stop':
        return `stopped stack ${tgt}`;
      case 'container.start':
        return `started container ${short}`;
      case 'container.stop':
        return `stopped container ${short}`;
      case 'container.restart':
        return `restarted container ${short}`;
      case 'container.remove':
        return `removed container ${short}`;
      case 'container.update':
        return `updated container ${short}`;
      case 'container.rollback':
        return `rolled back container ${short}`;
      case 'image.pull':
        return `pulled image ${tgt}`;
      case 'image.remove':
        return `removed image ${short}`;
      case 'image.prune':
        return `pruned unused images`;
      case 'image.scan':
        return `scanned image ${tgt}`;
      case 'network.create':
        return `created network ${tgt}`;
      case 'network.remove':
        return `removed network ${tgt}`;
      case 'volume.create':
        return `created volume ${tgt}`;
      case 'volume.remove':
        return `removed volume ${tgt}`;
      case 'volume.prune':
        return `pruned unused volumes`;
      case 'user.create':
        return `created user ${tgt}`;
      case 'user.delete':
        return `deleted user ${tgt}`;
      case 'user.update':
        return `updated user ${tgt}`;
      case 'user.password':
        return `changed password for ${tgt}`;
      default:
        return e.action + (tgt ? ` — ${short}` : '');
    }
  }

  function fmtTime(ts: string): string {
    const t = new Date(ts);
    const diff = (Date.now() - t.getTime()) / 1000;
    if (diff < 60) return 'just now';
    if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
    if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
    if (diff < 30 * 86400) return `${Math.floor(diff / 86400)}d ago`;
    return t.toLocaleDateString();
  }

  function fmtBytes(n: number): string {
    if (n === 0) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.min(Math.floor(Math.log(n) / Math.log(1024)), units.length - 1);
    return `${(n / Math.pow(1024, i)).toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
  }

  // Pick a bar color based on percentage — green < 60%, yellow < 85%,
  // red above that. Keeps the dashboard a quick visual read.
  function barColor(pct: number): string {
    if (pct < 60) return 'var(--color-success-500)';
    if (pct < 85) return 'var(--color-warning-500)';
    return 'var(--color-danger-500)';
  }

  function textColor(pct: number): string {
    if (pct < 60) return 'var(--color-success-400)';
    if (pct < 85) return 'var(--color-warning-400)';
    return 'var(--color-danger-400)';
  }

  // Dashboard's stack grid is a preview, NOT the canonical list — at
  // 100+ stacks the 3-column card view becomes a wall of noise and
  // pushes Recent Activity / Quick Actions off-screen.
  //
  // Sort is deliberately stable alphabetical, NOT by state. The auto-
  // refresh ticks every few seconds, and sorting by `running/stopped`
  // made cards flip positions each tick while containers settled —
  // jumpy, distracting, and useless for the user. Filter buttons on
  // top already surface how many stacks are in each state.
  const STACK_PREVIEW_LIMIT = 12;
  const sortedStacks = $derived(
    [...stackCards].sort((a, b) => a.name.localeCompare(b.name))
  );
  const filteredStacks = $derived(
    (stackFilter === 'all' ? sortedStacks : sortedStacks.filter((s) => s.state === stackFilter))
      .slice(0, STACK_PREVIEW_LIMIT)
  );
  const hiddenStackCount = $derived(
    Math.max(
      0,
      (stackFilter === 'all'
        ? stackCards.length
        : stackCards.filter((s) => s.state === stackFilter).length) - STACK_PREVIEW_LIMIT
    )
  );
  const stackCounts = $derived({
    all: stackCards.length,
    running: stackCards.filter((s) => s.state === 'running').length,
    stopped: stackCards.filter((s) => s.state === 'stopped').length,
    unhealthy: stackCards.filter((s) => s.state === 'unhealthy' || s.state === 'partial').length
  });

  const canDeploy = $derived(allowed('stack.deploy'));
  const canImage = $derived(allowed('image.write'));
  const canBackup = $derived(allowed('user.manage'));
  const canScan = $derived(allowed('image.scan'));
</script>

<section class="space-y-6">
  <!-- Header row -->
  <div class="flex items-start justify-between flex-wrap gap-3">
    <div>
      <div class="flex items-center gap-2">
        <h1 class="text-[22px] font-semibold tracking-tight">Dashboard</h1>
        {#if isRemote}
          <span
            class="inline-flex items-center gap-1 text-[11px] px-2 py-0.5 rounded-full border border-[var(--color-brand-500)]/30 bg-[color-mix(in_srgb,var(--color-brand-500)_10%,transparent)] text-[var(--color-brand-400)]"
          >
            <Server class="w-3 h-3" />
            {hosts.selected?.name}
          </span>
        {/if}
      </div>
      <p class="text-sm text-[var(--fg-muted)] mt-0.5 flex items-center gap-1.5">
        {#if health?.docker}
          <CheckCircle2 class="w-3.5 h-3.5 text-[var(--color-success-400)]" />
          <span>Docker {health.version} · dockmesh {health.status}</span>
        {:else if health}
          <AlertTriangle class="w-3.5 h-3.5 text-[var(--color-warning-400)]" />
          <span class="text-[var(--color-warning-400)]">Docker daemon unreachable</span>
        {:else}
          <span>Loading system status…</span>
        {/if}
      </p>
    </div>
    <button
      onclick={load}
      class="dm-btn dm-btn-secondary dm-btn-sm"
      disabled={loading}
      aria-label="Refresh"
    >
      <RefreshCw class="w-3.5 h-3.5 {loading ? 'animate-spin' : ''}" />
      Refresh
    </button>
  </div>

  {#if error}
    <div
      class="dm-card p-4 border-[color-mix(in_srgb,var(--color-danger-500)_30%,transparent)] text-[var(--color-danger-400)] text-sm"
    >
      {error}
    </div>
  {/if}

  {#if health && !health.docker}
    <!--
      Docker-unreachable banner. Prominent but non-alarming — this is an
      auto-recoverable state that fixes itself as soon as the daemon
      opens its socket again. Most common trigger on macOS: the boot
      race where launchd fires dockmesh before Docker Desktop starts.
    -->
    <div
      class="dm-card p-4 border-[color-mix(in_srgb,var(--color-warning-500)_35%,transparent)] bg-[color-mix(in_srgb,var(--color-warning-500)_7%,transparent)]"
    >
      <div class="flex items-start gap-3">
        <AlertTriangle class="w-5 h-5 text-[var(--color-warning-400)] mt-0.5 flex-shrink-0" />
        <div class="flex-1 min-w-0 text-sm">
          <div class="font-medium text-[var(--fg)]">Docker daemon not responding</div>
          <p class="text-[var(--fg-muted)] mt-0.5">
            Container, stack, image and volume endpoints will return errors until Docker is available. Dockmesh checks for the socket every 10 seconds — as soon as Docker starts, this banner clears automatically. No restart needed.
          </p>
          <p class="text-xs text-[var(--fg-subtle)] mt-1.5">
            On macOS this often happens right after a reboot when dockmesh starts before Docker Desktop. Usually resolves within 30-60 seconds.
          </p>
        </div>
      </div>
    </div>
  {/if}

  <!-- ─────────── Row 1: System metrics ───────────
       Single-host mode: 4 cards (CPU / Memory / Disk / Containers).
       All-hosts mode: Containers card alone + a per-host table below.
       The CPU/Memory/Disk cards don't make sense in all-mode because
       there isn't one host whose numbers would go in them — instead we
       show a compact table with one row per host. -->
  {#if !isAll}
  <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-3">
    <!-- CPU -->
    <div class="dm-card p-4">
      <div class="flex items-center justify-between text-[11px] text-[var(--fg-muted)] uppercase tracking-wider font-medium">
        <div class="flex items-center gap-1.5">
          <Cpu class="w-3.5 h-3.5" />
          <span>CPU</span>
          {#if sysMetrics?.docker_limited}
            <span class="normal-case text-[10px] px-1.5 py-0.5 rounded-full bg-[color-mix(in_srgb,var(--color-brand-500)_15%,transparent)] text-[var(--color-brand-400)]" title="Docker Desktop resource limit — configured under Settings → Resources">
              Docker cap
            </span>
          {/if}
        </div>
        {#if sysMetrics}
          <span class="normal-case text-[var(--fg-subtle)]">{sysMetrics.cpu_cores} cores</span>
        {/if}
      </div>
      {#if loading || !sysMetrics}
        <Skeleton class="mt-2" width="4rem" height="1.5rem" />
        <Skeleton class="mt-2" width="100%" height="0.25rem" />
      {:else}
        <div class="mt-1.5 text-xl font-semibold font-mono tabular-nums leading-tight" style:color={textColor(sysMetrics.cpu_percent)}>
          <AnimatedNumber value={sysMetrics.cpu_percent} format={(n) => n.toFixed(0) + '%'} />
        </div>
        <div class="mt-2 h-1 rounded-full overflow-hidden bg-[var(--surface-hover)]">
          <div
            class="h-full rounded-full transition-all duration-500"
            style:width="{sysMetrics.cpu_percent}%"
            style:background={barColor(sysMetrics.cpu_percent)}
          ></div>
        </div>
        <div class="mt-1.5 text-[11px] text-[var(--fg-subtle)] tabular-nums">
          <AnimatedNumber value={sysMetrics.cpu_used_cores} format={(n) => n.toFixed(2)} /> / {sysMetrics.cpu_cores.toFixed(2)} cores
        </div>
        {#if sysMetrics.docker_limited && sysMetrics.host_cpu_cores}
          <div class="mt-0.5 text-[10px] text-[var(--fg-subtle)] tabular-nums">
            Host: {sysMetrics.host_cpu_used_cores?.toFixed(2) ?? '—'} / {sysMetrics.host_cpu_cores} cores ({sysMetrics.host_cpu_percent?.toFixed(0) ?? '—'}%)
          </div>
        {/if}
      {/if}
    </div>

    <!-- Memory -->
    <div class="dm-card p-4">
      <div class="flex items-center justify-between text-[11px] text-[var(--fg-muted)] uppercase tracking-wider font-medium">
        <div class="flex items-center gap-1.5">
          <MemoryStick class="w-3.5 h-3.5" />
          <span>Memory</span>
          {#if sysMetrics?.docker_limited}
            <span class="normal-case text-[10px] px-1.5 py-0.5 rounded-full bg-[color-mix(in_srgb,var(--color-brand-500)_15%,transparent)] text-[var(--color-brand-400)]" title="Docker Desktop resource limit — configured under Settings → Resources">
              Docker cap
            </span>
          {/if}
        </div>
        {#if sysMetrics && sysMetrics.mem_total > 0}
          <span class="normal-case text-[var(--fg-subtle)]">{fmtBytes(sysMetrics.mem_total)}</span>
        {/if}
      </div>
      {#if loading || !sysMetrics}
        <Skeleton class="mt-2" width="4rem" height="1.5rem" />
        <Skeleton class="mt-2" width="100%" height="0.25rem" />
      {:else if sysMetrics.mem_total === 0}
        <div class="mt-1.5 text-xl font-semibold text-[var(--fg-subtle)] leading-tight">—</div>
        <div class="mt-2 h-1 rounded-full bg-[var(--surface-hover)]"></div>
        <div class="mt-1.5 text-[11px] text-[var(--fg-subtle)]">unavailable on this host</div>
      {:else}
        <div class="mt-1.5 text-xl font-semibold font-mono tabular-nums leading-tight" style:color={textColor(sysMetrics.mem_percent)}>
          <AnimatedNumber value={sysMetrics.mem_percent} format={(n) => n.toFixed(0) + '%'} />
        </div>
        <div class="mt-2 h-1 rounded-full overflow-hidden bg-[var(--surface-hover)]">
          <div
            class="h-full rounded-full transition-all duration-500"
            style:width="{sysMetrics.mem_percent}%"
            style:background={barColor(sysMetrics.mem_percent)}
          ></div>
        </div>
        <div class="mt-1.5 text-[11px] text-[var(--fg-subtle)] tabular-nums">
          <AnimatedNumber value={sysMetrics.mem_used} format={fmtBytes} /> / {fmtBytes(sysMetrics.mem_total)}
        </div>
        {#if sysMetrics.docker_limited && sysMetrics.host_mem_total}
          <div class="mt-0.5 text-[10px] text-[var(--fg-subtle)] tabular-nums">
            Host: {sysMetrics.host_mem_used ? fmtBytes(sysMetrics.host_mem_used) : '—'} / {fmtBytes(sysMetrics.host_mem_total)} ({sysMetrics.host_mem_percent?.toFixed(0) ?? '—'}%)
          </div>
        {/if}
      {/if}
    </div>

    <!-- Disk -->
    <div class="dm-card p-4">
      <div class="flex items-center justify-between text-[11px] text-[var(--fg-muted)] uppercase tracking-wider font-medium">
        <div class="flex items-center gap-1.5">
          <HardDrive class="w-3.5 h-3.5" />
          <span>Disk</span>
        </div>
        {#if sysMetrics && sysMetrics.disk_path}
          <span class="normal-case text-[var(--fg-subtle)] font-mono truncate ml-2" title={sysMetrics.disk_path}>
            {sysMetrics.disk_path}
          </span>
        {/if}
      </div>
      {#if loading || !sysMetrics}
        <Skeleton class="mt-2" width="4rem" height="1.5rem" />
        <Skeleton class="mt-2" width="100%" height="0.25rem" />
      {:else if sysMetrics.disk_total === 0}
        <div class="mt-1.5 text-xl font-semibold text-[var(--fg-subtle)] leading-tight">—</div>
        <div class="mt-2 h-1 rounded-full bg-[var(--surface-hover)]"></div>
        <div class="mt-1.5 text-[11px] text-[var(--fg-subtle)]">unavailable on this host</div>
      {:else}
        <div class="mt-1.5 text-xl font-semibold font-mono tabular-nums leading-tight" style:color={textColor(sysMetrics.disk_percent)}>
          <AnimatedNumber value={sysMetrics.disk_percent} format={(n) => n.toFixed(0) + '%'} />
        </div>
        <div class="mt-2 h-1 rounded-full overflow-hidden bg-[var(--surface-hover)]">
          <div
            class="h-full rounded-full transition-all duration-500"
            style:width="{sysMetrics.disk_percent}%"
            style:background={barColor(sysMetrics.disk_percent)}
          ></div>
        </div>
        <div class="mt-1.5 text-[11px] text-[var(--fg-subtle)] tabular-nums">
          <AnimatedNumber value={sysMetrics.disk_used} format={fmtBytes} /> / {fmtBytes(sysMetrics.disk_total)}
        </div>
      {/if}
    </div>

    <!-- Containers -->
    <div class="dm-card p-4">
      <div class="flex items-center justify-between text-[11px] text-[var(--fg-muted)] uppercase tracking-wider font-medium">
        <div class="flex items-center gap-1.5">
          <Box class="w-3.5 h-3.5" />
          <span>Containers</span>
        </div>
        {#if agentCount.total > 0}
          <span class="normal-case text-[var(--fg-subtle)]">
            {agentCount.online}/{agentCount.total} host{agentCount.total === 1 ? '' : 's'}
          </span>
        {/if}
      </div>
      {#if loading}
        <Skeleton class="mt-2" width="4rem" height="1.5rem" />
        <Skeleton class="mt-3" width="100%" height="0.75rem" />
      {:else}
        <div class="mt-1.5 text-xl font-semibold leading-tight">
          <span class="font-mono tabular-nums">
            <AnimatedNumber value={containerStats.running} format={(n) => Math.round(n).toString()} />
          </span><span class="text-sm font-normal text-[var(--fg-subtle)]"> / <AnimatedNumber value={containerStats.total} format={(n) => Math.round(n).toString()} /></span>
        </div>
        <div class="mt-2.5 flex items-center gap-3 text-[11px]">
          <span class="flex items-center gap-1 text-[var(--fg-muted)]">
            <span class="w-1.5 h-1.5 rounded-full bg-[var(--color-success-500)]"></span>
            <AnimatedNumber value={containerStats.running} format={(n) => Math.round(n).toString()} /> running
          </span>
          {#if containerStats.stopped > 0}
            <span class="flex items-center gap-1 text-[var(--fg-muted)]">
              <span class="w-1.5 h-1.5 rounded-full bg-[var(--color-danger-500)]"></span>
              <AnimatedNumber value={containerStats.stopped} format={(n) => Math.round(n).toString()} /> stopped
            </span>
          {/if}
          {#if containerStats.unhealthy > 0}
            <span class="flex items-center gap-1 text-[var(--fg-muted)]">
              <span class="w-1.5 h-1.5 rounded-full bg-[var(--color-warning-500)]"></span>
              <AnimatedNumber value={containerStats.unhealthy} format={(n) => Math.round(n).toString()} /> unhealthy
            </span>
          {/if}
        </div>
      {/if}
    </div>
  </div>
  {:else}
  <!-- All-hosts mode row 1: aggregated totals + per-host health table -->
  <div class="grid grid-cols-1 lg:grid-cols-4 gap-3">
    <!-- Aggregated Containers card — same shape as single-mode, but
         the totals now span every host the fan-out contacted. -->
    <div class="dm-card p-4">
      <div class="flex items-center justify-between text-[11px] text-[var(--fg-muted)] uppercase tracking-wider font-medium">
        <div class="flex items-center gap-1.5">
          <Box class="w-3.5 h-3.5" />
          <span>Containers</span>
        </div>
        {#if agentCount.total > 0}
          <span class="normal-case text-[var(--fg-subtle)]">
            {agentCount.online}/{agentCount.total} host{agentCount.total === 1 ? '' : 's'}
          </span>
        {/if}
      </div>
      {#if loading}
        <Skeleton class="mt-2" width="4rem" height="1.5rem" />
        <Skeleton class="mt-3" width="100%" height="0.75rem" />
      {:else}
        <div class="mt-1.5 text-xl font-semibold leading-tight">
          <span class="font-mono tabular-nums">{containerStats.running}</span><span class="text-sm font-normal text-[var(--fg-subtle)]"> / {containerStats.total}</span>
        </div>
        <div class="mt-2.5 flex items-center gap-3 text-[11px] flex-wrap">
          <span class="flex items-center gap-1 text-[var(--fg-muted)]">
            <span class="w-1.5 h-1.5 rounded-full bg-[var(--color-success-500)]"></span>
            {containerStats.running} running
          </span>
          {#if containerStats.stopped > 0}
            <span class="flex items-center gap-1 text-[var(--fg-muted)]">
              <span class="w-1.5 h-1.5 rounded-full bg-[var(--color-danger-500)]"></span>
              {containerStats.stopped} stopped
            </span>
          {/if}
          {#if containerStats.unhealthy > 0}
            <span class="flex items-center gap-1 text-[var(--fg-muted)]">
              <span class="w-1.5 h-1.5 rounded-full bg-[var(--color-warning-500)]"></span>
              {containerStats.unhealthy} unhealthy
            </span>
          {/if}
        </div>
      {/if}
    </div>

    <!-- Per-host mini-table. Spans the remaining 3 columns on lg so
         it sits next to the Containers card. Each row is one host with
         CPU / RAM / Disk progress bars — a compact way to see which
         host is under pressure without drilling into a detail view. -->
    <div class="dm-card lg:col-span-3 overflow-hidden">
      <div class="px-4 py-3 border-b border-[var(--border)] flex items-center gap-2">
        <Layers class="w-3.5 h-3.5 text-[var(--fg-muted)]" />
        <h3 class="font-semibold text-xs uppercase tracking-wider text-[var(--fg-muted)]">Per-host system health</h3>
        <div class="flex-1"></div>
        <span class="text-[11px] text-[var(--fg-subtle)] tabular-nums">
          {perHostMetrics.length} host{perHostMetrics.length === 1 ? '' : 's'}
        </span>
      </div>
      {#if loading && perHostMetrics.length === 0}
        <div class="p-4 space-y-2">
          {#each Array(2) as _}
            <Skeleton width="100%" height="1.25rem" />
          {/each}
        </div>
      {:else if perHostMetrics.length === 0}
        <div class="p-6 text-center text-xs text-[var(--fg-muted)]">No host metrics available.</div>
      {:else}
        <div class="overflow-x-auto">
          <table class="w-full text-[12px]">
            <thead>
              <tr class="text-left text-[10px] uppercase tracking-wider text-[var(--fg-subtle)] border-b border-[var(--border)]">
                <th class="px-4 py-2 font-medium">Host</th>
                <th class="px-3 py-2 font-medium w-[22%]">CPU</th>
                <th class="px-3 py-2 font-medium w-[22%]">Memory</th>
                <th class="px-3 py-2 font-medium w-[22%]">Disk</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-[var(--border)]">
              {#each perHostMetrics as m}
                <tr class="hover:bg-[var(--surface-hover)] transition-colors">
                  <td class="px-4 py-2.5">
                    <div class="flex items-center gap-2">
                      <Server class="w-3 h-3 text-[var(--color-brand-400)]" />
                      <span class="font-mono text-[11px] text-[var(--fg)]">{m.host_name}</span>
                    </div>
                  </td>
                  <td class="px-3 py-2.5">
                    <div class="flex items-center gap-2">
                      <div class="flex-1 h-1 rounded-full bg-[var(--surface-hover)] overflow-hidden">
                        <div class="h-full rounded-full transition-all" style:width="{m.cpu_percent}%" style:background={barColor(m.cpu_percent)}></div>
                      </div>
                      <span class="font-mono text-[11px] tabular-nums shrink-0 w-8 text-right" style:color={textColor(m.cpu_percent)}>{m.cpu_percent.toFixed(0)}%</span>
                    </div>
                  </td>
                  <td class="px-3 py-2.5">
                    {#if m.mem_total > 0}
                      <div class="flex items-center gap-2">
                        <div class="flex-1 h-1 rounded-full bg-[var(--surface-hover)] overflow-hidden">
                          <div class="h-full rounded-full transition-all" style:width="{m.mem_percent}%" style:background={barColor(m.mem_percent)}></div>
                        </div>
                        <span class="font-mono text-[11px] tabular-nums shrink-0 w-8 text-right" style:color={textColor(m.mem_percent)}>{m.mem_percent.toFixed(0)}%</span>
                      </div>
                    {:else}
                      <span class="text-[var(--fg-subtle)]">—</span>
                    {/if}
                  </td>
                  <td class="px-3 py-2.5">
                    {#if m.disk_total > 0}
                      <div class="flex items-center gap-2">
                        <div class="flex-1 h-1 rounded-full bg-[var(--surface-hover)] overflow-hidden">
                          <div class="h-full rounded-full transition-all" style:width="{m.disk_percent}%" style:background={barColor(m.disk_percent)}></div>
                        </div>
                        <span class="font-mono text-[11px] tabular-nums shrink-0 w-8 text-right" style:color={textColor(m.disk_percent)}>{m.disk_percent.toFixed(0)}%</span>
                      </div>
                    {:else}
                      <span class="text-[var(--fg-subtle)]">—</span>
                    {/if}
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </div>
  </div>
  {/if}

  <!-- ─────────── Row 2: Stacks section ─────────── -->
  <div>
    <div class="flex items-end justify-between flex-wrap gap-2 mb-3">
      <div>
        <h3 class="text-sm font-semibold tracking-tight">Stacks</h3>
        <p class="text-xs text-[var(--fg-muted)] mt-0.5">
          {stackCards.length} stack{stackCards.length === 1 ? '' : 's'}
          {#if agentCount.total > 1}
            across {agentCount.total} hosts
          {/if}
        </p>
      </div>
      <div class="flex gap-1 text-xs">
        {#snippet pill(key: 'all' | 'running' | 'stopped' | 'unhealthy', label: string, n: number)}
          <button
            class="px-2.5 py-1 rounded-full border transition-colors {stackFilter === key
              ? 'bg-[color-mix(in_srgb,var(--color-brand-500)_12%,transparent)] border-[color-mix(in_srgb,var(--color-brand-500)_40%,transparent)] text-[var(--color-brand-300)]'
              : 'border-[var(--border)] text-[var(--fg-muted)] hover:bg-[var(--surface-hover)] hover:text-[var(--fg)]'}"
            onclick={() => (stackFilter = key)}
          >
            {label} <span class="tabular-nums">{n}</span>
          </button>
        {/snippet}
        {@render pill('all', 'All', stackCounts.all)}
        {@render pill('running', 'Running', stackCounts.running)}
        {@render pill('stopped', 'Stopped', stackCounts.stopped)}
        {@render pill('unhealthy', 'Issues', stackCounts.unhealthy)}
      </div>
    </div>

    {#if loading && stackCards.length === 0}
      <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
        {#each Array(3) as _}
          <div class="dm-card p-4 space-y-2">
            <Skeleton width="60%" height="1rem" />
            <Skeleton width="40%" height="0.85rem" />
            <Skeleton width="100%" height="1.25rem" />
          </div>
        {/each}
      </div>
    {:else if filteredStacks.length === 0}
      <div class="dm-card p-8 text-center text-sm text-[var(--fg-muted)]">
        {#if stackCards.length === 0}
          No stacks yet. <a href="/stacks" class="text-[var(--color-brand-400)] hover:underline">Create one →</a>
        {:else}
          No stacks match this filter.
        {/if}
      </div>
    {:else}
      <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
        {#each filteredStacks as s (s.name)}
          <a
            href="/stacks/{s.name}"
            class="dm-card p-4 hover:border-[var(--border-strong)] transition-colors group"
          >
            <div class="flex items-start justify-between gap-2 mb-2.5">
              <div class="font-medium text-sm truncate">{s.name}</div>
              {#if s.state === 'running'}
                <Badge variant="success" dot>running</Badge>
              {:else if s.state === 'unhealthy'}
                <Badge variant="warning" dot>unhealthy</Badge>
              {:else if s.state === 'partial'}
                <Badge variant="warning" dot>partial</Badge>
              {:else}
                <Badge variant="default" dot>stopped</Badge>
              {/if}
            </div>
            {#if s.services.length > 0}
              <div class="flex flex-wrap gap-1 mb-2">
                {#each s.services.slice(0, 5) as svc}
                  <span
                    class="font-mono text-[10px] px-1.5 py-0.5 rounded bg-[var(--surface-hover)] text-[var(--fg-muted)]"
                  >
                    {svc.name}
                  </span>
                {/each}
                {#if s.services.length > 5}
                  <span class="font-mono text-[10px] px-1.5 py-0.5 text-[var(--fg-subtle)]">
                    +{s.services.length - 5}
                  </span>
                {/if}
              </div>
            {/if}
            <div class="flex items-center justify-between text-[11px] text-[var(--fg-subtle)]">
              <span>{s.services.length} service{s.services.length === 1 ? '' : 's'}</span>
              {#if isAll && s.hosts.length > 0}
                <!-- Host pills: where this stack's containers actually
                     run. Visible only in all-mode so single-host views
                     don't duplicate the header host name on every card. -->
                <div class="flex items-center gap-1 flex-wrap justify-end">
                  {#each s.hosts as h}
                    <span class="inline-flex items-center gap-0.5 font-mono text-[10px] px-1 py-0.5 rounded border border-[var(--border)] text-[var(--fg-muted)]">
                      <Server class="w-2.5 h-2.5" />
                      {h.name}
                    </span>
                  {/each}
                </div>
              {/if}
            </div>
          </a>
        {/each}
      </div>
      {#if hiddenStackCount > 0}
        <!-- Overflow pointer: "+N more" link to the real list page.
             Avoids the 3-column card wall that the dashboard becomes
             once an install crosses ~30 stacks. -->
        <div class="mt-3 text-center text-xs text-[var(--fg-muted)]">
          <a href="/stacks" class="hover:text-[var(--color-brand-400)] hover:underline">
            +{hiddenStackCount} more — view all on the Stacks page →
          </a>
        </div>
      {/if}
    {/if}
  </div>

  <!-- ─────────── Row 3: Activity + Quick actions ─────────── -->
  <div class="grid grid-cols-1 lg:grid-cols-3 gap-4">
    {#if allowed('audit.read')}
      <div class="dm-card lg:col-span-2 flex flex-col">
        <div class="px-4 py-3 border-b border-[var(--border)] flex items-center gap-2">
          <Activity class="w-4 h-4 text-[var(--fg-muted)]" />
          <h3 class="font-semibold text-sm">Recent activity</h3>
          <div class="flex-1"></div>
          <a
            href="/settings"
            class="text-xs text-[var(--fg-muted)] hover:text-[var(--fg)] transition-colors"
          >
            View all →
          </a>
        </div>
        <div class="divide-y divide-[var(--border)] flex-1">
          {#if loading && recentAudit.length === 0}
            {#each Array(6) as _}
              <div class="px-4 py-2.5 flex items-center gap-3">
                <Skeleton width="5rem" height="1rem" />
                <Skeleton width="8rem" height="0.85rem" />
                <div class="flex-1"></div>
                <Skeleton width="3rem" height="0.75rem" />
              </div>
            {/each}
          {:else if recentAudit.length === 0}
            <div class="px-4 py-10 text-center text-sm text-[var(--fg-muted)]">No activity yet</div>
          {:else}
            {#each recentAudit as e}
              <div class="px-4 py-2.5 flex items-center gap-3 text-sm hover:bg-[var(--surface-hover)] transition-colors">
                <Badge variant={actionColor(e.action)} dot>
                  {e.action.split('.')[0]}
                </Badge>
                <span class="text-[var(--fg-muted)] truncate flex-1">{formatActivity(e)}</span>
                <span class="text-[11px] text-[var(--fg-subtle)] shrink-0 tabular-nums">
                  {fmtTime(e.ts)}
                </span>
              </div>
            {/each}
          {/if}
        </div>
      </div>
    {/if}

    <div class="dm-card flex flex-col {allowed('audit.read') ? '' : 'lg:col-span-3'}">
      <div class="px-4 py-3 border-b border-[var(--border)]">
        <h3 class="font-semibold text-sm">Quick actions</h3>
      </div>
      <div class="p-2 grid grid-cols-1 {allowed('audit.read') ? '' : 'sm:grid-cols-2 lg:grid-cols-4'} gap-1">
        {#snippet quickAction(href: string, Icon: any, title: string, sub: string)}
          <a
            {href}
            class="flex items-center gap-3 px-3 py-2.5 rounded-lg hover:bg-[var(--surface-hover)] transition-colors group"
          >
            <div
              class="w-8 h-8 rounded-lg border border-[var(--border)] bg-[color-mix(in_srgb,var(--color-brand-500)_8%,transparent)] text-[var(--color-brand-400)] flex items-center justify-center shrink-0 group-hover:bg-[color-mix(in_srgb,var(--color-brand-500)_15%,transparent)] group-hover:border-[var(--color-brand-500)]/30 transition-colors"
            >
              <Icon class="w-4 h-4" />
            </div>
            <div class="min-w-0">
              <div class="text-sm font-medium">{title}</div>
              <div class="text-[11px] text-[var(--fg-subtle)]">{sub}</div>
            </div>
          </a>
        {/snippet}
        {#if canDeploy}
          {@render quickAction('/stacks', Rocket, 'Deploy stack', 'From compose file')}
        {/if}
        {#if canImage}
          {@render quickAction('/images', Download, 'Pull image', 'From registry')}
        {/if}
        {#if canBackup}
          {@render quickAction('/backups', Archive, 'Create backup', 'Volumes + stacks')}
        {/if}
        {#if canScan}
          {@render quickAction('/images', ShieldCheck, 'Scan image', 'CVE check via Grype')}
        {/if}
      </div>
    </div>
  </div>
</section>
