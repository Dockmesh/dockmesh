<script lang="ts">
  // Host Detail — editorial rebuild based on `Dockmesh Wizard (6)/
  // host-detail.jsx`. Header strip + 6 tabs (Overview / Containers /
  // Stacks / Migrations / Tags / Maintenance) + drain modal preview.
  // Local-host edge case: id === 'local' uses system.info instead of
  // agents.get; many actions (drain/upgrade/revoke) are disabled.
  // See project_hosts_open_punch_list.md for deferred features
  // (per-container metrics, host-affinity panel).
  import { onMount, onDestroy } from 'svelte';
  import { page } from '$app/stores';
  import { goto } from '$app/navigation';
  import {
    api, ApiError,
    type Agent, type SystemMetrics, type Migration, type StackListEntry,
    type DrainPlan
  } from '$lib/api';
  import { allowed } from '$lib/rbac.svelte';
  import { Skeleton, EmptyState } from '$lib/components/ui';
  import { Eyebrow } from '$lib/components/editorial';
  import { toast } from '$lib/stores/toast.svelte';
  import { confirm } from '$lib/stores/confirm.svelte';
  import { pageContext } from '$lib/stores/pageContext.svelte';
  import {
    ChevronLeft, RefreshCw, ArrowDownToLine, Trash2, Terminal,
    X, Check, AlertTriangle, Info
  } from 'lucide-svelte';

  const id = $derived($page.params.id);
  const isLocal = $derived(id === 'local');
  const tabFromQuery = $derived(($page.url.searchParams.get('tab') ?? 'overview') as TabId);

  type TabId = 'overview' | 'containers' | 'stacks' | 'migrations' | 'tags' | 'maintenance';
  let tab = $state<TabId>('overview');
  $effect(() => {
    const t = tabFromQuery;
    if (['overview', 'containers', 'stacks', 'migrations', 'tags', 'maintenance'].includes(t)) {
      tab = t;
    }
  });

  // Host data
  let agent = $state<Agent | null>(null);
  let localName = $state('local');
  let metrics = $state<SystemMetrics | null>(null);
  let metricsHistory = $state<SystemMetrics[]>([]);
  let containers = $state<any[]>([]);
  let stacks = $state<StackListEntry[]>([]);
  let migrations = $state<Migration[]>([]);
  let tags = $state<string[]>([]);
  let allFleetTags = $state<string[]>([]);
  let upgradePolicyVersion = $state<string>('');
  let loading = $state(true);
  let loadError = $state<string | null>(null);

  // Tags tab state
  let tagsDraft = $state<string[]>([]);
  let tagInput = $state('');
  let tagsBusy = $state(false);

  // Drain modal
  let showDrain = $state(false);
  let drainPlan = $state<DrainPlan | null>(null);
  let drainLoading = $state(false);
  let drainBusy = $state(false);

  // Upgrade
  let upgradeBusy = $state(false);
  let revokeBusy = $state(false);

  async function loadAll() {
    loading = true;
    loadError = null;
    try {
      if (isLocal) {
        const [info, m, ctrRes, stk, hostList, polRes] = await Promise.all([
          api.system.info().catch(() => null),
          api.system.metrics('local').catch(() => null),
          api.containers.list(false, 'local').catch(() => []),
          api.stacks.list().catch(() => []),
          api.hosts.list().catch(() => []),
          api.agents.getUpgradePolicy().catch(() => null)
        ]);
        const local = hostList.find((h) => h.kind === 'local');
        localName = local?.name ?? 'local';
        agent = null;
        if (m && !('hosts' in m)) metrics = m;
        containers = Array.isArray(ctrRes) ? ctrRes : [];
        stacks = stk;
        upgradePolicyVersion = polRes?.server_version ?? '';
      } else {
        const [a, m, ctrRes, stk, polRes] = await Promise.all([
          api.agents.get(id).catch((e) => { loadError = e instanceof ApiError ? e.message : 'Load failed'; return null; }),
          api.system.metrics(id).catch(() => null),
          api.containers.list(false, id).catch(() => []),
          api.stacks.list().catch(() => []),
          api.agents.getUpgradePolicy().catch(() => null)
        ]);
        agent = a;
        if (m && !('hosts' in m)) metrics = m;
        containers = Array.isArray(ctrRes) ? ctrRes : [];
        stacks = stk;
        upgradePolicyVersion = polRes?.server_version ?? '';
      }
      // Tags + fleet tags + migrations regardless of local/remote.
      const [t, allT, migRes] = await Promise.all([
        api.hosts.listTags(id).catch(() => []),
        api.hosts.allTags().catch(() => []),
        api.migrations.list(50).catch(() => [])
      ]);
      tags = t;
      tagsDraft = [...t];
      allFleetTags = allT;
      migrations = migRes.filter((m) => m.source_host_id === id || m.target_host_id === id);
      // Track metric history for sparklines (rolling 16 samples).
      if (metrics) {
        metricsHistory = [...metricsHistory.slice(-15), metrics];
      }
    } catch (err) {
      loadError = err instanceof ApiError ? err.message : 'Load failed';
    } finally {
      loading = false;
    }
  }

  let refreshTimer: ReturnType<typeof setInterval> | null = null;
  onMount(() => {
    loadAll();
    refreshTimer = setInterval(() => {
      if (document.visibilityState === 'visible') loadAll();
    }, 10_000);
  });
  onDestroy(() => { if (refreshTimer) clearInterval(refreshTimer); });

  // ─── Stacks deployed on this host ─────────────────────────────────────
  // We don't have a "deployed-on" field on StackListEntry — Deployments
  // store maps stack→host. Filter by name matching containers' compose
  // labels as a proxy. For local, every stack is "deployed here".
  const stacksOnHost = $derived.by<StackListEntry[]>(() => {
    if (isLocal) return stacks; // simple — local hosts everything by default
    // Use container labels to find which stacks are running on this host.
    const projects = new Set<string>();
    for (const c of containers) {
      const proj = c?.Labels?.['com.docker.compose.project'];
      if (proj) projects.add(proj);
    }
    return stacks.filter((s) => projects.has(s.name));
  });

  // ─── Helpers ──────────────────────────────────────────────────────────
  function fmtAgo(iso?: string): string {
    if (!iso) return 'never';
    const d = (Date.now() - new Date(iso).getTime()) / 1000;
    if (d < 60) return `${Math.round(d)}s ago`;
    if (d < 3600) return `${Math.round(d / 60)}m ago`;
    if (d < 86400) return `${Math.round(d / 3600)}h ago`;
    return `${Math.round(d / 86400)}d ago`;
  }
  function fmtUptime(iso?: string): string {
    if (!iso) return '—';
    const d = (Date.now() - new Date(iso).getTime()) / 1000;
    if (d < 60) return `${Math.round(d)}s`;
    if (d < 3600) return `${Math.round(d / 60)}m`;
    if (d < 86400) return `${Math.round(d / 3600)}h`;
    return `${Math.round(d / 86400)}d`;
  }
  function fmtDate(iso?: string): string {
    if (!iso) return '—';
    return iso.slice(0, 10);
  }
  function statusDot(s: string): string {
    if (s === 'online') return 'ok-dot';
    if (s === 'offline') return 'fail-dot';
    if (s === 'pending') return 'warn-dot';
    return 'neutral-dot';
  }
  function gb(bytes: number): string {
    return (bytes / 1024 / 1024 / 1024).toFixed(1);
  }
  function selectTab(t: TabId) {
    tab = t;
    const u = new URL(window.location.href);
    u.searchParams.set('tab', t);
    window.history.replaceState({}, '', u.toString());
  }

  // Header derived
  const status = $derived(isLocal ? 'online' : (agent?.status ?? 'offline'));
  const hostname = $derived(isLocal ? 'localhost' : (agent?.hostname ?? ''));
  const hostKind = $derived(isLocal ? 'local' : 'agent');
  const hostName = $derived(isLocal ? localName : (agent?.name ?? id));
  const hostVersion = $derived(isLocal ? upgradePolicyVersion : (agent?.version ?? ''));

  $effect(() => {
    pageContext.set(hostName);
    return () => pageContext.clear();
  });
  const needsUpgrade = $derived(
    !isLocal && !!agent?.version && !!upgradePolicyVersion && agent.version !== upgradePolicyVersion
  );

  // ─── Tag actions ──────────────────────────────────────────────────────
  function addTagDraft(t: string) {
    const v = t.trim().toLowerCase();
    if (!v || tagsDraft.includes(v)) { tagInput = ''; return; }
    if (!/^[a-z0-9][a-z0-9-]{0,31}$/.test(v)) { toast.error('Invalid tag', 'lowercase letters, digits, hyphens, 1-32 chars'); return; }
    if (tagsDraft.length >= 20) { toast.error('Max 20 tags'); return; }
    tagsDraft = [...tagsDraft, v];
    tagInput = '';
  }
  async function saveTags() {
    tagsBusy = true;
    try {
      tags = await api.hosts.setTags(id, tagsDraft);
      tagsDraft = [...tags];
      toast.success('Tags saved');
    } catch (err) {
      toast.error('Save failed', err instanceof ApiError ? err.message : undefined);
    } finally { tagsBusy = false; }
  }
  function revertTags() { tagsDraft = [...tags]; }

  // ─── Maintenance actions ──────────────────────────────────────────────
  async function openDrain() {
    showDrain = true;
    drainPlan = null;
    drainLoading = true;
    try { drainPlan = await api.drains.plan(id); }
    catch (err) { toast.error('Plan failed', err instanceof ApiError ? err.message : undefined); }
    finally { drainLoading = false; }
  }
  async function executeDrain() {
    drainBusy = true;
    try {
      const d = await api.drains.execute(id);
      toast.success('Drain started', `${d.plan.length} stack(s) queued`);
      showDrain = false;
    } catch (err) {
      toast.error('Drain failed', err instanceof ApiError ? err.message : undefined);
    } finally { drainBusy = false; }
  }
  async function upgradeAgent() {
    if (isLocal) return;
    upgradeBusy = true;
    try {
      const r = await api.agents.upgrade(id);
      toast.success('Upgrade dispatched', `${hostName} → v${r.version}`);
      await loadAll();
    } catch (err) {
      toast.error('Upgrade failed', err instanceof ApiError ? err.message : undefined);
    } finally { upgradeBusy = false; }
  }
  async function rotateToken() {
    if (isLocal) return;
    if (!(await confirm.ask({
      title: `Re-issue token for ${hostName}`,
      message: 'Generate a new enrollment token?',
      body: 'The old certificate stays valid until the new one is presented. Use this if the agent’s mTLS cert is suspect.',
      confirmLabel: 'Re-issue'
    }))) return;
    try {
      // Reuse existing rotate-token endpoint.
      const res = await fetch(`/api/v1/agents/${id}/rotate-token`, {
        method: 'POST',
        headers: { 'Authorization': `Bearer ${localStorage.getItem('access_token') ?? ''}` }
      });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      const data = await res.json();
      toast.success('Token re-issued', 'New install command available — copy from the response details');
      // Surface the new token via prompt fallback (real UI would need a dedicated modal)
      if (typeof navigator !== 'undefined' && navigator.clipboard && data.token) {
        try { await navigator.clipboard.writeText(data.install_hint ?? ''); toast.info('Install command copied to clipboard'); } catch {}
      }
    } catch (err) {
      toast.error('Re-issue failed', err instanceof Error ? err.message : undefined);
    }
  }
  async function revokeHost() {
    if (isLocal) return;
    if (!(await confirm.ask({
      title: `Revoke ${hostName}`,
      message: `Revoke host "${hostName}"?`,
      body: 'The agent’s certificate is invalidated. Containers running there are forgotten by Dockmesh — they keep running on the host until you stop them manually.',
      confirmLabel: 'Revoke', danger: true
    }))) return;
    revokeBusy = true;
    try {
      await api.agents.delete(id);
      toast.success('Revoked', hostName);
      goto('/hosts');
    } catch (err) {
      toast.error('Revoke failed', err instanceof ApiError ? err.message : undefined);
    } finally { revokeBusy = false; }
  }

  // ─── Sparkline helper ─────────────────────────────────────────────────
  function sparkPoints(values: number[], max: number, w: number, h: number): string {
    if (values.length === 0) return '';
    const m = Math.max(max, 1);
    return values
      .map((v, i) => `${(i / Math.max(1, values.length - 1) * w).toFixed(1)},${(h - (v / m) * (h - 2) - 1).toFixed(1)}`)
      .join(' ');
  }
  const cpuHistory = $derived(metricsHistory.map((s) => s.cpu_percent));
  const memHistory = $derived(metricsHistory.map((s) => s.mem_percent));

  const memUsedGb = $derived(metrics ? (metrics.mem_used / 1024 / 1024 / 1024) : null);
  const memTotalGb = $derived(metrics ? (metrics.mem_total / 1024 / 1024 / 1024) : null);
  const memPct = $derived(metrics ? Math.round(metrics.mem_percent) : null);
  const cpuPct = $derived(metrics ? Math.round(metrics.cpu_percent) : null);
  const diskPct = $derived(metrics ? Math.round(metrics.disk_percent ?? 0) : null);
