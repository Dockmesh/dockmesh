<script lang="ts">
  import { untrack } from 'svelte';
  import { api, isFanOut, type SystemMetrics, type ContainerSummary, type HostInfo } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import { hosts } from '$lib/stores/host.svelte';
  import { autoRefresh } from '$lib/autorefresh';
  import { Skeleton } from '$lib/components/ui';
  import {
    EdMetric,
    EdRow,
    Eyebrow,
    StatusPill,
  } from '$lib/components/editorial';
  import {
    RefreshCw,
    Server,
    HardDrive,
    Layers,
    AlertTriangle,
    ArrowRight,
  } from 'lucide-svelte';

  type PerHostMetrics = SystemMetrics & { host_id: string; host_name: string };

  type StackCard = {
    name: string;
    state: 'running' | 'stopped' | 'unhealthy' | 'partial';
    services: Array<{ name: string; state: string }>;
    hosts: Array<{ id: string; name: string }>;
  };

  let health = $state<{ status: string; version: string; docker: boolean } | null>(null);
  let sysMetrics = $state<SystemMetrics | null>(null);
  let perHostMetrics = $state<PerHostMetrics[]>([]);
  let containerStats = $state({ total: 0, running: 0, stopped: 0, unhealthy: 0 });
  let stackCards = $state<StackCard[]>([]);
  let recentAudit = $state<any[]>([]);
  let hostList = $state<HostInfo[]>([]);
  let agentCount = $state({ online: 0, total: 0 });
  let loading = $state(true);
  let error = $state('');
  let stackFilter = $state<'all' | 'running' | 'stopped' | 'unhealthy'>('all');

  // Tiny in-memory series for the metric sparklines.
  const HISTORY_LIMIT = 16;
  let cpuHistory = $state<number[]>([]);
  let memHistory = $state<number[]>([]);
  let diskHistory = $state<number[]>([]);

  const isAll = $derived(hosts.isAll);
  const isRemote = $derived(hosts.id !== 'local' && hosts.id !== 'all');

  async function load() {
    const isFirstLoad = untrack(
      () => !sysMetrics && perHostMetrics.length === 0 && stackCards.length === 0
    );
    if (isFirstLoad) loading = true;
    error = '';
    try {
      const [h, sysRaw, summary, stacksList, audit, hostListRaw] = await Promise.all([
        api.health(),
        api.system.metrics(hosts.id).catch(() => null),
        api.containers
          .summary(hosts.id)
          .catch(
            (): ContainerSummary => ({
              total: 0,
              running: 0,
              stopped: 0,
              unhealthy: 0,
              by_stack: {},
            })
          ),
        api.stacks.list().catch(() => []),
        allowed('audit.view') ? api.audit.list(10).catch(() => []) : Promise.resolve([]),
        api.hosts.list().catch(() => []),
      ]);
      health = h;

      if (sysRaw && isFanOut(sysRaw)) {
        perHostMetrics = sysRaw.items as PerHostMetrics[];
        sysMetrics = null;
      } else {
        perHostMetrics = [];
        sysMetrics = sysRaw as SystemMetrics | null;
      }

      if (sysMetrics) {
        cpuHistory = [...cpuHistory, sysMetrics.cpu_percent].slice(-HISTORY_LIMIT);
        if (sysMetrics.mem_total > 0) {
          memHistory = [...memHistory, sysMetrics.mem_percent].slice(-HISTORY_LIMIT);
        }
        if (sysMetrics.disk_total > 0) {
          diskHistory = [...diskHistory, sysMetrics.disk_percent].slice(-HISTORY_LIMIT);
        }
      }

      containerStats.total = summary.total;
      containerStats.running = summary.running;
      containerStats.stopped = summary.stopped;
      containerStats.unhealthy = summary.unhealthy;

      const hostName = new Map<string, string>(
        hostListRaw.map((h: any) => [h.id, h.name] as [string, string])
      );
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
          hosts: rollup.hosts.map((id) => ({
            id,
            name: hostName.get(id) ?? (id === 'local' ? 'Local' : id),
          })),
        };
      });

      recentAudit = audit;
      hostList = hostListRaw;
      agentCount.total = hostListRaw.length;
      agentCount.online = hostListRaw.filter((x: any) => x.status === 'online').length;
    } catch (err: any) {
      error = err.message ?? 'Failed to load';
    } finally {
      loading = false;
    }
  }

  let prevHost = hosts.id;
  $effect(() => {
    const cur = hosts.id;
    if (cur !== prevHost) {
      prevHost = cur;
      cpuHistory = [];
      memHistory = [];
      diskHistory = [];
      load();
    }
  });

  $effect(() => {
    load();
  });

  $effect(() => autoRefresh(load, 10_000));

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

  function barColor(pct: number): string {
    if (pct < 60) return 'var(--color-success-500)';
    if (pct < 85) return 'var(--color-warning-500)';
    return 'var(--color-danger-500)';
  }

  function sparkColor(pct: number): string {
    if (pct < 60) return 'var(--accent)';
    if (pct < 85) return 'var(--color-warning-500)';
    return 'var(--color-danger-500)';
  }

  function rowStatus(s: StackCard['state']): 'running' | 'degraded' | 'failing' | 'stopped' {
    if (s === 'running') return 'running';
    if (s === 'unhealthy') return 'failing';
    if (s === 'partial') return 'degraded';
    return 'stopped';
  }

  function pillStatus(
    s: StackCard['state']
  ): 'running' | 'degraded' | 'failing' | 'stopped' {
    return rowStatus(s);
  }

  function hostStatusPill(s: HostInfo['status']): 'ok' | 'pending' | 'failing' | 'stopped' {
    if (s === 'online') return 'ok';
    if (s === 'pending') return 'pending';
    if (s === 'offline') return 'failing';
    return 'stopped';
  }

  function formatActivity(e: any): string {
    const tgt = e.target ?? '';
    const short = tgt.length > 12 && /^[0-9a-f]/.test(tgt) ? tgt.slice(0, 12) : tgt;
    switch (e.action) {
      case 'auth.login': return 'signed in';
      case 'auth.logout': return 'signed out';
      case 'auth.login_failed': return `failed sign-in${tgt ? ' for ' + tgt : ''}`;
      case 'auth.sso_login': return 'signed in via SSO';
      case 'stack.create': return `created stack ${tgt}`;
      case 'stack.update': return `updated stack ${tgt}`;
      case 'stack.delete': return `deleted stack ${tgt}`;
      case 'stack.deploy': return `deployed stack ${tgt}`;
      case 'stack.stop': return `stopped stack ${tgt}`;
      case 'container.start': return `started container ${short}`;
      case 'container.stop': return `stopped container ${short}`;
      case 'container.restart': return `restarted container ${short}`;
      case 'container.remove': return `removed container ${short}`;
      case 'container.update': return `updated container ${short}`;
      case 'container.rollback': return `rolled back container ${short}`;
      case 'image.pull': return `pulled image ${tgt}`;
      case 'image.remove': return `removed image ${short}`;
      case 'image.prune': return 'pruned unused images';
      case 'image.scan': return `scanned image ${tgt}`;
      case 'network.create': return `created network ${tgt}`;
      case 'network.remove': return `removed network ${tgt}`;
      case 'volume.create': return `created volume ${tgt}`;
      case 'volume.remove': return `removed volume ${tgt}`;
      case 'volume.prune': return 'pruned unused volumes';
      case 'user.create': return `created user ${tgt}`;
      case 'user.delete': return `deleted user ${tgt}`;
      case 'user.update': return `updated user ${tgt}`;
      case 'user.password': return `changed password for ${tgt}`;
      default: return e.action + (tgt ? ` — ${short}` : '');
    }
  }

  function activityKind(action: string): 'ok' | 'warn' | 'err' {
    if (action.includes('delete') || action.includes('remove') || action.includes('failed')) {
      return 'err';
    }
    if (action.includes('stop') || action.includes('rollback')) return 'warn';
    return 'ok';
  }

  // Per-host rows for the Hosts section. In single-host mode this is one
  // row built from the local host + sysMetrics. In all-mode we merge the
  // hostList (for tags/kind/status) with perHostMetrics (for resource %).
  type HostRowData = {
    id: string;
    name: string;
    kind: HostInfo['kind'];
    status: HostInfo['status'];
    tags: string[];
    cpuPct: number;
    cpuLabel: string;
    memPct: number | null;
    memLabel: string;
    diskPct: number | null;
    diskLabel: string;
  };

  const hostRows = $derived.by<HostRowData[]>(() => {
    if (perHostMetrics.length > 0) {
      // All-hosts mode: one row per metrics fan-out item.
      return perHostMetrics.map((m): HostRowData => {
        const meta = hostList.find((h) => h.id === m.host_id);
        return {
          id: m.host_id,
          name: m.host_name,
          kind: meta?.kind ?? 'agent',
          status: meta?.status ?? 'online',
          tags: meta?.tags ?? [],
          cpuPct: m.cpu_percent,
          cpuLabel: `${m.cpu_used_cores.toFixed(2)} / ${m.cpu_cores.toFixed(2)} cores`,
          memPct: m.mem_total > 0 ? m.mem_percent : null,
          memLabel: m.mem_total > 0
            ? `${fmtBytes(m.mem_used)} / ${fmtBytes(m.mem_total)}`
            : 'unavailable',
          diskPct: m.disk_total > 0 ? m.disk_percent : null,
          diskLabel: m.disk_total > 0
            ? `${fmtBytes(m.disk_used)} / ${fmtBytes(m.disk_total)}`
            : 'unavailable',
        };
      });
    }
    // Single-host mode: only render the host the dashboard is currently
    // pointed at, with sysMetrics filling in the bars.
    if (!sysMetrics) return [];
    const selected = hostList.find((h) => h.id === hosts.id) ?? hostList[0];
    const id = selected?.id ?? 'local';
    const name = selected?.name ?? 'Local';
    return [
      {
        id,
        name,
        kind: selected?.kind ?? 'local',
        status: selected?.status ?? 'online',
        tags: selected?.tags ?? [],
        cpuPct: sysMetrics.cpu_percent,
        cpuLabel: `${sysMetrics.cpu_used_cores.toFixed(2)} / ${sysMetrics.cpu_cores.toFixed(2)} cores`,
        memPct: sysMetrics.mem_total > 0 ? sysMetrics.mem_percent : null,
        memLabel: sysMetrics.mem_total > 0
          ? `${fmtBytes(sysMetrics.mem_used)} / ${fmtBytes(sysMetrics.mem_total)}`
          : 'unavailable',
        diskPct: sysMetrics.disk_total > 0 ? sysMetrics.disk_percent : null,
        diskLabel: sysMetrics.disk_total > 0
          ? `${fmtBytes(sysMetrics.disk_used)} / ${fmtBytes(sysMetrics.disk_total)}`
          : 'unavailable',
      },
    ];
  });

  const statusLine = $derived.by(() => {
    if (health && !health.docker) {
      return 'Docker is unreachable — dashboard is read-only until it returns.';
    }
    const issues = stackCards.filter(
      (s) => s.state === 'unhealthy' || s.state === 'partial'
    ).length;
    const stopped = stackCards.filter((s) => s.state === 'stopped').length;
    if (issues > 0) return `${issues} stack${issues === 1 ? ' needs' : 's need'} attention`;
    if (stopped > 0 && stackCards.length > 0) return `All running · ${stopped} idle`;
    if (stackCards.length === 0) return 'No stacks yet — deploy your first compose file to begin';
    return 'Everything running';
  });

  const subtitle = $derived.by(() => {
    const parts: string[] = [];
    if (containerStats.running) {
      parts.push(`${containerStats.running} of ${containerStats.total} containers running`);
    } else if (containerStats.total) {
      parts.push(`${containerStats.total} containers stopped`);
    }
    if (agentCount.total > 1) {
      parts.push(`${agentCount.online} of ${agentCount.total} hosts online`);
    } else if (health?.docker) {
      parts.push('Docker connected');
    }
    if (health?.version) {
      parts.push(`dockmesh ${health.version}`);
    }
    return parts.join(' · ');
  });

  const STACK_PREVIEW_LIMIT = 8;
  const sortedStacks = $derived(
    [...stackCards].sort((a, b) => a.name.localeCompare(b.name))
  );
  const filteredStacks = $derived(
    (stackFilter === 'all'
      ? sortedStacks
      : sortedStacks.filter((s) => s.state === stackFilter)
    ).slice(0, STACK_PREVIEW_LIMIT)
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
    unhealthy: stackCards.filter((s) => s.state === 'unhealthy' || s.state === 'partial')
      .length,
  });

  const canSeeAudit = $derived(allowed('audit.view'));
</script>

<section class="dashboard-frame">
  <!-- ────────────────────────────────────────────── Header -->
  <header class="dash-header">
    <div class="dash-header-text">
      <h1 class="ed-title dash-title">Overview</h1>
      <p class="ed-subtitle dash-subtitle">
        {statusLine}{#if subtitle} · {subtitle}{/if}{#if isRemote} · viewing {hosts.selected?.name}{/if}
      </p>
    </div>
    <div class="ed-actions">
      <button
        type="button"
        class="dm-btn dm-btn-ghost dm-btn-sm"
        onclick={load}
        disabled={loading}
        aria-label="Refresh"
        title="Refresh"
      >
        <RefreshCw size={13} strokeWidth={1.5} class={loading ? 'animate-spin' : ''} />
        Re-poll
      </button>
    </div>
  </header>

  {#if error}
    <p class="dash-error" role="alert">{error}</p>
  {/if}

  {#if health && !health.docker}
    <div class="dash-warn">
      <AlertTriangle size={14} strokeWidth={1.5} class="dash-warn-icon" />
      <div>
        <div class="dash-warn-title">Docker daemon not responding.</div>
        <p class="dash-warn-body">
          Container, stack, image and volume endpoints will return errors until Docker is available.
          Dockmesh re-checks the socket every 10 seconds — the banner clears automatically as soon
          as Docker comes back. No restart needed.
        </p>
      </div>
    </div>
  {/if}

  <!-- ────────────────────────────────────────────── 5-up metrics -->
  <section class="dash-metrics">
    {#if loading && !sysMetrics && perHostMetrics.length === 0}
      {#each Array(5) as _}
        <div class="ed-metric">
          <Skeleton width="6rem" height="0.6rem" />
          <Skeleton width="4rem" height="1.4rem" />
          <Skeleton width="100%" height="2rem" />
        </div>
      {/each}
    {:else if isAll}
      <EdMetric
        label="Containers · fleet"
        value={`${containerStats.running} / ${containerStats.total}`}
        meta={`${containerStats.stopped} stopped${containerStats.unhealthy > 0 ? ` · ${containerStats.unhealthy} unhealthy` : ''}`}
      />
      <EdMetric
        label="Hosts · online"
        value={`${agentCount.online} / ${agentCount.total}`}
        meta={agentCount.total - agentCount.online > 0
          ? `${agentCount.total - agentCount.online} offline`
          : 'all reachable'}
      />
      <EdMetric
        label="Stacks · running"
        value={`${stackCounts.running} / ${stackCounts.all}`}
        meta={stackCounts.unhealthy > 0
          ? `${stackCounts.unhealthy} need attention`
          : 'all healthy'}
        sparkColor={stackCounts.unhealthy > 0 ? 'var(--color-warning-500)' : 'var(--accent)'}
      />
      <EdMetric
        label="Fleet · CPU avg"
        value={perHostMetrics.length > 0
          ? (perHostMetrics.reduce((a, m) => a + m.cpu_percent, 0) / perHostMetrics.length).toFixed(0)
          : '—'}
        unit={perHostMetrics.length > 0 ? '%' : undefined}
        meta={`across ${perHostMetrics.length} host${perHostMetrics.length === 1 ? '' : 's'}`}
      />
      <EdMetric
        label="Activity · last 10"
        value={recentAudit.length}
        meta={recentAudit.length > 0 ? `latest ${fmtTime(recentAudit[0].ts)}` : 'nothing yet'}
      />
    {:else if sysMetrics}
      <EdMetric
        label={sysMetrics.docker_limited ? 'CPU · docker cap' : 'CPU'}
        value={sysMetrics.cpu_percent.toFixed(0)}
        unit="%"
        meta={`${sysMetrics.cpu_used_cores.toFixed(2)} / ${sysMetrics.cpu_cores.toFixed(2)} cores`}
        spark={cpuHistory.length > 1 ? cpuHistory : undefined}
        sparkColor={sparkColor(sysMetrics.cpu_percent)}
      />
      <EdMetric
        label={sysMetrics.docker_limited ? 'Memory · docker cap' : 'Memory'}
        value={sysMetrics.mem_total > 0 ? sysMetrics.mem_percent.toFixed(0) : '—'}
        unit={sysMetrics.mem_total > 0 ? '%' : undefined}
        meta={sysMetrics.mem_total > 0
          ? `${fmtBytes(sysMetrics.mem_used)} / ${fmtBytes(sysMetrics.mem_total)}`
          : 'unavailable on this host'}
        spark={sysMetrics.mem_total > 0 && memHistory.length > 1 ? memHistory : undefined}
        sparkColor={sparkColor(sysMetrics.mem_percent)}
      />
      <EdMetric
        label="Disk"
        value={sysMetrics.disk_total > 0 ? sysMetrics.disk_percent.toFixed(0) : '—'}
        unit={sysMetrics.disk_total > 0 ? '%' : undefined}
        meta={sysMetrics.disk_total > 0
          ? `${fmtBytes(sysMetrics.disk_used)} / ${fmtBytes(sysMetrics.disk_total)}`
          : 'unavailable on this host'}
        spark={sysMetrics.disk_total > 0 && diskHistory.length > 1 ? diskHistory : undefined}
        sparkColor={sparkColor(sysMetrics.disk_percent)}
      />
      <EdMetric
        label="Containers"
        value={`${containerStats.running} / ${containerStats.total}`}
        meta={`${containerStats.stopped} stopped${containerStats.unhealthy > 0 ? ` · ${containerStats.unhealthy} unhealthy` : ''}`}
        sparkColor={containerStats.unhealthy > 0
          ? 'var(--color-warning-500)'
          : 'var(--accent)'}
      />
      <EdMetric
        label="Stacks"
        value={`${stackCounts.running} / ${stackCounts.all}`}
        meta={stackCounts.unhealthy > 0
          ? `${stackCounts.unhealthy} need attention`
          : stackCounts.stopped > 0
            ? `${stackCounts.stopped} stopped`
            : 'all healthy'}
        sparkColor={stackCounts.unhealthy > 0 ? 'var(--color-warning-500)' : 'var(--accent)'}
      />
    {/if}
  </section>

  <!-- ────────────────────────────────────────────── Top row: Stacks | Activity
       Stacks list takes the wider column; Activity feed stretches to the
       same height as Stacks (matched via grid-row) so the right rail
       doesn't run past the section beneath. -->
  <section class="dash-top">
    <!-- Stacks block (left, primary) -->
    <div class="dash-block">
      <div class="dash-block-head">
        <Eyebrow>Stacks · {stackCards.length}</Eyebrow>
        <div class="ed-tabs dash-stack-tabs">
          <button
            class="ed-tab"
            class:active={stackFilter === 'all'}
            onclick={() => (stackFilter = 'all')}
          >All <span class="count">{stackCounts.all}</span></button>
          <button
            class="ed-tab"
            class:active={stackFilter === 'running'}
            onclick={() => (stackFilter = 'running')}
          >Running <span class="count">{stackCounts.running}</span></button>
          <button
            class="ed-tab"
            class:active={stackFilter === 'unhealthy'}
            onclick={() => (stackFilter = 'unhealthy')}
          >Issues <span class="count">{stackCounts.unhealthy}</span></button>
          <button
            class="ed-tab"
            class:active={stackFilter === 'stopped'}
            onclick={() => (stackFilter = 'stopped')}
          >Stopped <span class="count">{stackCounts.stopped}</span></button>
        </div>
      </div>

      {#if loading && stackCards.length === 0}
        <div class="dm-card dash-skeleton-stack">
          {#each Array(4) as _}
            <Skeleton width="100%" height="2.5rem" />
          {/each}
        </div>
      {:else if filteredStacks.length === 0}
        <div class="dm-card dash-stack-empty">
          {#if stackCards.length === 0}
            <div class="dash-empty-title">No stacks yet.</div>
            <p class="dash-empty-body">
              Deploy a <em class="ed-accent">compose</em> file to get started — Dockmesh
              manages the containers, network, and volumes from the file on disk.
            </p>
            <a href="/stacks" class="dm-btn dm-btn-primary dm-btn-sm">
              Create stack
              <ArrowRight size={13} strokeWidth={1.5} />
            </a>
          {:else}
            <p class="dash-empty-body">No stacks match this filter.</p>
          {/if}
        </div>
      {:else}
        <div class="dm-card dash-stack-list">
          {#each filteredStacks as s (s.name)}
            <EdRow
              status={rowStatus(s.state)}
              href={`/stacks/${s.name}`}
              columns="6px minmax(0, 1.6fr) 1.4fr 0.8fr auto"
            >
              <span class="dash-stack-name">
                <span class="dash-stack-id">{s.name}</span>
                <span class="dash-stack-meta">
                  {s.services.length} service{s.services.length === 1 ? '' : 's'}
                </span>
              </span>
              <span class="dash-stack-services">
                {#if s.services.length > 0}
                  {#each s.services.slice(0, 4) as svc (svc.name)}
                    <span class="dash-stack-service">{svc.name}</span>
                  {/each}
                  {#if s.services.length > 4}
                    <span class="dash-stack-service-more">+{s.services.length - 4}</span>
                  {/if}
                {:else}
                  <span class="dash-stack-service-empty">no containers</span>
                {/if}
              </span>
              <StatusPill status={pillStatus(s.state)} />
              <span class="dash-stack-arrow" aria-hidden="true">
                <ArrowRight size={13} strokeWidth={1.5} />
              </span>
            </EdRow>
          {/each}
        </div>

        {#if hiddenStackCount > 0}
          <div class="dash-overflow">
            <a href="/stacks">
              +{hiddenStackCount} more — view all on the Stacks page →
            </a>
          </div>
        {/if}
      {/if}
    </div>

    <!-- Activity feed (right rail). Capped at the height of the Stacks
         block via a max-height + scroll fallback so it never runs past
         the section beneath (Hosts gets its own full-width strip). -->
    {#if canSeeAudit}
      <div class="dash-block dash-side">
        <div class="dash-block-head">
          <Eyebrow>Activity · last {recentAudit.length || 10}</Eyebrow>
          <a href="/audit" class="dash-block-more">
            Full log
            <ArrowRight size={11} strokeWidth={1.5} />
          </a>
        </div>
        <div class="dm-card dash-feed-card">
          {#if loading && recentAudit.length === 0}
            <div class="dash-feed-empty"><Skeleton width="100%" height="2rem" /></div>
          {:else if recentAudit.length === 0}
            <div class="dash-feed-empty">No activity yet.</div>
          {:else}
            {#each recentAudit as e}
              <div class="ed-feed-item">
                <span class="ed-feed-time">{fmtTime(e.ts)}</span>
                <span class="ed-feed-text">
                  <span
                    class="dash-feed-dot"
                    data-kind={activityKind(e.action)}
                    aria-hidden="true"
                  ></span>
                  {#if e.actor_name}
                    <strong>{e.actor_name}</strong>
                  {/if}
                  {formatActivity(e)}
                </span>
                <span class="ed-feed-actor">
                  {e.action.split('.')[0]}
                </span>
              </div>
            {/each}
          {/if}
        </div>
      </div>
    {/if}
  </section>

  <!-- ────────────────────────────────────────────── Hosts (full-width)
       Transparent surface with hairline-only row dividers — matches the
       mockup HostRow component. The section gets the whole content
       column so per-host bars have generous space. -->
  <section class="dash-block">
    <div class="dash-block-head">
      <Eyebrow>
        Hosts · {hostRows.length} {agentCount.total > 1 ? 'connected' : ''}
      </Eyebrow>
      {#if agentCount.total > 1}
        <a href="/hosts" class="dash-block-more">
          Manage hosts
          <ArrowRight size={11} strokeWidth={1.5} />
        </a>
      {/if}
    </div>

    {#if loading && hostRows.length === 0}
      <div class="dash-host-list dash-host-empty">
        <Skeleton width="100%" height="3rem" />
      </div>
    {:else if hostRows.length === 0}
      <div class="dash-host-list dash-host-empty">
        No hosts reporting metrics.
      </div>
    {:else}
      <div class="dash-host-list">
        {#each hostRows as h (h.id)}
          <div class="dash-host-row">
            <div class="dash-host-id">
              {#if h.kind === 'local'}
                <HardDrive size={14} strokeWidth={1.5} class="dash-host-id-ico" />
              {:else if h.kind === 'agent'}
                <Server size={14} strokeWidth={1.5} class="dash-host-id-ico" />
              {:else}
                <Layers size={14} strokeWidth={1.5} class="dash-host-id-ico" />
              {/if}
              <div class="dash-host-id-block">
                <span class="dash-host-name">{h.name}</span>
                <span class="dash-host-meta">
                  {h.kind}
                  {#if h.tags.length > 0}
                    <span class="dash-host-sep">·</span>
                    {#each h.tags.slice(0, 4) as t (t)}
                      <span class="dash-host-tag">{t}</span>
                    {/each}
                    {#if h.tags.length > 4}
                      <span class="dash-host-tag-more">+{h.tags.length - 4}</span>
                    {/if}
                  {/if}
                </span>
              </div>
            </div>

            <div class="dash-host-bar">
              <div class="dash-host-bar-head">
                <span class="dash-host-bar-label">CPU</span>
                <span class="dash-host-bar-value">{h.cpuPct.toFixed(0)}%</span>
              </div>
              <span class="dm-bar">
                <span
                  class="dm-bar-fill"
                  style:width="{h.cpuPct}%"
                  style:background={barColor(h.cpuPct)}
                ></span>
              </span>
              <span class="dash-host-bar-meta">{h.cpuLabel}</span>
            </div>

            <div class="dash-host-bar">
              <div class="dash-host-bar-head">
                <span class="dash-host-bar-label">Mem</span>
                <span class="dash-host-bar-value">
                  {h.memPct === null ? '—' : `${h.memPct.toFixed(0)}%`}
                </span>
              </div>
              <span class="dm-bar">
                {#if h.memPct !== null}
                  <span
                    class="dm-bar-fill"
                    style:width="{h.memPct}%"
                    style:background={barColor(h.memPct)}
                  ></span>
                {/if}
              </span>
              <span class="dash-host-bar-meta">{h.memLabel}</span>
            </div>

            <div class="dash-host-bar">
              <div class="dash-host-bar-head">
                <span class="dash-host-bar-label">Disk</span>
                <span class="dash-host-bar-value">
                  {h.diskPct === null ? '—' : `${h.diskPct.toFixed(0)}%`}
                </span>
              </div>
              <span class="dm-bar">
                {#if h.diskPct !== null}
                  <span
                    class="dm-bar-fill"
                    style:width="{h.diskPct}%"
                    style:background={barColor(h.diskPct)}
                  ></span>
                {/if}
              </span>
              <span class="dash-host-bar-meta">{h.diskLabel}</span>
            </div>

            <StatusPill status={hostStatusPill(h.status)} />
          </div>
        {/each}
      </div>
    {/if}
  </section>
</section>

<style>
  .dashboard-frame {
    display: flex;
    flex-direction: column;
    gap: 36px;
    max-width: 1480px;
    padding-bottom: 48px;
  }

  /* ─────── Header ────── */
  .dash-header {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
    gap: 24px;
    flex-wrap: wrap;
  }
  .dash-header-text { min-width: 0; max-width: 80ch; flex: 1 1 60ch; }
  .dash-title {
    font-size: 40px;
    line-height: 1.1;
    letter-spacing: -0.02em;
    max-width: 38ch;
    text-wrap: balance;
    margin-top: 14px;
  }
  @media (max-width: 900px) {
    .dash-title { font-size: 32px; }
  }
  .dash-subtitle { margin-top: 14px; font-size: 14.5px; }

  /* ─────── Banners ────── */
  .dash-error {
    margin: 0;
    padding: 10px 14px;
    border: 1px solid color-mix(in srgb, var(--color-danger-500) 40%, var(--border));
    background: color-mix(in srgb, var(--color-danger-500) 8%, transparent);
    border-radius: 5px;
    color: var(--color-danger-400);
    font-size: 13px;
    line-height: 1.5;
  }
  .dash-warn {
    display: grid;
    grid-template-columns: auto 1fr;
    gap: 14px;
    padding: 14px 16px;
    border: 1px solid color-mix(in srgb, var(--color-warning-500) 40%, var(--border));
    background: color-mix(in srgb, var(--color-warning-500) 6%, transparent);
    border-radius: 6px;
  }
  :global(.dash-warn-icon) { color: var(--color-warning-400); margin-top: 4px; flex-shrink: 0; }
  .dash-warn-title { font-size: 13.5px; color: var(--fg); font-weight: 500; }
  .dash-warn-body { margin: 6px 0 0; font-size: 12.5px; color: var(--fg-muted); line-height: 1.6; }

  /* ─────── Metrics row ────── */
  .dash-metrics {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
    gap: 12px;
  }

  /* ─────── Top row: Stacks | Activity ──────
     Two-column grid with subgrid-aligned rows. The parent defines a
     head-row + content-row, and each .dash-block uses
     `grid-template-rows: subgrid` to inherit those rowlines. That pins
     both heads to the same baseline AND both cards to the same top
     edge — so a taller Stacks head (tabs) doesn't push its card below
     the Activity card on the right.
     Falls back gracefully on older browsers without subgrid: the cards
     simply line up by content height (the previous behaviour). */
  .dash-top {
    display: grid;
    grid-template-columns: minmax(0, 1.65fr) minmax(280px, 1fr);
    grid-template-rows: auto 1fr;
    align-items: stretch;
    column-gap: 32px;
    row-gap: 12px;
  }
  .dash-top > .dash-block {
    display: grid;
    grid-template-rows: subgrid;
    grid-row: 1 / span 2;
    /* Reset inner flex gap — the parent's row-gap now provides the
       12px spacing between head and card. */
    gap: 0;
  }
  @media (max-width: 1100px) {
    .dash-top {
      grid-template-columns: 1fr;
      grid-template-rows: auto;
      row-gap: 28px;
    }
    .dash-top > .dash-block {
      display: flex;
      flex-direction: column;
      grid-row: auto;
      gap: 12px;
    }
  }
  /* Right rail: clip + scroll inside so feed never overruns the Stacks
     column on the left. */
  .dash-side {
    min-width: 0;
    min-height: 0;
  }
  .dash-side .dash-feed-card {
    flex: 1;
    min-height: 0;
    overflow-y: auto;
  }

  .dash-block {
    display: flex;
    flex-direction: column;
    gap: 12px;
    min-width: 0;
  }
  .dash-block-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 16px;
    flex-wrap: wrap;
  }
  .dash-block-more {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--accent-fg);
    letter-spacing: 0.04em;
    text-decoration: none;
  }
  .dash-block-more:hover { color: var(--fg); }
  .dash-stack-tabs {
    border-bottom: 0;
    flex-wrap: wrap;
  }

  /* ── Stacks ── */
  .dash-skeleton-stack {
    display: flex;
    flex-direction: column;
    gap: 8px;
    padding: 16px;
  }
  .dash-stack-empty {
    padding: 28px 26px;
    display: flex;
    flex-direction: column;
    gap: 12px;
    align-items: flex-start;
  }
  .dash-empty-title {
    font-size: 14px;
    color: var(--fg);
    font-weight: 500;
  }
  .dash-empty-body {
    margin: 0;
    color: var(--fg-muted);
    font-size: 13px;
    line-height: 1.6;
    max-width: 60ch;
  }
  .dash-stack-list { overflow: hidden; }

  .dash-stack-name { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
  .dash-stack-id {
    font-size: 13.5px;
    color: var(--fg);
    font-weight: 500;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .dash-stack-meta {
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
    letter-spacing: 0.04em;
  }
  .dash-stack-services {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    align-items: center;
    min-width: 0;
  }
  .dash-stack-service {
    font-family: var(--font-mono);
    font-size: 10.5px;
    padding: 1px 6px;
    border-radius: 3px;
    background: var(--surface-hover);
    color: var(--fg-muted);
  }
  .dash-stack-service-more {
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
  }
  .dash-stack-service-empty {
    font-family: var(--font-mono);
    font-size: 11px;
    color: var(--fg-subtle);
    font-style: normal;
  }
  .dash-stack-arrow { color: var(--fg-subtle); display: inline-flex; }

  .dash-overflow {
    margin-top: 4px;
    text-align: center;
    font-size: 12px;
    color: var(--fg-muted);
  }
  .dash-overflow a { color: inherit; text-decoration: none; }
  .dash-overflow a:hover { color: var(--accent-fg); }

  /* ── Hosts — transparent surface, hairline-only row dividers
        (no card chrome). Top + bottom rules pin the list visually
        without giving it a "tile" appearance. ── */
  .dash-host-list {
    border-top: 1px solid var(--border);
  }
  .dash-host-empty {
    padding: 22px 24px;
    color: var(--fg-muted);
    text-align: center;
    font-size: 13px;
  }
  .dash-host-row {
    display: grid;
    grid-template-columns: minmax(0, 1.4fr) repeat(3, minmax(120px, 1fr)) auto;
    gap: 24px;
    padding: 20px 4px;
    align-items: center;
    border-bottom: 1px solid var(--border-subtle);
  }
  .dash-host-row:last-child { border-bottom: 0; }
  @media (max-width: 1280px) {
    .dash-host-row {
      grid-template-columns: 1fr 1fr;
      grid-row-gap: 14px;
    }
    .dash-host-row > :first-child { grid-column: 1 / -1; }
  }
  .dash-host-id {
    display: flex;
    align-items: center;
    gap: 10px;
    min-width: 0;
  }
  :global(.dash-host-id-ico) { color: var(--color-brand-400); flex-shrink: 0; }
  .dash-host-id-block {
    display: flex;
    flex-direction: column;
    gap: 4px;
    min-width: 0;
  }
  .dash-host-name {
    font-family: var(--font-mono);
    font-size: 13px;
    color: var(--fg);
    font-weight: 500;
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .dash-host-meta {
    display: inline-flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 6px;
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
    letter-spacing: 0.04em;
  }
  .dash-host-sep { color: var(--border-strong); }
  .dash-host-tag {
    border: 1px solid var(--border);
    padding: 0 6px;
    border-radius: 3px;
    color: var(--fg-muted);
  }
  .dash-host-tag-more { color: var(--fg-subtle); }

  .dash-host-bar { display: flex; flex-direction: column; gap: 5px; min-width: 0; }
  .dash-host-bar-head {
    display: flex;
    justify-content: space-between;
    align-items: baseline;
  }
  .dash-host-bar-label {
    font-family: var(--font-mono);
    font-size: 10px;
    letter-spacing: 0.14em;
    text-transform: uppercase;
    color: var(--fg-subtle);
  }
  .dash-host-bar-value {
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: var(--fg);
    font-variant-numeric: tabular-nums;
  }
  .dash-host-bar-meta {
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--fg-subtle);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }

  /* ── Activity feed ── */
  .dash-feed-card { padding: 4px 16px; }
  .dash-feed-empty {
    padding: 26px;
    text-align: center;
    color: var(--fg-muted);
    font-size: 12.5px;
  }
  .dash-feed-dot {
    display: inline-block;
    width: 5px;
    height: 5px;
    border-radius: 999px;
    margin-right: 8px;
    background: var(--color-success-500);
    transform: translateY(-1px);
  }
  .dash-feed-dot[data-kind='warn'] { background: var(--color-warning-500); }
  .dash-feed-dot[data-kind='err']  { background: var(--color-danger-500); }

  :global(.animate-spin) { animation: dash-spin 0.9s linear infinite; }
  @keyframes dash-spin { to { transform: rotate(360deg); } }
</style>