</script>

<section class="ed-host-detail">
  {#if loading && !agent && !isLocal}
    <div class="dm-card" style="padding: 22px;"><Skeleton width="60%" height="2rem" /></div>
  {:else if loadError && !isLocal}
    <div class="dm-card" style="padding: 32px;">
      <EmptyState title="Failed to load" description={loadError} />
    </div>
  {:else}
    <!-- Breadcrumb -->
    <div class="ed-host-crumb">
      <a href="/hosts" class="ed-host-crumb-link">
        <ChevronLeft size={12} strokeWidth={1.5} /> Hosts
      </a>
    </div>

    <!-- Header -->
    <header class="ed-host-header">
      <div class="ed-host-header-text">
        <div class="ed-host-name-line">
          <span class={statusDot(status)}></span>
          <h1 class="ed-title ed-host-title">{hostName}</h1>
        </div>
        <p class="ed-subtitle ed-host-subtitle">
          {hostKind}{hostname ? ` · ${hostname}` : ''}{!isLocal && agent?.os ? ` · ${agent.os}/${agent.arch}` : ''}{!isLocal && agent?.docker_version ? ` · docker ${agent.docker_version}` : ''}{agent?.online_since ? ` · up ${fmtUptime(agent.online_since)}` : ''}
        </p>
        {#if tags.length > 0}
          <div class="ed-host-tags-row">
            {#each tags as t (t)}<span class="ed-hosts-tag-chip">{t}</span>{/each}
          </div>
        {/if}
      </div>

      <div class="ed-host-header-actions">
        <div class="ed-host-version-line font-mono">
          {#if hostVersion}<span>v{hostVersion}</span>{/if}
          {#if needsUpgrade}<span class="dm-pill dm-pill-warning ed-host-version-pill">↑ v{upgradePolicyVersion} avail.</span>{/if}
        </div>
        <div class="ed-actions">
          <button type="button" class="dm-btn dm-btn-secondary dm-btn-sm" disabled={isLocal || status !== 'online'} onclick={openDrain} title={isLocal ? "Local server can't be drained" : ''}>
            <ArrowDownToLine size={11} strokeWidth={1.5} /> Drain
          </button>
          {#if needsUpgrade}
            <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" disabled={upgradeBusy} onclick={upgradeAgent}>
              <RefreshCw size={11} strokeWidth={1.5} /> {upgradeBusy ? 'Upgrading…' : 'Upgrade agent'}
            </button>
          {/if}
        </div>
      </div>
    </header>

    <!-- Tab strip -->
    <nav class="ed-host-tabs" role="tablist">
      {#each [
        ['overview', 'Overview', null],
        ['containers', 'Containers', containers.length],
        ['stacks', 'Stacks', stacksOnHost.length],
        ['migrations', 'Migrations', migrations.length],
        ['tags', 'Tags', tags.length],
        ['maintenance', 'Maintenance', null]
      ] as [tid, label, count] (tid)}
        <button
          type="button"
          role="tab"
          aria-selected={tab === tid}
          class="ed-host-tab"
          class:active={tab === tid}
          onclick={() => selectTab(tid as TabId)}
        >
          {label}
          {#if count != null}<span class="ed-count">{count}</span>{/if}
        </button>
      {/each}
    </nav>

    <!-- ─── Overview ─── -->
    {#if tab === 'overview'}
      <div class="ed-host-grid">
        <!-- Identity card -->
        <div class="ed-host-card ed-host-identity">
          <div class="ed-host-card-title">Identity</div>
          <dl class="ed-host-kv">
            <dt>id</dt><dd class="font-mono">{id}</dd>
            <dt>kind</dt><dd>{hostKind}</dd>
            <dt>hostname</dt><dd class="font-mono">{hostname || '—'}</dd>
            {#if !isLocal && agent}
              <dt>os / arch</dt><dd class="font-mono">{agent.os ? `${agent.os}/${agent.arch}` : '—'}</dd>
              <dt>docker</dt><dd class="font-mono">{agent.docker_version || '—'}</dd>
              <dt>agent version</dt>
              <dd class="font-mono">
                {agent.version || '—'}
                {#if needsUpgrade}<span class="ed-host-kv-warn">· server is on v{upgradePolicyVersion}</span>{/if}
              </dd>
              <dt>cert fingerprint</dt><dd class="font-mono ed-host-kv-fingerprint">{agent.cert_fingerprint || '—'}</dd>
              <dt>last seen</dt><dd class="font-mono">{fmtAgo(agent.last_seen_at)}</dd>
              <dt>online since</dt><dd class="font-mono">{agent.online_since ? `${fmtAgo(agent.online_since)} (${fmtUptime(agent.online_since)} up)` : '—'}</dd>
              <dt>enrolled</dt><dd class="font-mono">{fmtDate(agent.created_at)}</dd>
            {:else}
              <dt>server version</dt><dd class="font-mono">{hostVersion || '—'}</dd>
              <dt>uptime</dt><dd class="font-mono">{metrics?.uptime_seconds ? `${Math.floor(metrics.uptime_seconds / 86400)}d ${Math.floor((metrics.uptime_seconds % 86400) / 3600)}h` : '—'}</dd>
            {/if}
          </dl>
        </div>

        <!-- Live snapshot -->
        <div class="ed-host-card ed-host-snapshot">
          <div class="ed-host-card-title">Live snapshot</div>
          <div class="ed-host-stat-row">
            <span class="ed-host-stat-label font-mono">CPU</span>
            <span class="ed-host-stat-value font-mono">{cpuPct ?? '—'}{cpuPct != null ? '%' : ''}</span>
            {#if cpuHistory.length > 1}
              <svg class="ed-host-spark" width="80" height="22" viewBox="0 0 80 22">
                <polyline points={sparkPoints(cpuHistory, 100, 80, 22)} fill="none" stroke="var(--accent)" stroke-width="1.4" />
              </svg>
            {/if}
          </div>
          <div class="ed-host-stat-row">
            <span class="ed-host-stat-label font-mono">Memory</span>
            <span class="ed-host-stat-value font-mono">{memPct ?? '—'}{memPct != null ? '%' : ''}</span>
            {#if memHistory.length > 1}
              <svg class="ed-host-spark" width="80" height="22" viewBox="0 0 80 22">
                <polyline points={sparkPoints(memHistory, 100, 80, 22)} fill="none" stroke="var(--accent)" stroke-width="1.4" />
              </svg>
            {/if}
            {#if memUsedGb != null && memTotalGb != null && memTotalGb > 0}
              <span class="ed-host-stat-hint font-mono">{memUsedGb.toFixed(1)} / {memTotalGb.toFixed(0)} GB</span>
            {/if}
          </div>
          <div class="ed-host-stat-row">
            <span class="ed-host-stat-label font-mono">Disk</span>
            <span class="ed-host-stat-value font-mono">{diskPct ?? '—'}{diskPct != null ? '%' : ''}</span>
            <div class="ed-hosts-bar" style="flex: 1; max-width: 80px;">
              <span class="ed-hosts-bar-fill" style="width: {Math.min(100, diskPct ?? 0)}%"></span>
            </div>
          </div>
          <div class="ed-host-snapshot-tiles">
            <div class="ed-host-tile">
              <span class="ed-host-tile-label font-mono">containers</span>
              <span class="ed-host-tile-value">{containers.length}</span>
            </div>
            <div class="ed-host-tile">
              <span class="ed-host-tile-label font-mono">stacks</span>
              <span class="ed-host-tile-value">{stacksOnHost.length}</span>
            </div>
          </div>
        </div>

        <!-- Stacks deployed here (Overview block) -->
        <div class="ed-host-card ed-host-stacks-block">
          <div class="ed-host-card-title">Stacks deployed here</div>
          {#if stacksOnHost.length === 0}
            <p class="ed-host-empty">
              No stacks deployed on <span class="ed-accent-mono">{hostName}</span>.
              {status === 'pending' ? "Host hasn't connected yet." : "It's idle — fine target for a new deploy."}
            </p>
          {:else}
            <div class="ed-host-stacks-list">
              {#each stacksOnHost.slice(0, 8) as s (s.name)}
                <a href="/stacks/{s.name}" class="ed-host-stacks-row">
                  <span class="font-mono ed-host-stacks-name">{s.name}</span>
                  <span class="font-mono ed-host-stacks-meta">
                    {#if s.containers !== undefined}{s.containers} ctr{:else}—{/if}
                  </span>
                </a>
              {/each}
              {#if stacksOnHost.length > 8}
                <button type="button" class="dm-btn dm-btn-ghost dm-btn-xs" onclick={() => selectTab('stacks')}>+ {stacksOnHost.length - 8} more →</button>
              {/if}
            </div>
          {/if}
        </div>

        <!-- Recent migrations on Overview -->
        <div class="ed-host-card ed-host-mig-block">
          <div class="ed-host-card-title">Recent migrations</div>
          {#if migrations.length === 0}
            <p class="ed-host-empty">No migrations involving <span class="ed-accent-mono">{hostName}</span> yet.</p>
          {:else}
            <div class="ed-host-mig-list">
              {#each migrations.slice(0, 5) as m (m.id)}
                {@const dir = m.source_host_id === id ? 'out' : 'in'}
                <div class="ed-host-mig-row">
                  <span class="ed-host-mig-dir font-mono" class:out={dir === 'out'} class:in={dir === 'in'}>
                    {dir === 'out' ? '↗' : '↙'} {dir}
                  </span>
                  <span class="font-mono ed-host-mig-stack">{m.stack_name}</span>
                  <span class="font-mono ed-host-mig-status">{m.status}</span>
                  <span class="font-mono ed-host-mig-when">{fmtAgo(m.completed_at ?? m.started_at)}</span>
                </div>
              {/each}
              {#if migrations.length > 5}
                <button type="button" class="dm-btn dm-btn-ghost dm-btn-xs" onclick={() => selectTab('migrations')}>+ {migrations.length - 5} more →</button>
              {/if}
            </div>
          {/if}
        </div>
      </div>

    <!-- ─── Containers ─── -->
    {:else if tab === 'containers'}
      {#if containers.length === 0}
        <div class="dm-card" style="padding: 36px;">
          <EmptyState title="No containers running" description={status === 'pending' ? "Host hasn't connected yet." : `No containers on ${hostName}.`} />
        </div>
      {:else}
        <div class="ed-host-table">
          <div class="ed-host-tablerow head">
            <span>name</span>
            <span>image</span>
            <span>state</span>
            <span>uptime</span>
          </div>
          {#each containers as c (c.Id)}
            <a href="/containers/{c.Id}{isLocal ? '' : `?host=${id}`}" class="ed-host-tablerow">
              <span class="font-mono ed-host-table-name">{c.Names?.[0]?.replace(/^\//, '') ?? c.Id.slice(0, 12)}</span>
              <span class="font-mono ed-host-table-image">{c.Image}</span>
              <span class="ed-host-table-state">
                <span class={c.State === 'running' ? 'ok-dot' : 'fail-dot'}></span>
                <span class="font-mono">{c.State}</span>
              </span>
              <span class="font-mono ed-host-table-uptime">{c.Status?.replace(/^Up\s+/, '') ?? '—'}</span>
            </a>
          {/each}
        </div>
        <p class="font-mono ed-host-foot-note">
          Live per-container CPU/Memory columns are deferred — see
          <code>project_hosts_open_punch_list.md</code> in the repo. Click a row for live stats.
        </p>
      {/if}

    <!-- ─── Stacks ─── -->
    {:else if tab === 'stacks'}
      {#if stacksOnHost.length === 0}
        <div class="dm-card" style="padding: 36px;">
          <EmptyState title="No stacks here" description={`No stacks deployed on ${hostName}.`} />
        </div>
      {:else}
        <div class="ed-host-table">
          <div class="ed-host-tablerow stacks head">
            <span>stack</span>
            <span>services</span>
            <span>state</span>
            <span></span>
          </div>
          {#each stacksOnHost as s (s.name)}
            <div class="ed-host-tablerow stacks">
              <a href="/stacks/{s.name}" class="font-mono ed-host-table-name">{s.name}</a>
              <span class="font-mono ed-host-table-meta">{s.containers ?? '—'}</span>
              <span class="ed-host-table-state">
                <span class="ok-dot"></span>
                <span class="font-mono">{s.has_volumes ? 'stateful' : 'running'}</span>
              </span>
              <span class="ed-host-table-actions">
                <a href="/stacks/{s.name}?action=migrate" class="dm-btn dm-btn-ghost dm-btn-xs">Migrate…</a>
              </span>
            </div>
          {/each}
        </div>
      {/if}

    <!-- ─── Migrations ─── -->
    {:else if tab === 'migrations'}
      {#if migrations.length === 0}
        <div class="dm-card" style="padding: 36px;">
          <EmptyState title="No migrations" description={`No stack migrations involving ${hostName} yet.`} />
        </div>
      {:else}
        <div class="ed-host-table">
          <div class="ed-host-tablerow migrations head">
            <span>stack</span>
            <span>direction</span>
            <span>status</span>
            <span>started</span>
            <span>by</span>
            <span>completed</span>
          </div>
          {#each migrations as m (m.id)}
            {@const dir = m.source_host_id === id ? `→ ${m.target_host_id}` : `← ${m.source_host_id}`}
            <div class="ed-host-tablerow migrations">
              <a href="/stacks/{m.stack_name}" class="font-mono ed-host-table-name">{m.stack_name}</a>
              <span class="font-mono ed-host-table-meta">{dir}</span>
              <span class="ed-host-table-state">
                <span class={m.status === 'completed' ? 'ok-dot' : m.status === 'failed' ? 'fail-dot' : 'warn-dot'}></span>
                <span class="font-mono">{m.status}</span>
              </span>
              <span class="font-mono ed-host-table-meta">{fmtAgo(m.started_at)}</span>
              <span class="font-mono ed-host-table-meta">{m.initiated_by}</span>
              <span class="font-mono ed-host-table-meta">{m.completed_at ? fmtAgo(m.completed_at) : '—'}</span>
            </div>
          {/each}
        </div>
      {/if}

    <!-- ─── Tags ─── -->
    {:else if tab === 'tags'}
      <div class="ed-host-grid">
        <div class="ed-host-card" style="grid-column: span 7;">
          <div class="ed-host-card-title">Tags on this host</div>
          <p class="ed-host-empty" style="margin-top: 0;">
            Tags drive stack <span class="ed-accent-mono">host-affinity</span> rules. A stack with selector <code>tag:gpu</code> can only be deployed on hosts carrying that tag.
          </p>
          <div class="ed-host-tags-edit">
            {#each tagsDraft as t (t)}
              <span class="ed-hosts-tag-chip">
                {t}
                <button type="button" class="ed-host-tags-remove" onclick={() => (tagsDraft = tagsDraft.filter((x) => x !== t))} aria-label="Remove">
                  <X size={10} strokeWidth={1.5} />
                </button>
              </span>
            {/each}
            <input
              type="text"
              class="ed-host-tags-input font-mono"
              placeholder={tagsDraft.length === 0 ? 'add a tag…' : '+ add tag'}
              bind:value={tagInput}
              onkeydown={(e) => { if (e.key === 'Enter' || e.key === ',') { e.preventDefault(); addTagDraft(tagInput); } }}
            />
          </div>
          {#if allFleetTags.filter((t) => !tagsDraft.includes(t)).length > 0}
            <div class="ed-host-tags-suggest">
              <span class="font-mono ed-host-tags-suggest-label">fleet</span>
              {#each allFleetTags.filter((t) => !tagsDraft.includes(t)) as s (s)}
                <button type="button" class="dm-btn dm-btn-ghost dm-btn-xs" onclick={() => addTagDraft(s)}>+ {s}</button>
              {/each}
            </div>
          {/if}
          <div class="ed-host-tags-actions">
            <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" disabled={tagsBusy} onclick={saveTags}>
              {tagsBusy ? 'Saving…' : 'Save changes'}
            </button>
            <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={revertTags}>Revert</button>
          </div>
        </div>

        <div class="ed-host-card" style="grid-column: span 5;">
          <div class="ed-host-card-title">Affected affinity</div>
          <div class="ed-host-affinity-empty">
            <Info size={14} strokeWidth={1.5} class="ed-host-affinity-icon" />
            <p class="ed-host-empty" style="margin: 0;">
              No host-affinity rules defined yet — coming with the stack-affinity feature. When stacks declare a host-selector this panel will list which selectors match this host's tags.
            </p>
          </div>
        </div>
      </div>

    <!-- ─── Maintenance ─── -->
    {:else if tab === 'maintenance'}
      <div class="ed-host-grid">
        <div class="ed-host-card" style="grid-column: span 6;">
          <div class="ed-host-card-title">Drain</div>
          <p class="ed-host-empty" style="margin-top: 0;">
            Migrates every stack off <span class="ed-accent-mono">{hostName}</span> to other matching hosts, then marks it cordoned. Use before reboots, kernel upgrades, or pulling the plug.
          </p>
          <button type="button" class="dm-btn dm-btn-secondary dm-btn-sm" disabled={isLocal} onclick={openDrain}>
            <ArrowDownToLine size={11} strokeWidth={1.5} /> Plan drain…
          </button>
          {#if isLocal}
            <p class="font-mono ed-host-empty" style="margin-top: 8px;">
              local server can't be drained — it runs Dockmesh itself.
            </p>
          {/if}
        </div>

        <div class="ed-host-card" style="grid-column: span 6;">
          <div class="ed-host-card-title">Upgrade agent</div>
          <p class="ed-host-empty" style="margin-top: 0;">
            {#if isLocal}
              Server self-update lives in System settings — not a per-host concern.
            {:else if !needsUpgrade}
              {hostVersion ? `Already on v${hostVersion}.` : 'Version unknown.'} Nothing to do.
            {:else}
              This host is on <span class="ed-accent-mono">v{hostVersion}</span>. Server is on v{upgradePolicyVersion}. Upgrades are zero-downtime — agent reconnects within ~10s.
            {/if}
          </p>
          <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" disabled={isLocal || !needsUpgrade || upgradeBusy} onclick={upgradeAgent}>
            <RefreshCw size={11} strokeWidth={1.5} /> {upgradeBusy ? 'Upgrading…' : 'Upgrade now'}
          </button>
        </div>

        <div class="ed-host-card" style="grid-column: span 6;">
          <div class="ed-host-card-title">Re-issue token</div>
          <p class="ed-host-empty" style="margin-top: 0;">
            If the agent's mTLS cert is suspect — leaked from CI logs, host wiped, etc. — generate a new enrollment token. The old cert is revoked the moment the new one is presented.
          </p>
          <button type="button" class="dm-btn dm-btn-secondary dm-btn-sm" disabled={isLocal} onclick={rotateToken}>
            <RefreshCw size={11} strokeWidth={1.5} /> Re-issue
          </button>
        </div>

        <div class="ed-host-card ed-host-card-danger" style="grid-column: span 6;">
          <div class="ed-host-card-title ed-host-card-danger-title">Revoke</div>
          <p class="ed-host-empty" style="margin-top: 0;">
            Permanently removes <span class="ed-accent-mono">{hostName}</span> from the fleet. Cert is invalidated. Containers running there are forgotten by Dockmesh — they keep running on the host until you stop them manually.
          </p>
          <button type="button" class="dm-btn dm-btn-sm ed-host-danger-btn" disabled={isLocal || revokeBusy} onclick={revokeHost}>
            <Trash2 size={11} strokeWidth={1.5} /> {revokeBusy ? 'Revoking…' : `Revoke ${hostName}…`}
          </button>
        </div>
      </div>
    {/if}
  {/if}
</section>

<!-- ─── Drain modal ─── -->
{#if showDrain}
  <div class="ed-modal-backdrop" onmousedown={(e) => { if (e.target === e.currentTarget) showDrain = false; }}>
    <div class="ed-modal" role="dialog" aria-modal="true" style="width: min(640px, 92vw);">
      <header class="ed-modal-head">
        <div>
          <Eyebrow>Drain · {hostName}</Eyebrow>
          <h2 class="ed-modal-title">Move every stack off {hostName}</h2>
        </div>
        <button type="button" class="dm-btn dm-btn-ghost dm-btn-xs" onclick={() => (showDrain = false)} aria-label="Close">
          <X size={13} strokeWidth={1.5} />
        </button>
      </header>
      <div class="ed-modal-body">
        {#if drainLoading}
          <p class="ed-host-empty"><RefreshCw size={12} strokeWidth={1.5} class="ed-spin" /> Generating drain plan…</p>
        {:else if drainPlan}
          {#if drainPlan.entries.length === 0}
            <p class="ed-host-empty">No stacks running here. Drain is a no-op — host will be cordoned and ready for maintenance.</p>
          {:else}
            {@const stuck = drainPlan.entries.filter((e) => !e.feasible)}
            {#if stuck.length > 0}
              <div class="ed-host-drain-warn">
                <AlertTriangle size={14} strokeWidth={1.5} class="ed-host-drain-warn-icon" />
                <div>
                  <div class="ed-host-drain-warn-title">{stuck.length} stack{stuck.length === 1 ? '' : 's'} can't be moved.</div>
                  <div class="font-mono ed-host-drain-warn-detail">No other host carries a matching tag. Pin the stack here, drop the affinity, or add the tag elsewhere.</div>
                </div>
              </div>
            {/if}
            <div class="ed-host-drain-list">
              {#each drainPlan.entries as e (e.stack_name)}
                <div class="ed-host-drain-row">
                  <span class="font-mono">{e.stack_name}</span>
                  <span class="ed-host-drain-arrow">→</span>
                  <span class="font-mono" class:not-feasible={!e.feasible}>
                    {e.feasible ? e.target_name : 'no candidate'}
                  </span>
                </div>
              {/each}
            </div>
          {/if}
        {/if}
      </div>
      <footer class="ed-modal-foot">
        <span class="font-mono ed-modal-foot-status">ESC to cancel</span>
        <div class="ed-actions">
          <button type="button" class="dm-btn dm-btn-ghost dm-btn-sm" onclick={() => (showDrain = false)}>Cancel</button>
          <button type="button" class="dm-btn dm-btn-primary dm-btn-sm" disabled={drainBusy || !drainPlan?.feasible} onclick={executeDrain}>
            <ArrowDownToLine size={11} strokeWidth={1.5} /> {drainBusy ? 'Starting…' : 'Begin drain'}
          </button>
        </div>
      </footer>
    </div>
  </div>
{/if}

<style>
  .ed-host-detail { display: flex; flex-direction: column; gap: 18px; }
  .ed-host-crumb { font-family: var(--font-mono); font-size: 11px; color: var(--fg-subtle); }
  .ed-host-crumb-link {
    display: inline-flex; align-items: center; gap: 4px;
    color: var(--fg-subtle); text-decoration: none;
  }
  .ed-host-crumb-link:hover { color: var(--accent-fg); }

  /* ─── Status dots (also defined on hosts list, kept here for isolation) ─── */
  :global(.ok-dot) { display: inline-block; width: 8px; height: 8px; border-radius: 999px; background: var(--color-success-400); }
  :global(.warn-dot) { display: inline-block; width: 8px; height: 8px; border-radius: 999px; background: var(--color-warning-400); }
  :global(.fail-dot) { display: inline-block; width: 8px; height: 8px; border-radius: 999px; background: var(--color-danger-400); }
  :global(.neutral-dot) { display: inline-block; width: 8px; height: 8px; border-radius: 999px; background: var(--fg-subtle); }
  :global(.ed-spin) { animation: ed-spin 1s linear infinite; }
  @keyframes ed-spin { to { transform: rotate(360deg); } }
  .ed-accent-mono { color: var(--accent-fg); font-family: var(--font-mono); font-style: normal; font-weight: 500; }

  /* ─── Header ─── */
  .ed-host-header {
    display: flex; align-items: flex-start; justify-content: space-between;
    gap: 24px; flex-wrap: wrap;
  }
  .ed-host-header-text { min-width: 0; flex: 1; }
  .ed-host-name-line { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; margin-top: 6px; }
  .ed-host-title { font-size: 26px; margin: 0; font-style: normal; }
  .ed-host-meta-row {
    margin-top: 8px; padding-left: 20px;
    display: flex; flex-wrap: wrap; gap: 14px;
    font-size: 11.5px; color: var(--fg-subtle);
  }
  .ed-host-tags-row {
    display: flex; flex-wrap: wrap; gap: 4px;
    margin-top: 10px; padding-left: 20px;
  }
  .ed-host-header-actions {
    display: flex; flex-direction: column; align-items: flex-end; gap: 10px;
  }
  .ed-host-version-line {
    font-size: 11.5px; color: var(--fg);
    display: flex; align-items: center; gap: 10px;
  }
  .ed-host-version-pill { font-size: 9.5px; padding: 1px 6px; }

  /* ─── Tabs ─── */
  .ed-host-tabs {
    display: flex; gap: 0;
    border-bottom: 1px solid var(--border);
  }
  .ed-host-tab {
    background: transparent; border: 0;
    padding: 10px 14px;
    border-bottom: 2px solid transparent;
    font-family: var(--font-mono);
    font-size: 11.5px;
    color: var(--fg-muted);
    cursor: pointer;
    display: inline-flex; align-items: center; gap: 6px;
    margin-bottom: -1px;
  }
  .ed-host-tab:hover { color: var(--fg); }
  .ed-host-tab.active { color: var(--accent-fg); border-bottom-color: var(--accent); }
  /* ed-host-tab-count moved to global .ed-count in app.css */

  /* ─── Grid layout for cards ─── */
  .ed-host-grid {
    display: grid;
    grid-template-columns: repeat(12, 1fr);
    gap: 18px;
  }
  .ed-host-card {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 16px 18px;
  }
  .ed-host-identity { grid-column: span 7; }
  .ed-host-snapshot { grid-column: span 5; display: flex; flex-direction: column; gap: 12px; }
  .ed-host-stacks-block { grid-column: span 7; }
  .ed-host-mig-block { grid-column: span 5; }
  @media (max-width: 980px) {
    .ed-host-identity, .ed-host-snapshot, .ed-host-stacks-block, .ed-host-mig-block { grid-column: span 12; }
  }
  .ed-host-card-title {
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
    letter-spacing: 0.06em;
    text-transform: uppercase;
    margin-bottom: 12px;
  }

  /* ─── Identity KV ─── */
  .ed-host-kv {
    display: grid;
    grid-template-columns: 130px 1fr;
    gap: 8px 14px;
    font-size: 12px;
    margin: 0;
  }
  .ed-host-kv dt {
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg-subtle);
    letter-spacing: 0.04em;
    text-transform: lowercase;
  }
  .ed-host-kv dd { margin: 0; color: var(--fg); word-break: break-word; }
  .ed-host-kv-warn { color: var(--color-warning-400); margin-left: 8px; }
  .ed-host-kv-fingerprint { font-size: 10.5px; color: var(--fg-muted); }

  /* ─── Snapshot stats ─── */
  .ed-host-stat-row {
    display: flex; align-items: center; gap: 10px;
  }
  .ed-host-stat-label { width: 60px; font-size: 11px; color: var(--fg-subtle); }
  .ed-host-stat-value { font-size: 14px; color: var(--fg); min-width: 50px; }
  .ed-host-spark { flex-shrink: 0; }
  .ed-host-stat-hint {
    margin-left: auto; font-size: 10.5px; color: var(--fg-subtle);
  }
  .ed-hosts-bar { height: 2px; background: var(--border); border-radius: 1px; position: relative; overflow: hidden; }
  .ed-hosts-bar-fill { position: absolute; inset: 0; right: auto; background: var(--accent); border-radius: 1px; }
  .ed-host-snapshot-tiles {
    display: grid; grid-template-columns: 1fr 1fr; gap: 10px; margin-top: 4px;
  }
  .ed-host-tile {
    padding: 10px 12px;
    border: 1px solid var(--border);
    border-radius: 5px;
    background: var(--bg);
  }
  .ed-host-tile-label {
    font-size: 9.5px; color: var(--fg-subtle);
    text-transform: uppercase; letter-spacing: 0.08em;
  }
  .ed-host-tile-value {
    font-size: 22px; color: var(--fg); font-weight: 500;
    margin-top: 4px; display: block;
  }

  /* ─── Stacks block ─── */
  .ed-host-empty {
    margin: 8px 0 0;
    color: var(--fg-muted);
    font-size: 12.5px;
    line-height: 1.55;
  }
  .ed-host-stacks-list { display: flex; flex-direction: column; gap: 6px; margin-top: 10px; }
  .ed-host-stacks-row {
    display: flex; align-items: center; justify-content: space-between;
    padding: 8px 10px;
    border: 1px solid var(--border);
    border-radius: 5px;
    background: var(--bg);
    text-decoration: none;
    color: var(--fg);
  }
  .ed-host-stacks-row:hover { border-color: var(--border-strong); }
  .ed-host-stacks-name { font-size: 12px; color: var(--fg); }
  .ed-host-stacks-meta { font-size: 11px; color: var(--fg-subtle); }

  /* ─── Migration block (overview) ─── */
  .ed-host-mig-list { display: flex; flex-direction: column; gap: 6px; margin-top: 10px; }
  .ed-host-mig-row {
    display: grid;
    grid-template-columns: 60px 1fr 70px 80px;
    gap: 8px;
    align-items: center;
    padding: 6px 8px;
    border: 1px solid var(--border);
    border-radius: 4px;
    background: var(--bg);
    font-size: 11px;
  }
  .ed-host-mig-dir { color: var(--fg-subtle); }
  .ed-host-mig-dir.out { color: var(--color-warning-400); }
  .ed-host-mig-dir.in { color: var(--accent-fg); }
  .ed-host-mig-stack { color: var(--fg); }
  .ed-host-mig-status { color: var(--fg-muted); }
  .ed-host-mig-when { color: var(--fg-subtle); text-align: right; }

  /* ─── Tabular layout for non-Overview tabs ─── */
  .ed-host-table {
    border: 1px solid var(--border);
    border-radius: 6px;
    overflow: hidden;
  }
  .ed-host-tablerow {
    display: grid;
    grid-template-columns: 2fr 2fr 1fr 1fr;
    gap: 12px;
    padding: 10px 14px;
    border-bottom: 1px solid var(--border);
    align-items: center;
    text-decoration: none;
    color: var(--fg);
  }
  .ed-host-tablerow:hover { background: var(--surface-hover); }
  .ed-host-tablerow:last-child { border-bottom: 0; }
  .ed-host-tablerow.head {
    background: var(--bg-elevated);
    color: var(--fg-subtle);
    font-family: var(--font-mono);
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    cursor: default;
  }
  .ed-host-tablerow.head:hover { background: var(--bg-elevated); }
  .ed-host-tablerow.stacks { grid-template-columns: 2fr 0.7fr 1fr 1fr; }
  .ed-host-tablerow.migrations { grid-template-columns: 1.6fr 1fr 0.8fr 0.9fr 0.9fr 0.9fr; }
  .ed-host-table-name { font-size: 12px; color: var(--fg); text-decoration: none; }
  .ed-host-table-name:hover { color: var(--accent-fg); }
  .ed-host-table-image { font-size: 11px; color: var(--fg-muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .ed-host-table-state { display: inline-flex; align-items: center; gap: 6px; font-size: 11px; color: var(--fg-muted); }
  .ed-host-table-uptime { font-size: 11px; color: var(--fg-subtle); }
  .ed-host-table-meta { font-size: 11px; color: var(--fg-subtle); }
  .ed-host-table-actions { display: inline-flex; gap: 4px; justify-content: flex-end; }
  .ed-host-foot-note {
    font-size: 10.5px; color: var(--fg-subtle); margin: 12px 0 0;
  }
  .ed-host-foot-note code { color: var(--accent-fg); }

  /* ─── Tags tab ─── */
  .ed-host-tags-edit {
    display: flex; flex-wrap: wrap; gap: 6px;
    padding: 10px 12px;
    border: 1px solid var(--border);
    border-radius: 5px;
    background: var(--bg);
    margin-top: 14px;
  }
  .ed-hosts-tag-chip {
    display: inline-flex; align-items: center; gap: 4px;
    padding: 2px 6px;
    border: 1px solid var(--border-strong);
    border-radius: 3px;
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--fg);
    background: var(--surface);
  }
  .ed-host-tags-input {
    flex: 1 1 140px; border: 0; background: transparent;
    color: var(--fg); font-size: 11.5px; outline: none; min-width: 100px;
  }
  .ed-host-tags-input::placeholder { color: var(--fg-subtle); }
  .ed-host-tags-remove {
    background: transparent; border: 0; cursor: pointer;
    color: var(--fg-subtle); padding: 0;
    display: inline-flex;
  }
  .ed-host-tags-remove:hover { color: var(--color-danger-400); }
  .ed-host-tags-suggest {
    display: flex; gap: 6px; flex-wrap: wrap; margin-top: 10px; align-items: center;
  }
  .ed-host-tags-suggest-label {
    font-size: 9.5px; color: var(--fg-subtle); letter-spacing: 0.1em;
    text-transform: uppercase; margin-right: 4px;
  }
  .ed-host-tags-actions { display: flex; gap: 8px; margin-top: 16px; }

  .ed-host-affinity-empty {
    display: flex; align-items: flex-start; gap: 10px;
    padding: 14px;
    border: 1px dashed var(--border);
    border-radius: 5px;
    background: var(--bg);
    margin-top: 12px;
  }
  :global(.ed-host-affinity-icon) { color: var(--fg-subtle); flex-shrink: 0; margin-top: 2px; }

  /* ─── Maintenance tab cards ─── */
  .ed-host-card-danger { border-color: color-mix(in srgb, var(--color-danger-500) 30%, var(--border)); }
  .ed-host-card-danger-title { color: var(--color-danger-400); }
  .ed-host-danger-btn {
    color: var(--color-danger-400);
    border-color: color-mix(in srgb, var(--color-danger-500) 40%, var(--border));
    background: transparent;
  }
  .ed-host-danger-btn:hover:not(:disabled) {
    background: color-mix(in srgb, var(--color-danger-500) 8%, transparent);
  }

  /* ─── Modal chrome ─── */
  .ed-modal-backdrop {
    position: fixed; inset: 0;
    background: color-mix(in srgb, var(--bg) 70%, transparent);
    backdrop-filter: blur(6px); -webkit-backdrop-filter: blur(6px);
    z-index: 50;
    display: flex; align-items: center; justify-content: center;
  }
  .ed-modal {
    background: var(--bg-elevated);
    border: 1px solid var(--border-strong);
    border-radius: 8px;
    max-height: 92vh;
    display: flex; flex-direction: column;
    overflow: hidden;
  }
  .ed-modal-head {
    display: flex; align-items: flex-start; justify-content: space-between;
    gap: 16px; padding: 18px 22px;
    border-bottom: 1px solid var(--border);
  }
  .ed-modal-title {
    margin: 4px 0 0;
    font-size: 18px; font-weight: 600;
    color: var(--fg); font-family: var(--font-sans);
  }
  .ed-modal-body { padding: 18px 22px; overflow-y: auto; }
  .ed-modal-foot {
    display: flex; align-items: center; justify-content: space-between;
    gap: 12px; padding: 14px 22px;
    border-top: 1px solid var(--border);
    background: var(--bg-elevated);
  }
  .ed-modal-foot-status { font-size: 11px; color: var(--fg-subtle); }

  /* Drain modal-specific */
  .ed-host-drain-warn {
    display: flex; align-items: flex-start; gap: 10px;
    padding: 10px 14px;
    border: 1px solid color-mix(in srgb, var(--color-warning-500) 50%, var(--border));
    background: color-mix(in srgb, var(--color-warning-500) 6%, transparent);
    border-radius: 5px;
    margin-bottom: 14px;
  }
  :global(.ed-host-drain-warn-icon) { color: var(--color-warning-400); margin-top: 2px; flex-shrink: 0; }
  .ed-host-drain-warn-title { font-size: 12.5px; color: var(--fg); font-weight: 500; }
  .ed-host-drain-warn-detail { font-size: 11px; color: var(--fg-muted); margin-top: 3px; }
  .ed-host-drain-list { display: flex; flex-direction: column; gap: 6px; }
  .ed-host-drain-row {
    display: grid;
    grid-template-columns: 1.4fr auto 1.4fr;
    align-items: center; gap: 10px;
    padding: 10px 12px;
    border: 1px solid var(--border);
    border-radius: 5px;
    background: var(--bg);
    font-size: 12px;
  }
  .ed-host-drain-arrow { color: var(--fg-subtle); }
  .ed-host-drain-row .not-feasible { color: var(--color-warning-400); }
</style>
